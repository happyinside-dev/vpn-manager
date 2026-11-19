// Package ruleset maintains validated, immutable routing snapshots.
package ruleset

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var Tags = []string{"geoip-ru", "ru-blocked", "ru-blocked-ip"}

const maxSize = 64 << 20

type Source struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256,omitempty"`
}
type Config struct {
	Sources map[string]Source `json:"sources"`
}
type entry struct {
	Binary []byte          `json:"binary"`
	Rules  json.RawMessage `json:"rules"`
	SHA256 string          `json:"sha256"`
}
type snapshot map[string]entry

type Manager struct {
	dir, binary string
	config      Config
	client      *http.Client
	mu          sync.RWMutex
	update      sync.Mutex
	current     snapshot
}

// New loads the last committed snapshot without contacting upstreams.
func New(dir, binary string, cfg Config) (*Manager, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	m := &Manager{dir: dir, binary: binary, config: cfg, client: &http.Client{Timeout: 90 * time.Second}, current: snapshot{}}
	b, err := os.ReadFile(filepath.Join(dir, "current", "snapshot.json"))
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	var cached snapshot
	if err = json.Unmarshal(b, &cached); err != nil {
		return nil, err
	}
	for _, tag := range Tags {
		e, ok := cached[tag]
		if !ok || fmt.Sprintf("%x", sha256.Sum256(e.Binary)) != e.SHA256 {
			return nil, fmt.Errorf("invalid cached %s checksum", tag)
		}
		disk, err := os.ReadFile(filepath.Join(dir, "current", tag+".srs"))
		if err != nil || !bytes.Equal(disk, e.Binary) {
			return nil, fmt.Errorf("cached %s binary mismatch", tag)
		}
		rules, err := m.validate(context.Background(), e.Binary)
		if err != nil {
			return nil, fmt.Errorf("cached %s: %w", tag, err)
		}
		e.Rules = rules
		cached[tag] = e
	}
	m.current = cached
	return m, nil
}

func (m *Manager) validate(ctx context.Context, b []byte) (json.RawMessage, error) {
	if len(b) < 4 || len(b) > maxSize || !bytes.Equal(b[:3], []byte{'S', 'R', 'S'}) {
		return nil, fmt.Errorf("invalid SRS size or magic")
	}
	dir, err := os.MkdirTemp(m.dir, ".validate-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	input, output := filepath.Join(dir, "rules.srs"), filepath.Join(dir, "rules.json")
	if err = os.WriteFile(input, b, 0600); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err = exec.CommandContext(ctx, m.binary, "rule-set", "decompile", "--output", output, input).Run(); err != nil {
		return nil, fmt.Errorf("sing-box validation: %w", err)
	}
	f, err := os.Open(output)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil || len(data) > maxSize {
		return nil, fmt.Errorf("decoded rules exceed limit")
	}
	var source struct {
		Version int               `json:"version"`
		Rules   []json.RawMessage `json:"rules"`
	}
	if err = json.Unmarshal(data, &source); err != nil {
		return nil, err
	}
	if source.Version < 1 || len(source.Rules) == 0 {
		return nil, fmt.Errorf("empty or invalid rule source")
	}
	return json.Marshal(source.Rules)
}

// Refresh commits all three files together. Any failure preserves the entire old generation.
func (m *Manager) Refresh(ctx context.Context) error {
	m.update.Lock()
	defer m.update.Unlock()
	next := snapshot{}
	for _, tag := range Tags {
		src, ok := m.config.Sources[tag]
		if !ok {
			return fmt.Errorf("missing source %s", tag)
		}
		u, err := url.Parse(src.URL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return fmt.Errorf("%s requires an HTTPS source", tag)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
		if err != nil {
			return err
		}
		client := *m.client
		client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
			if r.URL.Scheme != "https" || len(via) >= 5 {
				return fmt.Errorf("unsafe redirect")
			}
			return nil
		}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("%s: %w", tag, err)
		}
		b, readErr := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || readErr != nil || len(b) > maxSize {
			return fmt.Errorf("%s download failed (HTTP %d)", tag, resp.StatusCode)
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(b))
		if src.SHA256 != "" && src.SHA256 != sum {
			return fmt.Errorf("%s checksum mismatch", tag)
		}
		rules, err := m.validate(ctx, b)
		if err != nil {
			return fmt.Errorf("%s: %w", tag, err)
		}
		next[tag] = entry{Binary: b, Rules: rules, SHA256: sum}
	}
	m.mu.RLock()
	unchanged := len(m.current) == len(next)
	for _, tag := range Tags {
		if m.current[tag].SHA256 != next[tag].SHA256 {
			unchanged = false
		}
	}
	m.mu.RUnlock()
	if unchanged {
		return nil
	}
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	// Build a complete generation, then switch one symlink atomically.
	generation, err := os.MkdirTemp(m.dir, "generation-")
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(generation)
		}
	}()
	write := func(name string, data []byte) error {
		f, err := os.OpenFile(filepath.Join(generation, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		if _, err = f.Write(data); err != nil {
			f.Close()
			return err
		}
		if err = f.Sync(); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}
	for _, tag := range Tags {
		if err = write(tag+".srs", next[tag].Binary); err != nil {
			return err
		}
	}
	if err = write("snapshot.json", data); err != nil {
		return err
	}
	gd, err := os.Open(generation)
	if err != nil {
		return err
	}
	err = gd.Sync()
	gd.Close()
	if err != nil {
		return err
	}
	link := generation + ".link"
	if err = os.Symlink(filepath.Base(generation), link); err != nil {
		return err
	}
	defer os.Remove(link)
	previous, _ := os.Readlink(filepath.Join(m.dir, "current"))
	if err = os.Rename(link, filepath.Join(m.dir, "current")); err != nil {
		return err
	}
	committed = true
	m.mu.Lock()
	m.current = next
	m.mu.Unlock()
	d, err := os.Open(m.dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err = d.Sync(); err != nil {
		return err
	}
	// Keep current and the preceding generation; remove only manager-owned directories.
	entries, _ := os.ReadDir(m.dir)
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "generation-") && e.Name() != filepath.Base(generation) && e.Name() != previous {
			_ = os.RemoveAll(filepath.Join(m.dir, e.Name()))
		}
	}
	return nil
}

func (m *Manager) Inline(tag string) (map[string]any, error) {
	sets, err := m.InlineSets([]string{tag})
	if err != nil {
		return nil, err
	}
	return sets[tag], nil
}

// InlineSets reads all requested lists from the same immutable generation.
func (m *Manager) InlineSets(tags []string) (map[string]map[string]any, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sets := map[string]map[string]any{}
	for _, tag := range tags {
		e, ok := m.current[tag]
		if !ok {
			return nil, fmt.Errorf("routing rules unavailable: %s", tag)
		}
		sets[tag] = map[string]any{"type": "inline", "tag": tag, "rules": e.Rules}
	}
	return sets, nil
}

func (m *Manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tag := r.PathValue("tag")
	m.mu.RLock()
	e, ok := m.current[tag]
	m.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("ETag", `"`+e.SHA256+`"`)
	http.ServeContent(w, r, tag+".srs", time.Time{}, bytes.NewReader(e.Binary))
}

func (m *Manager) Run(ctx context.Context, log *slog.Logger) {
	for {
		if ctx.Err() != nil {
			return
		}
		delay := 24 * time.Hour
		if err := m.Refresh(ctx); err != nil {
			log.Warn("rule-set refresh failed; retaining last good snapshot", "error", err)
			delay = 5 * time.Minute
		} else {
			log.Info("rule-set snapshot updated")
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
