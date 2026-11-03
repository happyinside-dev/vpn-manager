package links

import (
	"fmt"
	"net/url"

	"github.com/proxy/proxy-manager/internal/config"
	"github.com/proxy/proxy-manager/internal/users"
)

type Result struct {
	VLESS    string `json:"vless"`
	Hysteria string `json:"hysteria"`
}

func ForUser(c config.Config, u users.User) Result {
	host := c.EndpointHost()
	q := url.Values{}
	q.Set("encryption", "none")
	q.Set("flow", c.Xray.Flow)
	q.Set("security", "reality")
	q.Set("sni", c.Xray.SNI)
	q.Set("fp", "chrome")
	q.Set("pbk", c.Xray.PublicKey)
	q.Set("sid", c.Xray.ShortID)
	q.Set("type", "tcp")
	q.Set("headerType", "none")
	v := url.URL{Scheme: "vless", User: url.User(u.Xray.UUID), Host: fmt.Sprintf("%s:%d", host, c.Port), RawQuery: q.Encode(), Fragment: u.Name + "-Xray"}

	hq := url.Values{}
	hq.Set("sni", c.Hysteria.SNI)
	if c.Hysteria.Insecure {
		hq.Set("insecure", "1")
	}
	if c.Hysteria.CertificatePin != "" {
		hq.Set("pinSHA256", c.Hysteria.CertificatePin)
	}
	hyURL := url.URL{Scheme: "hysteria2", User: url.UserPassword(u.Hysteria.Username, u.Hysteria.Password), Host: fmt.Sprintf("%s:%d", host, c.Port), Path: "/", RawQuery: hq.Encode(), Fragment: u.Name + "-Hysteria"}
	return Result{VLESS: v.String(), Hysteria: hyURL.String()}
}
