package traffic

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoryPersistenceWindowAndResets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traffic.json")
	c, err := New(Config{File: path})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	up, down := uint64(100), uint64(200)
	fail := false
	reader := func(_ context.Context, name string) (map[string]uint64, error) {
		if name == "Hysteria" {
			return map[string]uint64{}, nil
		}
		if fail {
			return nil, errors.New("offline")
		}
		return map[string]uint64{"alice\x00uplink": up, "alice\x00downlink": down}, nil
	}
	c.read = reader
	poll := func(at time.Time) {
		t.Helper()
		if err := c.Poll(context.Background(), at); err != nil {
			t.Fatal(err)
		}
	}
	poll(now) // First read establishes a baseline; old traffic has no known date.
	up, down = 150, 300
	poll(now.Add(time.Minute))
	if r := c.Report("alice", now.Add(time.Minute)); r.Total != 150 || r.Week != 150 {
		t.Fatalf("bad delta: %+v", r)
	}
	// Restart the collector with unchanged daemon counters: no double counting.
	c, err = New(Config{File: path})
	if err != nil {
		t.Fatal(err)
	}
	c.read = reader
	poll(now.Add(2 * time.Minute))
	up, down = 10, 20
	poll(now.Add(3 * time.Minute))
	if r := c.Report("alice", now.Add(3*time.Minute)); r.Total != 180 {
		t.Fatalf("reset: %+v", r)
	}
	fail = true
	if err := c.Poll(context.Background(), now.Add(4*time.Minute)); err == nil {
		t.Fatal("missing source failure")
	}
	if r := c.Report("alice", now.Add(4*time.Minute)); r.Total != 180 || len(r.Unavailable) != 1 {
		t.Fatalf("lost history: %+v", r)
	}
	fail = false
	up, down = 20, 40
	poll(now.Add(8 * 24 * time.Hour))
	if r := c.Report("alice", now.Add(8*24*time.Hour)); r.Total != 210 || r.Week != 30 {
		t.Fatalf("window: %+v", r)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("history must be private")
	}
}

func TestFailedSaveDoesNotAdvanceBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traffic.json")
	c, _ := New(Config{File: path})
	n := uint64(10)
	c.read = func(context.Context, string) (map[string]uint64, error) {
		return map[string]uint64{"alice\x00up": n}, nil
	}
	now := time.Now()
	if err := c.Poll(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	c.cfg.File = filepath.Dir(path) // Rename onto an existing directory must fail.
	n = 20
	if err := c.Poll(context.Background(), now.Add(time.Minute)); err == nil {
		t.Fatal("expected write failure")
	}
	if r := c.Report("alice", now); r.Total != 0 {
		t.Fatal("uncommitted data exposed")
	}
	c.cfg.File = path
	if err := c.Poll(context.Background(), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if r := c.Report("alice", now); r.Total != 20 {
		t.Fatalf("delta lost: %+v", r)
	}
}

func TestXrayParsing(t *testing.T) {
	values, err := parseXray([]byte(`{"stat":[{"name":"user>>>alice>>>traffic>>>uplink","value":"123"},{"name":"user>>>alice>>>traffic>>>downlink","value":456},{"name":"inbound>>>api>>>traffic>>>downlink","value":999}]}`))
	if err != nil || len(values) != 2 || values["alice\x00uplink"] != 123 || values["alice\x00downlink"] != 456 {
		t.Fatalf("%v %v", values, err)
	}
	if _, err := parseXray([]byte(`{"stat":[{"name":"user>>>alice>>>traffic>>>uplink","value":-1}]}`)); err == nil {
		t.Fatal("negative counter accepted")
	}
}

func TestHysteriaReader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/traffic" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "secret" {
			t.Error("wrong API request or counters reset")
		}
		_, _ = w.Write([]byte(`{"alice":{"tx":123,"rx":456}}`))
	}))
	defer server.Close()
	c, _ := New(Config{File: filepath.Join(t.TempDir(), "traffic.json"), HysteriaURL: server.URL, HysteriaSecret: "secret"})
	v, err := c.readSource(context.Background(), "Hysteria")
	if err != nil || v["alice\x00tx"] != 123 || v["alice\x00rx"] != 456 {
		t.Fatalf("%v %v", v, err)
	}
}

func TestCorruptHistoryIsNotOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traffic.json")
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{File: path}); err == nil {
		t.Fatal("corrupt history accepted")
	}
}

func TestDaemonRestartWithHigherCounters(t *testing.T) {
	c, _ := New(Config{File: filepath.Join(t.TempDir(), "traffic.json")})
	epoch := "first"
	c.epoch = func(context.Context, string) string { return epoch }
	n := uint64(100)
	c.read = func(_ context.Context, name string) (map[string]uint64, error) {
		if name == "Hysteria" {
			return map[string]uint64{}, nil
		}
		return map[string]uint64{"alice\x00up": n}, nil
	}
	now := time.Now()
	if err := c.Poll(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	epoch = "second"
	n = 200
	if err := c.Poll(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if r := c.Report("alice", now); r.Total != 200 {
		t.Fatalf("restart missed: %+v", r)
	}
}
