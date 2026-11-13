package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"

	"log/slog"
	"net/http"
	"proxy-dev/vpn-manager/internal/importurl"
	"time"

	"proxy-dev/vpn-manager/internal/subscription"
	"proxy-dev/vpn-manager/internal/users"
)

var publicHostRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:-]{0,252}$`)

type Server struct{ http *http.Server }

func New(addr string, svc *subscription.Service, log *slog.Logger) *Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	serveSub := func(platform string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			token := r.PathValue("token")
			w.Header().Set("Cache-Control", "no-store")
			if token == "" || len(token) > 512 {
				http.NotFound(w, r)
				return
			}
			mode := r.PathValue("mode")
			if mode != "" && mode != "bypass-ru" && mode != "blocked" {
				http.NotFound(w, r)
				return
			}
			b, err := svc.GenerateWithRouting(r.Context(), token, platform, mode)
			if err != nil {
				if !errors.Is(err, users.ErrNotFound) {
					log.Error("subscription generation failed", "error", err, "platform", platform)
				}
				http.NotFound(w, r)
				return
			}
			if r.URL.Query().Get("import") == "1" {
				// Preserve /sub/{token} as a JSON subscription for every other request.
				// nginx forwards the original public Host header to this service.
				publicHost := r.Host
				if !publicHostRE.MatchString(publicHost) || strings.HasPrefix(publicHost, "127.") || strings.HasPrefix(publicHost, "localhost") {
					http.NotFound(w, r)
					return
				}
				sourceURL := "https://" + publicHost + r.URL.Path
				title := "Proxy VPN"
				if mode != "" {
					title += " " + mode
				}
				if platform != "universal" {
					title += " " + platform
				}
				importLink := importurl.Build(sourceURL, title)
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("X-Content-Type-Options", "nosniff")
				w.Header().Set("Referrer-Policy", "no-referrer")
				w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
				_, _ = fmt.Fprintf(w, `<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Proxy VPN — импорт</title><style>body{background:#10121a;color:#f4f5fb;font-family:system-ui,Arial,sans-serif;display:grid;place-items:center;min-height:95vh;margin:0}.card{width:min(90vw,440px);text-align:center;background:#1c2030;padding:32px 22px;border-radius:22px}h1{font-size:25px}.btn{display:block;background:#7764f8;color:white;text-decoration:none;font-weight:700;padding:18px 10px;border-radius:13px;margin:26px 0 14px}p{color:#b5bbcc;line-height:1.5}.small{font-size:13px;word-break:break-all}</style></head><body><main class="card"><h1>Proxy VPN</h1><p>Профиль для %s готов к импорту.</p><a class="btn" href="%s">🚀 Импортировать в sing-box</a><p>Если приложение не открылось, открой страницу во внешнем браузере или скопируй ссылку Import из Telegram.</p><p class="small">URL профиля: %s</p></main></body></html>`, html.EscapeString(platform), html.EscapeString(importLink), html.EscapeString(sourceURL))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(b)
		}
	}
	mux.HandleFunc("GET /sub/{token}", serveSub("universal"))
	mux.HandleFunc("GET /sub/{token}/android", serveSub("android"))
	mux.HandleFunc("GET /sub/{token}/ios", serveSub("ios"))
	mux.HandleFunc("GET /sub/{token}/macos", serveSub("macos"))
	mux.HandleFunc("GET /sub/{token}/linux", serveSub("linux"))
	mux.HandleFunc("GET /sub/{token}/tv", serveSub("tv"))
	mux.HandleFunc("GET /sub/{token}/openwrt", serveSub("openwrt"))
	mux.HandleFunc("GET /sub/{token}/universal", serveSub("universal"))
	for _, platform := range []string{"universal", "android", "ios", "macos", "linux", "tv", "openwrt"} {
		mux.HandleFunc("GET /sub/{token}/"+platform+"/{mode}", serveSub(platform))
	}

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > 4096 {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		mux.ServeHTTP(w, r)
	})
	return &Server{http: &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}}
}
func (s *Server) ListenAndServe() error { return s.http.ListenAndServe() }
func (s *Server) HTTP() *http.Server    { return s.http }
