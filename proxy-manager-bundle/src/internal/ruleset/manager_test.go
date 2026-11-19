package ruleset

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

func TestSnapshotLifecycle(t *testing.T) {
	binary, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("sing-box not installed")
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source.json")
	compiled := filepath.Join(dir, "source.srs")
	if err := os.WriteFile(source, []byte(`{"version":3,"rules":[{"domain_suffix":["example.org"]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(binary, "rule-set", "compile", "--output", compiled, source).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	valid, err := os.ReadFile(compiled)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	content := valid
	status := 200
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.WriteHeader(status)
		w.Write(content)
	}))
	defer upstream.Close()
	cfg := Config{Sources: map[string]Source{}}
	for _, tag := range Tags {
		cfg.Sources[tag] = Source{URL: upstream.URL + "/" + tag}
	}
	cache := filepath.Join(dir, "cache")
	m, err := New(cache, binary, cfg)
	if err != nil {
		t.Fatal(err)
	}
	m.client = upstream.Client()
	if _, err := m.Inline("geoip-ru"); err == nil {
		t.Fatal("empty cache accepted")
	}
	if err := m.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(cache, "current", "snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		status int
		body   []byte
	}{{404, []byte("missing")}, {200, []byte("SRSbroken")}, {200, []byte("<html>error</html>")}} {
		mu.Lock()
		status = test.status
		content = test.body
		mu.Unlock()
		if err := m.Refresh(context.Background()); err == nil {
			t.Fatal("bad upstream accepted")
		}
		after, _ := os.ReadFile(filepath.Join(cache, "current", "snapshot.json"))
		if string(before) != string(after) {
			t.Fatal("last good snapshot changed")
		}
	}
	restored, err := New(cache, binary, Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range Tags {
		rs, err := restored.Inline(tag)
		if err != nil || rs["type"] != "inline" {
			t.Fatal("offline reload failed", err)
		}
	}
	mux := http.NewServeMux()
	mux.Handle("GET /sub/rules/files/{tag}/file.srs", restored)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/sub/rules/files/geoip-ru/file.srs", nil))
	if w.Code != 200 || string(w.Body.Bytes()) != string(valid) || w.Header().Get("ETag") == "" {
		t.Fatal("wrong SRS response")
	}
	req := httptest.NewRequest("GET", "/sub/rules/files/geoip-ru/file.srs", nil)
	req.Header.Set("If-None-Match", w.Header().Get("ETag"))
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 304 {
		t.Fatal(w.Code)
	}
	mu.Lock()
	status = 200
	content = valid
	mu.Unlock()
	bad := cfg.Sources["geoip-ru"]
	bad.SHA256 = "bad"
	cfg.Sources["geoip-ru"] = bad
	m.config = cfg
	if err := m.Refresh(context.Background()); err == nil {
		t.Fatal("checksum mismatch accepted")
	}
	// In-memory snapshots remain usable during failed asynchronous refreshes.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.Run(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var snapshotData snapshot
	if err := json.Unmarshal(before, &snapshotData); err != nil {
		t.Fatal(err)
	}
	e := snapshotData["geoip-ru"]
	e.Binary = []byte("corrupt")
	snapshotData["geoip-ru"] = e
	corrupt, _ := json.Marshal(snapshotData)
	os.WriteFile(filepath.Join(cache, "current", "snapshot.json"), corrupt, 0600)
	if _, err := New(cache, binary, cfg); err == nil {
		t.Fatal("corrupt disk snapshot accepted")
	}
}
