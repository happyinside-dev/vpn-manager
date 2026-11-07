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
			{{"text": "🏠 Меню"}},
		}
	} else {
		rows = [][]map[string]string{
			{{"text": "🔗 Мои ссылки"}, {"text": "ℹ️ Статус"}},
			{{"text": "🔄 Новые секреты"}, {"text": "🏠 Меню"}},
		}
	}
	_ = b.postUI(ctx, chatID, "Выбери действие 👇", map[string]any{"keyboard": rows, "resize_keyboard": true, "is_persistent": true}, "")
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
		rows = append(rows, []uiButton{{Text: st + " " + u.Name, Callback: "user:" + u.Name}})
	}
	rows = append(rows, []uiButton{{Text: "➕ Создать", Callback: "add"}})
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
	b.sendInline(ctx, chatID, fmt.Sprintf("👤 %s\n%s\nTelegram привязан: %s", name, state, linked), [][]uiButton{
		{{Text: "🔗 Ссылки", Callback: "links:" + name}, {Text: "🔑 Новый код", Callback: "invite:" + name}},
		{{Text: "🔄 Сменить секреты", Callback: "confirm-rotate:" + name}},
		{{Text: toggle, Callback: act + ":" + name}, {Text: "🗑 Удалить", Callback: "confirm-delete:" + name}},
		{{Text: "🔓 Отвязать Telegram", Callback: "confirm-unbind:" + name}},
		{{Text: "⬅️ К списку", Callback: "users"}},
	})
}
func (b *Bot) sendPlatformMenu(ctx context.Context, chatID int64, name string) {
	b.sendInline(ctx, chatID, "📲 Выбери устройство. Для него придут QR-код, Source и готовая Import-ссылка:", [][]uiButton{
		{{Text: "🤖 Android", Callback: "platform:" + name + ":android"}, {Text: "🍎 iPhone/iPad", Callback: "platform:" + name + ":ios"}},
		{{Text: "💻 macOS", Callback: "platform:" + name + ":macos"}, {Text: "🐧 Linux", Callback: "platform:" + name + ":linux"}},
		{{Text: "📺 Android TV", Callback: "platform:" + name + ":tv"}, {Text: "🌐 Universal", Callback: "platform:" + name + ":universal"}},
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
	if strings.HasPrefix(m.Text, "/") {
		return false
	}
	b.mu.Lock()
	delete(b.pending, m.Chat.ID)
	b.mu.Unlock()
	if !b.isMaster(m.From.ID) {
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
	if action == "users" && master {
		b.sendUsersMenu(ctx, chat)
		return
	}
	if action == "add" && master {
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
	if !master && u.TelegramID != c.From.ID {
		return
	}
	switch action {
	case "user":
		if master {
			b.sendUserCard(ctx, chat, account)
		}
	case "links":
		b.sendPlatformMenu(ctx, chat, account)
	case "alllinks":
		b.sendAllLinks(ctx, chat, *u)
	case "platform":
		if len(data) != 3 {
			return
		}
		variants := map[string]struct{ label, suffix, title string }{
			"android": {"Android", "/android", " Android"}, "ios": {"iPhone / iPad", "/ios", " iOS"}, "macos": {"macOS", "/macos", " macOS"}, "linux": {"Linux", "/linux", " Linux"}, "tv": {"Android TV / IPTV", "/tv", " TV"}, "universal": {"Universal", "", ""},
		}
		v, ok := variants[data[2]]
		if !ok {
			return
		}
		b.sendVariant(ctx, chat, *u, v.label, v.suffix, b.cfg.ProfileName+v.title)
	case "invite":
		if !master {
			return
		}
		code, e := b.createInvite(account)
		if e != nil {
			_ = b.sendMessage(ctx, chat, "Не удалось создать код.")
			return
		}
		b.sendInvitation(ctx, chat, account, code)
	case "confirm-delete":
		if master {
			b.sendInline(ctx, chat, "⚠️ Точно удалить профиль "+account+"? Все ссылки перестанут работать.", [][]uiButton{{{Text: "🗑 Подтвердить удаление", Callback: "delete:" + account}}, {{Text: "Отмена", Callback: "user:" + account}}})
		}
	case "confirm-rotate":
		b.sendInline(ctx, chat, "⚠️ Перегенерировать UUID и Hysteria пароль для "+account+"? Ссылка подписки сохранится, но устройства потребуется обновить и переподключить.", [][]uiButton{{{Text: "🔄 Подтвердить", Callback: "rotate:" + account}}, {{Text: "Отмена", Callback: "user:" + account}}})
	case "confirm-unbind":
		if master {
			b.sendInline(ctx, chat, "Отвязать Telegram от "+account+"?", [][]uiButton{{{Text: "Подтвердить", Callback: "unbind:" + account}}, {{Text: "Отмена", Callback: "user:" + account}}})
		}
	case "delete", "enable", "disable", "unbind":
		if !master {
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
		if master {
			b.handleMaster(ctx, Message{Chat: Chat{ID: chat}, From: c.From}, "/rotate", []string{account})
		} else {
			b.handleUser(ctx, Message{Chat: Chat{ID: chat}, From: c.From}, *u, "/rotate", nil)
		}
	}
}
