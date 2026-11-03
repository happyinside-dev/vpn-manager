package importurl

import (
	"strings"
	"testing"
)

func TestBuild(t *testing.T) {
	got := Build("https://vpn.example.com/sub/a+b/c?d=e", "Proxy VPN")
	if !strings.HasPrefix(got, "sing-box://import-remote-profile?url=") {
		t.Fatal(got)
	}
	if !strings.Contains(got, "https%3A%2F%2Fvpn.example.com%2Fsub%2Fa%2Bb%2Fc%3Fd%3De") {
		t.Fatal(got)
	}
	if !strings.HasSuffix(got, "#Proxy%20VPN") {
		t.Fatal(got)
	}
}
