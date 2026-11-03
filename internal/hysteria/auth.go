package hysteria

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/proxy/proxy-manager/internal/database"
)

type AuthHandler struct{ Store *database.Store }
type request struct {
	Addr string `json:"addr"`
	Auth string `json:"auth"`
	TX   int64  `json:"tx"`
}
type response struct {
	OK bool   `json:"ok"`
	ID string `json:"id,omitempty"`
}

func (h AuthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(response{})
		return
	}
	var req request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		json.NewEncoder(w).Encode(response{})
		return
	}
	user, pass, hasColon := strings.Cut(req.Auth, ":")
	if hasColon && user != "" && pass != "" {
		u, err := h.Store.FindHysteriaByUsername(r.Context(), user)
		if err == nil && u.Enabled && u.Hysteria.Enabled && subtle.ConstantTimeCompare([]byte(pass), []byte(u.Hysteria.Password)) == 1 {
			json.NewEncoder(w).Encode(response{OK: true, ID: u.Name})
			return
		}
	} else if req.Auth != "" {
		// Compatibility path for a migrated old Hysteria installation that used a single raw password.
		rows, err := h.Store.ListUsers(r.Context())
		if err == nil {
			for _, u := range rows {
				if u.Enabled && u.Hysteria.Enabled && u.Hysteria.LegacyCompat && subtle.ConstantTimeCompare([]byte(req.Auth), []byte(u.Hysteria.Password)) == 1 {
					json.NewEncoder(w).Encode(response{OK: true, ID: u.Name})
					return
				}
			}
		}
	}
	json.NewEncoder(w).Encode(response{})
}
