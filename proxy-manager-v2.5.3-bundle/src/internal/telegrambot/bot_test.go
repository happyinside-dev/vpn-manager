package telegrambot

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSplitCommand(t *testing.T) {
	cmd, args := splitCommand("/rotate@proxyvpn_bot demo")
	if cmd != "/rotate" || !reflect.DeepEqual(args, []string{"demo"}) {
		t.Fatalf("got %q %#v", cmd, args)
	}
}

func TestLoadConfigDefaultsAndMasters(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "telegram.json")
	body := `{"token_file":"/tmp/token","masters":[123,456],"base_url":"https://vpn.example.com"}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UsersFile != "/etc/vpn-manager/users.yaml" || cfg.ManagerPath != "/usr/local/sbin/proxy-manager" || len(cfg.Masters) != 2 {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

func TestLoadConfigRejectsHTTP(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "telegram.json")
	if err := os.WriteFile(p, []byte(`{"masters":[123],"base_url":"http://vpn.example"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(p); err == nil {
		t.Fatal("expected https validation error")
	}
}
