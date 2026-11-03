package subscription

import (
	"context"
	"errors"
	"proxy-dev/vpn-manager/internal/model"
	"testing"
)

type us struct {
	u   *model.User
	err error
}

func (u us) FindByToken(context.Context, string) (*model.User, error) { return u.u, u.err }

type sp struct {
	s   *model.ServerConfig
	err error
}

func (s sp) Current(context.Context) (*model.ServerConfig, error) { return s.s, s.err }

type gen struct {
	b   []byte
	err error
}

func (g gen) Generate(context.Context, model.User, model.ServerConfig) ([]byte, error) {
	return g.b, g.err
}
func (g gen) GenerateForPlatform(context.Context, model.User, model.ServerConfig, string) ([]byte, error) {
	return g.b, g.err
}
func TestGenerate(t *testing.T) {
	u := &model.User{Name: "a"}
	s := &model.ServerConfig{Host: "h"}
	svc := New(us{u: u}, sp{s: s}, gen{b: []byte("ok")})
	b, err := svc.Generate(context.Background(), "token")
	if err != nil || string(b) != "ok" {
		t.Fatalf("%s %v", b, err)
	}
}
func TestErrors(t *testing.T) {
	e := errors.New("x")
	if _, err := New(us{err: e}, sp{}, gen{}).Generate(context.Background(), "x"); !errors.Is(err, e) {
		t.Fatal(err)
	}
	u := &model.User{}
	s := &model.ServerConfig{}
	if _, err := New(us{u: u}, sp{s: s}, gen{err: e}).Generate(context.Background(), "x"); !errors.Is(err, e) {
		t.Fatal(err)
	}
}
