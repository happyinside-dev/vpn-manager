package service_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/proxy/proxy-manager/internal/audit"
	"github.com/proxy/proxy-manager/internal/database"
	usersvc "github.com/proxy/proxy-manager/internal/service"
	"github.com/proxy/proxy-manager/internal/users"
	xr "github.com/proxy/proxy-manager/internal/xray"
)

type fakeXray struct {
	users map[string]string
	fail  bool
}

func (f *fakeXray) AddUser(_ context.Context, email, id, flow string) error {
	if f.fail {
		return context.DeadlineExceeded
	}
	f.users[email] = id
	return nil
}
func (f *fakeXray) RemoveUser(_ context.Context, email string) error {
	if f.fail {
		return context.DeadlineExceeded
	}
	delete(f.users, email)
	return nil
}
func (f *fakeXray) ListUsers(context.Context) ([]xr.RuntimeUser, error) {
	out := []xr.RuntimeUser{}
	for e := range f.users {
		out = append(out, xr.RuntimeUser{Email: e})
	}
	return out, nil
}
func (f *fakeXray) Health(context.Context) error { return nil }
func testSvc(t *testing.T) (*usersvc.UserService, *database.Store, *fakeXray) {
	t.Helper()
	st, err := database.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	fx := &fakeXray{users: map[string]string{}}
	return &usersvc.UserService{Store: st, Xray: fx, Audit: audit.Logger{DB: st.DB}, Flow: "xtls-rprx-vision"}, st, fx
}
func TestCreateDuplicateAndToggles(t *testing.T) {
	s, st, fx := testSvc(t)
	defer st.DB.Close()
	ctx := context.Background()
	u, err := s.Create(ctx, "test", users.CreateRequest{Name: "alice", XrayEnabled: true, HysteriaEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(fx.users) != 1 {
		t.Fatalf("runtime users=%d", len(fx.users))
	}
	if _, err = s.Create(ctx, "test", users.CreateRequest{Name: "alice"}); err == nil {
		t.Fatal("expected duplicate error")
	}
	if err = s.Disable(ctx, "test", u.ID); err != nil {
		t.Fatal(err)
	}
	if len(fx.users) != 0 {
		t.Fatal("xray user remained after account disable")
	}
	if err = s.Enable(ctx, "test", u.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.SetXray(ctx, "test", u.ID, false); err != nil {
		t.Fatal(err)
	}
	if len(fx.users) != 0 {
		t.Fatal("xray protocol disable failed")
	}
	if err = s.SetHysteria(ctx, "test", u.ID, false); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, u.ID)
	if got.Hysteria.Enabled {
		t.Fatal("hysteria still enabled")
	}
}
func TestRegenerateAndReconcile(t *testing.T) {
	s, st, fx := testSvc(t)
	defer st.DB.Close()
	ctx := context.Background()
	u, err := s.Create(ctx, "test", users.CreateRequest{Name: "bob", XrayEnabled: true, HysteriaEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	oldX, oldH := u.Xray.UUID, u.Hysteria.Password
	u, err = s.RegenerateXray(ctx, "test", u.ID)
	if err != nil || u.Xray.UUID == oldX {
		t.Fatal("xray credential did not rotate")
	}
	u, err = s.RegenerateHysteria(ctx, "test", u.ID)
	if err != nil || u.Hysteria.Password == oldH {
		t.Fatal("hysteria credential did not rotate")
	}
	fx.users = map[string]string{}
	if err = s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fx.users) != 1 {
		t.Fatal("reconcile did not restore runtime")
	}
}
