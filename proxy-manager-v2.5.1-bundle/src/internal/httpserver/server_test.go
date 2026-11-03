package httpserver

import (
	"context"
	"proxy-dev/vpn-manager/internal/model"
	"proxy-dev/vpn-manager/internal/subscription"
	"proxy-dev/vpn-manager/internal/users"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type us struct{}

func (us) FindByToken(_ context.Context, t string) (*model.User, error) {
	if t != "good-token-abcdefghijklmnopqrstuvwxyz" {
		return nil, users.ErrNotFound
	}
	return &model.User{Name: "a"}, nil
}

type sp struct{}

func (sp) Current(context.Context) (*model.ServerConfig, error) {
	return &model.ServerConfig{Host: "h"}, nil
}

type gen struct{}

func (gen) Generate(context.Context, model.User, model.ServerConfig) ([]byte, error) {
	return []byte(`{"ok":true}`), nil
}
func (gen) GenerateForPlatform(context.Context, model.User, model.ServerConfig, string) ([]byte, error) {
	return []byte(`{"ok":true}`), nil
}
func handler() http.Handler {
	svc := subscription.New(us{}, sp{}, gen{})
	return New("127.0.0.1:0", svc, slog.New(slog.NewTextHandler(io.Discard, nil))).HTTP().Handler
}
func TestHealth(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	handler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"ok"`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
func TestSubscription(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/sub/good-token-abcdefghijklmnopqrstuvwxyz", nil)
	w := httptest.NewRecorder()
	handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatal(ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatal(cc)
	}
}
func TestUnknown(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/sub/nope", nil)
	w := httptest.NewRecorder()
	handler().ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}

func TestAndroidSubscription(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/sub/good-token-abcdefghijklmnopqrstuvwxyz/android", nil)
	w := httptest.NewRecorder()
	handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}
