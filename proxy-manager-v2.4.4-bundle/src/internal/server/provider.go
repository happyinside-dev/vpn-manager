package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"proxy-dev/vpn-manager/internal/config"
	"proxy-dev/vpn-manager/internal/model"
)

type Provider interface {
	Current(context.Context) (*model.ServerConfig, error)
}

type FileProvider struct{ snapshot model.ServerConfig }

func NewFileProvider(path string) (*FileProvider, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read server config: %w", err)
	}
	c, err := parseServerYAML(b)
	if err != nil {
		return nil, err
	}
	if err := validate(c); err != nil {
		return nil, err
	}
	return &FileProvider{snapshot: c}, nil
}
func (p *FileProvider) Current(context.Context) (*model.ServerConfig, error) {
	c := p.snapshot
	return &c, nil
}

func parseServerYAML(data []byte) (model.ServerConfig, error) {
	lines, err := config.CleanLines(data)
	if err != nil {
		return model.ServerConfig{}, err
	}
	var out model.ServerConfig
	section := ""
	seenRoot := map[string]bool{}
	for _, line := range lines {
		ind := config.Indent(line)
		trim := strings.TrimSpace(line)
		if ind == 0 {
			if !strings.HasSuffix(trim, ":") {
				return out, fmt.Errorf("invalid top-level YAML line %q", trim)
			}
			section = strings.TrimSuffix(trim, ":")
			if seenRoot[section] {
				return out, fmt.Errorf("duplicate section %q", section)
			}
			seenRoot[section] = true
			if section != "server" && section != "vless" && section != "hysteria2" {
				return out, fmt.Errorf("unknown section %q", section)
			}
			continue
		}
		if ind != 2 || section == "" {
			return out, fmt.Errorf("invalid indentation near %q", trim)
		}
		k, v, ok := config.SplitKV(trim)
		if !ok {
			return out, fmt.Errorf("invalid YAML entry %q", trim)
		}
		switch section {
		case "server":
			if k != "host" {
				return out, fmt.Errorf("unknown server field %q", k)
			}
			out.Host = v
		case "vless":
			switch k {
			case "enabled":
				out.VLESS.Enabled, err = config.BoolValue(v)
			case "port":
				out.VLESS.Port, err = config.IntValue(v)
			case "sni":
				out.VLESS.SNI = v
			case "public_key":
				out.VLESS.PublicKey = v
			case "short_id":
				out.VLESS.ShortID = v
			case "flow":
				out.VLESS.Flow = v
			case "fingerprint":
				out.VLESS.Fingerprint = v
			default:
				return out, fmt.Errorf("unknown vless field %q", k)
			}
		case "hysteria2":
			switch k {
			case "enabled":
				out.Hysteria.Enabled, err = config.BoolValue(v)
			case "port":
				out.Hysteria.Port, err = config.IntValue(v)
			case "sni":
				out.Hysteria.SNI = v
			case "insecure":
				out.Hysteria.Insecure, err = config.BoolValue(v)
			case "pin_sha256":
				out.Hysteria.PinSHA256 = v
			default:
				return out, fmt.Errorf("unknown hysteria2 field %q", k)
			}
		}
		if err != nil {
			return out, fmt.Errorf("%s.%s: %w", section, k, err)
		}
	}
	return out, nil
}

func validate(c model.ServerConfig) error {
	if strings.TrimSpace(c.Host) == "" {
		return errors.New("server.host is required")
	}
	if !c.VLESS.Enabled && !c.Hysteria.Enabled {
		return errors.New("at least one protocol must be enabled")
	}
	if c.VLESS.Enabled {
		if c.VLESS.Port < 1 || c.VLESS.Port > 65535 {
			return errors.New("vless.port must be 1..65535")
		}
		if c.VLESS.SNI == "" || c.VLESS.PublicKey == "" || c.VLESS.ShortID == "" {
			return errors.New("enabled vless requires sni, public_key and short_id")
		}
		if c.VLESS.Flow == "" {
			c.VLESS.Flow = "xtls-rprx-vision"
		}
		if c.VLESS.Fingerprint == "" {
			c.VLESS.Fingerprint = "chrome"
		}
	}
	if c.Hysteria.Enabled {
		if c.Hysteria.Port < 1 || c.Hysteria.Port > 65535 {
			return errors.New("hysteria2.port must be 1..65535")
		}
		if c.Hysteria.SNI == "" {
			return errors.New("enabled hysteria2 requires sni")
		}
	}
	return nil
}
