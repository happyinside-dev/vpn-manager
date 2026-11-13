package profile

import (
	"context"
	"encoding/json"
	"proxy-dev/vpn-manager/internal/model"
	"strings"
	"testing"
)

func srv(v, h bool) model.ServerConfig {
	return model.ServerConfig{Host: "198.51.100.10", VLESS: model.VLESSConfig{Enabled: v, Port: 443, SNI: "www.microsoft.com", PublicKey: "PUBLIC", ShortID: "0123456789abcdef", Flow: "xtls-rprx-vision", Fingerprint: "chrome"}, Hysteria: model.HysteriaConfig{Enabled: h, Port: 443, SNI: "vpn.example.com"}}
}

var usr = model.User{Name: "mama", Token: "secret", VLESSUUID: "11111111-1111-4111-8111-111111111111", HysteriaPassword: "hy-secret"}

func generate(t *testing.T, s model.ServerConfig, platform ...string) map[string]any {
	t.Helper()
	var b []byte
	var err error
	if len(platform) > 0 {
		b, err = New().GenerateForPlatform(context.Background(), usr, s, platform[0])
	} else {
		b, err = New().Generate(context.Background(), usr, s)
	}
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(b) {
		t.Fatal("invalid json")
	}
	if strings.Contains(string(b), "PRIVATE") || strings.Contains(string(b), "reality_private_key") {
		t.Fatal("private key leaked")
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
func outByTag(t *testing.T, m map[string]any, tag string) map[string]any {
	t.Helper()
	for _, x := range m["outbounds"].([]any) {
		o := x.(map[string]any)
		if o["tag"] == tag {
			return o
		}
	}
	t.Fatalf("tag %s not found", tag)
	return nil
}
func TestVLESSOnly(t *testing.T) {
	m := generate(t, srv(true, false))
	o := outByTag(t, m, "reality")
	if o["uuid"] != usr.VLESSUUID {
		t.Fatal("wrong uuid")
	}
	if _, ok := findTag(m, "hysteria2"); ok {
		t.Fatal("unexpected hysteria2")
	}
}
func TestHysteriaOnly(t *testing.T) {
	m := generate(t, srv(false, true))
	o := outByTag(t, m, "hysteria2")
	if o["password"] != "mama:hy-secret" {
		t.Fatalf("wrong hy password: %v", o["password"])
	}
}
func TestBothProtocols(t *testing.T) {
	m := generate(t, srv(true, true))
	if _, ok := findTag(m, "auto"); !ok {
		t.Fatal("auto missing")
	}
	if _, ok := findTag(m, "proxy"); !ok {
		t.Fatal("proxy missing")
	}
}
func TestMobileRuleSetsAreInline(t *testing.T) {
	m := generate(t, srv(true, true), "android")
	if _, ok := m["http_clients"]; ok {
		t.Fatal("mobile profile should not require remote HTTP clients")
	}
	route := m["route"].(map[string]any)
	for _, x := range route["rule_set"].([]any) {
		rs := x.(map[string]any)
		if rs["type"] != "inline" {
			t.Fatalf("rule-set %v is not inline", rs["tag"])
		}
	}
}
func TestAndroidHasPackageRules(t *testing.T) {
	m := generate(t, srv(true, true), "android")
	rules := m["route"].(map[string]any)["rules"].([]any)
	found := false
	for _, x := range rules {
		if _, ok := x.(map[string]any)["package_name_regex"]; ok {
			found = true
		}
	}
	if !found {
		t.Fatal("android package rule missing")
	}
}
func findTag(m map[string]any, tag string) (map[string]any, bool) {
	for _, x := range m["outbounds"].([]any) {
		o := x.(map[string]any)
		if o["tag"] == tag {
			return o, true
		}
	}
	return nil, false
}

func TestRoutingModes(t *testing.T) {
	for _, platform := range []string{"universal", "android", "ios", "macos", "linux", "tv", "openwrt"} {
		for _, mode := range []string{"bypass-ru", "blocked"} {
			t.Run(platform+"/"+mode, func(t *testing.T) {
				b, err := New().GenerateWithRouting(context.Background(), usr, srv(true, true), platform, mode)
				if err != nil {
					t.Fatal(err)
				}
				var cfg map[string]any
				if err := json.Unmarshal(b, &cfg); err != nil {
					t.Fatal(err)
				}
				route := cfg["route"].(map[string]any)
				dns := cfg["dns"].(map[string]any)
				wantRoute, wantDNS := "proxy", "dns-remote"
				if mode == "blocked" {
					wantRoute, wantDNS = "direct", "dns-direct"
				}
				if route["final"] != wantRoute || dns["final"] != wantDNS {
					t.Fatal("wrong routing or DNS policy")
				}
				if mode == "blocked" && (strings.Contains(string(b), "package_name_regex") || strings.Contains(string(b), "ru-yandex")) {
					t.Fatal("Russian bypass overrides blocked resources")
				}
				if platform == "openwrt" {
					inbound := cfg["inbounds"].([]any)[0].(map[string]any)
					if inbound["auto_redirect"] != true || inbound["interface_name"] != "singtun0" || len(inbound["address"].([]any)) != 2 {
						t.Fatal("router TUN settings missing")
					}
					if strings.Contains(string(b), "clash_api") {
						t.Fatal("router controller exposed")
					}
				}
			})
		}
	}
}

func TestRejectInvalidRouting(t *testing.T) {
	if _, err := New().GenerateWithRouting(context.Background(), usr, srv(true, true), "openwrt", "typo"); err == nil {
		t.Fatal("invalid mode accepted")
	}
}
