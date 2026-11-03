package api

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/proxy/proxy-manager/internal/audit"
	authpkg "github.com/proxy/proxy-manager/internal/auth"
	"github.com/proxy/proxy-manager/internal/config"
	"github.com/proxy/proxy-manager/internal/database"
	hy "github.com/proxy/proxy-manager/internal/hysteria"
	"github.com/proxy/proxy-manager/internal/links"
	usersvc "github.com/proxy/proxy-manager/internal/service"
	"github.com/proxy/proxy-manager/internal/users"
	xrayapi "github.com/proxy/proxy-manager/internal/xray"
)

//go:embed static/*
var staticFS embed.FS

type Server struct {
	Config config.Config
	Store  *database.Store
	Users  *usersvc.UserService
	Auth   *authpkg.Manager
	Xray   xrayapi.Client
	Audit  audit.Logger
	Log    *slog.Logger
}
type ctxKey int

const sessionKey ctxKey = 1

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, code, msg string) {
	var e apiError
	e.Error.Code = code
	e.Error.Message = msg
	writeJSON(w, status, e)
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, "INVALID_JSON", "Invalid request body")
		return false
	}
	return true
}

func (s *Server) MainHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", s.login)
	mux.Handle("/api/logout", s.requireAuth(http.HandlerFunc(s.logout)))
	mux.Handle("/api/session", s.requireAuth(http.HandlerFunc(s.session)))
	mux.Handle("/api/status", s.requireAuth(http.HandlerFunc(s.status)))
	mux.Handle("/api/settings", s.requireAuth(http.HandlerFunc(s.settings)))
	mux.Handle("/api/admin/password", s.requireAuth(http.HandlerFunc(s.changePassword)))
	mux.Handle("/api/users", s.requireAuth(http.HandlerFunc(s.usersRoot)))
	mux.Handle("/api/users/", s.requireAuth(http.HandlerFunc(s.userRoute)))
	sub, _ := fs.Sub(staticFS, "static")
	files := http.FileServer(http.FS(sub))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			fail(w, 404, "NOT_FOUND", "Not found")
			return
		}
		if r.URL.Path != "/" {
			if _, err := fs.Stat(sub, strings.TrimPrefix(r.URL.Path, "/")); err == nil {
				files.ServeHTTP(w, r)
				return
			}
		}
		b, _ := fs.ReadFile(sub, "index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	})
	return securityHeaders(mux)
}
func (s *Server) InternalHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/internal/hysteria/auth", loopbackOnly(hy.AuthHandler{Store: s.Store}))
	mux.HandleFunc("/internal/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]bool{"ok": true}) })
	return mux
}
func loopbackOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			http.Error(w, "forbidden", 403)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("pm_session")
		if err != nil {
			fail(w, 401, "UNAUTHORIZED", "Authentication required")
			return
		}
		ss, err := s.Auth.Session(r.Context(), c.Value)
		if err != nil {
			fail(w, 401, "UNAUTHORIZED", "Authentication required")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
			if r.Header.Get("X-CSRF-Token") == "" || r.Header.Get("X-CSRF-Token") != ss.CSRF {
				fail(w, 403, "CSRF_FAILED", "Invalid CSRF token")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey, ss)))
	})
}
func actor(r *http.Request) string {
	if ss, ok := r.Context().Value(sessionKey).(authpkg.Session); ok {
		return ss.Username
	}
	return "unknown"
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}
	var q struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &q) {
		return
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	token, ss, err := s.Auth.Login(r.Context(), host, q.Username, q.Password)
	if err != nil {
		s.Log.Warn("admin login failed", "remote", host)
		fail(w, 401, "LOGIN_FAILED", "Invalid credentials or too many attempts")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "pm_session", Value: token, Path: "/", HttpOnly: true, Secure: s.Config.SessionSecure, SameSite: http.SameSiteStrictMode, Expires: ss.Expires})
	_ = s.Audit.Record(r.Context(), ss.Username, "admin_login", "admin", ss.Username, map[string]any{"remote": host})
	writeJSON(w, 200, map[string]any{"username": ss.Username, "csrf_token": ss.CSRF})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}
	c, _ := r.Cookie("pm_session")
	if c != nil {
		s.Auth.Logout(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "pm_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.Config.SessionSecure, SameSite: http.SameSiteStrictMode})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		fail(w, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}
	ss, _ := r.Context().Value(sessionKey).(authpkg.Session)
	writeJSON(w, 200, map[string]any{"username": ss.Username, "csrf_token": ss.CSRF})
}
func (s *Server) usersRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		us, err := s.Users.List(r.Context())
		if err != nil {
			fail(w, 500, "DB_ERROR", "Failed to list users")
			return
		}
		for i := range us {
			us[i].Hysteria.Password = ""
		}
		writeJSON(w, 200, us)
	case "POST":
		var q users.CreateRequest
		if !decode(w, r, &q) {
			return
		}
		u, err := s.Users.Create(r.Context(), actor(r), q)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				fail(w, 409, "USER_EXISTS", "User name or protocol username already exists")
			} else {
				fail(w, 500, "CREATE_FAILED", err.Error())
			}
			return
		}
		writeJSON(w, 201, s.withLinks(u))
	default:
		fail(w, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
	}
}
func splitUserPath(p string) (string, []string) {
	x := strings.Trim(strings.TrimPrefix(p, "/api/users/"), "/")
	parts := strings.Split(x, "/")
	if len(parts) == 0 {
		return "", nil
	}
	return parts[0], parts[1:]
}
func (s *Server) userRoute(w http.ResponseWriter, r *http.Request) {
	id, rest := splitUserPath(r.URL.Path)
	if id == "" {
		fail(w, 404, "USER_NOT_FOUND", "User not found")
		return
	}
	if len(rest) == 0 {
		switch r.Method {
		case "GET":
			u, err := s.Users.Get(r.Context(), id)
			if err != nil {
				fail(w, 404, "USER_NOT_FOUND", "User not found")
				return
			}
			writeJSON(w, 200, s.withLinks(u))
		case "PATCH":
			var q struct {
				Name string `json:"name"`
			}
			if !decode(w, r, &q) {
				return
			}
			u, err := s.Users.Rename(r.Context(), actor(r), id, q.Name)
			if err != nil {
				fail(w, 400, "UPDATE_FAILED", err.Error())
				return
			}
			writeJSON(w, 200, s.withLinks(u))
		case "DELETE":
			if err := s.Users.Delete(r.Context(), actor(r), id); err != nil {
				fail(w, 500, "DELETE_FAILED", err.Error())
				return
			}
			w.WriteHeader(204)
		default:
			fail(w, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		}
		return
	}
	if r.Method == "GET" && len(rest) == 1 && rest[0] == "links" {
		u, err := s.Users.Get(r.Context(), id)
		if err != nil {
			fail(w, 404, "USER_NOT_FOUND", "User not found")
			return
		}
		writeJSON(w, 200, links.ForUser(s.Config, u))
		return
	}
	if r.Method != "POST" {
		fail(w, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}
	var err error
	switch strings.Join(rest, "/") {
	case "enable":
		err = s.Users.Enable(r.Context(), actor(r), id)
	case "disable":
		err = s.Users.Disable(r.Context(), actor(r), id)
	case "xray/enable":
		err = s.Users.SetXray(r.Context(), actor(r), id, true)
	case "xray/disable":
		err = s.Users.SetXray(r.Context(), actor(r), id, false)
	case "hysteria/enable":
		err = s.Users.SetHysteria(r.Context(), actor(r), id, true)
	case "hysteria/disable":
		err = s.Users.SetHysteria(r.Context(), actor(r), id, false)
	case "xray/regenerate":
		_, err = s.Users.RegenerateXray(r.Context(), actor(r), id)
	case "hysteria/regenerate":
		_, err = s.Users.RegenerateHysteria(r.Context(), actor(r), id)
	default:
		fail(w, 404, "NOT_FOUND", "Not found")
		return
	}
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			fail(w, 404, "USER_NOT_FOUND", "User not found")
		} else {
			fail(w, 500, "OPERATION_FAILED", err.Error())
		}
		return
	}
	u, _ := s.Users.Get(r.Context(), id)
	writeJSON(w, 200, s.withLinks(u))
}
func (s *Server) withLinks(u users.User) map[string]any {
	return map[string]any{"user": u, "links": links.ForUser(s.Config, u)}
}

func activeService(name string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", name).Run() == nil
}
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		fail(w, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	dbOK := s.Store.Health(ctx) == nil
	xOK := s.Xray.Health(ctx) == nil
	us, _ := s.Users.List(ctx)
	n := 0
	for _, u := range us {
		if u.Enabled {
			n++
		}
	}
	writeJSON(w, 200, map[string]any{"proxy_manager": "running", "database": dbOK, "xray_service": activeService(s.Config.Services.Xray), "hysteria_service": activeService(s.Config.Services.Hysteria), "xray_api": xOK, "ip": s.Config.EndpointHost(), "port": s.Config.Port, "xray_sni": s.Config.Xray.SNI, "hysteria_sni": s.Config.Hysteria.SNI, "enabled_users": n})
}
func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		fail(w, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}
	writeJSON(w, 200, map[string]any{"listen": s.Config.Listen, "public_host": s.Config.EndpointHost(), "port": s.Config.Port, "xray_sni": s.Config.Xray.SNI, "hysteria_sni": s.Config.Hysteria.SNI})
}
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}
	var q struct {
		Current string `json:"current_password"`
		Next    string `json:"new_password"`
	}
	if !decode(w, r, &q) {
		return
	}
	if len(q.Next) < 14 {
		fail(w, 400, "WEAK_PASSWORD", "New password must be at least 14 characters")
		return
	}
	if err := s.Auth.ChangePassword(r.Context(), q.Current, q.Next); err != nil {
		fail(w, 400, "PASSWORD_CHANGE_FAILED", err.Error())
		return
	}
	_ = s.Audit.Record(r.Context(), actor(r), "admin_password_changed", "admin", actor(r), nil)
	writeJSON(w, 200, map[string]bool{"ok": true})
}
