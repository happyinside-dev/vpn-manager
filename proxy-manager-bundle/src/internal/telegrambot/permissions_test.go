package telegrambot

import (
	"os"
	"path/filepath"
	"testing"

	"proxy-dev/vpn-manager/internal/model"
	"proxy-dev/vpn-manager/internal/users"
)

func TestDelegatedPermissionsAndRevocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.yaml")
	data := "users:\n  - name: parent\n    token: abcdefghijklmnopqrstuvwxyz123456\n    telegram_id: 42\n    can_manage_users: true\n  - name: child\n    token: abcdefghijklmnopqrstuvwxyz123457\n    created_by: 42\n  - name: stranger\n    token: abcdefghijklmnopqrstuvwxyz123458\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	b := &Bot{cfg: Config{UsersFile: path}, masters: map[int64]struct{}{99: {}}}
	for _, tc := range []struct {
		id   int64
		cmd  string
		args []string
		want bool
	}{
		{42, "/add", nil, true}, {42, "/users", nil, true}, {42, "/rotate", []string{"child"}, true},
		{42, "/delete", []string{"stranger", "CONFIRM"}, false}, {42, "/permissions", []string{"parent", "manage", "on"}, false},
		{42, "/selftest", nil, false}, {43, "/rotate", []string{"child"}, false}, {99, "/selftest", nil, true},
	} {
		if got := b.authorizeManagement(tc.id, tc.cmd, tc.args); got != tc.want {
			t.Fatalf("%+v: got %t", tc, got)
		}
	}
	if err := users.UpdateAccessFile(path, "parent", func(u *model.User) { u.CanManageUsers = false }); err != nil {
		t.Fatal(err)
	}
	if b.owns(42, "child") || b.authorizeManagement(42, "/add", nil) {
		t.Fatal("revoked permissions still active")
	}
	child, err := users.FindByNameFile(path, "child")
	if err != nil || child.CreatedBy != 42 {
		t.Fatal("ownership lost", err)
	}
}

func TestAccessMetadataSurvivesSync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.yaml")
	state := filepath.Join(dir, "state.json")
	if err := os.WriteFile(state, []byte(`{"accounts":{"alice":{"name":"alice","enabled":true}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := users.SyncFromProxyState(path, state); err != nil {
		t.Fatal(err)
	}
	if err := users.UpdateAccessFile(path, "alice", func(u *model.User) { u.CanRotate = true; u.CanManageUsers = true; u.CreatedBy = 42 }); err != nil {
		t.Fatal(err)
	}
	if err := users.SyncFromProxyState(path, state); err != nil {
		t.Fatal(err)
	}
	u, err := users.FindByNameFile(path, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !u.CanRotate || !u.CanManageUsers || u.CreatedBy != 42 {
		t.Fatalf("metadata lost: %+v", u)
	}
}
