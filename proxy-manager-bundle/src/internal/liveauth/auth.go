// Package liveauth provides Hysteria authentication independently of subscriptions.
package liveauth

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

// Handler reads an atomically replaced, credential-only snapshot on every request.
// No cached allow decision survives an account disable, rotation, or deletion.
func Handler(path string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if _, err := read(path); err != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		w.WriteHeader(200)
	})
	mux.HandleFunc("POST /auth", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			Auth string `json:"auth"`
		}
		result := struct {
			OK bool   `json:"ok"`
			ID string `json:"id,omitempty"`
		}{}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) == nil {
			name, pass, ok := strings.Cut(req.Auth, ":")
			if entries, err := read(path); err == nil && ok && pass != "" {
				expected, found := entries[name]
				if found && subtle.ConstantTimeCompare([]byte(pass), []byte(expected)) == 1 {
					result.OK = true
					result.ID = name
				}
			}
		}
		_ = json.NewEncoder(w).Encode(result)
	})
	return mux
}

func read(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries map[string]string
	err = json.Unmarshal(b, &entries)
	return entries, err
}
