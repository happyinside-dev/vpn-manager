package telegrambot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"proxy-dev/vpn-manager/internal/traffic"
	"proxy-dev/vpn-manager/internal/users"
)

func formatBytes(n uint64) string {
	value := float64(n)
	units := []string{"Б", "КиБ", "МиБ", "ГиБ", "ТиБ", "ПиБ", "ЭиБ"}
	i := 0
	for value >= 1024 && i < len(units)-1 {
		value /= 1024
		i++
	}
	return fmt.Sprintf("%.2f %s", value, units[i])
}

func trafficNote(r traffic.Report) string {
	var s strings.Builder
	if r.Since.IsZero() {
		s.WriteString("\nСбор ещё не начался.")
	} else {
		fmt.Fprintf(&s, "\nУчёт с %s UTC. Сумма отправленного и полученного трафика Xray + Hysteria. Фоновое обновление раз в минуту; кнопка «Обновить» запрашивает свежие данные.", r.Since.UTC().Format("02.01.2006 15:04"))
	}
	for _, name := range []string{"Xray", "Hysteria"} {
		if updated := r.Updated[name]; !updated.IsZero() {
			fmt.Fprintf(&s, "\n%s: обновлено %s UTC", name, updated.UTC().Format("02.01 15:04"))
		}
	}
	if len(r.Unavailable) > 0 {
		s.WriteString("\n⚠️ Нет свежих данных: " + strings.Join(r.Unavailable, ", ") + ". Показаны сохранённые данные; проверь включение API статистики.")
	}
	return s.String()
}

func (b *Bot) refreshTraffic(ctx context.Context) {
	if err := b.traffic.Poll(ctx, time.Now().UTC()); err != nil {
		b.log.Warn("traffic refresh failed", "error", err)
	}
}

func trafficValues(r traffic.Report) (string, string) {
	total, week := formatBytes(r.Total), formatBytes(r.Week)
	if len(r.Unavailable) > 0 {
		if r.Total == 0 {
			total = "нет полных данных"
		}
		if r.Week == 0 {
			week = "нет полных данных"
		}
	}
	return total, week
}

func (b *Bot) sendTraffic(ctx context.Context, chatID int64, name string) {
	if b.traffic == nil {
		_ = b.sendMessage(ctx, chatID, "Сбор трафика не настроен.")
		return
	}
	b.refreshTraffic(ctx)
	r := b.traffic.Report(name, time.Now().UTC())
	b.log.Info("telegram traffic report", "account", name, "total_bytes", r.Total, "week_bytes", r.Week, "history_file", b.cfg.Traffic.File)
	total, week := trafficValues(r)
	text := fmt.Sprintf("📊 Статистика · %s\n\nС начала учёта: %s\nЗа последние 7 дней: %s\n%s", name, total, week, trafficNote(r))
	if r.Total == 0 && len(r.Unavailable) == 0 {
		text += "\nПосле начала учёта прирост трафика этого профиля ещё не зафиксирован. Первый снимок — начальная точка, его байты в историю не входят."
	}
	rows := [][]uiButton{{{Text: "🔄 Обновить", Callback: "traffic:" + name}}}
	if b.isMaster(chatID) {
		rows = append(rows, []uiButton{{Text: "⬅️ К пользователю", Callback: "user:" + name}}, []uiButton{{Text: "👥 Другой пользователь", Callback: "users"}})
	} else {
		rows = append(rows, []uiButton{{Text: "🔗 Мои ссылки", Callback: "links:" + name}})
	}
	rows = append(rows, []uiButton{{Text: "🏠 Главное меню", Callback: "menu"}})
	b.sendInline(ctx, chatID, text, rows)
}

func (b *Bot) sendTrafficOverview(ctx context.Context, chatID int64) {
	all, err := users.ListFile(b.cfg.UsersFile)
	if err != nil {
		_ = b.sendMessage(ctx, chatID, "Не удалось загрузить пользователей.")
		return
	}
	if b.traffic == nil {
		_ = b.sendMessage(ctx, chatID, "Сбор трафика не настроен.")
		return
	}
	b.refreshTraffic(ctx)
	now := time.Now().UTC()
	header := "📊 Трафик пользователей\nС начала учёта / последние 7 дней\n\n"
	text := header
	for _, u := range all {
		r := b.traffic.Report(u.Name, now)
		total, week := trafficValues(r)
		line := fmt.Sprintf("👤 %s: %s / %s\n", u.Name, total, week)
		if len([]rune(text+line)) > 3500 {
			_ = b.sendMessage(ctx, chatID, text)
			text = header
		}
		text += line
	}
	_ = b.sendMessage(ctx, chatID, text)
	_ = b.sendMessage(ctx, chatID, trafficNote(b.traffic.Report("", now)))
}
