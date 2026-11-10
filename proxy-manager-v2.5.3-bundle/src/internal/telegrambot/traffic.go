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
		fmt.Fprintf(&s, "\nУчёт с %s UTC. Сумма отправленного и полученного трафика Xray + Hysteria. Обновление раз в минуту.", r.Since.UTC().Format("02.01.2006 15:04"))
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

func (b *Bot) sendTraffic(ctx context.Context, chatID int64, name string) {
	if b.traffic == nil {
		_ = b.sendMessage(ctx, chatID, "Сбор трафика не настроен.")
		return
	}
	r := b.traffic.Report(name, time.Now().UTC())
	text := fmt.Sprintf("📊 Трафик · %s\n\nЗа всё время: %s\nЗа последние 7 дней: %s\n%s", name, formatBytes(r.Total), formatBytes(r.Week), trafficNote(r))
	b.sendInline(ctx, chatID, text, [][]uiButton{{{Text: "🔄 Обновить", Callback: "traffic:" + name}}})
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
	now := time.Now().UTC()
	header := "📊 Трафик пользователей\nЗа всё время / последние 7 дней\n\n"
	text := header
	for _, u := range all {
		r := b.traffic.Report(u.Name, now)
		line := fmt.Sprintf("👤 %s: %s / %s\n", u.Name, formatBytes(r.Total), formatBytes(r.Week))
		if len([]rune(text+line)) > 3500 {
			_ = b.sendMessage(ctx, chatID, text)
			text = header
		}
		text += line
	}
	_ = b.sendMessage(ctx, chatID, text)
	_ = b.sendMessage(ctx, chatID, trafficNote(b.traffic.Report("", now)))
}
