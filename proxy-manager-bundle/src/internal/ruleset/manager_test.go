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
	"time"
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

func compileFixture(t *testing.T, binary, domain string) []byte {
	t.Helper()
	dir := t.TempDir()
	input, output := filepath.Join(dir, "rules.json"), filepath.Join(dir, "rules.srs")
	data, _ := json.Marshal(map[string]any{"version": 3, "rules": []any{map[string]any{"domain_suffix": []string{domain}}}})
	if err := os.WriteFile(input, data, 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(binary, "rule-set", "compile", "--output", output, input).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	b, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLocalDirectoryAutomaticReload(t *testing.T) {
	binary, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("sing-box not installed")
	}
	input, cache := t.TempDir(), t.TempDir()
	first, second := compileFixture(t, binary, "first.example"), compileFixture(t, binary, "other.example")
	m, err := New(cache, binary, Config{LocalDir: input})
	if err != nil {
		t.Fatal(err)
	}
	m.pollInterval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("poller did not stop")
		}
	}()
	write := func(name string, b []byte) {
		t.Helper()
		tmp := filepath.Join(input, name+".tmp")
		if err := os.WriteFile(tmp, b, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, filepath.Join(input, name)); err != nil {
			t.Fatal(err)
		}
	}
	wait := func(predicate func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !predicate() {
			if time.Now().After(deadline) {
				t.Fatal("automatic reload timed out")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	write("geoip-ru.srs", first)
	if err := m.Refresh(ctx); err == nil {
		t.Fatal("partial initial set was accepted")
	}
	if m.revision() != "" { // An empty snapshot has an empty revision.
		t.Fatal("partial set was published")
	}
	write("geosite-ru-blocked.srs", first)
	write("geoip-ru-blocked.srs", first)
	wait(func() bool { return m.revision() != "" })
	before := m.revision()
	generation, err := os.Readlink(filepath.Join(cache, "current"))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := os.Readlink(filepath.Join(cache, "current"))
	if generation != unchanged {
		t.Fatal("unchanged input created another generation")
	}
	write("geosite-ru-blocked.srs", []byte("SRSbroken"))
	if err := m.Refresh(ctx); err == nil {
		t.Fatal("corrupt input accepted")
	}
	if m.revision() != before {
		t.Fatal("corrupt input replaced active generation")
	}
	write("geosite-ru-blocked.srs", second)
	wait(func() bool { return m.revision() != before })
	current := m.revision()
	if err := os.Remove(filepath.Join(input, "geoip-ru.srs")); err != nil {
		t.Fatal(err)
	}
	if err := m.Refresh(ctx); err == nil {
		t.Fatal("missing input accepted")
	}
	if m.revision() != current {
		t.Fatal("removing input discarded cached rules")
	}
	restored, err := New(cache, binary, Config{LocalDir: input})
	if err != nil {
		t.Fatal(err)
	}
	if restored.revision() != current {
		t.Fatal("offline snapshot reload failed")
	}
}

func TestLocalOverridesRemoteSources(t *testing.T) {
	binary, err := exec.LookPath("sing-box")
	if err != nil {
		t.Skip("sing-box not installed")
	}
	input := t.TempDir()
	valid := compileFixture(t, binary, "local.example")
	cfg := Config{LocalDir: input, Sources: map[string]Source{}}
	for _, tag := range Tags {
		cfg.Sources[tag] = Source{URL: "http://must-not-be-used.invalid", SHA256: "upstream-only-pin"}
		if err := os.WriteFile(filepath.Join(input, localFiles[tag]), valid, 0644); err != nil {
			t.Fatal(err)
		}
	}
	m, err := New(t.TempDir(), binary, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := m.revision()
	for _, name := range localFiles {
		if err := os.Remove(filepath.Join(input, name)); err != nil {
			t.Fatal(err)
		}
	}
	local, err := m.useLocal()
	if err != nil || !local {
		t.Fatal("local mode silently fell back to upstream")
	}
	if err := m.Refresh(context.Background()); err == nil {
		t.Fatal("removed files accepted")
	}
	if m.revision() != before {
		t.Fatal("last good rules lost")
	}
}
