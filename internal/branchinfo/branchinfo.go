// Package branchinfo derives everything gw needs from the current git context:
// the branch name, a DNS-safe slug, and deterministic ports.
package branchinfo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Info describes the current worktree.
type Info struct {
	Branch string // raw branch name, e.g. "feature/auth"
	Slug   string // DNS-safe, e.g. "feature-auth"
	IsMain bool   // true when this is the repo's default branch or the main worktree
}

var nonDNS = regexp.MustCompile(`[^a-z0-9-]+`)

// Slugify turns a branch name into a valid DNS label (RFC 1035, max 63 chars).
// Long names are truncated with a 6-char hash suffix for uniqueness.
func Slugify(branch string) string {
	s := strings.ToLower(branch)
	s = nonDNS.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "detached"
	}
	const max = 63
	if len(s) > max {
		h := sha256.Sum256([]byte(branch))
		suffix := hex.EncodeToString(h[:])[:6]
		s = strings.TrimRight(s[:max-7], "-") + "-" + suffix
	}
	return s
}

// WorktreeRoot returns the top-level directory of the git worktree that
// contains dir. For a linked worktree nested inside the main repo this is the
// linked worktree's own root — not the main repo's — because git resolves the
// worktree from the nearest .git, so operations anchor to the right branch.
func WorktreeRoot(dir string) (string, error) {
	root, err := gitOut(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("not a git repository (or git missing): %w", err)
	}
	return root, nil
}

// Detect reads the current branch via git. dir may be "" for cwd.
func Detect(dir string) (Info, error) {
	branch, err := gitOut(dir, "branch", "--show-current")
	if err != nil {
		return Info{}, fmt.Errorf("not a git repository (or git missing): %w", err)
	}
	if branch == "" { // detached HEAD: fall back to short SHA
		sha, _ := gitOut(dir, "rev-parse", "--short", "HEAD")
		branch = "detached-" + sha
	}
	// A linked worktree's git-common-dir differs from its git-dir.
	gitDir, _ := gitOut(dir, "rev-parse", "--git-dir")
	commonDir, _ := gitOut(dir, "rev-parse", "--git-common-dir")
	isMain := gitDir == commonDir
	return Info{Branch: branch, Slug: Slugify(branch), IsMain: isMain}, nil
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// Port range for hashed allocation. Above well-known dev ports, below ephemeral.
const (
	portBase  = 20000
	portRange = 20000
)

// PortFor deterministically maps (branch, service) to a port, then linearly
// probes for a free one so collisions between different branches degrade
// gracefully instead of failing.
func PortFor(branch, service string) int {
	h := fnv.New32a()
	h.Write([]byte(branch + "\x00" + service))
	p := portBase + int(h.Sum32())%portRange
	for i := 0; i < 200; i++ {
		cand := portBase + (p-portBase+i)%portRange
		if free(cand) {
			return cand
		}
	}
	return p // give up probing; let the app surface the bind error
}

func free(port int) bool {
	l, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 150*time.Millisecond)
	if err != nil {
		return true // nothing listening
	}
	l.Close()
	return false
}
