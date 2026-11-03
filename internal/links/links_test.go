package links_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/proxy/proxy-manager/internal/config"
	"github.com/proxy/proxy-manager/internal/links"
	"github.com/proxy/proxy-manager/internal/users"
)

func TestEscaping(t *testing.T) {
	var c config.Config
	c.PublicIP = "1.2.3.4"
	c.Port = 443
	c.Xray.SNI = "example.com"
	c.Xray.PublicKey = "pk+/?"
	c.Xray.ShortID = "abcd"
	c.Xray.Flow = "xtls-rprx-vision"
	c.Hysteria.SNI = "hy.example.com"
	u := users.User{Name: "phone one"}
	u.Xray.UUID = "11111111-1111-4111-8111-111111111111"
	u.Hysteria.Username = "a:b"
	u.Hysteria.Password = "p@ss/word"
	r := links.ForUser(c, u)
	if _, err := url.Parse(r.VLESS); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.Hysteria, "p@ss/word") {
		t.Fatal("hysteria credential not escaped")
	}
	if !strings.Contains(r.VLESS, "pbk=pk%2B%2F%3F") {
		t.Fatalf("query not escaped: %s", r.VLESS)
	}
}
