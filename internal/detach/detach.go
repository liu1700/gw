// Package detach re-executes the gw binary as a session-independent process
// (own session id, output to a logfile, pid tracked in a pidfile), so
// services keep running after the terminal or agent session that started
// them exits.
package detach

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Spawn starts `gw <args...>` detached. Returns the child pid.
func Spawn(args []string, dir, logPath, pidPath string) (int, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(pidPath), 0o700); err != nil {
		return 0, err
	}
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	defer lf.Close()
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(exe, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdout, cmd.Stderr = lf, lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		syscall.Kill(-pid, syscall.SIGTERM)
		return 0, err
	}
	cmd.Process.Release()
	return pid, nil
}

// Alive reports whether the process recorded in pidPath is still running.
func Alive(pidPath string) (int, bool) {
	b, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	if syscall.Kill(pid, 0) == syscall.ESRCH {
		return pid, false
	}
	return pid, true
}

// Stop TERMs the detached process group, escalating to KILL after a grace
// period. Returns the pid and whether anything was actually running.
func Stop(pidPath string) (int, bool) {
	pid, ok := Alive(pidPath)
	if !ok {
		os.Remove(pidPath)
		return pid, false
	}
	syscall.Kill(-pid, syscall.SIGTERM) // Setsid makes pgid == pid
	for i := 0; i < 30; i++ {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if syscall.Kill(pid, 0) != syscall.ESRCH {
		syscall.Kill(-pid, syscall.SIGKILL)
	}
	os.Remove(pidPath)
	return pid, true
}
