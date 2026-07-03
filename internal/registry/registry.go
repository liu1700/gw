// Package registry is the shared "hostname -> port" table between `gw up`
// (writer) and the proxy daemon (reader). MVP transport: a JSON state file
// with mtime-based cache invalidation. Simple, crash-safe enough, and lets
// `gw list` work without talking to the daemon. Swap for a unix socket API
// if write volume ever matters (it won't for local dev).
package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type Route struct {
	Host    string    `json:"host"`    // e.g. "web.feature-auth.myapp.localhost"
	Port    int       `json:"port"`    // upstream 127.0.0.1 port
	PID     int       `json:"pid"`     // owning process, for pruning
	Branch  string    `json:"branch"`  // for `gw list` display
	Service string    `json:"service"` // for `gw list` display
	Since   time.Time `json:"since"`
}

func path() string {
	if d := os.Getenv("GW_STATE_DIR"); d != "" {
		return filepath.Join(d, "routes.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gw", "routes.json")
}

var mu sync.Mutex

func Load() (map[string]Route, error) {
	b, err := os.ReadFile(path())
	if os.IsNotExist(err) {
		return map[string]Route{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]Route{}
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]Route{}, nil // corrupt file: start fresh
	}
	return m, nil
}

func save(m map[string]Route) error {
	if err := os.MkdirAll(filepath.Dir(path()), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	tmp := path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path()) // atomic on same fs
}

func Register(r Route) error {
	mu.Lock()
	defer mu.Unlock()
	m, err := Load()
	if err != nil {
		return err
	}
	r.Since = time.Now()
	m[r.Host] = r
	return save(m)
}

func Unregister(host string) error {
	mu.Lock()
	defer mu.Unlock()
	m, err := Load()
	if err != nil {
		return err
	}
	delete(m, host)
	return save(m)
}

// UnregisterPID removes every route owned by a process (used on gw run exit).
func UnregisterPID(pid int) error {
	mu.Lock()
	defer mu.Unlock()
	m, err := Load()
	if err != nil {
		return err
	}
	for h, r := range m {
		if r.PID == pid {
			delete(m, h)
		}
	}
	return save(m)
}

// Alive reports whether the process that registered a route still exists.
// Routes from crashed or KILLed processes would otherwise linger forever.
func Alive(r Route) bool {
	if r.PID <= 0 {
		return true
	}
	return syscall.Kill(r.PID, 0) != syscall.ESRCH
}

// PruneDead drops routes whose owning process is gone and returns the
// remaining live set.
func PruneDead() (map[string]Route, error) {
	mu.Lock()
	defer mu.Unlock()
	m, err := Load()
	if err != nil {
		return nil, err
	}
	changed := false
	for h, r := range m {
		if !Alive(r) {
			delete(m, h)
			changed = true
		}
	}
	if changed {
		if err := save(m); err != nil {
			return m, err
		}
	}
	return m, nil
}

// Cached is a read-through view for the proxy hot path: reload only when the
// file's mtime changes.
type Cached struct {
	mu     sync.RWMutex
	routes map[string]Route
	mtime  time.Time
}

func NewCached() *Cached { return &Cached{routes: map[string]Route{}} }

func (c *Cached) Lookup(host string) (Route, bool) {
	st, err := os.Stat(path())
	c.mu.RLock()
	fresh := err == nil && st.ModTime().Equal(c.mtime)
	if fresh {
		r, ok := c.routes[host]
		c.mu.RUnlock()
		return r, ok
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if m, err2 := Load(); err2 == nil {
		c.routes = m
		if err == nil {
			c.mtime = st.ModTime()
		}
	}
	r, ok := c.routes[host]
	return r, ok
}
