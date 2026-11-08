package subscription

import (
	"context"
	"proxy-dev/vpn-manager/internal/profile"
	"proxy-dev/vpn-manager/internal/server"
	"proxy-dev/vpn-manager/internal/users"
)

type Service struct {
	users   users.Store
	servers server.Provider
	profile profile.Generator
}

func New(u users.Store, s server.Provider, p profile.Generator) *Service {
	return &Service{users: u, servers: s, profile: p}
}
func (s *Service) Generate(ctx context.Context, token string) ([]byte, error) {
	return s.GenerateForPlatform(ctx, token, "universal")
}
func (s *Service) GenerateForPlatform(ctx context.Context, token, platform string) ([]byte, error) {
	return s.GenerateWithRouting(ctx, token, platform, "")
}
func (s *Service) GenerateWithRouting(ctx context.Context, token, platform, mode string) ([]byte, error) {
	u, err := s.users.FindByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	sv, err := s.servers.Current(ctx)
	if err != nil {
		return nil, err
	}
	return s.profile.GenerateWithRouting(ctx, *u, *sv, platform, mode)
}
