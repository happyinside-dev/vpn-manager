package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeServer(t *testing.T, body string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "server.yaml")
	if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

const base = `server:
  host: "vpn.example.com"
vless:
  enabled: true
  port: 443
  sni: "www.microsoft.com"
  public_key: "pub"
  short_id: "0123456789abcdef"
  flow: "xtls-rprx-vision"
  fingerprint: "chrome"
hysteria2:
  enabled: false
  port: 443
  sni: "vpn.example.com"
  insecure: false
  pin_sha256: ""
`

func TestValidSnapshot(t *testing.T) {
	p, err := NewFileProvider(writeServer(t, base))
	if err != nil {
		t.Fatal(err)
	}
	c, err := p.Current(context.Background())
	if err != nil || c.Host != "vpn.example.com" || !c.VLESS.Enabled {
		t.Fatalf("bad snapshot: %#v %v", c, err)
	}
}
func TestMissingVLESS(t *testing.T) {
	bad := `server:
  host: x
vless:
  enabled: true
  port: 443
  sni: x
hysteria2:
  enabled: false
`
	if _, err := NewFileProvider(writeServer(t, bad)); err == nil {
		t.Fatal("expected validation error")
	}
}
func TestMissingHysteria(t *testing.T) {
	bad := `server:
  host: x
vless:
  enabled: false
hysteria2:
  enabled: true
  port: 443
`
	if _, err := NewFileProvider(writeServer(t, bad)); err == nil {
		t.Fatal("expected validation error")
	}
}
func TestDisabledProtocol(t *testing.T) {
	ok := `server:
  host: x
vless:
  enabled: false
hysteria2:
  enabled: true
  port: 443
  sni: x.example
  insecure: false
`
	if _, err := NewFileProvider(writeServer(t, ok)); err != nil {
		t.Fatal(err)
	}
}
