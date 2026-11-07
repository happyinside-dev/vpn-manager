package users

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"proxy-dev/vpn-manager/internal/config"
	"proxy-dev/vpn-manager/internal/model"
)

var ErrNotFound = errors.New("user not found")

type Store interface {
	FindByToken(context.Context, string) (*model.User, error)
}

type YAMLStore struct{ users []model.User }

func NewYAMLStore(path string) (*YAMLStore, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat users config: %w", err)
	}
	if st.Mode().Perm()&0o027 != 0 {
		return nil, fmt.Errorf("users config permissions too open: %04o (want 0640/0600 or stricter)", st.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read users config: %w", err)
	}
	u, err := parseUsersYAML(b)
	if err != nil {
		return nil, err
	}
	seenToken := map[string]bool{}
	seenName := map[string]bool{}
	for i := range u {
		if u[i].Name == "" || u[i].Token == "" {
			return nil, fmt.Errorf("user %d requires name and token", i+1)
		}
		if len(u[i].Token) < 32 {
			return nil, fmt.Errorf("user %q token is too short", u[i].Name)
		}
		if seenToken[u[i].Token] {
			return nil, fmt.Errorf("duplicate token detected")
		}
		if seenName[u[i].Name] {
			return nil, fmt.Errorf("duplicate user name %q", u[i].Name)
		}
		seenToken[u[i].Token] = true
		seenName[u[i].Name] = true
	}
	return &YAMLStore{users: u}, nil
}

func (s *YAMLStore) FindByToken(_ context.Context, token string) (*model.User, error) {
	for i := range s.users {
		if len(token) == len(s.users[i].Token) && subtle.ConstantTimeCompare([]byte(token), []byte(s.users[i].Token)) == 1 {
			if !s.users[i].Enabled {
				return nil, ErrNotFound
			}
			u := s.users[i]
			return &u, nil
		}
	}
	return nil, ErrNotFound
}

func parseUsersYAML(data []byte) ([]model.User, error) {
	lines, err := config.CleanLines(data)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "users:" {
		return nil, errors.New("users.yaml must start with users:")
	}
	var out []model.User
	var cur *model.User
	for _, line := range lines[1:] {
		ind := config.Indent(line)
		trim := strings.TrimSpace(line)
		if ind == 2 && strings.HasPrefix(trim, "-") {
			out = append(out, model.User{Enabled: true})
			cur = &out[len(out)-1]
			rest := strings.TrimSpace(strings.TrimPrefix(trim, "-"))
			if rest == "" {
				continue
			}
			k, v, ok := config.SplitKV(rest)
			if !ok {
				return nil, fmt.Errorf("invalid user item")
			}
			if err := assign(cur, k, v); err != nil {
				return nil, err
			}
			continue
		}
		if ind != 4 || cur == nil {
			return nil, fmt.Errorf("invalid users.yaml indentation near %q", trim)
		}
		k, v, ok := config.SplitKV(trim)
		if !ok {
			return nil, fmt.Errorf("invalid user field")
		}
		if err := assign(cur, k, v); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func assign(u *model.User, k, v string) error {
	switch k {
	case "name":
		u.Name = v
	case "token":
		u.Token = v
	case "enabled":
		enabled, err := config.BoolValue(v)
		if err != nil {
			return fmt.Errorf("user %q enabled: %w", u.Name, err)
		}
		u.Enabled = enabled
	case "vless_uuid":
		u.VLESSUUID = v
	case "hysteria_password":
		u.HysteriaPassword = v
	case "telegram_id":
		if strings.TrimSpace(v) == "" || strings.TrimSpace(v) == "0" {
			u.TelegramID = 0
			break
		}
		id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return fmt.Errorf("user %q telegram_id: %w", u.Name, err)
		}
		u.TelegramID = id
	default:
		return fmt.Errorf("unknown user field %q", k)
	}
	return nil
}

type proxyState struct {
	Accounts map[string]struct {
		Name             string `json:"name"`
		Enabled          bool   `json:"enabled"`
		VLESSUUID        string `json:"xray_uuid"`
		HysteriaPassword string `json:"hysteria_password"`
	} `json:"accounts"`
}

// SyncFromProxyState updates protocol credentials and enabled state from the
// Bash installer's state while preserving each existing subscription token.
// New accounts receive a cryptographically random 256-bit token; deleted
// accounts are removed. This function is intended to run as root from the
// installer/account-manager integration.
func SyncFromProxyState(usersPath, statePath string) error {
	stateBytes, err := os.ReadFile(statePath)
	if err != nil {
		return fmt.Errorf("read proxy state: %w", err)
	}
	var ps proxyState
	if err := json.Unmarshal(stateBytes, &ps); err != nil {
		return fmt.Errorf("parse proxy state: %w", err)
	}
	if ps.Accounts == nil {
		return errors.New("proxy state has no accounts")
	}

	existing := map[string]model.User{}
	if b, err := os.ReadFile(usersPath); err == nil {
		parsed, err := parseUsersYAML(b)
		if err != nil {
			return fmt.Errorf("parse existing users config: %w", err)
		}
		for _, u := range parsed {
			existing[u.Name] = u
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read existing users config: %w", err)
	}

	names := make([]string, 0, len(ps.Accounts))
	for name := range ps.Accounts {
		names = append(names, name)
	}
	sort.Strings(names)

	synced := make([]model.User, 0, len(names))
	for _, name := range names {
		a := ps.Accounts[name]
		if a.Name == "" {
			a.Name = name
		}
		token := existing[name].Token
		if token == "" {
			token, err = randomToken()
			if err != nil {
				return err
			}
		}
		synced = append(synced, model.User{
			Name:             a.Name,
			Token:            token,
			Enabled:          a.Enabled,
			VLESSUUID:        a.VLESSUUID,
			HysteriaPassword: a.HysteriaPassword,
			TelegramID:       existing[name].TelegramID,
		})
	}
	return writeUsersYAMLAtomic(usersPath, synced)
}

func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate subscription token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func writeUsersYAMLAtomic(path string, users []model.User) error {
	// Preserve owner/group and permissions of an existing users file so root-side
	// Telegram administration cannot accidentally revoke the unprivileged
	// subscription service's read access.
	var oldMode os.FileMode = 0o600
	oldUID, oldGID := -1, -1
	if st, err := os.Stat(path); err == nil {
		oldMode = st.Mode().Perm()
		if sys, ok := st.Sys().(*syscall.Stat_t); ok {
			oldUID, oldGID = int(sys.Uid), int(sys.Gid)
		}
	}
	var b strings.Builder
	b.WriteString("users:\n")
	for _, u := range users {
		fmt.Fprintf(&b, "  - name: %q\n", u.Name)
		fmt.Fprintf(&b, "    token: %q\n", u.Token)
		fmt.Fprintf(&b, "    enabled: %t\n", u.Enabled)
		fmt.Fprintf(&b, "    vless_uuid: %q\n", u.VLESSUUID)
		fmt.Fprintf(&b, "    hysteria_password: %q\n", u.HysteriaPassword)
		if u.TelegramID != 0 {
			fmt.Fprintf(&b, "    telegram_id: %d\n", u.TelegramID)
		}
	}

	dir := "."
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		if i == 0 {
			dir = "/"
		} else {
			dir = path[:i]
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create users config directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".users.yaml.tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary users config: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	defer cleanup()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write users config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync users config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close users config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace users config: %w", err)
	}
	if oldUID >= 0 && oldGID >= 0 {
		_ = os.Chown(path, oldUID, oldGID)
	}
	return os.Chmod(path, oldMode)
}

// FindByNameFile returns a user from the root-only YAML config for administrative CLI use.
func FindByNameFile(path, name string) (*model.User, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read users config: %w", err)
	}
	parsed, err := parseUsersYAML(b)
	if err != nil {
		return nil, err
	}
	for i := range parsed {
		if parsed[i].Name == name {
			u := parsed[i]
			return &u, nil
		}
	}
	return nil, ErrNotFound
}

// ListFile returns all configured users for administrative use.
func ListFile(path string) ([]model.User, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read users config: %w", err)
	}
	return parseUsersYAML(b)
}

// FindByTelegramIDFile resolves a Telegram account to exactly one VPN user.
func FindByTelegramIDFile(path string, id int64) (*model.User, error) {
	all, err := ListFile(path)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].TelegramID == id {
			u := all[i]
			return &u, nil
		}
	}
	return nil, ErrNotFound
}

// SetTelegramIDFile binds or unbinds a Telegram numeric user id.
func SetTelegramIDFile(path, name string, id int64) error {
	all, err := ListFile(path)
	if err != nil {
		return err
	}
	found := false
	for i := range all {
		if id != 0 && all[i].TelegramID == id && all[i].Name != name {
			return fmt.Errorf("telegram id already bound")
		}
		if all[i].Name == name {
			all[i].TelegramID = id
			found = true
		}
	}
	if !found {
		return ErrNotFound
	}
	return writeUsersYAMLAtomic(path, all)
}

// RotateTokenFile replaces only a subscription bearer token. VPN protocol credentials are untouched.
func RotateTokenFile(path, name string) (string, error) {
	all, err := ListFile(path)
	if err != nil {
		return "", err
	}
	var token string
	found := false
	for i := range all {
		if all[i].Name == name {
			token, err = randomToken()
			if err != nil {
				return "", err
			}
			all[i].Token = token
			found = true
			break
		}
	}
	if !found {
		return "", ErrNotFound
	}
	if err := writeUsersYAMLAtomic(path, all); err != nil {
		return "", err
	}
	return token, nil
}
