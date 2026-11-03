package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/proxy/proxy-manager/internal/audit"
	"github.com/proxy/proxy-manager/internal/database"
	"github.com/proxy/proxy-manager/internal/users"
	xrayapi "github.com/proxy/proxy-manager/internal/xray"
)

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type UserService struct {
	Store *database.Store
	Xray  xrayapi.Client
	Audit audit.Logger
	Flow  string
}

func xrayEmail(id string) string { return "pm-" + id + "@proxy-manager" }
func password() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func hyUsername(name string) string { return strings.ToLower(name) }
func now() string                   { return time.Now().UTC().Format(time.RFC3339Nano) }

func (s *UserService) Create(ctx context.Context, actor string, req users.CreateRequest) (users.User, error) {
	req.Name = strings.TrimSpace(req.Name)
	if !validName.MatchString(req.Name) {
		return users.User{}, fmt.Errorf("invalid user name")
	}
	hp, err := password()
	if err != nil {
		return users.User{}, err
	}
	uid := uuid.NewString()
	xid := uuid.NewString()
	hid := uuid.NewString()
	xuuid := uuid.NewString()
	ts := now()
	tx, err := s.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return users.User{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO users(id,name,enabled,created_at,updated_at) VALUES(?,?,1,?,?)`, uid, req.Name, ts, ts); err != nil {
		return users.User{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO xray_profiles(id,user_id,uuid,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?)`, xid, uid, xuuid, boolInt(req.XrayEnabled), ts, ts); err != nil {
		return users.User{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hysteria_profiles(id,user_id,username,password,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, hid, uid, hyUsername(req.Name), hp, boolInt(req.HysteriaEnabled), ts, ts); err != nil {
		return users.User{}, err
	}
	if req.XrayEnabled {
		if err = s.Xray.AddUser(ctx, xrayEmail(uid), xuuid, s.Flow); err != nil {
			return users.User{}, fmt.Errorf("xray provision: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		if req.XrayEnabled {
			_ = s.Xray.RemoveUser(ctx, xrayEmail(uid))
		}
		return users.User{}, err
	}
	_ = s.Audit.Record(ctx, actor, "user_created", "user", uid, map[string]any{"name": req.Name, "xray_enabled": req.XrayEnabled, "hysteria_enabled": req.HysteriaEnabled})
	return s.Store.GetUser(ctx, uid)
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func (s *UserService) Get(ctx context.Context, id string) (users.User, error) {
	return s.Store.GetUser(ctx, id)
}
func (s *UserService) List(ctx context.Context) ([]users.User, error) { return s.Store.ListUsers(ctx) }

func (s *UserService) setAccount(ctx context.Context, actor, id string, enable bool) error {
	u, err := s.Store.GetUser(ctx, id)
	if err != nil {
		return err
	}
	if u.Enabled == enable {
		return nil
	}
	tx, err := s.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	if _, err = tx.ExecContext(ctx, `UPDATE users SET enabled=?,updated_at=? WHERE id=?`, boolInt(enable), ts, u.ID); err != nil {
		return err
	}
	shouldX := enable && u.Xray.Enabled
	if shouldX {
		err = s.Xray.AddUser(ctx, xrayEmail(u.ID), u.Xray.UUID, s.Flow)
	} else if u.Xray.Enabled {
		err = s.Xray.RemoveUser(ctx, xrayEmail(u.ID))
	}
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	action := "user_disabled"
	if enable {
		action = "user_enabled"
	}
	_ = s.Audit.Record(ctx, actor, action, "user", u.ID, nil)
	return nil
}
func (s *UserService) Enable(ctx context.Context, a, id string) error {
	return s.setAccount(ctx, a, id, true)
}
func (s *UserService) Disable(ctx context.Context, a, id string) error {
	return s.setAccount(ctx, a, id, false)
}

func (s *UserService) SetXray(ctx context.Context, actor, id string, enable bool) error {
	u, err := s.Store.GetUser(ctx, id)
	if err != nil {
		return err
	}
	if u.Xray.Enabled == enable {
		return nil
	}
	tx, err := s.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE xray_profiles SET enabled=?,updated_at=? WHERE user_id=?`, boolInt(enable), now(), u.ID); err != nil {
		return err
	}
	if u.Enabled {
		if enable {
			err = s.Xray.AddUser(ctx, xrayEmail(u.ID), u.Xray.UUID, s.Flow)
		} else {
			err = s.Xray.RemoveUser(ctx, xrayEmail(u.ID))
		}
		if err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	action := "xray_disabled"
	if enable {
		action = "xray_enabled"
	}
	_ = s.Audit.Record(ctx, actor, action, "user", u.ID, nil)
	return nil
}
func (s *UserService) SetHysteria(ctx context.Context, actor, id string, enable bool) error {
	u, err := s.Store.GetUser(ctx, id)
	if err != nil {
		return err
	}
	if u.Hysteria.Enabled == enable {
		return nil
	}
	_, err = s.Store.DB.ExecContext(ctx, `UPDATE hysteria_profiles SET enabled=?,updated_at=? WHERE user_id=?`, boolInt(enable), now(), u.ID)
	if err == nil {
		action := "hysteria_disabled"
		if enable {
			action = "hysteria_enabled"
		}
		_ = s.Audit.Record(ctx, actor, action, "user", u.ID, nil)
	}
	return err
}
func (s *UserService) RegenerateXray(ctx context.Context, actor, id string) (users.User, error) {
	u, err := s.Store.GetUser(ctx, id)
	if err != nil {
		return users.User{}, err
	}
	newID := uuid.NewString()
	tx, err := s.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return users.User{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE xray_profiles SET uuid=?,updated_at=? WHERE user_id=?`, newID, now(), u.ID); err != nil {
		return users.User{}, err
	}
	if u.Enabled && u.Xray.Enabled {
		if err = s.Xray.RemoveUser(ctx, xrayEmail(u.ID)); err != nil {
			return users.User{}, err
		}
		if err = s.Xray.AddUser(ctx, xrayEmail(u.ID), newID, s.Flow); err != nil {
			_ = s.Xray.AddUser(ctx, xrayEmail(u.ID), u.Xray.UUID, s.Flow)
			return users.User{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return users.User{}, err
	}
	_ = s.Audit.Record(ctx, actor, "xray_credentials_regenerated", "user", u.ID, nil)
	return s.Store.GetUser(ctx, u.ID)
}
func (s *UserService) RegenerateHysteria(ctx context.Context, actor, id string) (users.User, error) {
	u, err := s.Store.GetUser(ctx, id)
	if err != nil {
		return users.User{}, err
	}
	p, err := password()
	if err != nil {
		return users.User{}, err
	}
	if _, err = s.Store.DB.ExecContext(ctx, `UPDATE hysteria_profiles SET password=?,updated_at=? WHERE user_id=?`, p, now(), u.ID); err != nil {
		return users.User{}, err
	}
	_ = s.Audit.Record(ctx, actor, "hysteria_credentials_regenerated", "user", u.ID, nil)
	return s.Store.GetUser(ctx, u.ID)
}
func (s *UserService) Delete(ctx context.Context, actor, id string) error {
	u, err := s.Store.GetUser(ctx, id)
	if err != nil {
		return err
	}
	tx, err := s.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM users WHERE id=?`, u.ID); err != nil {
		return err
	}
	if u.Enabled && u.Xray.Enabled {
		if err = s.Xray.RemoveUser(ctx, xrayEmail(u.ID)); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	_ = s.Audit.Record(ctx, actor, "user_deleted", "user", u.ID, map[string]any{"name": u.Name})
	return nil
}
func (s *UserService) Rename(ctx context.Context, actor, id, name string) (users.User, error) {
	name = strings.TrimSpace(name)
	if !validName.MatchString(name) {
		return users.User{}, fmt.Errorf("invalid user name")
	}
	u, err := s.Store.GetUser(ctx, id)
	if err != nil {
		return users.User{}, err
	}
	_, err = s.Store.DB.ExecContext(ctx, `UPDATE users SET name=?,updated_at=? WHERE id=?`, name, now(), u.ID)
	if err != nil {
		return users.User{}, err
	}
	return s.Store.GetUser(ctx, u.ID)
}

func (s *UserService) Reconcile(ctx context.Context) error {
	expected, err := s.Store.ActiveXrayUsers(ctx)
	if err != nil {
		return err
	}
	runtime, err := s.Xray.ListUsers(ctx)
	if err != nil {
		return err
	}
	want := map[string]users.User{}
	for _, u := range expected {
		want[xrayEmail(u.ID)] = u
	}
	have := map[string]bool{}
	for _, r := range runtime {
		if strings.HasPrefix(r.Email, "pm-") && strings.HasSuffix(r.Email, "@proxy-manager") {
			have[r.Email] = true
		}
	}
	var errs []error
	for email, u := range want {
		if !have[email] {
			if e := s.Xray.AddUser(ctx, email, u.Xray.UUID, s.Flow); e != nil {
				errs = append(errs, e)
			}
		}
	}
	for email := range have {
		if _, ok := want[email]; !ok {
			if e := s.Xray.RemoveUser(ctx, email); e != nil {
				errs = append(errs, e)
			}
		}
	}
	return errors.Join(errs...)
}

var _ = sql.ErrNoRows
