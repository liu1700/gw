#!/usr/bin/env bash
# Claude Code WorktreeRemove hook for gw: run per-branch teardown
# (drop the branch database, etc.) before the worktree disappears.
set -euo pipefail

INPUT=$(cat)
WT_DIR=$(printf '%s' "$INPUT" | jq -r '.path // empty')
[ -z "$WT_DIR" ] && exit 0

if command -v gw >/dev/null 2>&1 && [ -f "${WT_DIR}/gw.toml" ]; then
  (cd "$WT_DIR" && gw clean) > /dev/tty 2>&1 || true
fi
