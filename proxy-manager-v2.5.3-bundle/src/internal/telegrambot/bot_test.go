package telegrambot

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSplitCommand(t *testing.T) {
	cmd, args := splitCommand("/rotate@proxyvpn_bot demo")
	if cmd != "/rotate" || !reflect.DeepEqual(args, []string{"demo"}) {
		t.Fatalf("got %q %#v", cmd, args)
	}
}

func TestLoadConfigDefaultsAndMasters(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "telegram.json")
	body := `{"token_file":"/tmp/token","masters":[123,456],"base_url":"https://vpn.example.com"}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UsersFile != "/etc/vpn-manager/users.yaml" || cfg.ManagerPath != "/usr/local/sbin/proxy-manager" || len(cfg.Masters) != 2 {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

func TestLoadConfigRejectsHTTP(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "telegram.json")
	if err := os.WriteFile(p, []byte(`{"masters":[123],"base_url":"http://vpn.example"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("expected https validation error")
	}
}

type captureTransport struct{ messages []url.Values }

func (c *captureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.Contains(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		data, _ := io.ReadAll(r.Body)
		values, _ := url.ParseQuery(string(data))
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			c.messages = append(c.messages, values)
		}
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Header: make(http.Header)}, nil
}

func TestRoutingCallbackFlowAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.yaml")
	if err := os.WriteFile(path, []byte("users:\n  - name: alice\n    token: abcdefghijklmnopqrstuvwxyz123456\n    telegram_id: 42\n    enabled: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	transport := &captureTransport{}
	b := &Bot{cfg: Config{UsersFile: path, BaseURL: "https://vpn.example.com", ProfileName: "VPN"}, client: &http.Client{Transport: transport}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), masters: map[int64]struct{}{99: {}}}
	callback := func(id int64, data string) {
		b.handleCallback(context.Background(), CallbackQuery{ID: "test", From: User{ID: id}, Message: &Message{Chat: Chat{ID: id}}, Data: data})
	}
	callback(42, "platform:alice:openwrt")
	if len(transport.messages) != 1 {
		t.Fatal("missing routing menu")
	}
	markup := transport.messages[0].Get("reply_markup")
	if !strings.Contains(markup, "route:alice:openwrt:blocked") || !strings.Contains(markup, "route:alice:openwrt:bypass-ru") {
		t.Fatal("routing choices missing")
	}
	for _, mode := range []string{"blocked", "bypass-ru"} {
		transport.messages = nil
		callback(42, "route:alice:openwrt:"+mode)
		if len(transport.messages) < 1 || !strings.Contains(transport.messages[0].Get("text"), "/openwrt/"+mode) || !strings.Contains(transport.messages[0].Get("reply_markup"), "/openwrt/"+mode+"?import=1") {
			t.Fatal("mode lost in Source/import links")
		}
	}
	for _, test := range []struct {
		id   int64
		data string
	}{{43, "route:alice:openwrt:blocked"}, {42, "route:alice:openwrt:typo"}, {42, "route:alice:unknown:blocked"}} {
		transport.messages = nil
		callback(test.id, test.data)
		if len(transport.messages) != 0 {
			t.Fatal("unauthorized or invalid callback accepted")
		}
	}
	callback(99, "platform:alice:openwrt")
	if len(transport.messages) != 1 {
		t.Fatal("master cannot choose routing for user")
	}
	for _, platform := range platformOrder {
		if len("route:"+strings.Repeat("a", 32)+":"+platform+":bypass-ru") > 64 {
			t.Fatal("callback exceeds Telegram limit")
		}
	}
}
