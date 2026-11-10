// Package traffic persists deltas of non-resetting daemon counters.
package traffic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	File           string `json:"file"`
	XrayBinary     string `json:"xray_binary"`
	XrayServer     string `json:"xray_server"`
	HysteriaURL    string `json:"hysteria_url"`
	HysteriaSecret string `json:"hysteria_secret,omitempty"`
}

func (c *Config) Defaults(usersFile string) {
	if c.File == "" {
		c.File = filepath.Join(filepath.Dir(usersFile), "traffic.json")
	}
	if c.XrayBinary == "" {
		c.XrayBinary = "xray"
	}
	if c.XrayServer == "" {
		c.XrayServer = "127.0.0.1:10085"
	}
	if c.HysteriaURL == "" {
		c.HysteriaURL = "http://127.0.0.1:10086"
	}
}

type point struct {
	At    time.Time `json:"at"`
	Bytes uint64    `json:"bytes"`
}
type account struct {
	Total  uint64  `json:"total"`
	Points []point `json:"points,omitempty"`
}
type source struct {
	Epoch   string            `json:"epoch,omitempty"`
	Since   time.Time         `json:"since"`
	Updated time.Time         `json:"updated"`
	Last    map[string]uint64 `json:"last"`
}
type state struct {
	Sources  map[string]source  `json:"sources"`
	Accounts map[string]account `json:"accounts"`
}
type Collector struct {
	mu       sync.Mutex
	cfg      Config
	data     state
	failures map[string]bool
	read     func(context.Context, string) (map[string]uint64, error)
	epoch    func(context.Context, string) string
}
type Report struct {
	Total, Week uint64
	Since       time.Time
	Updated     map[string]time.Time
	Unavailable []string
}

func New(cfg Config) (*Collector, error) {
	c := &Collector{cfg: cfg, data: state{Sources: map[string]source{}, Accounts: map[string]account{}}, failures: map[string]bool{}}
	b, err := os.ReadFile(cfg.File)
	if err == nil {
		if err = json.Unmarshal(b, &c.data); err != nil {
			return nil, fmt.Errorf("parse traffic history: %w", err)
		}
		if c.data.Sources == nil || c.data.Accounts == nil {
			return nil, errors.New("invalid traffic history")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	c.read = c.readSource
	c.epoch = daemonEpoch
	return c, nil
}

// Poll never resets daemon counters. Failed reads leave their baselines intact.
// Commit the baselines and totals together so a restart cannot count a delta twice.
func (c *Collector) Poll(ctx context.Context, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Work on a copy: a failed write must not advance the in-memory baselines.
	encoded, err := json.Marshal(c.data)
	if err != nil {
		return err
	}
	var next state
	if err = json.Unmarshal(encoded, &next); err != nil {
		return err
	}
	var errs []error
	for _, name := range []string{"Xray", "Hysteria"} {
		values, err := c.read(ctx, name)
		c.failures[name] = err != nil
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		prev := next.Sources[name]
		epoch := c.epoch(ctx, name)
		restarted := epoch != "" && prev.Epoch != "" && epoch != prev.Epoch
		if epoch != "" {
			prev.Epoch = epoch
		}
		first := prev.Since.IsZero()
		if first {
			prev.Since = now
		}
		for key, value := range values {
			// Keys are account + NUL + direction; directions are compared separately.
			user, _, ok := strings.Cut(key, "\x00")
			if !ok || user == "__sentinel__" {
				continue
			}
			var delta uint64
			if !first {
				old := prev.Last[key]
				if restarted {
					old = 0
				}
				if value >= old {
					delta = value - old
				} else {
					delta = value
				} // daemon counter reset
			}
			if delta > 0 {
				a := next.Accounts[user]
				a.Total += delta
				if n := len(a.Points); n > 0 && a.Points[n-1].At.Equal(now) {
					a.Points[n-1].Bytes += delta
				} else {
					a.Points = append(a.Points, point{now, delta})
				}
				next.Accounts[user] = a
			}
		}
		// Missing counters in a successful snapshot were removed/reset by the daemon.
		prev.Last, prev.Updated = values, now
		next.Sources[name] = prev
	}
	for user, a := range next.Accounts {
		i := 0
		for i < len(a.Points) && !a.Points[i].At.After(now.Add(-7*24*time.Hour)) {
			i++
		}
		a.Points = a.Points[i:]
		next.Accounts[user] = a
	}
	if err := c.save(next); err != nil {
		return errors.Join(append(errs, err)...)
	}
	c.data = next
	return errors.Join(errs...)
}

func (c *Collector) Report(user string, now time.Time) Report {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.data.Accounts[user]
	r := Report{Total: a.Total, Updated: map[string]time.Time{}}
	for _, p := range a.Points {
		if p.At.After(now.Add(-7 * 24 * time.Hour)) {
			r.Week += p.Bytes
		}
	}
	for _, name := range []string{"Xray", "Hysteria"} {
		s := c.data.Sources[name]
		if !s.Since.IsZero() && (r.Since.IsZero() || s.Since.Before(r.Since)) {
			r.Since = s.Since
		}
		r.Updated[name] = s.Updated
		if c.failures[name] || s.Updated.IsZero() || now.Sub(s.Updated) > 2*time.Minute {
			r.Unavailable = append(r.Unavailable, name)
		}
	}
	return r
}

func (c *Collector) save(data state) error {
	if err := os.MkdirAll(filepath.Dir(c.cfg.File), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(c.cfg.File), ".traffic-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = json.NewEncoder(f).Encode(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), c.cfg.File)
}

func (c *Collector) readSource(ctx context.Context, name string) (map[string]uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if name == "Xray" {
		data, err := exec.CommandContext(ctx, c.cfg.XrayBinary, "api", "statsquery", "--server="+c.cfg.XrayServer, "-pattern", "user>>>").Output()
		if err != nil {
			return nil, err
		}
		return parseXray(data)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.cfg.HysteriaURL, "/")+"/traffic", nil)
	if err != nil {
		return nil, err
	}
	if c.cfg.HysteriaSecret != "" {
		req.Header.Set("Authorization", c.cfg.HysteriaSecret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var values map[string]struct {
		TX uint64 `json:"tx"`
		RX uint64 `json:"rx"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&values); err != nil {
		return nil, err
	}
	if values == nil {
		return nil, errors.New("invalid Hysteria statistics")
	}
	out := map[string]uint64{}
	for user, v := range values {
		out[user+"\x00tx"] = v.TX
		out[user+"\x00rx"] = v.RX
	}
	return out, nil
}

func parseXray(data []byte) (map[string]uint64, error) {
	var response struct {
		Stat []struct {
			Name  string          `json:"name"`
			Value json.RawMessage `json:"value"`
		} `json:"stat"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	out := map[string]uint64{}
	for _, s := range response.Stat {
		parts := strings.Split(s.Name, ">>>")
		if len(parts) != 4 || parts[0] != "user" || parts[2] != "traffic" || (parts[3] != "uplink" && parts[3] != "downlink") {
			continue
		}
		var value uint64
		// protobuf JSON encodes int64 as strings; also accept numeric CLI output.
		if len(s.Value) > 0 {
			n, err := strconv.ParseUint(strings.Trim(string(s.Value), `"`), 10, 64)
			if err != nil {
				return nil, err
			}
			value = n
		}
		out[parts[1]+"\x00"+parts[3]] = value
	}
	return out, nil
}

// InvocationID distinguishes daemon restarts even when the new counters have
// already exceeded the previous sample. Without systemd, use decrease detection.
func daemonEpoch(ctx context.Context, name string) string {
	service := "xray"
	if name == "Hysteria" {
		service = "hysteria-server"
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "systemctl", "show", "--property=InvocationID", "--value", service).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
