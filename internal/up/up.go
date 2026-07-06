// Package up orchestrates `gw up`: for each service in gw.toml, derive a
// deterministic port, inject the env contract, register the route, spawn the
// process, and aggregate logs. First run in a branch also fires hooks.setup.
package up

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/liu1700/gw/internal/branchinfo"
	"github.com/liu1700/gw/internal/certs"
	"github.com/liu1700/gw/internal/config"
	"github.com/liu1700/gw/internal/registry"
)

// Run starts every service and blocks until interrupted or all exit.
func Run(cfg *config.Config, info branchinfo.Info) error {
	scheme := "https"
	certs.LoadOrCreate() // ensure the CA exists before injecting its path

	// 1. Resolve ports and hostnames for the whole branch first, so every
	//    process can receive the full set of sibling URLs.
	type planned struct {
		svc  config.Service
		port int
		host string
	}
	var plan []planned
	// Make Node/Python HTTP clients trust our CA out of the box. NODE_EXTRA_CA_CERTS
	// is additive (Node appends it to its roots), so it gets the gw CA alone;
	// REQUESTS_CA_BUNDLE / SSL_CERT_FILE *replace* the trust store in Python/OpenSSL,
	// so they get a bundle that also carries the public roots — otherwise the
	// service's own outbound HTTPS to public endpoints would fail to verify.
	bundle := certs.CombinedBundlePath()
	shared := map[string]string{ // env every service receives
		"GW_BRANCH":           info.Branch,
		"GW_SLUG":             info.Slug,
		"GW_DOMAIN":           cfg.Domain,
		"NODE_EXTRA_CA_CERTS": certs.CACertPath(),
		"REQUESTS_CA_BUNDLE":  bundle,
		"SSL_CERT_FILE":       bundle,
	}
	for _, svc := range cfg.Services {
		port := branchinfo.PortFor(info.Branch, svc.Name)
		host := cfg.HostFor(svc.Name, info.Slug, info.IsMain)
		plan = append(plan, planned{svc, port, host})
		upper := strings.ToUpper(svc.Name)
		shared["GW_PORT_"+upper] = fmt.Sprint(port)
		if svc.Proxy == config.ProxyNone {
			continue // not routed — a GW_URL_* would point nowhere
		}
		shared["GW_URL_"+upper] = scheme + "://" + host
		// Framework conventions for exposing env to browser bundles.
		shared["NEXT_PUBLIC_GW_URL_"+upper] = scheme + "://" + host
		shared["VITE_GW_URL_"+upper] = scheme + "://" + host
	}
	for k, v := range cfg.Env { // user-templated env (DATABASE_URL etc.)
		shared[k] = config.Render(v, info.Branch, info.Slug)
	}

	// 2. First-run setup hook (create db, run migrations, ...).
	if err := runHook(cfg, info, shared, "setup"); err != nil {
		return fmt.Errorf("hooks.setup failed: %w", err)
	}

	// 3. Spawn services.
	var wg sync.WaitGroup
	procs := make([]*exec.Cmd, 0, len(plan))
	self := os.Getpid()

	for _, p := range plan {
		env := os.Environ()
		for k, v := range shared {
			env = append(env, k+"="+v)
		}
		env = append(env,
			"PORT="+fmt.Sprint(p.port),
			"HOST=127.0.0.1",
		)
		if p.svc.Proxy != config.ProxyNone {
			env = append(env, "GW_URL="+scheme+"://"+p.host)
		}
		for k, v := range p.svc.Env {
			env = append(env, k+"="+config.Render(v, info.Branch, info.Slug))
		}

		cmd := exec.Command("sh", "-c", expandPort(p.svc.Cmd, p.port))
		cmd.Dir = filepath.Join(cfg.Root, p.svc.Dir)
		cmd.Env = env
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // kill whole tree on exit

		stdout, _ := cmd.StdoutPipe()
		stderr, _ := cmd.StderrPipe()
		if err := cmd.Start(); err != nil {
			shutdown(procs)
			return fmt.Errorf("start %s: %w", p.svc.Name, err)
		}
		procs = append(procs, cmd)

		registry.Register(registry.Route{
			Host: p.host, Port: p.port, PID: self,
			Branch: info.Branch, Service: p.svc.Name,
			Mode: p.svc.Proxy,
		})
		fmt.Println(serviceLine(p.svc.Name, p.host, p.port, p.svc.Proxy))

		// Per-service supervision: drain the pipes (EOF == the process exited),
		// reap it, then drop its route immediately. Without this a crashed
		// service's route lingers until the whole `gw up` exits, and a detached
		// start can't tell a healthy service from one that died on boot.
		prefix := fmt.Sprintf("[%s] ", p.svc.Name)
		var pipes sync.WaitGroup
		pipes.Add(2)
		go func() { defer pipes.Done(); pipe(prefix, stdout) }()
		go func() { defer pipes.Done(); pipe(prefix, stderr) }()
		wg.Add(1)
		go func(cmd *exec.Cmd, host, name string) {
			defer wg.Done()
			pipes.Wait()
			err := cmd.Wait()
			registry.Unregister(host)
			fmt.Printf("%sgw: service exited (%s)\n", prefix, exitReason(err))
		}(cmd, p.host, p.svc.Name)
	}
	fmt.Println("\ngw: all services up — Ctrl-C to stop")

	// 4. Wait for Ctrl-C, then tear down routes and process groups.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-sig:
	case <-done:
	}
	shutdown(procs)
	registry.UnregisterPID(self)
	return nil
}

// exitReason renders a service process's exit for the aggregated log.
func exitReason(err error) string {
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}

// expandPort substitutes $PORT in the command string for frameworks that
// take ports as CLI flags rather than env (the env var is also set).
func expandPort(cmd string, port int) string {
	return strings.ReplaceAll(cmd, "$PORT", fmt.Sprint(port))
}

// serviceLine renders one service's address line for up/status output.
func serviceLine(name, host string, port int, mode string) string {
	switch mode {
	case config.ProxyPassthrough:
		return fmt.Sprintf("  %-8s → https://%s  (:%d, %s)", name, host, port, config.ModeLabel(mode))
	case config.ProxyNone:
		return fmt.Sprintf("  %-8s → 127.0.0.1:%d  (%s)", name, port, config.ModeLabel(mode))
	default:
		return fmt.Sprintf("  %-8s → https://%s  (:%d)", name, host, port)
	}
}

func runHook(cfg *config.Config, info branchinfo.Info, shared map[string]string, name string) error {
	h, ok := cfg.Hooks[name]
	if !ok || h == "" {
		return nil
	}
	// Idempotence marker: hooks.setup runs once per branch. Scoped by domain
	// so equally-named branches in different projects don't share a marker.
	marker := filepath.Join(stateDir(), "hooks", ident(cfg, info)+"."+name)
	legacy := filepath.Join(stateDir(), "hooks", info.Slug+".setup") // pre-scoping name
	if name == "setup" {
		if _, err := os.Stat(legacy); err == nil {
			os.Rename(legacy, marker) // migrate so setup doesn't re-fire on upgrade
		}
		if _, err := os.Stat(marker); err == nil {
			return nil
		}
	}
	rendered := config.Render(h, info.Branch, info.Slug)
	fmt.Printf("gw: running hooks.%s: %s\n", name, rendered)
	cmd := exec.Command("sh", "-c", rendered)
	cmd.Dir = cfg.Root
	cmd.Env = os.Environ()
	for k, v := range shared {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	if name == "setup" {
		os.MkdirAll(filepath.Dir(marker), 0o700)
		os.WriteFile(marker, nil, 0o644)
	}
	if name == "teardown" {
		os.Remove(filepath.Join(stateDir(), "hooks", ident(cfg, info)+".setup"))
		os.Remove(legacy) // else the next setup would migrate it back and skip
	}
	return nil
}

// Teardown is called by `gw clean` (and the WorktreeRemove hook).
func Teardown(cfg *config.Config, info branchinfo.Info) error {
	return runHook(cfg, info, map[string]string{"GW_BRANCH": info.Branch, "GW_SLUG": info.Slug}, "teardown")
}

func stateDir() string {
	if d := os.Getenv("GW_STATE_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gw")
}

func pipe(prefix string, r interface{ Read([]byte) (int, error) }) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		fmt.Println(prefix + sc.Text())
	}
}

func shutdown(procs []*exec.Cmd) {
	for _, c := range procs {
		if c.Process != nil {
			syscall.Kill(-c.Process.Pid, syscall.SIGTERM) // whole process group
		}
	}
}
