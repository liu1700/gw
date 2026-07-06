package detect

import (
	"os"
	"path/filepath"
	"testing"
)

// ScanHardcoded must not descend into a nested git worktree/submodule: those
// belong to another branch's tree and were the source of `gw init` emitting
// hundreds of unrelated matches.
func TestScanHardcodedSkipsNestedGitTrees(t *testing.T) {
	root := t.TempDir()

	// A hardcoded URL in the real checkout — should be reported.
	write(t, filepath.Join(root, "app.ts"), `const api = "http://localhost:3000";`)

	// A nested worktree: its own .git marker plus a source file with a URL.
	nested := filepath.Join(root, "wt", "foo")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(nested, ".git"), "gitdir: /somewhere/.git/worktrees/foo")
	write(t, filepath.Join(nested, "app.ts"), `const api = "http://localhost:9999";`)

	hits := ScanHardcoded(root)
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit (only the top-level file), got %d: %+v", len(hits), hits)
	}
	if hits[0].File != "app.ts" {
		t.Errorf("expected app.ts, got %q", hits[0].File)
	}
}

func TestIsGitRoot(t *testing.T) {
	root := t.TempDir()
	if isGitRoot(root) {
		t.Error("empty dir should not be a git root")
	}
	// Linked worktrees use a .git file, not a directory.
	write(t, filepath.Join(root, ".git"), "gitdir: ...")
	if !isGitRoot(root) {
		t.Error("dir with a .git file should be a git root")
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
