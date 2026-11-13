package telegrambot

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"proxy-dev/vpn-manager/internal/importurl"
	"proxy-dev/vpn-manager/internal/model"
	"proxy-dev/vpn-manager/internal/traffic"
	"proxy-dev/vpn-manager/internal/users"
)

type Config struct {
	Traffic     traffic.Config `json:"traffic"`
	TokenFile   string         `json:"token_file"`
	Masters     []int64        `json:"masters"`
	UsersFile   string         `json:"users_file"`
	ManagerPath string         `json:"manager_path"`
	BaseURL     string         `json:"base_url"`
	ProfileName string         `json:"profile_name"`
	InvitesFile string         `json:"invites_file"`
	AuditLog    string         `json:"audit_log"`
}

type Bot struct {
	traffic    *traffic.Collector
	cfg        Config
	token      string
	client     *http.Client
	log        *slog.Logger
	masters    map[int64]struct{}
	mu         sync.Mutex
	pending    map[int64]pendingAction
	navigation map[int64]bool
}

type updateResponse struct {
	OK     bool     `json:"ok"`
	Result []Update `json:"result"`
}

type Update struct {
	UpdateID int64          `json:"update_id"`
	Message  *Message       `json:"message"`
	Callback *CallbackQuery `json:"callback_query"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	Chat      Chat   `json:"chat"`
	From      User   `json:"from"`
	Text      string `json:"text"`
}

type Chat struct {
	ID int64 `json:"id"`
}
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type invite struct {
	Code      string    `json:"code"`
	User      string    `json:"user"`
	ExpiresAt time.Time `json:"expires_at"`
}

type inviteFile struct {
	Invites []invite `json:"invites"`
}

func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parse telegram config: %w", err)
	}
	if c.TokenFile == "" {
		c.TokenFile = "/etc/vpn-manager/telegram-token"
	}
	if c.UsersFile == "" {
		c.UsersFile = "/etc/vpn-manager/users.yaml"
	}
	if c.ManagerPath == "" {
		c.ManagerPath = "/usr/local/sbin/proxy-manager"
	}
	if c.ProfileName == "" {
		c.ProfileName = "Proxy VPN"
	}
	if c.InvitesFile == "" {
		c.InvitesFile = "/etc/vpn-manager/telegram-invites.json"
	}
	if c.AuditLog == "" {
		c.AuditLog = "/var/log/proxy-manager/telegram-audit.log"
	}
	if len(c.Masters) == 0 {
		return Config{}, errors.New("telegram config has no master ids")
	}
	if !strings.HasPrefix(c.BaseURL, "https://") {
		return Config{}, errors.New("telegram base_url must use https")
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	return c, nil
}

func New(cfg Config, log *slog.Logger) (*Bot, error) {
	b, err := os.ReadFile(cfg.TokenFile)
	if err != nil {
		return nil, fmt.Errorf("read telegram token: %w", err)
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return nil, errors.New("empty telegram token")
	}
	cfg.Traffic.Defaults(cfg.UsersFile)
	collector, err := traffic.New(cfg.Traffic)
	if err != nil {
		return nil, fmt.Errorf("load traffic history: %w", err)
	}
	masters := map[int64]struct{}{}
	for _, id := range cfg.Masters {
		masters[id] = struct{}{}
	}
	return &Bot{traffic: collector, cfg: cfg, token: token, client: &http.Client{Timeout: 70 * time.Second}, log: log, masters: masters, pending: make(map[int64]pendingAction)}, nil
}

func (b *Bot) Run(ctx context.Context) error {
	var offset int64
	if b.traffic != nil {
		pollCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() {
			poll := func() {
				if err := b.traffic.Poll(pollCtx, time.Now().UTC()); err != nil && pollCtx.Err() == nil {
					b.log.Warn("traffic collection failed", "error", err)
				}
			}
			poll()
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-pollCtx.Done():
					return
				case <-ticker.C:
					poll()
				}
			}
		}()
	}
	b.log.Info("telegram bot started", "users_file", b.cfg.UsersFile, "traffic_file", b.cfg.Traffic.File)
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		updates, err := b.getUpdates(ctx, offset)
		if err != nil {
			b.log.Warn("telegram getUpdates failed", "error", b.redactToken(err.Error()))
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
			}
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			if u.Callback != nil {
				b.handleCallback(ctx, *u.Callback)
				continue
			}
			if u.Message == nil || u.Message.Text == "" {
				continue
			}
			b.handleMessage(ctx, *u.Message)
		}
	}
}

func (b *Bot) getUpdates(ctx context.Context, offset int64) ([]Update, error) {
	v := url.Values{}
	v.Set("timeout", "50")
	v.Set("offset", strconv.FormatInt(offset, 10))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.api("getUpdates")+"?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out updateResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("telegram getUpdates returned ok=false")
	}
	return out.Result, nil
}

func (b *Bot) api(method string) string {
	return "https://api.telegram.org/bot" + b.token + "/" + method
}

func (b *Bot) isMaster(id int64) bool { _, ok := b.masters[id]; return ok }

func splitCommand(s string) (string, []string) {
	f := strings.Fields(strings.TrimSpace(s))
	if len(f) == 0 {
		return "", nil
	}
	cmd := strings.ToLower(strings.SplitN(f[0], "@", 2)[0])
	return cmd, f[1:]
}

func (b *Bot) handleMessage(ctx context.Context, m Message) {
	if m.Chat.ID != m.From.ID {
		_ = b.sendMessage(ctx, m.Chat.ID, "🔒 Открой личный чат с ботом для управления VPN.")
		return
	}
	if b.handlePending(ctx, m) {
		return
	}
	cmd, args := splitCommand(m.Text)
	if cmd == "" {
		return
	}
	if b.isMaster(m.From.ID) {
		if shortcut := masterShortcut(m.Text); shortcut != "" {
			cmd = shortcut
			args = nil
		}
		b.handleMaster(ctx, m, cmd, args)
		return
	}
	if cmd == "/start" && len(args) == 1 || (cmd != "" && !strings.HasPrefix(cmd, "/") && len(args) == 0 && looksLikeInvite(cmd)) {
		code := cmd
		if len(args) == 1 {
			code = args[0]
		}
		if err := b.consumeInvite(m.From.ID, code); err == nil {
			_ = b.sendMessage(ctx, m.Chat.ID, "✅ Telegram привязан к VPN-профилю! Выбери действие ниже 👇")
			b.audit(m.From.ID, "user", "bind", "self", "success")
			b.sendMenu(ctx, m.Chat.ID, false)
			return
		}
	}
	u, err := users.FindByTelegramIDFile(b.cfg.UsersFile, m.From.ID)
	if err != nil {
		_ = b.sendMessage(ctx, m.Chat.ID, "🔒 Доступ пока не привязан. Попроси администратора прислать одноразовый код и отправь его сюда (можно просто кодом, без /start).")
		return
	}
	if shortcut := userShortcut(m.Text); shortcut != "" {
		cmd = shortcut
		args = nil
	}
	b.handleUser(ctx, m, *u, cmd, args)
}

func (b *Bot) handleUser(ctx context.Context, m Message, u model.User, cmd string, args []string) {
	if cmd != "/start" && cmd != "/help" && cmd != "/menu" {
		b.ensureNavigation(ctx, m.Chat.ID, b.isMaster(m.From.ID))
	}
	switch cmd {
	case "/start", "/help", "/menu":
		_ = b.sendMessage(ctx, m.Chat.ID, "👋 Proxy VPN\n\n/links — все ссылки и QR\n/android — Android\n/ios — iPhone / iPad\n/macos — macOS\n/linux — Linux\n/tv — Android TV / IPTV\n/openwrt — роутер OpenWRT\n/universal — универсальный профиль\n/traffic — мой трафик\n/status — статус профиля\n/rotate — новые VPN-секреты\n/rotate_token CONFIRM — новый subscription token")
		b.sendMenu(ctx, m.Chat.ID, false)
	case "/traffic":
		b.sendTraffic(ctx, m.Chat.ID, u.Name)
	case "/platforms":
		b.sendPlatformMenu(ctx, m.Chat.ID, u.Name)
	case "/links", "/alllinks":
		b.sendAllLinks(ctx, m.Chat.ID, u)
	case "/android", "/ios", "/macos", "/linux", "/tv", "/universal", "/openwrt":
		b.sendRoutingMenu(ctx, m.Chat.ID, u.Name, strings.TrimPrefix(cmd, "/"))
	case "/status":
		state := "🟢 enabled"
		if !u.Enabled {
			state = "🔴 disabled"
		}
		_ = b.sendMessage(ctx, m.Chat.ID, fmt.Sprintf("👤 %s\nStatus: %s\nTelegram: linked", u.Name, state))
	case "/rotate_prompt":
		b.sendInline(ctx, m.Chat.ID, "⚠️ Сменить VPN-секреты? Профиль останется по прежнему URL, но потребуется обновить его в sing-box и переподключиться.", [][]uiButton{{{Text: "🔄 Подтвердить", Callback: "rotate:" + u.Name}}, {{Text: "Отмена", Callback: "links:" + u.Name}}})
	case "/rotate":
		if err := b.runManager(ctx, "account", "rotate", u.Name); err != nil {
			_ = b.sendMessage(ctx, m.Chat.ID, "❌ Не удалось перегенерировать VPN-секреты: "+safeErr(err))
			return
		}
		b.audit(m.From.ID, "user", "rotate_credentials", u.Name, "success")
		_ = b.sendMessage(ctx, m.Chat.ID, "✅ VPN-секреты перегенерированы. Subscription URL не изменился. Обнови Remote Profile и переподключись.")
	case "/rotate_token":
		if len(args) != 1 || strings.ToUpper(args[0]) != "CONFIRM" {
			_ = b.sendMessage(ctx, m.Chat.ID, "⚠️ Это инвалидирует старые subscription URL на всех твоих устройствах.\n\nДля подтверждения: /rotate_token CONFIRM")
			return
		}
		if _, err := users.RotateTokenFile(b.cfg.UsersFile, u.Name); err != nil {
			_ = b.sendMessage(ctx, m.Chat.ID, "❌ Не удалось сменить token.")
			return
		}
		b.audit(m.From.ID, "user", "rotate_token", u.Name, "success")
		nu, _ := users.FindByNameFile(b.cfg.UsersFile, u.Name)
		_ = b.sendMessage(ctx, m.Chat.ID, "✅ Subscription token заменён. Старые URL больше не работают.")
		if nu != nil {
			b.sendAllLinks(ctx, m.Chat.ID, *nu)
		}
	default:
		_ = b.sendMessage(ctx, m.Chat.ID, "Не понял команду. Выбери действие в меню 👇")
		b.sendMenu(ctx, m.Chat.ID, false)
	}
}

func (b *Bot) handleMaster(ctx context.Context, m Message, cmd string, args []string) {
	if cmd != "/start" && cmd != "/help" && cmd != "/menu" {
		b.ensureNavigation(ctx, m.Chat.ID, b.isMaster(m.From.ID))
	}
	switch cmd {
	case "/traffic":
		if len(args) > 1 {
			_ = b.sendMessage(ctx, m.Chat.ID, "Используй /traffic или /traffic NAME")
			return
		}
		if len(args) == 1 {
			if _, err := users.FindByNameFile(b.cfg.UsersFile, args[0]); err != nil {
				_ = b.sendMessage(ctx, m.Chat.ID, "Профиль не найден.")
				return
			}
			b.sendTraffic(ctx, m.Chat.ID, args[0])
		} else {
			b.sendTrafficOverview(ctx, m.Chat.ID)
		}

	case "/start", "/help", "/menu":
		_ = b.sendMessage(ctx, m.Chat.ID, "👑 Proxy VPN master\n\n/users\n/add NAME\n/delete NAME CONFIRM\n/enable NAME\n/disable NAME\n/rotate NAME\n/rotate_token NAME CONFIRM\n/invite NAME\n/unbind NAME\n/links NAME\n/traffic [NAME] — трафик\n/status\n/selftest\n/xray\n/hysteria\n\nДля управления нажимай кнопки ниже.")
		b.sendMenu(ctx, m.Chat.ID, true)
	case "/users":
		if len(args) == 0 {
			b.sendUsersMenu(ctx, m.Chat.ID)
			return
		}
		all, err := users.ListFile(b.cfg.UsersFile)
		if err != nil {
			_ = b.sendMessage(ctx, m.Chat.ID, "❌ "+safeErr(err))
			return
		}
		var s strings.Builder
		s.WriteString("👥 Users\n")
		for _, u := range all {
			st := "🟢"
			if !u.Enabled {
				st = "🔴"
			}
			tg := ""
			if u.TelegramID != 0 {
				tg = " · Telegram linked"
			}
			fmt.Fprintf(&s, "%s %s%s\n", st, u.Name, tg)
		}
		_ = b.sendMessage(ctx, m.Chat.ID, s.String())
	case "/add":
		if len(args) == 0 {
			b.startPrompt(ctx, m.Chat.ID, "add", "", "Введи имя нового VPN-профиля (латиница, цифры, дефис, подчёркивание):")
			return
		}
		if len(args) != 1 || !validAccountName(args[0]) {
			_ = b.sendMessage(ctx, m.Chat.ID, "Имя: латиница, цифры, _ и -, до 32 символов. Например: /add mama")
			return
		}
		name := args[0]
		if _, err := users.FindByNameFile(b.cfg.UsersFile, name); err == nil {
			_ = b.sendMessage(ctx, m.Chat.ID, "⚠️ Такой профиль уже существует. Выбери его в списке пользователей.")
			return
		}
		if err := b.runManager(ctx, "account", "add", name); err != nil {
			if _, check := users.FindByNameFile(b.cfg.UsersFile, name); check != nil {
				_ = b.sendMessage(ctx, m.Chat.ID, "❌ Создание не завершилось. Администратор может проверить sync и журнал.")
				return
			}
			b.log.Warn("account created but manager returned error", "account", name, "error", safeErr(err))
		}
		code, err := b.createInvite(name)
		if err != nil {
			_ = b.sendMessage(ctx, m.Chat.ID, "✅ Профиль создан, но invitation создать не удалось: "+safeErr(err))
			return
		}
		b.audit(m.From.ID, "master", "create_user", name, "success")
		_ = b.sendMessage(ctx, m.Chat.ID, "✅ Профиль "+name+" создан.")
		b.sendInvitation(ctx, m.Chat.ID, name, code)
	case "/delete":
		if len(args) != 2 || strings.ToUpper(args[1]) != "CONFIRM" {
			_ = b.sendMessage(ctx, m.Chat.ID, "Usage: /delete NAME CONFIRM")
			return
		}
		if err := b.runManager(ctx, "account", "delete", args[0]); err != nil {
			if _, check := users.FindByNameFile(b.cfg.UsersFile, args[0]); check == nil {
				_ = b.sendMessage(ctx, m.Chat.ID, "⚠️ Удаление не завершилось: "+safeErr(err))
				return
			}
			b.log.Warn("account deleted but manager returned error", "account", args[0], "error", safeErr(err))
		}
		b.audit(m.From.ID, "master", "delete_user", args[0], "success")
		_ = b.sendMessage(ctx, m.Chat.ID, "✅ Профиль удалён.")
	case "/enable", "/disable", "/rotate":
		if len(args) != 1 {
			_ = b.sendMessage(ctx, m.Chat.ID, "Usage: "+cmd+" NAME")
			return
		}
		action := strings.TrimPrefix(cmd, "/")
		if err := b.runManager(ctx, "account", action, args[0]); err != nil {
			_ = b.sendMessage(ctx, m.Chat.ID, "❌ "+safeErr(err))
			return
		}
		b.audit(m.From.ID, "master", action, args[0], "success")
		_ = b.sendMessage(ctx, m.Chat.ID, "✅ Done: "+action+" "+args[0])
	case "/rotate_token":
		if len(args) != 2 || strings.ToUpper(args[1]) != "CONFIRM" {
			_ = b.sendMessage(ctx, m.Chat.ID, "Usage: /rotate_token NAME CONFIRM")
			return
		}
		if _, err := users.RotateTokenFile(b.cfg.UsersFile, args[0]); err != nil {
			_ = b.sendMessage(ctx, m.Chat.ID, "❌ "+safeErr(err))
			return
		}
		b.audit(m.From.ID, "master", "rotate_token", args[0], "success")
		_ = b.sendMessage(ctx, m.Chat.ID, "✅ Subscription token заменён; старые URL инвалидированы.")
	case "/invite":
		if len(args) != 1 {
			_ = b.sendMessage(ctx, m.Chat.ID, "Usage: /invite NAME")
			return
		}
		if _, err := users.FindByNameFile(b.cfg.UsersFile, args[0]); err != nil {
			_ = b.sendMessage(ctx, m.Chat.ID, "❌ User not found")
			return
		}
		code, err := b.createInvite(args[0])
		if err != nil {
			_ = b.sendMessage(ctx, m.Chat.ID, "❌ "+safeErr(err))
			return
		}
		b.sendInvitation(ctx, m.Chat.ID, args[0], code)
	case "/unbind":
		if len(args) != 1 {
			_ = b.sendMessage(ctx, m.Chat.ID, "Usage: /unbind NAME")
			return
		}
		if err := users.SetTelegramIDFile(b.cfg.UsersFile, args[0], 0); err != nil {
			_ = b.sendMessage(ctx, m.Chat.ID, "❌ "+safeErr(err))
			return
		}
		b.audit(m.From.ID, "master", "unbind", args[0], "success")
		_ = b.sendMessage(ctx, m.Chat.ID, "✅ Telegram отвязан.")
	case "/links":
		if len(args) != 1 {
			_ = b.sendMessage(ctx, m.Chat.ID, "Usage: /links NAME")
			return
		}
		u, err := users.FindByNameFile(b.cfg.UsersFile, args[0])
		if err != nil {
			_ = b.sendMessage(ctx, m.Chat.ID, "❌ User not found")
			return
		}
		b.sendAllLinks(ctx, m.Chat.ID, *u)
	case "/status":
		out, err := b.runManagerOutput(ctx, "status")
		if err != nil && out == "" {
			_ = b.sendMessage(ctx, m.Chat.ID, "❌ "+safeErr(err))
			return
		}
		_ = b.sendMessage(ctx, m.Chat.ID, truncate("🖥 Server status\n\n"+out, 3900))
	case "/selftest":
		_ = b.sendMessage(ctx, m.Chat.ID, "⏳ Running self-test...")
		out, err := b.runManagerOutput(ctx, "self-test")
		prefix := "✅ Self-test passed\n\n"
		if err != nil {
			prefix = "❌ Self-test failed\n\n"
		}
		_ = b.sendMessage(ctx, m.Chat.ID, truncate(prefix+out, 3900))
	case "/xray":
		out, _ := exec.CommandContext(ctx, "systemctl", "is-active", "xray").CombinedOutput()
		ver, _ := b.runManagerOutput(ctx, "xray", "version")
		_ = b.sendMessage(ctx, m.Chat.ID, "Xray: "+strings.TrimSpace(string(out))+"\n"+strings.TrimSpace(ver))
	case "/hysteria":
		out, _ := exec.CommandContext(ctx, "systemctl", "is-active", "hysteria-server").CombinedOutput()
		_ = b.sendMessage(ctx, m.Chat.ID, "Hysteria2: "+strings.TrimSpace(string(out)))
	default:
		// Master may also be a normal bound user.
		if u, err := users.FindByTelegramIDFile(b.cfg.UsersFile, m.From.ID); err == nil {
			b.handleUser(ctx, m, *u, cmd, args)
			return
		}
		_ = b.sendMessage(ctx, m.Chat.ID, "Выбери действие в меню 👇")
		b.sendMenu(ctx, m.Chat.ID, true)
	}
}

func (b *Bot) runManager(ctx context.Context, args ...string) error {
	_, err := b.runManagerOutput(ctx, args...)
	return err
}

func (b *Bot) runManagerOutput(ctx context.Context, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, b.cfg.ManagerPath, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return strings.TrimSpace(buf.String()), err
}

func (b *Bot) sendAllLinks(ctx context.Context, chatID int64, u model.User) {
	b.sendRoutingMenu(ctx, chatID, u.Name, "all")
}

func (b *Bot) sendVariant(ctx context.Context, chatID int64, u model.User, label, suffix, title string) {
	source := fmt.Sprintf("%s/sub/%s%s", b.cfg.BaseURL, u.Token, suffix)
	imp := importurl.Build(source, title)
	if png, err := qrPNG(imp); err == nil {
		_ = b.sendPhoto(ctx, chatID, png, "📱 "+label+" · QR для импорта")
	}
	htmlText := fmt.Sprintf("📱 <b>%s</b>\n\nSource — ручной Remote Profile:\n<pre>%s</pre>\nImport — готовая ссылка для sing-box:\n<pre>%s</pre>", html.EscapeString(label), html.EscapeString(source), html.EscapeString(imp))
	// Telegram URL buttons accept HTTPS/tg links, not sing-box:// custom schemes.
	// Open a trusted HTTPS landing page with an explicit import button instead.
	landingURL := source + "?import=1"
	if err := b.postUI(ctx, chatID, htmlText, inlineKeyboard{Rows: [][]uiButton{
		{{Text: "🚀 Импортировать", URL: landingURL}},
	}}, "HTML"); err != nil {
		b.log.Warn("telegram import button send failed", "error", err.Error())
	}
}

func qrPNG(s string) ([]byte, error) {
	cmd := exec.Command("qrencode", "-t", "PNG", "-o", "-", "-s", "6", "-m", "2", s)
	return cmd.Output()
}

func (b *Bot) sendMessage(ctx context.Context, chatID int64, text string) error {
	form := url.Values{}
	form.Set("chat_id", strconv.FormatInt(chatID, 10))
	form.Set("text", truncate(text, 4090))
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, b.api("sendMessage"), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("telegram sendMessage: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	return nil
}

func (b *Bot) sendHTML(ctx context.Context, chatID int64, text string) error {
	form := url.Values{}
	form.Set("chat_id", strconv.FormatInt(chatID, 10))
	form.Set("text", truncate(text, 4090))
	form.Set("parse_mode", "HTML")
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, b.api("sendMessage"), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("telegram sendMessage: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	return nil
}

func (b *Bot) sendPhoto(ctx context.Context, chatID int64, png []byte, caption string) error {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("chat_id", strconv.FormatInt(chatID, 10))
	_ = mw.WriteField("caption", truncate(caption, 1000))
	part, err := mw.CreateFormFile("photo", "import-qr.png")
	if err != nil {
		return err
	}
	if _, err := part.Write(png); err != nil {
		return err
	}
	_ = mw.Close()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, b.api("sendPhoto"), &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("telegram sendPhoto: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	return nil
}

func (b *Bot) createInvite(name string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, err := users.FindByNameFile(b.cfg.UsersFile, name); err != nil {
		return "", err
	}
	f, _ := b.loadInvites()
	now := time.Now().UTC()
	live := f.Invites[:0]
	for _, in := range f.Invites {
		if in.ExpiresAt.After(now) && in.User != name {
			live = append(live, in)
		}
	}
	var raw [5]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	code := strings.ToUpper(hex.EncodeToString(raw[:]))
	code = code[:5] + "-" + code[5:]
	f.Invites = append(live, invite{Code: code, User: name, ExpiresAt: now.Add(10 * time.Minute)})
	if err := b.saveInvites(f); err != nil {
		return "", err
	}
	return code, nil
}

func (b *Bot) consumeInvite(telegramID int64, code string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	f, _ := b.loadInvites()
	now := time.Now().UTC()
	idx := -1
	var target string
	for i, in := range f.Invites {
		if strings.EqualFold(in.Code, code) && in.ExpiresAt.After(now) {
			idx = i
			target = in.User
			break
		}
	}
	if idx < 0 {
		return errors.New("invalid invite")
	}
	if err := users.SetTelegramIDFile(b.cfg.UsersFile, target, telegramID); err != nil {
		return err
	}
	f.Invites = append(f.Invites[:idx], f.Invites[idx+1:]...)
	return b.saveInvites(f)
}

func (b *Bot) loadInvites() (inviteFile, error) {
	data, err := os.ReadFile(b.cfg.InvitesFile)
	if errors.Is(err, os.ErrNotExist) {
		return inviteFile{}, nil
	}
	if err != nil {
		return inviteFile{}, err
	}
	var f inviteFile
	if err := json.Unmarshal(data, &f); err != nil {
		return inviteFile{}, err
	}
	return f, nil
}

func (b *Bot) saveInvites(f inviteFile) error {
	if err := os.MkdirAll(filepath.Dir(b.cfg.InvitesFile), 0700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(f, "", "  ")
	tmp := b.cfg.InvitesFile + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, b.cfg.InvitesFile); err != nil {
		return err
	}
	return os.Chmod(b.cfg.InvitesFile, 0600)
}

func (b *Bot) audit(actor int64, role, action, target, result string) {
	if err := os.MkdirAll(filepath.Dir(b.cfg.AuditLog), 0750); err != nil {
		return
	}
	f, err := os.OpenFile(b.cfg.AuditLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s actor=%d role=%s action=%s target=%s result=%s\n", time.Now().UTC().Format(time.RFC3339), actor, role, action, target, result)
}

func (b *Bot) redactToken(s string) string {
	if b.token == "" {
		return s
	}
	return strings.ReplaceAll(s, b.token, "<redacted>")
}

func safeErr(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 240 {
		s = s[:240] + "…"
	}
	return s
}
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
