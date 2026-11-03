package xray

import (
	"context"
	"fmt"
	"strings"
	"time"

	handler "github.com/xtls/xray-core/app/proxyman/command"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/serial"
	vless "github.com/xtls/xray-core/proxy/vless"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type RuntimeUser struct{ Email string }
type Client interface {
	AddUser(context.Context, string, string, string) error
	RemoveUser(context.Context, string) error
	ListUsers(context.Context) ([]RuntimeUser, error)
	Health(context.Context) error
}

type GRPC struct {
	Addr, Tag, Flow string
	Timeout         time.Duration
}

func (g GRPC) dial(ctx context.Context) (*grpc.ClientConn, error) {
	dctx, cancel := context.WithTimeout(ctx, g.timeout())
	defer cancel()
	return grpc.DialContext(dctx, g.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
}
func (g GRPC) timeout() time.Duration {
	if g.Timeout == 0 {
		return 3 * time.Second
	}
	return g.Timeout
}
func (g GRPC) AddUser(ctx context.Context, email, uuid, flow string) error {
	conn, err := g.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if flow == "" {
		flow = g.Flow
	}
	_, err = handler.NewHandlerServiceClient(conn).AlterInbound(ctx, &handler.AlterInboundRequest{Tag: g.Tag, Operation: serial.ToTypedMessage(&handler.AddUserOperation{User: &protocol.User{Email: email, Account: serial.ToTypedMessage(&vless.Account{Id: uuid, Flow: flow})}})})
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "already exists") {
		return nil
	}
	return err
}
func (g GRPC) RemoveUser(ctx context.Context, email string) error {
	conn, err := g.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = handler.NewHandlerServiceClient(conn).AlterInbound(ctx, &handler.AlterInboundRequest{Tag: g.Tag, Operation: serial.ToTypedMessage(&handler.RemoveUserOperation{Email: email})})
	if err != nil && (strings.Contains(strings.ToLower(err.Error()), "not found") || strings.Contains(strings.ToLower(err.Error()), "doesn't exist")) {
		return nil
	}
	return err
}
func (g GRPC) ListUsers(ctx context.Context) ([]RuntimeUser, error) {
	conn, err := g.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	r, err := handler.NewHandlerServiceClient(conn).GetInboundUsers(ctx, &handler.GetInboundUserRequest{Tag: g.Tag})
	if err != nil {
		return nil, err
	}
	out := make([]RuntimeUser, 0, len(r.Users))
	for _, u := range r.Users {
		out = append(out, RuntimeUser{Email: u.Email})
	}
	return out, nil
}
func (g GRPC) Health(ctx context.Context) error {
	_, err := g.ListUsers(ctx)
	if err != nil {
		return fmt.Errorf("xray api: %w", err)
	}
	return nil
}
