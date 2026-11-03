package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"proxy-dev/vpn-manager/internal/model"
)

type Generator interface {
	Generate(context.Context, model.User, model.ServerConfig) ([]byte, error)
	GenerateForPlatform(context.Context, model.User, model.ServerConfig, string) ([]byte, error)
}

type SingBoxGenerator struct{}

func New() *SingBoxGenerator { return &SingBoxGenerator{} }

func (g *SingBoxGenerator) Generate(ctx context.Context, u model.User, s model.ServerConfig) ([]byte, error) {
	return g.GenerateForPlatform(ctx, u, s, "universal")
}

func (g *SingBoxGenerator) GenerateForPlatform(_ context.Context, u model.User, s model.ServerConfig, platform string) ([]byte, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	switch platform {
	case "", "universal", "android", "ios", "macos", "linux", "tv":
		if platform == "" {
			platform = "universal"
		}
	default:
		return nil, fmt.Errorf("unsupported platform %q", platform)
	}

	if s.VLESS.Enabled && u.VLESSUUID == "" {
		return nil, errors.New("vless enabled but user UUID is missing")
	}
	if s.Hysteria.Enabled && u.HysteriaPassword == "" {
		return nil, errors.New("hysteria2 enabled but user password is missing")
	}

	out := []any{}
	protocolTags := []string{}
	if s.Hysteria.Enabled {
		tls := map[string]any{"enabled": true, "server_name": s.Hysteria.SNI, "insecure": s.Hysteria.Insecure}
		if s.Hysteria.PinSHA256 != "" {
			tls["certificate_public_key_sha256"] = []string{s.Hysteria.PinSHA256}
		}
		out = append(out, map[string]any{
			"type": "hysteria2", "tag": "hysteria2", "server": s.Host, "server_port": s.Hysteria.Port,
			"password": fmt.Sprintf("%s:%s", u.Name, u.HysteriaPassword), "tls": tls,
		})
		protocolTags = append(protocolTags, "hysteria2")
	}
	if s.VLESS.Enabled {
		flow := s.VLESS.Flow
		if flow == "" {
			flow = "xtls-rprx-vision"
		}
		fp := s.VLESS.Fingerprint
		if fp == "" {
			fp = "chrome"
		}
		out = append(out, map[string]any{
			"type": "vless", "tag": "reality", "server": s.Host, "server_port": s.VLESS.Port,
			"uuid": u.VLESSUUID, "flow": flow,
			"tls": map[string]any{
				"enabled": true, "server_name": s.VLESS.SNI,
				"utls":    map[string]any{"enabled": true, "fingerprint": fp},
				"reality": map[string]any{"enabled": true, "public_key": s.VLESS.PublicKey, "short_id": s.VLESS.ShortID},
			},
		})
		protocolTags = append(protocolTags, "reality")
	}
	if len(protocolTags) == 0 {
		return nil, errors.New("no VPN protocols enabled")
	}

	selectorMembers := append([]string{}, protocolTags...)
	defaultTag := protocolTags[0]
	if len(protocolTags) > 1 {
		out = append(out, map[string]any{
			"type": "urltest", "tag": "auto", "outbounds": protocolTags,
			"url": "https://cp.cloudflare.com/generate_204", "interval": "3m", "tolerance": 150,
		})
		selectorMembers = append([]string{"auto"}, selectorMembers...)
		defaultTag = "auto"
	}
	out = append([]any{map[string]any{"type": "selector", "tag": "proxy", "outbounds": selectorMembers, "default": defaultTag}}, out...)
	out = append(out, map[string]any{"type": "direct", "tag": "direct"})

	inbound := map[string]any{
		"type": "tun", "tag": "tun-in", "address": []string{"172.19.0.1/30"}, "auto_route": true,
	}
	if platform == "android" || platform == "tv" || platform == "linux" || platform == "universal" {
		inbound["auto_redirect"] = true
	}

	// Remote rule-set downloads must not depend on the VPN being up. Since 1.14,
	// sing-box supports explicit reusable HTTP clients. Detouring this client via
	// direct avoids a startup cycle where remote rule-sets are fetched through
	// the default proxy before REALITY/Hysteria is usable.
	httpClients := []any{
		map[string]any{
			"tag":             "rules-direct",
			"detour":          "direct",
			"domain_resolver": "dns-bootstrap",
		},
	}

	ruleSets := []any{
		map[string]any{"type": "remote", "tag": "geoip-ru", "format": "binary", "url": "https://cdn.jsdelivr.net/gh/SagerNet/sing-geoip@rule-set/geoip-ru.srs", "http_client": "rules-direct", "update_interval": "7d"},
		map[string]any{"type": "remote", "tag": "geosite-yandex", "format": "binary", "url": "https://cdn.jsdelivr.net/gh/SagerNet/sing-geosite@rule-set/geosite-yandex.srs", "http_client": "rules-direct", "update_interval": "7d"},
		map[string]any{"type": "remote", "tag": "geosite-vk", "format": "binary", "url": "https://cdn.jsdelivr.net/gh/SagerNet/sing-geosite@rule-set/geosite-vk.srs", "http_client": "rules-direct", "update_interval": "7d"},
		map[string]any{"type": "remote", "tag": "geosite-category-gov-ru", "format": "binary", "url": "https://cdn.jsdelivr.net/gh/SagerNet/sing-geosite@rule-set/geosite-category-gov-ru.srs", "http_client": "rules-direct", "update_interval": "7d"},
	}

	routeRules := []any{
		map[string]any{"action": "sniff"},
		map[string]any{"protocol": "dns", "action": "hijack-dns"},
		map[string]any{"ip_is_private": true, "outbound": "direct"},
	}

	if platform == "android" || platform == "tv" {
		// Android package matching is platform-specific. Regexes intentionally
		// target common Russian banking, government, marketplace, messenger,
		// operator, media and navigation ecosystems without requiring a giant
		// fragile exact-package list.
		routeRules = append(routeRules, map[string]any{
			"package_name_regex": []string{
				`(?i)(sber|sberbank|alfabank|tinkoff|tbank|vtb|gazprombank|raiffeisen|rshb|rosselhoz|sovcom|halva|pochta.?bank|mts.?bank|psb|promsvyaz|mirpay|sbp)`,
				`(?i)(gosuslugi|ru\.rostel|nalog|fns|mos\.ru|mosru)`,
				`(?i)(ozon|wildberries|wb\.ru|megamarket|market\.yandex)`,
				`(?i)(vkontakte|vk\.|ru\.oneme|tam tam|telegram|yandex|2gis|avito)`,
				`(?i)(mts|megafon|beeline|tele2|t2\.ru|rostelecom)`,
				`(?i)(kinopoisk|ivi|okko|wink|rutube|premier|smotrim|start\.ru|kion)`,
			},
			"outbound": "direct",
		})
	}

	if platform == "tv" {
		routeRules = append(routeRules,
			map[string]any{"ip_cidr": []string{"224.0.0.0/4", "ff00::/8"}, "outbound": "direct"},
		)
	}

	routeRules = append(routeRules,
		map[string]any{"domain_suffix": []string{"ru", "su", "xn--p1ai"}, "outbound": "direct"},
		map[string]any{"rule_set": []string{"geosite-yandex", "geosite-vk", "geosite-category-gov-ru", "geoip-ru"}, "outbound": "direct"},
	)

	cfg := map[string]any{
		"$schema":      "https://sing-box.sagernet.org/schema.json",
		"log":          map[string]any{"level": "warn"},
		"http_clients": httpClients,
		"dns": map[string]any{
			"servers": []any{
				map[string]any{"type": "local", "tag": "dns-bootstrap"},
				map[string]any{"type": "https", "tag": "dns-remote", "server": "1.1.1.1", "detour": "proxy"},
				map[string]any{"type": "udp", "tag": "dns-direct", "server": "77.88.8.8"},
			},
			"rules": []any{
				map[string]any{"domain_suffix": []string{"ru", "su", "xn--p1ai"}, "server": "dns-direct"},
				map[string]any{"rule_set": []string{"geosite-yandex", "geosite-vk", "geosite-category-gov-ru"}, "server": "dns-direct"},
			},
			"final":    "dns-remote",
			"strategy": "ipv4_only",
		},
		"inbounds":  []any{inbound},
		"outbounds": out,
		"route": map[string]any{
			"rule_set":                ruleSets,
			"rules":                   routeRules,
			"final":                   "proxy",
			"auto_detect_interface":   true,
			"default_domain_resolver": "dns-direct",
			"default_http_client":     "rules-direct",
		},
		"experimental": map[string]any{
			"cache_file": map[string]any{"enabled": true},
			"clash_api":  map[string]any{"external_controller": "127.0.0.1:9090"},
		},
	}

	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal sing-box profile: %w", err)
	}
	return append(b, '\n'), nil
}
