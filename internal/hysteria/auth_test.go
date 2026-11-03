package hysteria_test

import (
	"bytes"
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/proxy/proxy-manager/internal/audit"
	"github.com/proxy/proxy-manager/internal/database"
	"github.com/proxy/proxy-manager/internal/hysteria"
	usersvc "github.com/proxy/proxy-manager/internal/service"
	"github.com/proxy/proxy-manager/internal/users"
	xr "github.com/proxy/proxy-manager/internal/xray"
)

type nopXray struct{}

func (nopXray) AddUser(context.Context, string, string, string) error { return nil }
func (nopXray) RemoveUser(context.Context, string) error              { return nil }
func (nopXray) ListUsers(context.Context) ([]xr.RuntimeUser, error)   { return nil, nil }
func (nopXray) Health(context.Context) error                          { return nil }
func TestAuthAcceptRejectAndDisabled(t *testing.T) {
	st, err := database.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.DB.Close()
	if err = st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	svc := usersvc.UserService{Store: st, Xray: nopXray{}, Audit: audit.Logger{DB: st.DB}}
	u, err := svc.Create(context.Background(), "test", users.CreateRequest{Name: "alice", HysteriaEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	h := hysteria.AuthHandler{Store: st}
	call := func(auth string) string {
		r := httptest.NewRequest("POST", "http://127.0.0.1/internal/hysteria/auth", bytes.NewBufferString(`{"auth":"`+auth+`"}`))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Body.String()
	}
	if got := call(u.Hysteria.Username + ":" + u.Hysteria.Password); !bytes.Contains([]byte(got), []byte(`"ok":true`)) {
		t.Fatalf("expected accept: %s", got)
	}
	if got := call(u.Hysteria.Username + ":wrong"); bytes.Contains([]byte(got), []byte(`"ok":true`)) {
		t.Fatal("accepted wrong password")
	}
	if err = svc.Disable(context.Background(), "test", u.ID); err != nil {
		t.Fatal(err)
	}
	if got := call(u.Hysteria.Username + ":" + u.Hysteria.Password); bytes.Contains([]byte(got), []byte(`"ok":true`)) {
		t.Fatal("accepted disabled account")
	}
}
