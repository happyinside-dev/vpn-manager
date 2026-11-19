// Package ruleset maintains validated, immutable routing snapshots.
package ruleset

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
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
	"sync/atomic"
	"time"
)

var Tags = []string{"geoip-ru", "ru-blocked", "ru-blocked-ip"}

const maxSize = 64 << 20

type Source struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256,omitempty"`
}
type Config struct {
	Sources  map[string]Source `json:"sources"`
	LocalDir string            `json:"-"`
}
type entry struct {
	Binary []byte          `json:"binary"`
	Rules  json.RawMessage `json:"rules"`
	SHA256 string          `json:"sha256"`
}
type snapshot map[string]entry

type Manager struct {
	dir, binary        string
	config             Config
	client             *http.Client
	mu                 sync.RWMutex
	update             sync.Mutex
	current            snapshot
	pollInterval       time.Duration
	localSelected      atomic.Bool
	structuredSelected atomic.Bool
}

// New loads the last committed snapshot without contacting upstreams.
func New(dir, binary string, cfg Config) (*Manager, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	m := &Manager{dir: dir, binary: binary, config: cfg, client: &http.Client{Timeout: 90 * time.Second}, current: snapshot{}, pollInterval: 15 * time.Second}
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
		if !ok {
			continue
		}
		if fmt.Sprintf("%x", sha256.Sum256(e.Binary)) != e.SHA256 {
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
	_, domains := cached["ru-blocked"]
	_, ips := cached["ru-blocked-ip"]
	if domains != ips {
		return nil, fmt.Errorf("incomplete cached blocked rules")
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

func (m *Manager) download(ctx context.Context, tag string, src Source) ([]byte, error) {
	u, err := url.Parse(src.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("%s requires an HTTPS source", tag)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		return nil, err
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
		return nil, fmt.Errorf("%s: %w", tag, err)
	}
	b, readErr := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || readErr != nil || len(b) > maxSize {
		return nil, fmt.Errorf("%s download failed (HTTP %d)", tag, resp.StatusCode)
	}
	return b, nil
}

var localFiles = map[string]string{
	"geoip-ru":      "geoip-ru.srs",
	"ru-blocked":    "geosite-ru-blocked.srs",
	"ru-blocked-ip": "geoip-ru-blocked.srs",
}

// Any recognized local file selects the complete local set. Never silently mix
// a partially uploaded local generation with upstream data.
func (m *Manager) useLocal() (bool, error) {
	if m.config.LocalDir == "" {
		return false, nil
	}
	if m.localSelected.Load() || len(m.config.Sources) == 0 {
		return true, nil
	}
	for tag := range localFiles {
		_, err := os.Stat(m.localPath(tag))
		if err == nil {
			m.localSelected.Store(true)
			return true, nil
		}
		if !os.IsNotExist(err) {
			return false, err
		}
	}
	return false, nil
}

func localGroup(tag string) string {
	if tag == "geoip-ru" {
		return "bypass-ru"
	}
	return "blocked"
}

func (m *Manager) localPath(tag string) string {
	nested := filepath.Join(m.config.LocalDir, localGroup(tag), localFiles[tag])
	if _, err := os.Lstat(nested); !os.IsNotExist(err) {
		return nested
	}
	return filepath.Join(m.config.LocalDir, localFiles[tag])
}

func (m *Manager) structuredLocal() bool {
	if m.structuredSelected.Load() {
		return true
	}
	for tag, name := range localFiles {
		if _, err := os.Lstat(filepath.Join(m.config.LocalDir, localGroup(tag), name)); !os.IsNotExist(err) {
			m.structuredSelected.Store(true)
			return true
		}
	}
	for _, name := range localFiles {
		if _, err := os.Lstat(filepath.Join(m.config.LocalDir, name)); !os.IsNotExist(err) {
			return false
		}
	}
	return true
}

// Each mode is a separate transaction; blocked always needs its domain and IP lists together.
func (m *Manager) refreshLocalGroups(ctx context.Context) error {
	var failures []error
	for _, tags := range [][]string{{"geoip-ru"}, {"ru-blocked", "ru-blocked-ip"}} {
		m.mu.RLock()
		next := snapshot{}
		for tag, e := range m.current {
			next[tag] = e
		}
		m.mu.RUnlock()
		err := func() error {
			for _, tag := range tags {
				path := filepath.Join(m.config.LocalDir, localGroup(tag), localFiles[tag])
				b, err := readLocal(path)
				if err != nil {
					return fmt.Errorf("%s: %w", localGroup(tag), err)
				}
				sum := fmt.Sprintf("%x", sha256.Sum256(b))
				if old, ok := next[tag]; ok && old.SHA256 == sum {
					continue
				}
				rules, err := m.validate(ctx, b)
				if err != nil {
					return fmt.Errorf("%s: %w", tag, err)
				}
				next[tag] = entry{Binary: b, Rules: rules, SHA256: sum}
			}
			for _, tag := range tags {
				b, err := readLocal(filepath.Join(m.config.LocalDir, localGroup(tag), localFiles[tag]))
				if err != nil {
					return err
				}
				if fmt.Sprintf("%x", sha256.Sum256(b)) != next[tag].SHA256 {
					return fmt.Errorf("%s changed during validation", tag)
				}
			}
			return m.commit(next)
		}()
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func readLocal(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular SRS input file: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() > maxSize {
		return nil, fmt.Errorf("not a regular SRS file or exceeds size limit: %s", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if len(b) > maxSize || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, fmt.Errorf("local file changed while reading: %s", path)
	}
	return b, nil
}

// Refresh updates each local mode independently; a blocked domain/IP pair is atomic.
// Legacy flat directories and HTTPS sources retain their complete-set transaction.
func (m *Manager) Refresh(ctx context.Context) error {
	m.update.Lock()
	defer m.update.Unlock()
	local, err := m.useLocal()
	if err != nil {
		return err
	}
	if local && m.structuredLocal() {
		return m.refreshLocalGroups(ctx)
	}
	next := snapshot{}
	for _, tag := range Tags {
		src := m.config.Sources[tag]
		var b []byte
		var err error
		if local {
			b, err = readLocal(filepath.Join(m.config.LocalDir, localFiles[tag]))
		} else {
			if src.URL == "" {
				return fmt.Errorf("missing source %s", tag)
			}
			b, err = m.download(ctx, tag, src)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", tag, err)
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(b))
		if !local && src.SHA256 != "" && src.SHA256 != sum {
			return fmt.Errorf("%s checksum mismatch", tag)
		}
		m.mu.RLock()
		previous, exists := m.current[tag]
		m.mu.RUnlock()
		if exists && previous.SHA256 == sum {
			next[tag] = previous
			continue
		}
		rules, err := m.validate(ctx, b)
		if err != nil {
			return fmt.Errorf("%s: %w", tag, err)
		}
		next[tag] = entry{Binary: b, Rules: rules, SHA256: sum}
	}
	if local {
		for _, tag := range Tags {
			b, err := readLocal(filepath.Join(m.config.LocalDir, localFiles[tag]))
			if err != nil {
				return err
			}
			if fmt.Sprintf("%x", sha256.Sum256(b)) != next[tag].SHA256 {
				return fmt.Errorf("local rule files changed during validation; retrying next scan")
			}
		}
	}
	return m.commit(next)
}

func (m *Manager) commit(next snapshot) error {
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
	for tag, e := range next {
		if err = write(tag+".srs", e.Binary); err != nil {
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

func (m *Manager) revision() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var revision string
	for _, tag := range Tags {
		revision += m.current[tag].SHA256
	}
	return revision
}

func (m *Manager) Run(ctx context.Context, log *slog.Logger) {
	var nextRemote time.Time
	var lastError string
	for {
		if ctx.Err() != nil {
			return
		}
		local, err := m.useLocal()
		if err != nil || local || !time.Now().Before(nextRemote) {
			before := m.revision()
			if err == nil {
				err = m.Refresh(ctx)
			}
			if before != m.revision() {
				log.Info("rule-set snapshot updated", "local", local)
			}
			if err != nil {
				if err.Error() != lastError {
					log.Warn("rule-set refresh failed; retaining last good snapshot", "error", err)
					lastError = err.Error()
				}
				nextRemote = time.Now().Add(5 * time.Minute)
			} else {
				lastError = ""
				nextRemote = time.Now().Add(24 * time.Hour)
			}
		}
		timer := time.NewTimer(m.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
