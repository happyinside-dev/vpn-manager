package httpserver

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"proxy-dev/vpn-manager/internal/subscription"
	"proxy-dev/vpn-manager/internal/users"
)

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
			b, err := svc.GenerateForPlatform(r.Context(), token, platform)
			if err != nil {
				if !errors.Is(err, users.ErrNotFound) {
					log.Error("subscription generation failed", "error", err, "platform", platform)
				}
				http.NotFound(w, r)
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
