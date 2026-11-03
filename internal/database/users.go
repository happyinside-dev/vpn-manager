package database

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/proxy/proxy-manager/internal/users"
)

var ErrNotFound = errors.New("not found")

func parseTime(s string) time.Time { t, _ := time.Parse(time.RFC3339Nano, s); return t }

func scanUser(row interface{ Scan(...any) error }) (users.User, error) {
	var u users.User
	var ue, xe, he, hl int
	var uc, uu, xc, xu, hc, hu string
	err := row.Scan(&u.ID, &u.Name, &ue, &uc, &uu,
		&u.Xray.ID, &u.Xray.UserID, &u.Xray.UUID, &xe, &xc, &xu,
		&u.Hysteria.ID, &u.Hysteria.UserID, &u.Hysteria.Username, &u.Hysteria.Password, &he, &hc, &hu, &hl)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	if err != nil {
		return u, err
	}
	u.Enabled = ue != 0
	u.Xray.Enabled = xe != 0
	u.Hysteria.Enabled = he != 0
	u.Hysteria.LegacyCompat = hl != 0
	u.CreatedAt = parseTime(uc)
	u.UpdatedAt = parseTime(uu)
	u.Xray.CreatedAt = parseTime(xc)
	u.Xray.UpdatedAt = parseTime(xu)
	u.Hysteria.CreatedAt = parseTime(hc)
	u.Hysteria.UpdatedAt = parseTime(hu)
	return u, nil
}

const userSelect = `SELECT u.id,u.name,u.enabled,u.created_at,u.updated_at,
 x.id,x.user_id,x.uuid,x.enabled,x.created_at,x.updated_at,
 h.id,h.user_id,h.username,h.password,h.enabled,h.created_at,h.updated_at,h.legacy_compat
 FROM users u JOIN xray_profiles x ON x.user_id=u.id JOIN hysteria_profiles h ON h.user_id=u.id`

func (s *Store) GetUser(ctx context.Context, idOrName string) (users.User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, userSelect+` WHERE u.id=? OR u.name=? COLLATE NOCASE LIMIT 1`, idOrName, idOrName))
}
func (s *Store) ListUsers(ctx context.Context) ([]users.User, error) {
	rows, err := s.DB.QueryContext(ctx, userSelect+` ORDER BY u.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []users.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
func (s *Store) ActiveXrayUsers(ctx context.Context) ([]users.User, error) {
	rows, err := s.DB.QueryContext(ctx, userSelect+` WHERE u.enabled=1 AND x.enabled=1 ORDER BY u.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []users.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
func (s *Store) FindHysteriaByUsername(ctx context.Context, name string) (users.User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, userSelect+` WHERE h.username=? COLLATE NOCASE LIMIT 1`, name))
}
