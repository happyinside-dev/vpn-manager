package telegrambot

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"proxy-dev/vpn-manager/internal/model"
	"proxy-dev/vpn-manager/internal/users"
)

type pendingAction struct {
	Kind, Account string
	Until         time.Time
}
type uiButton struct {
	Text     string `json:"text"`
	Callback string `json:"callback_data,omitempty"`
	URL      string `json:"url,omitempty"`
}
type inlineKeyboard struct {
	Rows [][]uiButton `json:"inline_keyboard"`
}

var nameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,32}$`)

func validAccountName(s string) bool { return nameRE.MatchString(s) }
func looksLikeInvite(s string) bool {
	return regexp.MustCompile(`(?i)^[a-f0-9]{5}-[a-f0-9]{5}$`).MatchString(s)
}
func masterShortcut(s string) string {
	switch strings.TrimSpace(s) {
	case "📊 Статистика", "📊 Трафик":
		return "/traffic"
	case "👥 Пользователи":
		return "/users"
	case "➕ Создать профиль":
		return "/add"
	case "🖥 Сервер":
		return "/status"
	case "🧪 Self-test":
		return "/selftest"
	case "🏠 Меню":
		return "/menu"
	}
	return ""
}
func userShortcut(s string) string {
	switch strings.TrimSpace(s) {
	case "📊 Моя статистика", "📊 Мой трафик":
		return "/traffic"
	case "🔗 Мои ссылки":
		return "/platforms"
	case "🔄 Новые секреты":
		return "/rotate_prompt"
	case "ℹ️ Статус":
		return "/status"
	case "🏠 Меню":
		return "/menu"
	}
	return ""
}
func (b *Bot) postUI(ctx context.Context, chatID int64, text string, markup any, parseMode string) error {
	values := url.Values{"chat_id": {strconv.FormatInt(chatID, 10)}, "text": {text}}
	if parseMode != "" {
		values.Set("parse_mode", parseMode)
	}
	if markup != nil {
		data, err := json.Marshal(markup)
		if err != nil {
			return err
		}
		values.Set("reply_markup", string(data))
	}
	req, err := http.NewRequestWithContext(ctx, "POST", b.api("sendMessage"), strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("telegram UI send failed (%d)", resp.StatusCode)
	}
	return nil
}
func (b *Bot) sendMenu(ctx context.Context, chatID int64, master bool) {
	rows := [][]map[string]string{}
	if master {
		rows = [][]map[string]string{
			{{"text": "👥 Пользователи"}, {"text": "➕ Создать профиль"}},
			{{"text": "🖥 Сервер"}, {"text": "🧪 Self-test"}},
			{{"text": "📊 Статистика"}, {"text": "🏠 Меню"}},
		}
	} else {
		rows = [][]map[string]string{
			{{"text": "🔗 Мои ссылки"}, {"text": "ℹ️ Статус"}},
			{{"text": "📊 Моя статистика"}},
			{{"text": "🔄 Новые секреты"}, {"text": "🏠 Меню"}},
		}
	}
	if !master {
		if u, err := users.FindByTelegramIDFile(b.cfg.UsersFile, chatID); err == nil {
			if !u.CanRotate {
				rows[2] = []map[string]string{{"text": "🏠 Меню"}}
			}
			if u.Enabled && u.CanManageUsers {
				rows = append(rows, []map[string]string{{"text": "👥 Пользователи"}, {"text": "➕ Создать профиль"}})
			}
		}
	}
	menuText := "Выбери действие 👇"
	if master {
		all, err := users.ListFile(b.cfg.UsersFile)
		if err == nil {
			var overview strings.Builder
			overview.WriteString("👥 Создатели профилей\n")
			groups := map[int64][]string{}
			order := []int64{}
			for _, u := range all {
				if _, ok := groups[u.CreatedBy]; !ok {
					order = append(order, u.CreatedBy)
				}
				groups[u.CreatedBy] = append(groups[u.CreatedBy], u.Name)
			}
			for _, id := range order {
				label := "Администратор / ранее созданные"
				if id != 0 {
					label = "Telegram " + strconv.FormatInt(id, 10)
					for _, u := range all {
						if u.TelegramID == id {
							label = u.Name
							break
						}
					}
				}
				fmt.Fprintf(&overview, "%s → %s\n", label, strings.Join(groups[id], ", "))
			}
			menuText = overview.String() + "\nВыбери действие 👇"
		}
	}
	if err := b.postUI(ctx, chatID, menuText, map[string]any{"keyboard": rows, "resize_keyboard": true, "is_persistent": true, "one_time_keyboard": false}, ""); err == nil {
		b.mu.Lock()
		if b.navigation == nil {
			b.navigation = make(map[int64]bool)
		}
		b.navigation[chatID] = master
		b.mu.Unlock()
	}
}

// Install the persistent keyboard on the first authorized interaction, including
// commands and buttons left in the chat before the bot was upgraded.
func (b *Bot) ensureNavigation(ctx context.Context, chatID int64, master bool) {
	b.mu.Lock()
	role, ready := b.navigation[chatID]
	b.mu.Unlock()
	if !ready || role != master {
		b.sendMenu(ctx, chatID, master)
	}
}

func (b *Bot) sendInline(ctx context.Context, chatID int64, text string, rows [][]uiButton) {
	if err := b.postUI(ctx, chatID, text, inlineKeyboard{Rows: rows}, ""); err != nil {
		b.log.Warn("telegram inline keyboard failed", "error", err.Error())
	}
}
func (b *Bot) sendUsersMenu(ctx context.Context, chatID int64) {
	all, err := users.ListFile(b.cfg.UsersFile)
	if err != nil {
		_ = b.sendMessage(ctx, chatID, "Не удалось загрузить список пользователей.")
		return
	}
	rows := make([][]uiButton, 0, len(all)+1)
	for _, u := range all {
		st := "🟢"
		if !u.Enabled {
			st = "🔴"
		}
		if !b.isMaster(chatID) && !b.owns(chatID, u.Name) {
			continue
		}
		creator := "администратор / ранее создан"
		if u.CreatedBy != 0 {
			creator = strconv.FormatInt(u.CreatedBy, 10)
			for _, parent := range all {
				if parent.TelegramID == u.CreatedBy {
					creator = parent.Name
					break
				}
			}
		}
		rows = append(rows, []uiButton{{Text: st + " " + u.Name + " · создал: " + creator, Callback: "user:" + u.Name}})
	}
	rows = append(rows, []uiButton{{Text: "➕ Создать", Callback: "add"}}, []uiButton{{Text: "🏠 Главное меню", Callback: "menu"}})
	b.sendInline(ctx, chatID, "👥 Пользователи — выбери профиль:", rows)
}
func (b *Bot) sendUserCard(ctx context.Context, chatID int64, name string) {
	u, err := users.FindByNameFile(b.cfg.UsersFile, name)
	if err != nil {
		_ = b.sendMessage(ctx, chatID, "Профиль не найден.")
		return
	}
	state := "🟢 Активен"
	toggle := "🚫 Выключить"
	act := "disable"
	if !u.Enabled {
		state = "🔴 Отключён"
		toggle = "✅ Включить"
		act = "enable"
	}
	linked := "нет"
	if u.TelegramID != 0 {
		linked = "да"
	}
	text := fmt.Sprintf("👤 %s\n%s\nTelegram привязан: %s", name, state, linked)
	rows := [][]uiButton{
		{{Text: "🔗 Ссылки", Callback: "links:" + name}, {Text: "🔑 Новый код", Callback: "invite:" + name}},
		{{Text: "📊 Статистика", Callback: "traffic:" + name}},
		{{Text: "🔄 Сменить секреты", Callback: "confirm-rotate:" + name}},
		{{Text: toggle, Callback: act + ":" + name}, {Text: "🗑 Удалить", Callback: "confirm-delete:" + name}},
		{{Text: "🔓 Отвязать Telegram", Callback: "confirm-unbind:" + name}},
		{{Text: "⬅️ К списку", Callback: "users"}},
	}
	if b.isMaster(chatID) {
		text += fmt.Sprintf("\nСмена секретов: %t\nСоздание и управление: %t\nСоздатель Telegram ID: %d", u.CanRotate, u.CanManageUsers, u.CreatedBy)
		rows = append(rows, []uiButton{{Text: "Секреты: разрешить", Callback: "permission:" + name + ":rotate:on"}, {Text: "Запретить", Callback: "permission:" + name + ":rotate:off"}}, []uiButton{{Text: "Управление: разрешить", Callback: "permission:" + name + ":manage:on"}, {Text: "Запретить", Callback: "permission:" + name + ":manage:off"}})
	}
	b.sendInline(ctx, chatID, text, rows)
}
func (b *Bot) sendPlatformMenu(ctx context.Context, chatID int64, name string) {
	b.sendInline(ctx, chatID, "📲 Выбери устройство, затем режим маршрутизации:", [][]uiButton{
		{{Text: "🤖 Android", Callback: "platform:" + name + ":android"}, {Text: "🍎 iPhone/iPad", Callback: "platform:" + name + ":ios"}},
		{{Text: "💻 macOS", Callback: "platform:" + name + ":macos"}, {Text: "🐧 Linux", Callback: "platform:" + name + ":linux"}},
		{{Text: "📺 Android TV", Callback: "platform:" + name + ":tv"}, {Text: "🌐 Universal", Callback: "platform:" + name + ":universal"}},
		{{Text: "📡 OpenWRT", Callback: "platform:" + name + ":openwrt"}},
		{{Text: "📦 Все платформы", Callback: "alllinks:" + name}},
	})
}
func (b *Bot) sendInvitation(ctx context.Context, chatID int64, name, code string) {
	msg := fmt.Sprintf("🔑 Код привязки для %s (10 минут):\n<pre>%s</pre>\n\nОтправь этот код пользователю. Он может просто прислать код боту сообщением, либо /start %s.\n\n⚠️ Новый код отменяет предыдущий.", html.EscapeString(name), html.EscapeString(code), html.EscapeString(code))
	_ = b.postUI(ctx, chatID, msg, inlineKeyboard{Rows: [][]uiButton{{{Text: "🔄 Выдать новый код", Callback: "invite:" + name}}, {{Text: "⬅️ К профилю", Callback: "user:" + name}}}}, "HTML")
}
func (b *Bot) startPrompt(ctx context.Context, chatID int64, kind, account, prompt string) {
	b.mu.Lock()
	b.pending[chatID] = pendingAction{Kind: kind, Account: account, Until: time.Now().Add(3 * time.Minute)}
	b.mu.Unlock()
	b.sendInline(ctx, chatID, prompt, [][]uiButton{{{Text: "✖️ Отмена", Callback: "cancel"}}})
}
func (b *Bot) handlePending(ctx context.Context, m Message) bool {
	b.mu.Lock()
	p, ok := b.pending[m.Chat.ID]
	if ok && time.Now().After(p.Until) {
		delete(b.pending, m.Chat.ID)
		ok = false
	}
	b.mu.Unlock()
	if !ok {
		return false
	}
	if strings.TrimSpace(m.Text) == "/cancel" {
		b.mu.Lock()
		delete(b.pending, m.Chat.ID)
		b.mu.Unlock()
		_ = b.sendMessage(ctx, m.Chat.ID, "Отменено.")
		return true
	}
	// Navigation cancels text entry so a menu button cannot become an account name.
	if strings.HasPrefix(m.Text, "/") || masterShortcut(m.Text) != "" || userShortcut(m.Text) != "" {
		b.mu.Lock()
		delete(b.pending, m.Chat.ID)
		b.mu.Unlock()
		return false
	}
	b.mu.Lock()
	delete(b.pending, m.Chat.ID)
	b.mu.Unlock()
	if !b.isMaster(m.From.ID) && !b.canManage(m.From.ID) {
		return true
	}
	if p.Kind == "add" {
		if !validAccountName(strings.TrimSpace(m.Text)) {
			_ = b.sendMessage(ctx, m.Chat.ID, "Некорректное имя. Используй /add NAME (латиница/цифры/_/-).")
			return true
		}
		b.handleMaster(ctx, m, "/add", []string{strings.TrimSpace(m.Text)})
		return true
	}
	return false
}
func (b *Bot) answerCallback(ctx context.Context, id string) {
	v := url.Values{"callback_query_id": {id}}
	req, err := http.NewRequestWithContext(ctx, "POST", b.api("answerCallbackQuery"), strings.NewReader(v.Encode()))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := b.client.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}
func (b *Bot) handleCallback(ctx context.Context, c CallbackQuery) {
	b.answerCallback(ctx, c.ID)
	if c.Message == nil || c.Message.Chat.ID != c.From.ID {
		return
	}
	chat := c.Message.Chat.ID
	data := strings.Split(c.Data, ":")
	action := data[0]
	master := b.isMaster(c.From.ID)
	// Explicitly re-check permissions for every callback; old buttons never grant access.
	var account string
	if len(data) > 1 {
		account = data[1]
	}
	if action == "cancel" {
		b.mu.Lock()
		delete(b.pending, chat)
		b.mu.Unlock()
		_ = b.sendMessage(ctx, chat, "Отменено.")
		return
	}
	if action == "menu" {
		if !master {
			if _, err := users.FindByTelegramIDFile(b.cfg.UsersFile, c.From.ID); err != nil {
				return
			}
		}
		b.mu.Lock()
		delete(b.pending, chat)
		b.mu.Unlock()
		b.sendMenu(ctx, chat, master)
		return
	}
	if action == "users" && (master || b.canManage(c.From.ID)) {
		b.ensureNavigation(ctx, chat, true)
		b.sendUsersMenu(ctx, chat)
		return
	}
	if action == "add" && (master || b.canManage(c.From.ID)) {
		b.ensureNavigation(ctx, chat, true)
		b.startPrompt(ctx, chat, "add", "", "Введи имя нового профиля:")
		return
	}
	if account == "" || !validAccountName(account) {
		return
	}
	u, err := users.FindByNameFile(b.cfg.UsersFile, account)
	if err != nil {
		_ = b.sendMessage(ctx, chat, "Профиль не найден или уже удалён.")
		return
	}
	owner := b.owns(c.From.ID, account)
	if action == "permission" {
		if master && len(data) == 4 {
			b.handleMaster(ctx, Message{Chat: Chat{ID: chat}, From: c.From}, "/permissions", data[1:])
		}
		return
	}
	if !master && !owner && u.TelegramID != c.From.ID {
		return
	}
	b.ensureNavigation(ctx, chat, master)
	switch action {
	case "traffic":
		b.sendTraffic(ctx, chat, account)
	case "user":
		if master || owner {
			b.sendUserCard(ctx, chat, account)
		}
	case "links":
		b.sendPlatformMenu(ctx, chat, account)
	case "alllinks":
		b.sendRoutingMenu(ctx, chat, account, "all")
	case "platform":
		if len(data) == 3 {
			if _, ok := platformVariants[data[2]]; ok {
				b.sendRoutingMenu(ctx, chat, account, data[2])
			}
		}
	case "route":
		if len(data) != 4 || (data[3] != "bypass-ru" && data[3] != "blocked") {
			return
		}
		if data[2] == "all" {
			for _, platform := range platformOrder {
				b.sendRoutedVariant(ctx, chat, *u, platform, data[3])
			}
		} else {
			b.sendRoutedVariant(ctx, chat, *u, data[2], data[3])
		}

	case "invite":
		if !master && !owner {
			return
		}
		code, e := b.createInvite(account)
		if e != nil {
			_ = b.sendMessage(ctx, chat, "Не удалось создать код.")
			return
		}
		b.sendInvitation(ctx, chat, account, code)
	case "confirm-delete":
		if master || owner {
			b.sendInline(ctx, chat, "⚠️ Точно удалить профиль "+account+"? Все ссылки перестанут работать.", [][]uiButton{{{Text: "🗑 Подтвердить удаление", Callback: "delete:" + account}}, {{Text: "Отмена", Callback: "user:" + account}}})
		}
	case "confirm-rotate":
		if !master && !owner && !u.CanRotate {
			return
		}
		b.sendInline(ctx, chat, "⚠️ Перегенерировать UUID и Hysteria пароль для "+account+"? Ссылка подписки сохранится, но устройства потребуется обновить и переподключить.", [][]uiButton{{{Text: "🔄 Подтвердить", Callback: "rotate:" + account}}, {{Text: "Отмена", Callback: "user:" + account}}})
	case "confirm-unbind":
		if master || owner {
			b.sendInline(ctx, chat, "Отвязать Telegram от "+account+"?", [][]uiButton{{{Text: "Подтвердить", Callback: "unbind:" + account}}, {{Text: "Отмена", Callback: "user:" + account}}})
		}
	case "delete", "enable", "disable", "unbind":
		if !master && !owner {
			return
		}
		b.handleMaster(ctx, Message{Chat: Chat{ID: chat}, From: c.From}, "/"+action, func() []string {
			if action == "delete" {
				return []string{account, "CONFIRM"}
			}
			return []string{account}
		}())
		b.sendUsersMenu(ctx, chat)
	case "rotate":
		if master || owner {
			b.handleMaster(ctx, Message{Chat: Chat{ID: chat}, From: c.From}, "/rotate", []string{account})
		} else {
			b.handleUser(ctx, Message{Chat: Chat{ID: chat}, From: c.From}, *u, "/rotate", nil)
		}
	}
}

var platformOrder = []string{"universal", "android", "ios", "macos", "linux", "tv", "openwrt"}
var platformVariants = map[string]string{
	"universal": "Universal", "android": "Android", "ios": "iPhone / iPad", "macos": "macOS", "linux": "Linux", "tv": "Android TV / IPTV", "openwrt": "OpenWRT",
}

func (b *Bot) sendRoutingMenu(ctx context.Context, chatID int64, name, platform string) {
	b.sendInline(ctx, chatID, "Выбери режим:\n\n🌍 Всё через VPN, кроме российских сайтов.\n🎯 Всё напрямую, кроме ресурсов из обновляемого списка блокировок.\n\nВыбор закреплён в ссылке: разные устройства могут использовать разные режимы.", [][]uiButton{
		{{Text: "🌍 VPN, кроме РФ", Callback: "route:" + name + ":" + platform + ":bypass-ru"}},
		{{Text: "🎯 Только заблокированные", Callback: "route:" + name + ":" + platform + ":blocked"}},
		{{Text: "⬅️ Устройства", Callback: "links:" + name}},
	})
}

func (b *Bot) sendRoutedVariant(ctx context.Context, chatID int64, u model.User, platform, mode string) {
	label, ok := platformVariants[platform]
	if !ok {
		return
	}
	modeLabel := "VPN, кроме РФ"
	if mode == "blocked" {
		modeLabel = "Только заблокированные"
	}
	b.sendVariant(ctx, chatID, u, label+" · "+modeLabel, "/"+platform+"/"+mode, b.cfg.ProfileName+" "+label+" · "+modeLabel)
	if platform == "openwrt" {
		_ = b.sendMessage(ctx, chatID, "OpenWRT: Source отдаёт JSON для sing-box (TUN + auto_redirect/fw4). Сохрани его в /etc/sing-box/config.json и проверь: sing-box check -c /etc/sing-box/config.json. Нужны kmod-tun, nftables/fw4 и sing-box с поддержкой выбранного протокола. DNS клиентов LAN должен идти через роутер. Инструкция по установке и DNS — в ROUTING.md комплекта.")
	}
}
