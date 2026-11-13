package telegrambot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	b := &Bot{navigation: map[int64]bool{42: false, 99: true}, cfg: Config{UsersFile: usersPath}, traffic: collector, client: &http.Client{Transport: transport}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), masters: map[int64]struct{}{99: {}}}
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
	// A direct command after an upgrade must install the bottom keyboard too.
	transport.messages = nil
	b.navigation = nil
	b.handleMessage(context.Background(), Message{From: User{ID: 99}, Chat: Chat{ID: 99}, Text: "👥 Пользователи"})
	if len(transport.messages) != 2 {
		t.Fatal("persistent navigation and user picker expected")
	}
	var keyboard struct {
		Persistent bool                  `json:"is_persistent"`
		OneTime    bool                  `json:"one_time_keyboard"`
		Rows       [][]map[string]string `json:"keyboard"`
	}
	if err := json.Unmarshal([]byte(transport.messages[0].Get("reply_markup")), &keyboard); err != nil {
		t.Fatal(err)
	}
	if !keyboard.Persistent || keyboard.OneTime || len(keyboard.Rows) == 0 {
		t.Fatal("bottom navigation must stay visible")
	}
	if !strings.Contains(transport.messages[1].Get("reply_markup"), "user:alice") {
		t.Fatal("user picker missing")
	}
	transport.messages = nil
	callback(99, 99, "user:alice")
	if len(transport.messages) != 1 || !strings.Contains(transport.messages[0].Get("reply_markup"), "📊 Статистика") {
		t.Fatal("statistics button missing from user card")
	}
	transport.messages = nil
	callback(99, 99, "traffic:alice")
	if len(transport.messages) != 1 {
		t.Fatal("navigation resent on every action")
	}
	markup := transport.messages[0].Get("reply_markup")
	for _, target := range []string{"traffic:alice", "user:alice", "users", "menu"} {
		if !strings.Contains(markup, `"callback_data":"`+target+`"`) {
			t.Fatalf("missing statistics navigation: %s", target)
		}
	}
	transport.messages = nil
	callback(99, 99, "user:alice")
	callback(99, 99, "users")
	callback(99, 99, "menu")
	if len(transport.messages) != 3 || !strings.Contains(transport.messages[2].Get("reply_markup"), `"is_persistent":true`) {
		t.Fatal("back and home flow broken")
	}
	b.pending = map[int64]pendingAction{99: {Kind: "add", Until: time.Now().Add(time.Minute)}}
	transport.messages = nil
	b.handleMessage(context.Background(), Message{From: User{ID: 99}, Chat: Chat{ID: 99}, Text: "👥 Пользователи"})
	if _, ok := b.pending[99]; ok {
		t.Fatal("navigation did not cancel pending text input")
	}
	if len(transport.messages) != 1 || !strings.Contains(transport.messages[0].Get("reply_markup"), "user:alice") {
		t.Fatal("navigation was treated as a profile name")
	}
	transport.messages = nil
	b.navigation = nil
	callback(42, 42, "traffic:alice")
	if len(transport.messages) != 2 {
		t.Fatal("user bottom navigation not installed")
	}
	if strings.Contains(transport.messages[0].Get("reply_markup"), "👥 Пользователи") || strings.Contains(transport.messages[1].Get("reply_markup"), `"callback_data":"users"`) {
		t.Fatal("admin navigation exposed to regular user")
	}

}

// Exercise actual readers: opening the report must fetch changed daemon counters,
// even when the periodic collector has not ticked yet.
func TestStatisticsRefreshReadsLiveCounters(t *testing.T) {
	dir := t.TempDir()
	xray := filepath.Join(dir, "xray")
	if err := os.WriteFile(xray, []byte("#!/bin/sh\nprintf '{\"stat\":[]}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var bytes atomic.Uint64
	bytes.Store(100)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Error("counters must not be cleared")
		}
		fmt.Fprintf(w, `{"alice":{"tx":%d,"rx":0}}`, bytes.Load())
	}))
	defer server.Close()
	collector, err := traffic.New(traffic.Config{File: filepath.Join(dir, "traffic.json"), XrayBinary: xray, HysteriaURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err = collector.Poll(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	transport := &captureTransport{}
	b := &Bot{traffic: collector, client: &http.Client{Transport: transport}, log: slog.New(slog.NewTextHandler(io.Discard, nil)), masters: map[int64]struct{}{99: {}}}
	b.sendTraffic(context.Background(), 99, "alice")
	if !strings.Contains(transport.messages[0].Get("text"), "Первый снимок") {
		t.Fatal("initial zero needs a baseline explanation")
	}
	for _, value := range []uint64{200, 300, 300} {
		bytes.Store(value)
		transport.messages = nil
		b.sendTraffic(context.Background(), 99, "alice")
		if r := collector.Report("alice", time.Now()); r.Total != value-100 || r.Week != value-100 {
			t.Fatalf("button failed to fetch live delta, or counted it twice: %+v", r)
		}
	}
	// A failed API cannot be presented as a trustworthy zero.
	server.Close()
	transport.messages = nil
	b.sendTraffic(context.Background(), 99, "bob")
	text := transport.messages[0].Get("text")
	if !strings.Contains(text, "нет полных данных") || strings.Contains(text, "С начала учёта: 0.00") {
		t.Fatalf("API failure disguised as zero: %s", text)
	}
}
