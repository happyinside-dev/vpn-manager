# Proxy Manager

🌐 Язык: [English](README.md) | **Русский**

Небольшая самостоятельно размещаемая панель управления для одного VPS с Xray VLESS/REALITY и Hysteria 2. Основным источником данных является SQLite: список пользователей в памяти Xray синхронизируется с SQLite, а Hysteria передаёт проверку подлинности Proxy Manager через HTTP-эндпоинт, доступный только на loopback-интерфейсе.

## Архитектура

```text
Браузер --(127.0.0.1:18080 / SSH-туннель)--> Proxy Manager
                                               |-- SQLite
                                               |-- Xray HandlerService gRPC 127.0.0.1:10085
Hysteria --HTTP auth 127.0.0.1:18081-----------|
```

Публичные порты прокси остаются прежними: Xray — TCP/443, Hysteria — UDP/443. Никогда не открывайте порты 10085, 18080 или 18081 в UFW. Параметр `internal_listen` намеренно отделён от адреса прослушивания панели, чтобы обратный HTTPS-прокси не мог случайно сделать эндпоинт авторизации Hysteria общедоступным.

## Механизмы безопасности

- Пароль администратора хешируется с помощью Argon2id и никогда не хранится в открытом виде.
- Сессия использует случайный непрозрачный 256-битный токен; в SQLite хранится только его SHA-256. Cookie имеет атрибуты HttpOnly и SameSite=Strict; при доступе к панели через HTTPS установите `session_secure: true`.
- Запросы API, изменяющие данные, требуют авторизованной сессии и заголовка `X-CSRF-Token`.
- Попытки входа ограничиваются по частоте с помощью механизма в памяти процесса.
- Пароли Hysteria — случайные 192-битные значения; они никогда не записываются в логи или метаданные аудита.
- Приватный ключ REALITY и приватный TLS-ключ Hysteria **не** хранятся в Proxy Manager.
- Слушатели Xray API и авторизации Hysteria доступны только через loopback-интерфейс.
- Сервис работает от отдельного пользователя `proxy-manager` с усиленными настройками безопасности systemd.

## Сборка

Требования: Go 1.23+, Node 20+ (рекомендуется 22), npm, компилятор C и заголовочные файлы SQLite (`libsqlite3-dev`), поскольку проект использует `mattn/go-sqlite3`.

```bash
make build
```

Команда `make build-web` собирает React/Vite и копирует полученные ресурсы в `internal/api/static`; затем Go встраивает их в итоговый исполняемый файл.

Проверки:

```bash
make fmt
make test
make vet
```

Зависимость Xray для Go зафиксирована на версии `github.com/xtls/xray-core v1.241231.0`, чтобы обеспечить стабильный интерфейс protobuf-клиента. Через HandlerService она успешно взаимодействует с более новыми совместимыми серверами Xray. В актуальной документации Xray API описывается как gRPC и поддерживает добавление и удаление пользователей VLESS через HandlerService. Подробнее: https://xtls.github.io/en/config/api.html

Формат HTTP-запросов и ответов авторизации Hysteria, а также синтаксис маскировки основаны на официальной документации Hysteria 2: https://v2.hysteria.network/docs/advanced/Full-Server-Config/ . Кодирование URI соответствует https://v2.hysteria.network/docs/developers/URI-Scheme/ .

## Структура установки

```text
/usr/local/bin/proxy-manager
/etc/proxy-manager/config.yaml
/var/lib/proxy-manager/proxy-manager.db
/var/log/proxy-manager/
/etc/systemd/system/proxy-manager.service
```

Рекомендуемые права доступа:

```bash
chown proxy-manager:proxy-manager /var/lib/proxy-manager
chmod 0700 /var/lib/proxy-manager
chmod 0600 /var/lib/proxy-manager/proxy-manager.db*
chown root:proxy-manager /etc/proxy-manager/config.yaml
chmod 0640 /etc/proxy-manager/config.yaml
```

## Конфигурация

Используйте `config.example.yaml` в качестве основы. Здесь должны находиться только **публичный** ключ REALITY и short ID. Параметр `public_host` используется при генерации ссылок, а `public_ip` — как резервный вариант.

Xray может использовать упрощённую конфигурацию API (поддерживается современными версиями):

```json
"api": {
  "tag": "api",
  "listen": "127.0.0.1:10085",
  "services": ["HandlerService", "StatsService"]
}
```

Управляемый входящий обработчик VLESS/REALITY должен иметь постоянный тег, по умолчанию `vless-reality`. Изначально массив `clients` может быть пустым: Proxy Manager добавляет пользователей динамически.

Hysteria:

```yaml
auth:
  type: http
  http:
    url: http://127.0.0.1:18081/internal/hysteria/auth

masquerade:
  type: proxy
  proxy:
    url: https://HY_SNI
    rewriteHost: true
```

HTTP-бэкенд Hysteria получает `{addr, auth, tx}`, а Proxy Manager возвращает HTTP 200 с `{ok,id}`. Для новых учётных записей поле `auth` должно содержать `username:password`.

## Инициализация

```bash
install -d -o proxy-manager -g proxy-manager -m 0700 /var/lib/proxy-manager
proxy-manager --config /etc/proxy-manager/config.yaml init-admin \
  --username admin --password-file /run/secure-admin-password
systemctl enable --now proxy-manager.service
```

Не передавайте первоначальный пароль через аргументы командной строки установщика. Используйте временный файл с паролем, созданный от имени root и принадлежащий пользователю `proxy-manager`, а затем удалите его.

## Самый безопасный доступ: SSH-проброс портов

Адрес бэкенда по умолчанию: `127.0.0.1:18080`.

```bash
ssh -L 8080:127.0.0.1:18080 proxy@SERVER
```

Откройте `http://127.0.0.1:8080`. Открывать порт панели в файрволе не требуется.

При необходимости можно настроить обратный HTTPS-прокси на `127.0.0.1:18080`; включите `session_secure: true` и **никогда** не проксируйте порт 18081 или путь `/internal`.

## CLI

```bash
proxy-manager user add alice
proxy-manager user list
proxy-manager user list --json
proxy-manager user show alice
proxy-manager user enable alice
proxy-manager user disable alice
proxy-manager user delete alice

proxy-manager xray enable alice
proxy-manager xray disable alice
proxy-manager xray regenerate alice

proxy-manager hysteria enable alice
proxy-manager hysteria disable alice
proxy-manager hysteria regenerate alice

proxy-manager links alice
proxy-manager reconcile
```

CLI работает с той же базой SQLite и использует тот же сервисный слой, что и веб-API.

## REST API

Реализованные эндпоинты:

```text
POST   /api/login
POST   /api/logout
GET    /api/session
GET    /api/status
GET    /api/settings
POST   /api/admin/password

GET    /api/users
POST   /api/users
GET    /api/users/:id
PATCH  /api/users/:id
DELETE /api/users/:id
POST   /api/users/:id/enable
POST   /api/users/:id/disable
POST   /api/users/:id/xray/enable
POST   /api/users/:id/xray/disable
POST   /api/users/:id/xray/regenerate
POST   /api/users/:id/hysteria/enable
POST   /api/users/:id/hysteria/disable
POST   /api/users/:id/hysteria/regenerate
GET    /api/users/:id/links
```

Ошибки возвращаются в формате `{ "error": { "code": "...", "message": "..." } }`.

## Синхронизация состояния / восстановление после перезапуска

При запуске и каждые 30 секунд Proxy Manager:

1. Считывает активные профили Xray из SQLite.
2. Получает текущий список пользователей Xray через `GetInboundUsers` сервиса HandlerService.
3. Добавляет отсутствующих управляемых пользователей.
4. Удаляет устаревших пользователей, у которых поле `email` принадлежит внутреннему пространству имён Proxy Manager `pm-<id>@proxy-manager`.

Посторонние и статически заданные пользователи Xray не удаляются. Этот периодический цикл также восстанавливает состояние после независимого перезапуска Xray.

## Атомарность операций

Изменения учётных записей и Xray выполняются внутри транзакции SQLite с попыткой провести соответствующую операцию через Xray gRPC. Если Xray отклоняет изменение, транзакция БД откатывается. При обновлении учётных данных система также пытается восстановить прежние данные Xray, если добавление новых завершается неудачей. Синхронизация состояния служит дополнительной защитой на случай сбоев процесса в критический момент.

Hysteria не поддерживает изменение пользователей во время работы: HTTP-обработчик авторизации напрямую читает зафиксированное состояние SQLite.

## Миграция старой установки с одним пользователем

Перед изменением конфигураций любого из демонов создайте резервные копии:

- Конфигурации Xray.
- Конфигурации Hysteria.
- Сертификата и ключа Hysteria.
- Существующей базы SQLite Proxy Manager, если она есть.

Затем, пока в старых конфигурациях ещё содержатся исходные учётные данные, выполните:

```bash
proxy-manager --config /etc/proxy-manager/config.yaml migrate-legacy \
  --xray-config /usr/local/etc/xray/config.json \
  --hysteria-config /etc/hysteria/config.yaml
```

Автоматическая миграция намеренно работает консервативно. Она отказывается обрабатывать конфигурацию Xray с более чем одним старым клиентом VLESS, а также авторизацию Hysteria, отличную от `password`, поскольку такие случаи неоднозначны.

При обычной миграции старой установки с одним пользователем создаётся учётная запись `legacy` с сохранением прежнего UUID Xray и пароля Hysteria. Профиль Hysteria получает флаг совместимости, благодаря которому работают как старая строка авторизации, содержащая только пароль, так и новый формат `legacy:password`.

**Важен порядок действий для Xray:** после импорта удалите перенесённого пользователя из статического списка VLESS `clients`, но сохраните его UUID в SQLite. Затем перезапустите Xray и выполните `proxy-manager reconcile`. Это предотвращает конфликт одинаковых UUID и сохраняет работоспособность существующей ссылки VLESS.

## Интеграция с `proxy-setup.sh`

`install/proxy-setup.sh` — полностью интегрированная версия предоставленного установочного скрипта. Bash по-прежнему отвечает за первоначальную настройку сервера (установку Xray/Hysteria, SSH, UFW/BBR, резервное копирование и логи), тогда как всё состояние учётных записей и повседневное управление пользователями находятся в Proxy Manager. `install/proxy-setup.integration.sh` остаётся сокращённым справочным примером точек интеграции.

Интегрированный установщик может установить Proxy Manager из готового бинарного файла (`PROXY_MANAGER_BIN=/path/to/proxy-manager`) или, при запуске из дерева исходников, собрать его из `PROXY_MANAGER_SOURCE` (по умолчанию — корень проекта). Для сборки из исходников требуются Go 1.23+, совместимые Node/npm, компилятор C и заголовочные файлы SQLite.

Существующие флаги сохраняют прежнее назначение:

- `-sni DOMAIN`: задаёт SNI одновременно для Xray и Hysteria.
- `-xsni DOMAIN`: переопределяет SNI только для Xray.
- `-hsni DOMAIN`: переопределяет SNI только для Hysteria.
- `-s`: сохраняет серверные ключи REALITY и TLS-сертификат/ключ Hysteria при повторных запусках.

Установщик должен вычислять итоговые значения в следующем порядке:

```bash
XRAY_SNI="$SNI"
HY_SNI="$SNI"
[[ -n "$XSNI" ]] && XRAY_SNI="$XSNI"
[[ -n "$HSNI" ]] && HY_SNI="$HSNI"
```

Маскировка Hysteria использует `HY_SNI`; отдельной настройки маскировки в интерфейсе нет.

### Безопасная первая миграция со старого установщика для одного пользователя

Если интегрированный установщик обнаруживает старый статический UUID Xray и/или `auth.type: password` в Hysteria, а в Proxy Manager ещё нет пользователей, он отказывается продолжать без флага `-s`. Это предотвращает случайную смену ключей REALITY или сертификата Hysteria, которая незаметно сделала бы старые ссылки недействительными. При использовании `-s` старые учётные данные импортируются в учётную запись `legacy` до замены конфигураций демонов.

В старом установщике Xray входящий обработчик не имел тега. Логика миграции принимает один VLESS-обработчик без тега как исходные данные старой установки; затем новая конфигурация Xray использует постоянный тег `vless-reality` и пустой статический список клиентов. Proxy Manager добавляет импортированный UUID в работающий Xray через HandlerService при синхронизации.

При новой установке без старого пользователя установщик создаёт одну начальную учётную запись `default`. При последующих запусках он сохраняет базу SQLite и не генерирует учётные данные пользователей заново.

Повторные запуски никогда не должны удалять `/var/lib/proxy-manager/proxy-manager.db` или менять ключи REALITY и TLS-материалы Hysteria как побочный эффект создания пользователей или управления ими.

## Проверка состояния

`GET /api/status` возвращает безопасные логические значения и метаданные о SQLite, Xray API, systemd-сервисе Xray, systemd-сервисе Hysteria, состоянии Proxy Manager, настроенных адресах/портах/SNI и количестве активных пользователей. Приватные ключи и пароли никогда не возвращаются.

Для локальных проверок установщика:

```bash
curl -f http://127.0.0.1:18081/internal/health
systemctl is-active proxy-manager xray hysteria-server
proxy-manager reconcile
```

## Тесты

Автоматические тесты проверяют миграции, создание дублирующихся учётных записей, включение и отключение учётных записей и протоколов, обновление учётных данных, разрешение/запрет авторизации Hysteria и поведение при отключённых пользователях, экранирование URL и синхронизацию Xray через тестовый адаптер.
