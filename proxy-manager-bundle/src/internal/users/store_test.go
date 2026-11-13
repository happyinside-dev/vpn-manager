package users

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func writeUsers(t *testing.T, body string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "users.yaml")
	if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}
func TestLookupAndUnknown(t *testing.T) {
	st, err := NewYAMLStore(writeUsers(t, "users:\n  - name: mama\n    token: abcdefghijklmnopqrstuvwxyz123456\n    vless_uuid: u1\n    hysteria_password: p1\n"))
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.FindByToken(context.Background(), "abcdefghijklmnopqrstuvwxyz123456")
	if err != nil || u.Name != "mama" {
		t.Fatalf("lookup: %#v %v", u, err)
	}
	if _, err := st.FindByToken(context.Background(), "nope"); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
func TestMalformedAndDuplicate(t *testing.T) {
	if _, err := NewYAMLStore(writeUsers(t, "bad:\n x: y\n")); err == nil {
		t.Fatal("expected malformed YAML error")
	}
	dup := "users:\n  - name: a\n    token: abcdefghijklmnopqrstuvwxyz123456\n  - name: b\n    token: abcdefghijklmnopqrstuvwxyz123456\n"
	if _, err := NewYAMLStore(writeUsers(t, dup)); err == nil {
		t.Fatal("expected duplicate token error")
	}
}
func TestPermissions(t *testing.T) {
	f := writeUsers(t, "users:\n  - name: a\n    token: abcdefghijklmnopqrstuvwxyz123456\n")
	if err := os.Chmod(f, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewYAMLStore(f); err == nil {
		t.Fatal("expected permissions error")
	}
}

func TestDisabledUserKeepsTokenButCannotAuthenticate(t *testing.T) {
	st, err := NewYAMLStore(writeUsers(t, "users:\n  - name: mama\n    token: abcdefghijklmnopqrstuvwxyz123456\n    enabled: false\n    vless_uuid: u1\n    hysteria_password: p1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.FindByToken(context.Background(), "abcdefghijklmnopqrstuvwxyz123456"); err != ErrNotFound {
		t.Fatalf("want ErrNotFound for disabled user, got %v", err)
	}
}

func TestSyncFromProxyStatePreservesTokensAndCredentials(t *testing.T) {
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.yaml")
	statePath := filepath.Join(dir, "state.json")
	oldToken := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := os.WriteFile(usersPath, []byte("users:\n  - name: mama\n    token: \""+oldToken+"\"\n    enabled: true\n    vless_uuid: \"old\"\n    hysteria_password: \"oldpass\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := `{"accounts":{"mama":{"name":"mama","enabled":false,"xray_uuid":"newuuid","hysteria_password":"newpass"},"papa":{"name":"papa","enabled":true,"xray_uuid":"papauuid","hysteria_password":"papapass"}}}`
	if err := os.WriteFile(statePath, []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SyncFromProxyState(usersPath, statePath); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(usersPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, oldToken) {
		t.Fatal("existing token was not preserved")
	}
	if !strings.Contains(text, `vless_uuid: "newuuid"`) || !strings.Contains(text, `hysteria_password: "newpass"`) {
		t.Fatal("credentials were not synchronized")
	}
	if !strings.Contains(text, "enabled: false") {
		t.Fatal("disabled state was not synchronized")
	}
	st, err := NewYAMLStore(usersPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.FindByToken(context.Background(), oldToken); err != ErrNotFound {
		t.Fatalf("disabled synchronized user should be unavailable, got %v", err)
	}
	// papa must have received a new random token and remain available.
	var papaToken string
	for _, line := range strings.Split(text, "\n") {
		_ = line
	}
	parsed, err := parseUsersYAML(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range parsed {
		if u.Name == "papa" {
			papaToken = u.Token
		}
	}
	if len(papaToken) != 64 {
		t.Fatalf("new token length=%d, want 64 hex chars", len(papaToken))
	}
}

func TestSyncFromProxyStateDeleteDoesNotReuseToken(t *testing.T) {
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.yaml")
	statePath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(usersPath, []byte("users:\n  - name: deleted\n    token: \"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\"\n    enabled: true\n    vless_uuid: \"u\"\n    hysteria_password: \"p\"\n  - name: kept\n    token: \"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc\"\n    enabled: true\n    vless_uuid: \"u2\"\n    hysteria_password: \"p2\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := `{"accounts":{"kept":{"name":"kept","enabled":true,"xray_uuid":"u3","hysteria_password":"p3"}}}`
	if err := os.WriteFile(statePath, []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SyncFromProxyState(usersPath, statePath); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(usersPath)
	if strings.Contains(string(b), "deleted") {
		t.Fatal("deleted account remained in users config")
	}
	if !strings.Contains(string(b), "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc") {
		t.Fatal("kept token changed")
	}
}

func TestEmptyStoreAndSyncAfterLastDelete(t *testing.T) {
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.yaml")
	statePath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(usersPath, []byte("users:\n  - name: last\n    token: \"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd\"\n    enabled: true\n    vless_uuid: \"u\"\n    hysteria_password: \"p\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte(`{"accounts":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SyncFromProxyState(usersPath, statePath); err != nil {
		t.Fatal(err)
	}
	st, err := NewYAMLStore(usersPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.FindByToken(context.Background(), "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"); err != ErrNotFound {
		t.Fatalf("old token after last delete: want ErrNotFound, got %v", err)
	}
}

func TestTelegramBindingAndTokenRotationPreserveUser(t *testing.T) {
	f := writeUsers(t, "users:\n  - name: demo\n    token: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n    enabled: true\n    vless_uuid: u1\n    hysteria_password: p1\n")
	if err := SetTelegramIDFile(f, "demo", 123456789); err != nil {
		t.Fatal(err)
	}
	u, err := FindByTelegramIDFile(f, 123456789)
	if err != nil || u.Name != "demo" {
		t.Fatalf("telegram lookup: %#v %v", u, err)
	}
	old := u.Token
	newToken, err := RotateTokenFile(f, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if newToken == old || len(newToken) != 64 {
		t.Fatalf("bad rotated token: %q", newToken)
	}
	u, err = FindByTelegramIDFile(f, 123456789)
	if err != nil || u.Token != newToken || u.VLESSUUID != "u1" || u.HysteriaPassword != "p1" {
		t.Fatalf("rotation changed unrelated fields: %#v %v", u, err)
	}
}

func TestSyncPreservesTelegramBinding(t *testing.T) {
	dir := t.TempDir()
	usersPath := filepath.Join(dir, "users.yaml")
	statePath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(usersPath, []byte("users:\n  - name: demo\n    token: \"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"\n    enabled: true\n    vless_uuid: \"old\"\n    hysteria_password: \"oldpass\"\n    telegram_id: 123456789\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	state := `{"accounts":{"demo":{"name":"demo","enabled":true,"xray_uuid":"newuuid","hysteria_password":"newpass"}}}`
	if err := os.WriteFile(statePath, []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SyncFromProxyState(usersPath, statePath); err != nil {
		t.Fatal(err)
	}
	u, err := FindByTelegramIDFile(usersPath, 123456789)
	if err != nil || u.VLESSUUID != "newuuid" || u.HysteriaPassword != "newpass" {
		t.Fatalf("binding not preserved: %#v %v", u, err)
	}
	st, err := os.Stat(usersPath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o640 {
		t.Fatalf("mode changed: got %04o want 0640", st.Mode().Perm())
	}
}

func TestFileStoreSeesCredentialAndTokenChanges(t *testing.T) {
	token := "abcdefghijklmnopqrstuvwxyz123456"
	path := writeUsers(t, "users:\n  - name: alice\n    token: "+token+"\n    vless_uuid: old\n    hysteria_password: old\n")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := RotateTokenFile(path, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.FindByToken(context.Background(), token); err != ErrNotFound {
		t.Fatalf("old token: %v", err)
	}
	if _, err = store.FindByToken(context.Background(), rotated); err != nil {
		t.Fatalf("new token: %v", err)
	}
	state := filepath.Join(t.TempDir(), "state.json")
	if err = os.WriteFile(state, []byte(`{"accounts":{"alice":{"name":"alice","enabled":true,"xray_uuid":"new","hysteria_password":"new"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = SyncFromProxyState(path, state); err != nil {
		t.Fatal(err)
	}
	u, err := store.FindByToken(context.Background(), rotated)
	if err != nil || u.VLESSUUID != "new" || u.HysteriaPassword != "new" {
		t.Fatalf("credentials: %+v %v", u, err)
	}
	if err = os.WriteFile(state, []byte(`{"accounts":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = SyncFromProxyState(path, state); err != nil {
		t.Fatal(err)
	}
	if _, err = store.FindByToken(context.Background(), rotated); err != ErrNotFound {
		t.Fatalf("deleted user: %v", err)
	}
}

func TestConcurrentCredentialAndTelegramWrites(t *testing.T) {
	var body strings.Builder
	body.WriteString("users:\n")
	for i := 0; i < 16; i++ {
		fmt.Fprintf(&body, "  - name: user%d\n    token: %064d\n", i, i+1)
	}
	path := writeUsers(t, body.String())
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 16; i++ {
		name := fmt.Sprintf("user%d", i)
		wg.Add(2)
		go func() { defer wg.Done(); errs <- SetTelegramIDFile(path, name, int64(i+1)) }()
		go func() { defer wg.Done(); _, err := RotateTokenFile(path, name); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 16; i++ {
		u, err := FindByNameFile(path, fmt.Sprintf("user%d", i))
		if err != nil || u.TelegramID != int64(i+1) || u.Token == fmt.Sprintf("%064d", i+1) {
			t.Fatalf("lost concurrent update: %+v %v", u, err)
		}
	}
}
