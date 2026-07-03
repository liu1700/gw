// gw — branch-aware local HTTPS gateway.
//
// Every git worktree gets trusted HTTPS URLs derived from its branch name:
//
//	main worktree      https://web.myapp.localhost
//	feature/auth tree  https://web.feature-auth.myapp.localhost
//
// Zero per-branch configuration: commit gw.toml once, run `gw up` anywhere.
package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/liu1700/gw/internal/branchinfo"
	"github.com/liu1700/gw/internal/certs"
	"github.com/liu1700/gw/internal/config"
	"github.com/liu1700/gw/internal/detect"
	"github.com/liu1700/gw/internal/proxy"
	"github.com/liu1700/gw/internal/registry"
	"github.com/liu1700/gw/internal/up"
)

const fallbackProxyPort = 8443

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "init":
		err = cmdInit()
	case "trust":
		err = certs.Trust()
	case "proxy":
		// MVP runs in foreground; daemonize via `gw proxy &` or a
		// launchd/systemd unit. TODO: self-daemonize + pidfile.
		err = proxy.Listen(fallbackProxyPort)
	case "up":
		err = cmdUp()
	case "list":
		err = cmdList()
	case "doctor":
		err = cmdDoctor()
	case "clean":
		err = cmdClean()
	case "version":
		fmt.Println("gw 0.1.0-dev")
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gw:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`gw — branch-aware local HTTPS gateway

  gw init      detect your stack, generate gw.toml, flag hardcoded URLs
  gw trust     create the local CA and install it into the system trust store
  gw proxy     run the HTTPS gateway (foreground; :443 with fallback :8443)
  gw up        start all services for the current worktree's branch
  gw list      show active routes across all branches
  gw doctor    diagnose DNS / CA / proxy issues
  gw clean     run teardown hooks for the current branch (drop db etc.)
`)
}

func cmdInit() error {
	root, _ := os.Getwd()
	dets := detect.Scan(root)
	if len(dets) == 0 {
		return fmt.Errorf("no known framework detected — write gw.toml by hand (see README)")
	}
	domain := filepath.Base(root) + ".localhost" // zero-DNS default; real domains are opt-in
	for _, d := range dets {
		fmt.Printf("✓ detected %-8s (%s) → %s\n", d.Kind, d.Name, d.Cmd)
	}
	path, err := detect.WriteConfig(root, domain, dets)
	if err != nil {
		return err
	}
	fmt.Printf("✓ wrote %s — commit it so every worktree shares it\n", path)
	fmt.Printf("→ default domain %q needs no DNS setup; switch to a real domain\n", domain)
	fmt.Println("  (e.g. dev.example.com with a wildcard A record to 127.0.0.1) any time.")

	if hits := detect.ScanHardcoded(root); len(hits) > 0 {
		fmt.Println("\n⚠ hardcoded addresses found — replace with the injected env vars")
		fmt.Println("  (frontend: NEXT_PUBLIC_GW_URL_API / process.env.GW_URL_API,")
		fmt.Println("   backend CORS: os.environ[\"GW_URL_WEB\"]):")
		max := 12
		for i, h := range hits {
			if i == max {
				fmt.Printf("  … and %d more\n", len(hits)-max)
				break
			}
			fmt.Printf("  %s:%d  %s\n", h.File, h.Line, truncate(h.Text, 80))
		}
	}
	fmt.Println("\nnext: gw trust && gw proxy &   then in any worktree: gw up")
	return nil
}

func cmdUp() error {
	cwd, _ := os.Getwd()
	path, err := config.Find(cwd)
	if err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	info, err := branchinfo.Detect(cfg.Root)
	if err != nil {
		return err
	}
	fmt.Printf("gw: branch %s (%s worktree)\n\n", info.Branch, ternary(info.IsMain, "main", "linked"))
	return up.Run(cfg, info)
}

func cmdList() error {
	routes, err := registry.Load()
	if err != nil {
		return err
	}
	if len(routes) == 0 {
		fmt.Println("no active routes — run `gw up` in a worktree")
		return nil
	}
	for _, r := range routes {
		fmt.Printf("  %-50s → :%-6d %s @ %s\n", "https://"+r.Host, r.Port, r.Service, r.Branch)
	}
	return nil
}

func cmdDoctor() error {
	ok := true

	// CA present?
	if _, err := os.Stat(certs.CACertPath()); err != nil {
		fmt.Println("✗ local CA missing — run `gw trust`")
		ok = false
	} else {
		fmt.Println("✓ local CA exists (system trust not verified — check browser padlock)")
	}

	// Proxy reachable?
	if conn, err := net.Dial("tcp", "127.0.0.1:443"); err == nil {
		conn.Close()
		fmt.Println("✓ something is listening on :443")
	} else if conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", fallbackProxyPort)); err == nil {
		conn.Close()
		fmt.Printf("✓ proxy on fallback :%d (grant :443 with setcap/sudo for portless URLs)\n", fallbackProxyPort)
	} else {
		fmt.Println("✗ proxy not running — run `gw proxy &`")
		ok = false
	}

	// Domain resolution.
	if cwd, err := os.Getwd(); err == nil {
		if p, err := config.Find(cwd); err == nil {
			if cfg, err := config.Load(p); err == nil {
				probe := "gw-doctor-probe." + cfg.Domain
				if addrs, err := net.LookupHost(probe); err == nil && has127(addrs) {
					fmt.Printf("✓ *.%s resolves to 127.0.0.1\n", cfg.Domain)
				} else if strings.HasSuffix(cfg.Domain, ".localhost") {
					fmt.Printf("~ *.%s: browsers resolve .localhost natively; CLI tools may need /etc/hosts\n", cfg.Domain)
				} else {
					fmt.Printf("✗ *.%s does not resolve to 127.0.0.1\n", cfg.Domain)
					fmt.Println("  add a wildcard A record: *." + cfg.Domain + " → 127.0.0.1")
					fmt.Println("  (or run local dnsmasq; see README → DNS)")
					ok = false
				}
			}
		}
	}
	if ok {
		fmt.Println("all good ✓")
	}
	return nil
}

func cmdClean() error {
	cwd, _ := os.Getwd()
	path, err := config.Find(cwd)
	if err != nil {
		return err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	info, err := branchinfo.Detect(cfg.Root)
	if err != nil {
		return err
	}
	return up.Teardown(cfg, info)
}

func has127(addrs []string) bool {
	for _, a := range addrs {
		if a == "127.0.0.1" || a == "::1" {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func ternary(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}
