package telegrambot

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"proxy-dev/vpn-manager/internal/traffic"
)

func TestTrafficAccessAndCommands(t *testing.T) {
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.yaml")
	if err := os.WriteFile(usersPath, []byte("users:\n  - name: alice\n    token: abcdefghijklmnopqrstuvwxyz123456\n    telegram_id: 42\n  - name: bob\n    token: abcdefghijklmnopqrstuvwxyz123457\n    telegram_id: 43\n"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	history, _ := json.Marshal(map[string]any{
		"sources": map[string]any{"Xray": map[string]any{"since": now, "updated": now}, "Hysteria": map[string]any{"since": now, "updated": now}},
		"accounts": map[string]any{
			"alice": map[string]any{"total": 2048, "points": []any{map[string]any{"at": now, "bytes": 1024}}},
			"bob":   map[string]any{"total": 4096},
		},
	})
	historyPath := filepath.Join(dir, "traffic.json")
	if err := os.WriteFile(historyPath, history, 0600); err != nil {
		t.Fatal(err)
	}
	collector, err := traffic.New(traffic.Config{File: historyPath})
	if err != nil {
		t.Fatal(err)
	}
	transport := &captureTransport{}
	b := &Bot{cfg: Config{UsersFile: usersPath}, traffic: collector, client: &http.Client{Transport: transport}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), masters: map[int64]struct{}{99: {}}}
	callback := func(id, chat int64, data string) {
		b.handleCallback(context.Background(), CallbackQuery{ID: "test", From: User{ID: id}, Message: &Message{Chat: Chat{ID: chat}}, Data: data})
	}
	for _, test := range []struct {
		id, chat int64
		data     string
	}{{43, 43, "traffic:alice"}, {44, 44, "traffic:alice"}, {42, -1, "traffic:alice"}} {
		callback(test.id, test.chat, test.data)
		if len(transport.messages) != 0 {
			t.Fatal("traffic disclosed to unauthorized user or group")
		}
	}
	callback(42, 42, "traffic:alice")
	if len(transport.messages) != 1 {
		t.Fatal("own traffic unavailable")
	}
	text := transport.messages[0].Get("text")
	if !strings.Contains(text, "2.00 КиБ") || !strings.Contains(text, "1.00 КиБ") || strings.Contains(text, "bob") {
		t.Fatalf("bad report: %s", text)
	}
	transport.messages = nil
	callback(99, 99, "traffic:bob")
	if len(transport.messages) != 1 || !strings.Contains(transport.messages[0].Get("text"), "4.00 КиБ") {
		t.Fatal("master cannot read another profile")
	}
	transport.messages = nil
	b.handleMessage(context.Background(), Message{From: User{ID: 99}, Chat: Chat{ID: 99}, Text: "/traffic"})
	if len(transport.messages) < 1 || !strings.Contains(transport.messages[0].Get("text"), "alice") || !strings.Contains(transport.messages[0].Get("text"), "bob") {
		t.Fatal("master overview missing profiles")
	}
	transport.messages = nil
	b.handleMessage(context.Background(), Message{From: User{ID: 42}, Chat: Chat{ID: 42}, Text: "/traffic bob"})
	if len(transport.messages) != 1 || strings.Contains(transport.messages[0].Get("text"), "bob") {
		t.Fatal("command bypassed ownership")
	}
}
