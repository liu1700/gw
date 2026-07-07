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

// version is overridden at release time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	// Top-level help: `gw`, `gw help`, `gw -h`, `gw --help`.
	if cmd == "help" || cmd == "-h" || cmd == "--help" {
		usage()
		return
	}

	allowed, known := commandFlags[cmd]
	if !known {
		fmt.Fprintf(os.Stderr, "gw: unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}

	// `gw <cmd> -h|--help` prints subcommand help and exits with no side effects.
	if hasArg("-h") || hasArg("--help") {
		fmt.Print(subUsage[cmd])
		return
	}

	// Reject unrecognized flags rather than silently executing the command.
	if err := validateFlags(cmd, args, allowed); err != nil {
		fmt.Fprintln(os.Stderr, "gw:", err)
		fmt.Fprint(os.Stderr, "\n", subUsage[cmd])
		os.Exit(2)
	}

	var err error
	switch cmd {
	case "init":
		err = cmdInit()
	case "trust":
		err = certs.Trust()
	case "proxy":
		switch {
		case hasArg("stop"):
			err = up.ProxyStop()
		case hasArg("-d"), hasArg("--detach"):
			err = up.ProxyDetach()
		default:
			err = proxy.Listen(fallbackProxyPort)
		}
	case "up":
		err = cmdUp(hasArg("-d") || hasArg("--detach"))
	case "down":
		err = cmdDown()
	case "logs":
		err = cmdLogs()
	case "list":
		err = cmdList()
	case "doctor":
		err = cmdDoctor()
	case "clean":
		err = cmdClean()
	case "version":
		fmt.Println("gw " + version)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gw:", err)
		os.Exit(1)
	}
}

// commandFlags lists the recognized subcommands and the optional flags each
// accepts (beyond -h/--help, which every command handles). Positional args
// such as `proxy stop` are validated by the command itself, not here.
var commandFlags = map[string][]string{
	"init":    {},
	"trust":   {},
	"proxy":   {"-d", "--detach"},
	"up":      {"-d", "--detach"},
	"down":    {},
	"logs":    {},
	"list":    {},
	"doctor":  {},
	"clean":   {},
	"version": {},
}

// validateFlags returns an error if args contains a dash-prefixed flag that the
// command does not accept. Non-flag positionals are left for the command.
func validateFlags(cmd string, args, allowed []string) error {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			continue
		}
		ok := false
		for _, f := range allowed {
			if a == f {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("unknown flag %q for `gw %s` (try `gw %s --help`)", a, cmd, cmd)
		}
	}
	return nil
}

// subUsage maps each subcommand to its help text, printed on `gw <cmd> --help`.
var subUsage = map[string]string{
	"init": `gw init — detect your stack, generate gw.toml, flag hardcoded URLs

  usage: gw init

Scans the current directory for a known framework, writes gw.toml, and
reports hardcoded addresses to replace with the injected GW_URL_* env vars.
`,
	"trust": `gw trust — create the local CA and add it to the system trust store

  usage: gw trust

No sudo required on macOS. Modifies the system trust store.
`,
	"up": `gw up — start this worktree's services

  usage: gw up [-d|--detach]

  -d, --detach   run services in the background; starts the proxy if needed

Without -d, services run in the foreground.
`,
	"down": `gw down — stop this worktree's detached services

  usage: gw down
`,
	"logs": `gw logs — show logs from this worktree's detached services

  usage: gw logs
`,
	"list": `gw list — show active routes across all branches

  usage: gw list
`,
	"proxy": `gw proxy — run the HTTPS gateway

  usage: gw proxy [-d|--detach | stop]

  -d, --detach   run the proxy in the background
  stop           stop the detached proxy

Without arguments, runs the proxy in the foreground.
`,
	"doctor": `gw doctor — diagnose DNS / CA / proxy issues

  usage: gw doctor
`,
	"clean": `gw clean — run teardown hooks for the current branch

  usage: gw clean
`,
	"version": `gw version — print the gw version

  usage: gw version
`,
}

func usage() {
	fmt.Print(`gw — branch-aware local HTTPS gateway

  gw init         detect your stack, generate gw.toml, flag hardcoded URLs
  gw trust        create the local CA and trust it (no sudo on macOS)
  gw up -d        start this worktree's services, detached; starts the proxy
                  if needed (omit -d to run in the foreground)
  gw down         stop this worktree's detached services
  gw logs         show logs from this worktree's detached services
  gw list         show active routes across all branches
  gw proxy        run the HTTPS gateway in the foreground (-d detached, stop)
  gw doctor       diagnose DNS / CA / proxy issues
  gw clean        run teardown hooks for the current branch (drop db etc.)
`)
}

func hasArg(s string) bool {
	for _, a := range os.Args[2:] {
		if a == s {
			return true
		}
	}
	return false
}

// loadCtx resolves gw.toml and the current branch for worktree-scoped commands.
func loadCtx() (*config.Config, branchinfo.Info, error) {
	cwd, _ := os.Getwd()
	path, err := config.Find(cwd)
	if err != nil {
		return nil, branchinfo.Info{}, err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, branchinfo.Info{}, err
	}
	// Anchor to the worktree that contains cwd, not the directory holding
	// gw.toml. A worktree nested inside the main repo whose branch hasn't
	// committed its own gw.toml would otherwise resolve the *main* repo's
	// branch (bare domain) and run the main repo's files. gw.toml is shared
	// config; the branch and working tree come from the current worktree.
	if root, err := branchinfo.WorktreeRoot(cwd); err == nil {
		cfg.Root = root
	}
	info, err := branchinfo.Detect(cfg.Root)
	if err != nil {
		return nil, branchinfo.Info{}, err
	}
	return cfg, info, nil
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
	fmt.Println("\nnext: gw trust   then in any worktree: gw up -d")
	fmt.Println("tip: on Claude Code? install the plugin so agents drive gw for you —")
	fmt.Println("     /plugin marketplace add liu1700/gw && /plugin install gw@gw-marketplace")
	return nil
}

func cmdUp(detached bool) error {
	cfg, info, err := loadCtx()
	if err != nil {
		return err
	}
	if detached {
		return up.Detach(cfg, info)
	}
	fmt.Printf("gw: branch %s (%s worktree)\n\n", info.Branch, ternary(info.IsMain, "main", "linked"))
	return up.Run(cfg, info)
}

func cmdDown() error {
	cfg, info, err := loadCtx()
	if err != nil {
		return err
	}
	return up.Down(cfg, info)
}

func cmdLogs() error {
	cfg, info, err := loadCtx()
	if err != nil {
		return err
	}
	p := up.LogPath(cfg, info)
	b, err := os.ReadFile(p)
	if err != nil {
		return fmt.Errorf("no logs for branch %s — start services with `gw up -d` first", info.Branch)
	}
	fmt.Printf("== %s ==\n", p)
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > 120 {
		fmt.Printf("… (%d earlier lines)\n", len(lines)-120)
		lines = lines[len(lines)-120:]
	}
	fmt.Println(strings.Join(lines, "\n"))
	return nil
}

func cmdList() error {
	routes, err := registry.PruneDead()
	if err != nil {
		return err
	}
	if len(routes) == 0 {
		fmt.Println("no active routes — run `gw up -d` in a worktree")
		return nil
	}
	for _, r := range routes {
		addr := "https://" + r.Host
		if r.Mode == config.ProxyNone {
			addr = r.Host // no https endpoint exists
		}
		tag := ""
		if l := config.ModeLabel(r.Mode); l != "" {
			tag = "  [" + l + "]"
		}
		fmt.Printf("  %-50s → :%-6d %s @ %s%s\n", addr, r.Port, r.Service, r.Branch, tag)
	}
	return nil
}

func cmdClean() error {
	cfg, info, err := loadCtx()
	if err != nil {
		return err
	}
	return up.Teardown(cfg, info)
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
