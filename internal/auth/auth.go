package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

type Manager struct {
	DB       *sql.DB
	TTL      time.Duration
	mu       sync.Mutex
	attempts map[string][]time.Time
}
type Session struct {
	Username, CSRF string
	Expires        time.Time
}

func New(db *sql.DB, ttl time.Duration) *Manager {
	return &Manager{DB: db, TTL: ttl, attempts: map[string][]time.Time{}}
}
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	const mem = 64 * 1024
	const it = 3
	const par = 2
	h := argon2.IDKey([]byte(password), salt, it, mem, par, 32)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", mem, it, par, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(h)), nil
}
func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var mem uint64
	var it, par uint64
	for _, p := range strings.Split(parts[3], ",") {
		kv := strings.SplitN(p, "=", 2)
		if len(kv) != 2 {
			continue
		}
		v, _ := strconv.ParseUint(kv[1], 10, 32)
		switch kv[0] {
		case "m":
			mem = v
		case "t":
			it = v
		case "p":
			par = v
		}
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, uint32(it), uint32(mem), uint8(par), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
func tokenHash(t string) string {
	h := sha256.Sum256([]byte(t))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
func (m *Manager) SetAdmin(ctx context.Context, username, password string) error {
	h, err := HashPassword(password)
	if err != nil {
		return err
	}
	_, err = m.DB.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES('admin_username',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, username)
	if err != nil {
		return err
	}
	_, err = m.DB.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES('admin_password_hash',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, h)
	return err
}
func (m *Manager) AdminConfigured(ctx context.Context) bool {
	var n int
	_ = m.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings WHERE key='admin_password_hash'`).Scan(&n)
	return n > 0
}
func (m *Manager) allowed(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	cut := time.Now().Add(-10 * time.Minute)
	xs := m.attempts[key][:0]
	for _, t := range m.attempts[key] {
		if t.After(cut) {
			xs = append(xs, t)
		}
	}
	m.attempts[key] = xs
	return len(xs) < 8
}
func (m *Manager) fail(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attempts[key] = append(m.attempts[key], time.Now())
}
func (m *Manager) Login(ctx context.Context, rateKey, user, pass string) (string, Session, error) {
	if !m.allowed(rateKey) {
		return "", Session{}, fmt.Errorf("rate limited")
	}
	var dbUser, hash string
	if err := m.DB.QueryRowContext(ctx, `SELECT (SELECT value FROM settings WHERE key='admin_username'),(SELECT value FROM settings WHERE key='admin_password_hash')`).Scan(&dbUser, &hash); err != nil {
		return "", Session{}, err
	}
	if subtle.ConstantTimeCompare([]byte(user), []byte(dbUser)) != 1 || !VerifyPassword(hash, pass) {
		m.fail(rateKey)
		return "", Session{}, fmt.Errorf("invalid credentials")
	}
	token, err := randomToken(32)
	if err != nil {
		return "", Session{}, err
	}
	csrf, err := randomToken(24)
	if err != nil {
		return "", Session{}, err
	}
	exp := time.Now().UTC().Add(m.TTL)
	_, err = m.DB.ExecContext(ctx, `INSERT INTO sessions(token_hash,username,csrf_token,expires_at,created_at) VALUES(?,?,?,?,?)`, tokenHash(token), dbUser, csrf, exp.Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	return token, Session{Username: dbUser, CSRF: csrf, Expires: exp}, err
}
func (m *Manager) Session(ctx context.Context, token string) (Session, error) {
	var s Session
	var exp string
	err := m.DB.QueryRowContext(ctx, `SELECT username,csrf_token,expires_at FROM sessions WHERE token_hash=?`, tokenHash(token)).Scan(&s.Username, &s.CSRF, &exp)
	if err != nil {
		return s, err
	}
	s.Expires, _ = time.Parse(time.RFC3339Nano, exp)
	if time.Now().After(s.Expires) {
		_, _ = m.DB.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=?`, tokenHash(token))
		return Session{}, fmt.Errorf("expired")
	}
	return s, nil
}
func (m *Manager) Logout(ctx context.Context, token string) {
	_, _ = m.DB.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=?`, tokenHash(token))
}
func (m *Manager) ChangePassword(ctx context.Context, current, next string) error {
	var hash string
	if err := m.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='admin_password_hash'`).Scan(&hash); err != nil {
		return err
	}
	if !VerifyPassword(hash, current) {
		return fmt.Errorf("invalid current password")
	}
	h, err := HashPassword(next)
	if err != nil {
		return err
	}
	_, err = m.DB.ExecContext(ctx, `UPDATE settings SET value=? WHERE key='admin_password_hash'`, h)
	if err == nil {
		_, _ = m.DB.ExecContext(ctx, `DELETE FROM sessions`)
	}
	return err
}
