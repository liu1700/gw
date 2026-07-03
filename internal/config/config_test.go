package config

import (
	"os"
	"path/filepath"
	"testing"
)

const sample = `# comment
domain = "myapp.localhost"

[services.web]  # Next.js
cmd = "pnpm dev"
dir = "frontend"

[services.api]
cmd = "uv run uvicorn main:app --port $PORT --host 127.0.0.1"

[env]
DATABASE_URL = "postgres://localhost:5432/myapp_{branch_snake}"

[hooks]
setup = "createdb myapp_{branch_snake} 2>/dev/null; true"
`

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gw.toml")
	if err := os.WriteFile(path, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Domain != "myapp.localhost" {
		t.Errorf("domain = %q", cfg.Domain)
	}
	if len(cfg.Services) != 2 || cfg.Services[0].Name != "web" || cfg.Services[1].Name != "api" {
		t.Errorf("services = %+v", cfg.Services)
	}
	if cfg.Services[0].Dir != "frontend" || cfg.Services[1].Dir != "." {
		t.Errorf("dirs = %q, %q", cfg.Services[0].Dir, cfg.Services[1].Dir)
	}
	if cfg.Env["DATABASE_URL"] == "" || cfg.Hooks["setup"] == "" {
		t.Errorf("env/hooks not parsed: %+v %+v", cfg.Env, cfg.Hooks)
	}
}

func TestLoadProxyModes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gw.toml")
	toml := `domain = "demo.localhost"

[services.web]
cmd = "pnpm dev"

[services.data]
cmd = "orlop-server"
proxy = "passthrough"

[services.worker]
cmd = "worker"
proxy = "none"
`
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{cfg.Services[0].Proxy, cfg.Services[1].Proxy, cfg.Services[2].Proxy}
	want := []string{ProxyHTTP, ProxyPassthrough, ProxyNone}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("service %d proxy = %q, want %q", i, got[i], want[i])
		}
	}
	// `proxy` is a reserved key, not per-service env.
	for i, svc := range cfg.Services {
		if _, leaked := svc.Env["proxy"]; leaked {
			t.Errorf("service %d: proxy leaked into Env", i)
		}
	}
}

func TestLoadRejectsBadProxyMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gw.toml")
	toml := "domain = \"demo.localhost\"\n\n[services.web]\ncmd = \"pnpm dev\"\nproxy = \"tcp\"\n"
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("want error for proxy = \"tcp\", got nil")
	}
}

func TestRender(t *testing.T) {
	got := Render("db_{branch_snake} at {slug} from {branch}", "feature/auth", "feature-auth")
	want := "db_feature_auth at feature-auth from feature/auth"
	if got != want {
		t.Errorf("Render = %q, want %q", got, want)
	}
}

func TestHostFor(t *testing.T) {
	cfg := &Config{Domain: "myapp.localhost"}
	if got := cfg.HostFor("web", "feature-auth", true); got != "web.myapp.localhost" {
		t.Errorf("main worktree host = %q", got)
	}
	if got := cfg.HostFor("web", "feature-auth", false); got != "web.feature-auth.myapp.localhost" {
		t.Errorf("linked worktree host = %q", got)
	}
}
