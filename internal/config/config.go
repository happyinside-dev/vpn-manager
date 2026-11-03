package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen         string        `yaml:"listen"`
	InternalListen string        `yaml:"internal_listen"`
	Database       string        `yaml:"database"`
	SessionSecure  bool          `yaml:"session_secure"`
	SessionTTL     time.Duration `yaml:"session_ttl"`
	PublicHost     string        `yaml:"public_host"`
	PublicIP       string        `yaml:"public_ip"`
	Port           int           `yaml:"port"`
	Xray           struct {
		APIAddress string `yaml:"api_address"`
		InboundTag string `yaml:"inbound_tag"`
		SNI        string `yaml:"sni"`
		PublicKey  string `yaml:"public_key"`
		ShortID    string `yaml:"short_id"`
		Flow       string `yaml:"flow"`
	} `yaml:"xray"`
	Hysteria struct {
		SNI            string `yaml:"sni"`
		CertificatePin string `yaml:"certificate_pin"`
		Insecure       bool   `yaml:"insecure"`
	} `yaml:"hysteria"`
	Services struct {
		Xray     string `yaml:"xray"`
		Hysteria string `yaml:"hysteria"`
	} `yaml:"services"`
}

func Load(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, err
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:18080"
	}
	if c.InternalListen == "" {
		c.InternalListen = c.Listen
	}
	if c.Database == "" {
		c.Database = "/var/lib/proxy-manager/proxy-manager.db"
	}
	if c.SessionTTL == 0 {
		c.SessionTTL = 12 * time.Hour
	}
	if c.Port == 0 {
		c.Port = 443
	}
	if c.Xray.APIAddress == "" {
		c.Xray.APIAddress = "127.0.0.1:10085"
	}
	if c.Xray.InboundTag == "" {
		c.Xray.InboundTag = "vless-reality"
	}
	if c.Xray.Flow == "" {
		c.Xray.Flow = "xtls-rprx-vision"
	}
	if c.Services.Xray == "" {
		c.Services.Xray = "xray.service"
	}
	if c.Services.Hysteria == "" {
		c.Services.Hysteria = "hysteria-server.service"
	}
	if c.PublicHost == "" && c.PublicIP == "" {
		return c, fmt.Errorf("public_host or public_ip is required")
	}
	if c.Xray.SNI == "" || c.Xray.PublicKey == "" || c.Xray.ShortID == "" {
		return c, fmt.Errorf("xray sni, public_key and short_id are required")
	}
	if c.Hysteria.SNI == "" {
		return c, fmt.Errorf("hysteria sni is required")
	}
	return c, nil
}

func (c Config) EndpointHost() string {
	if c.PublicHost != "" {
		return c.PublicHost
	}
	return c.PublicIP
}
