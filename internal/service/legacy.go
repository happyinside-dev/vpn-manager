package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/proxy/proxy-manager/internal/database"
	"gopkg.in/yaml.v3"
)

type LegacyResult struct {
	XrayUUID         string
	HysteriaPassword string
}

func DetectLegacy(xrayPath, hysteriaPath, inboundTag string) (LegacyResult, error) {
	var out LegacyResult
	xb, err := os.ReadFile(xrayPath)
	if err != nil {
		return out, err
	}
	var xc map[string]any
	if err = json.Unmarshal(xb, &xc); err != nil {
		return out, fmt.Errorf("parse xray: %w", err)
	}
	ins, _ := xc["inbounds"].([]any)
	var ids []string
	for _, raw := range ins {
		m, _ := raw.(map[string]any)
		if fmt.Sprint(m["protocol"]) != "vless" {
			continue
		}
		// Old proxy-setup.sh installations did not tag the single VLESS
		// inbound. Accept an untagged legacy inbound, but if a tag is
		// present it must match the managed inbound tag. Multiple clients
		// are still rejected below as ambiguous.
		tag := fmt.Sprint(m["tag"])
		if inboundTag != "" && tag != "" && tag != inboundTag {
			continue
		}
		settings, _ := m["settings"].(map[string]any)
		clients, _ := settings["clients"].([]any)
		for _, cr := range clients {
			cm, _ := cr.(map[string]any)
			if id, ok := cm["id"].(string); ok && id != "" {
				ids = append(ids, id)
			}
		}
	}
	if len(ids) > 1 {
		return out, fmt.Errorf("legacy Xray config has %d users; automatic single-user migration is ambiguous", len(ids))
	}
	if len(ids) == 1 {
		out.XrayUUID = ids[0]
	}
	hb, err := os.ReadFile(hysteriaPath)
	if err != nil {
		return out, err
	}
	var hc map[string]any
	if err = yaml.Unmarshal(hb, &hc); err != nil {
		return out, fmt.Errorf("parse hysteria: %w", err)
	}
	auth, _ := hc["auth"].(map[string]any)
	typ := fmt.Sprint(auth["type"])
	if typ != "" && typ != "password" {
		return out, fmt.Errorf("legacy Hysteria auth type %q is not a single password; refusing automatic migration", typ)
	}
	if p, ok := auth["password"].(string); ok {
		out.HysteriaPassword = p
	}
	if out.XrayUUID == "" && out.HysteriaPassword == "" {
		return out, fmt.Errorf("no legacy credentials detected")
	}
	return out, nil
}
func Backup(paths ...string) error {
	stamp := time.Now().UTC().Format("20060102T150405Z")
	for _, p := range paths {
		if p == "" {
			continue
		}
		b, err := os.ReadFile(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		dst := p + ".proxy-manager-backup-" + stamp
		if err := os.WriteFile(dst, b, 0600); err != nil {
			return err
		}
		_ = filepath.Clean(dst)
	}
	return nil
}
func ImportLegacy(ctx context.Context, store *database.Store, r LegacyResult) error {
	var n int
	if err := store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return fmt.Errorf("database already contains users; refusing automatic legacy migration")
	}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	uid := uuid.NewString()
	xuuid := r.XrayUUID
	if xuuid == "" {
		xuuid = uuid.NewString()
	}
	hp := r.HysteriaPassword
	if hp == "" {
		b := uuid.New()
		hp = b.String() + uuid.NewString()
	}
	tx, err := store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO users(id,name,enabled,created_at,updated_at) VALUES(?, 'legacy',1,?,?)`, uid, ts, ts); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO xray_profiles(id,user_id,uuid,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?)`, uuid.NewString(), uid, xuuid, boolInt(r.XrayUUID != ""), ts, ts); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hysteria_profiles(id,user_id,username,password,enabled,created_at,updated_at,legacy_compat) VALUES(?,?,?,?,?,?,?,?)`, uuid.NewString(), uid, "legacy", hp, boolInt(r.HysteriaPassword != ""), ts, ts, boolInt(r.HysteriaPassword != "")); err != nil {
		return err
	}
	return tx.Commit()
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
