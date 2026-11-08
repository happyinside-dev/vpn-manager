package profile

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Validate with the actual client parser when it is installed on the developer host.
func TestSingBoxCheck(t *testing.T) {
	binary, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("sing-box is not installed")
	}
	server := srv(true, true)
	server.VLESS.PublicKey = "WPj_DlCc80VteegkwHWJmtRGUyraQOGEHZWBw61jj0A"
	for _, platform := range []string{"ios", "macos", "linux", "universal", "openwrt"} {
		for _, mode := range []string{"", "bypass-ru", "blocked"} {
			t.Run(platform+"/"+mode, func(t *testing.T) {
				b, err := New().GenerateWithRouting(context.Background(), usr, server, platform, mode)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "config.json")
				if err := os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
				if out, err := exec.Command(binary, "check", "-c", path).CombinedOutput(); err != nil {
					t.Fatalf("sing-box check: %v\n%s", err, out)
				}
			})
		}
	}
}
