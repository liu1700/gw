// Package config loads gw.toml and renders {branch}/{slug} templates.
//
// NOTE: this is a deliberately minimal TOML-subset parser (tables, string
// key-values, comments) to keep the skeleton dependency-free. Swap for
// github.com/BurntSushi/toml before v0.1 if you want full TOML.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Proxy modes: how (and whether) the gateway fronts a service. The default
// (TLS-terminating HTTPS → HTTP reverse proxy) is canonically "" in memory
// and in the route registry; ProxyHTTP is its gw.toml spelling.
const (
	ProxyHTTP        = "http"        // default: TLS-terminating HTTPS → HTTP reverse proxy
	ProxyPassthrough = "passthrough" // SNI-routed TCP splice, TLS NOT terminated (mTLS survives)
	ProxyNone        = "none"        // supervised process + isolated port only, no routing
)

type Service struct {
	Name  string
	Cmd   string            // dev command, e.g. "pnpm dev"
	Dir   string            // working dir relative to worktree root, default "."
	Proxy string            // "" (http, default) | ProxyPassthrough | ProxyNone
	Env   map[string]string // per-service extra env (templated)
}

// ModeLabel is the human-readable annotation for non-default proxy modes
// ("" for plain http routing) — shared by every command that displays one.
func ModeLabel(mode string) string {
	switch mode {
	case ProxyPassthrough:
		return "TLS passthrough"
	case ProxyNone:
		return "not proxied"
	}
	return ""
}

type Config struct {
	Domain   string            // e.g. "myapp.localhost" or "dev.example.com"
	Services []Service         // ordered as declared
	Env      map[string]string // global templated env, e.g. DATABASE_URL
	Hooks    map[string]string // "setup", "teardown"
	Root     string            // repo/worktree root where gw.toml lives
}

// Find walks up from dir looking for gw.toml.
func Find(dir string) (string, error) {
	d, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		p := filepath.Join(d, "gw.toml")
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", fmt.Errorf("gw.toml not found — run `gw init` in your repo root")
		}
		d = parent
	}
}

func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cfg := &Config{Env: map[string]string{}, Hooks: map[string]string{}, Root: filepath.Dir(path)}
	var section string
	var cur *Service

	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		if strings.HasPrefix(raw, "[") {
			if i := strings.Index(raw, "#"); i >= 0 { // inline comment after table header
				raw = strings.TrimSpace(raw[:i])
			}
			if !strings.HasSuffix(raw, "]") {
				return nil, fmt.Errorf("%s:%d: malformed table header", path, line)
			}
			section = strings.Trim(raw, "[]")
			if name, ok := strings.CutPrefix(section, "services."); ok {
				cfg.Services = append(cfg.Services, Service{Name: name, Dir: ".", Env: map[string]string{}})
				cur = &cfg.Services[len(cfg.Services)-1]
			} else {
				cur = nil
			}
			continue
		}
		k, v, ok := strings.Cut(raw, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected key = \"value\"", path, line)
		}
		key := strings.TrimSpace(k)
		val := strings.TrimSpace(v)
		if strings.HasPrefix(val, `"`) { // quoted: take up to closing quote, drop trailing comment
			if end := strings.Index(val[1:], `"`); end >= 0 {
				val = val[1 : 1+end]
			} else {
				return nil, fmt.Errorf("%s:%d: unterminated string", path, line)
			}
		} else if i := strings.Index(val, "#"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		switch {
		case section == "" && key == "domain":
			cfg.Domain = val
		case section == "env":
			cfg.Env[key] = val
		case section == "hooks":
			cfg.Hooks[key] = val
		case cur != nil:
			switch key {
			case "cmd":
				cur.Cmd = val
			case "dir":
				cur.Dir = val
			case "proxy":
				switch val {
				case ProxyHTTP:
					cur.Proxy = "" // canonical zero value for the default mode
				case ProxyPassthrough, ProxyNone:
					cur.Proxy = val
				default:
					return nil, fmt.Errorf("%s:%d: proxy = %q — must be %q, %q or %q",
						path, line, val, ProxyHTTP, ProxyPassthrough, ProxyNone)
				}
			default:
				cur.Env[key] = val
			}
		}
	}
	if cfg.Domain == "" {
		return nil, fmt.Errorf("%s: missing top-level `domain`", path)
	}
	if len(cfg.Services) == 0 {
		return nil, fmt.Errorf("%s: no [services.*] defined", path)
	}
	return cfg, sc.Err()
}

// Render substitutes branch placeholders in a templated string.
//   {branch}       raw branch name        feature/auth
//   {slug}         DNS/db-safe slug       feature-auth
//   {branch_snake} underscore variant     feature_auth (for db names)
func Render(s, branch, slug string) string {
	r := strings.NewReplacer(
		"{branch}", branch,
		"{slug}", slug,
		"{branch_snake}", strings.ReplaceAll(slug, "-", "_"),
	)
	return r.Replace(s)
}

// HostFor computes the public hostname of a service in a given worktree.
// Main worktree gets no branch prefix; linked worktrees get "slug." prefixed.
func (c *Config) HostFor(service, slug string, isMain bool) string {
	if isMain {
		return service + "." + c.Domain
	}
	return service + "." + slug + "." + c.Domain
}
