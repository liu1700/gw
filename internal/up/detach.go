package up

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/liu1700/gw/internal/branchinfo"
	"github.com/liu1700/gw/internal/config"
	"github.com/liu1700/gw/internal/detach"
	"github.com/liu1700/gw/internal/registry"
)

func ident(cfg *config.Config, info branchinfo.Info) string {
	return cfg.Domain + "." + info.Slug
}

func pidPath(cfg *config.Config, info branchinfo.Info) string {
	return filepath.Join(stateDir(), "run", ident(cfg, info)+".pid")
}

// LogPath is where a detached `gw up -d` writes its aggregated service logs.
func LogPath(cfg *config.Config, info branchinfo.Info) string {
	return filepath.Join(stateDir(), "logs", ident(cfg, info)+".log")
}

// Detach runs `gw up` for this worktree as a session-independent process:
// it survives the terminal or agent session that started it. Idempotent —
// if services are already up for this branch it just reprints the URLs.
func Detach(cfg *config.Config, info branchinfo.Info) error {
	pp, lp := pidPath(cfg, info), LogPath(cfg, info)
	if pid, ok := detach.Alive(pp); ok {
		fmt.Printf("gw: services for branch %s already running (pid %d)\n", info.Branch, pid)
		printHosts(cfg, info)
		return nil
	}
	EnsureProxy()
	pid, err := detach.Spawn([]string{"up"}, cfg.Root, lp, pp)
	if err != nil {
		return err
	}
	time.Sleep(1500 * time.Millisecond) // catch instant failures
	if _, ok := detach.Alive(pp); !ok {
		b, _ := os.ReadFile(lp)
		os.Remove(pp)
		return fmt.Errorf("services failed to start:\n%s\n(full log: %s)", tailLines(string(b), 15), lp)
	}
	fmt.Printf("gw: services for branch %s up (detached, pid %d)\n", info.Branch, pid)
	printHosts(cfg, info)
	fmt.Println("\n  logs: gw logs    stop: gw down")
	return nil
}

// Down stops this worktree's detached services and clears their routes.
func Down(cfg *config.Config, info branchinfo.Info) error {
	pid, was := detach.Stop(pidPath(cfg, info))
	if !was {
		fmt.Printf("gw: nothing running for branch %s\n", info.Branch)
		return nil
	}
	registry.UnregisterPID(pid) // belt-and-braces if the child was KILLed
	fmt.Printf("gw: stopped services for branch %s (pid %d)\n", info.Branch, pid)
	return nil
}

func printHosts(cfg *config.Config, info branchinfo.Info) {
	for _, svc := range cfg.Services {
		fmt.Printf("  %-8s → https://%s\n", svc.Name, cfg.HostFor(svc.Name, info.Slug, info.IsMain))
	}
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// --- proxy lifecycle (the one machine-wide daemon) ---

func proxyPidPath() string { return filepath.Join(stateDir(), "run", "proxy.pid") }

// ProxyLogPath is where a detached proxy writes its log.
func ProxyLogPath() string { return filepath.Join(stateDir(), "logs", "proxy.log") }

// EnsureProxy starts the gateway detached if nothing is listening yet.
// Called from `gw up -d` so the happy path is a single command.
func EnsureProxy() {
	if _, ok := detach.Alive(proxyPidPath()); ok {
		return
	}
	if portOpen(443) || portOpen(8443) {
		return // started manually (foreground or otherwise)
	}
	if pid, err := detach.Spawn([]string{"proxy"}, "", ProxyLogPath(), proxyPidPath()); err == nil {
		fmt.Printf("gw: started proxy (detached, pid %d)\n", pid)
		time.Sleep(500 * time.Millisecond)
	}
}

// ProxyDetach is `gw proxy -d`: explicit detached start.
func ProxyDetach() error {
	if pid, ok := detach.Alive(proxyPidPath()); ok {
		fmt.Printf("gw: proxy already running (pid %d)\n", pid)
		return nil
	}
	if portOpen(443) || portOpen(8443) {
		return fmt.Errorf("something is already listening on :443/:8443 (a foreground `gw proxy`?)")
	}
	pid, err := detach.Spawn([]string{"proxy"}, "", ProxyLogPath(), proxyPidPath())
	if err != nil {
		return err
	}
	fmt.Printf("gw: proxy up (detached, pid %d) — log: %s\n", pid, ProxyLogPath())
	return nil
}

// ProxyStop is `gw proxy stop`.
func ProxyStop() error {
	pid, was := detach.Stop(proxyPidPath())
	if !was {
		fmt.Println("gw: no detached proxy running")
		return nil
	}
	fmt.Printf("gw: proxy stopped (pid %d)\n", pid)
	return nil
}

func portOpen(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 150*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}
