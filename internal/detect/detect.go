// Package detect powers `gw init`: sniff the stack, generate gw.toml,
// and point out hardcoded localhost URLs the user must parameterize.
package detect

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Detected struct {
	Name string // service name: "web", "api"
	Kind string // human label: "Next.js", "FastAPI", ...
	Cmd  string
	Dir  string
}

// Scan inspects the repo root (and one level of subdirectories, for the
// common frontend/ + backend/ layout) for known frameworks.
func Scan(root string) []Detected {
	var out []Detected
	dirs := []string{"."}
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && e.Name() != "node_modules" {
			dirs = append(dirs, e.Name())
		}
	}
	seen := map[string]bool{}
	for _, d := range dirs {
		for _, det := range scanDir(root, d) {
			if !seen[det.Name] {
				seen[det.Name] = true
				out = append(out, det)
			}
		}
	}
	return out
}

func scanDir(root, dir string) []Detected {
	var out []Detected
	abs := filepath.Join(root, dir)

	// --- JS: read package.json ---
	if b, err := os.ReadFile(filepath.Join(abs, "package.json")); err == nil {
		var pkg struct {
			Scripts      map[string]string `json:"scripts"`
			Dependencies map[string]string `json:"dependencies"`
		}
		if json.Unmarshal(b, &pkg) == nil {
			kind := ""
			switch {
			case pkg.Dependencies["next"] != "":
				kind = "Next.js"
			case pkg.Dependencies["nuxt"] != "":
				kind = "Nuxt"
			case pkg.Dependencies["vite"] != "" || hasDevDep(b, "vite"):
				kind = "Vite"
			}
			if kind != "" && pkg.Scripts["dev"] != "" {
				out = append(out, Detected{Name: "web", Kind: kind, Cmd: pm(abs) + " dev", Dir: dir})
			}
		}
	}

	// --- Python: pyproject/requirements ---
	deps := ""
	if b, err := os.ReadFile(filepath.Join(abs, "pyproject.toml")); err == nil {
		deps += string(b)
	}
	if b, err := os.ReadFile(filepath.Join(abs, "requirements.txt")); err == nil {
		deps += string(b)
	}
	low := strings.ToLower(deps)
	switch {
	case strings.Contains(low, "fastapi"):
		entry := guessASGIEntry(abs)
		out = append(out, Detected{Name: "api", Kind: "FastAPI",
			Cmd: pyRunner(abs) + "uvicorn " + entry + " --port $PORT --host 127.0.0.1", Dir: dir})
	case strings.Contains(low, "flask"):
		out = append(out, Detected{Name: "api", Kind: "Flask",
			Cmd: pyRunner(abs) + "flask run --port $PORT", Dir: dir})
	case strings.Contains(low, "django"):
		out = append(out, Detected{Name: "api", Kind: "Django",
			Cmd: pyRunner(abs) + "python manage.py runserver 127.0.0.1:$PORT", Dir: dir})
	}
	return out
}

func hasDevDep(pkgJSON []byte, dep string) bool {
	var pkg struct {
		DevDependencies map[string]string `json:"devDependencies"`
	}
	json.Unmarshal(pkgJSON, &pkg)
	return pkg.DevDependencies[dep] != ""
}

func pm(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, "pnpm-lock.yaml")); err == nil {
		return "pnpm"
	}
	if _, err := os.Stat(filepath.Join(dir, "yarn.lock")); err == nil {
		return "yarn"
	}
	if _, err := os.Stat(filepath.Join(dir, "bun.lockb")); err == nil {
		return "bun"
	}
	return "npm run"
}

func pyRunner(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, "uv.lock")); err == nil {
		return "uv run "
	}
	if _, err := os.Stat(filepath.Join(dir, "poetry.lock")); err == nil {
		return "poetry run "
	}
	return ""
}

func guessASGIEntry(dir string) string {
	for _, cand := range []string{"main.py", "app.py", "app/main.py", "src/main.py"} {
		if b, err := os.ReadFile(filepath.Join(dir, cand)); err == nil &&
			strings.Contains(string(b), "FastAPI(") {
			mod := strings.TrimSuffix(strings.ReplaceAll(cand, "/", "."), ".py")
			return mod + ":app"
		}
	}
	return "main:app"
}

// --- hardcoded URL scan ---

type Hardcoded struct {
	File string
	Line int
	Text string
}

var hardcodedRe = regexp.MustCompile(`(https?://)?(localhost|127\.0\.0\.1):\d{2,5}`)
var srcExt = map[string]bool{".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".py": true, ".env": true, ".mjs": true, ".vue": true, ".svelte": true}

// ScanHardcoded finds localhost:PORT literals in source files so `gw init`
// can print the exact diff the user needs to make.
func ScanHardcoded(root string) []Hardcoded {
	var out []Hardcoded
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			n := d.Name()
			if n == "node_modules" || n == ".git" || n == ".next" || n == "dist" ||
				n == ".venv" || n == "__pycache__" {
				return fs.SkipDir
			}
			return nil
		}
		if !srcExt[filepath.Ext(path)] && !strings.HasPrefix(d.Name(), ".env") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil || len(b) > 1<<20 {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(string(b), "\n") {
			if hardcodedRe.MatchString(line) {
				out = append(out, Hardcoded{File: rel, Line: i + 1, Text: strings.TrimSpace(line)})
			}
		}
		return nil
	})
	return out
}

// WriteConfig renders a starter gw.toml from detections.
func WriteConfig(root, domain string, dets []Detected) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# gw — branch-aware local HTTPS gateway\n")
	fmt.Fprintf(&b, "# Commit this file: every worktree shares it, zero per-branch config.\n\n")
	fmt.Fprintf(&b, "domain = %q\n", domain)
	for _, d := range dets {
		fmt.Fprintf(&b, "\n[services.%s]  # %s\n", d.Name, d.Kind)
		fmt.Fprintf(&b, "cmd = %q\n", d.Cmd)
		if d.Dir != "." {
			fmt.Fprintf(&b, "dir = %q\n", d.Dir)
		}
	}
	b.WriteString(`
# Per-branch backing services: share one server, isolate by name.
# Uncomment and adapt, then add setup/teardown hooks below.
# [env]
# DATABASE_URL = "postgres://localhost:5432/myapp_{branch_snake}"
# REDIS_URL = "redis://localhost:6379/0"

# [hooks]
# setup = "createdb myapp_{branch_snake} 2>/dev/null; true"
# teardown = "dropdb myapp_{branch_snake}"
`)
	path := filepath.Join(root, "gw.toml")
	if _, err := os.Stat(path); err == nil {
		return path, fmt.Errorf("gw.toml already exists")
	}
	return path, os.WriteFile(path, []byte(b.String()), 0o644)
}
