#!/usr/bin/env bash
# Claude Code WorktreeCreate hook for gw.
#
# Contract (per Claude Code docs): reads JSON on stdin with .name, and MUST
# print the absolute worktree path — and nothing else — on stdout. All
# progress goes to /dev/tty; all tool output is redirected away from stdout.
set -euo pipefail

INPUT=$(cat)
NAME=$(printf '%s' "$INPUT" | jq -r '.name')
REPO="${CLAUDE_PROJECT_DIR:?}"
WT_DIR="${REPO}/.claude/worktrees/${NAME}"
BRANCH="worktree-${NAME}"

log() { echo "gw-hook: $*" > /dev/tty 2>/dev/null || true; }

log "creating worktree ${WT_DIR} (branch ${BRANCH})"
mkdir -p "${REPO}/.claude/worktrees"
git -C "$REPO" worktree add -b "$BRANCH" "$WT_DIR" HEAD >/dev/null 2>&1

# Copy untracked env files the app needs (gw.toml itself is committed).
for f in .env .env.local; do
  [ -f "${REPO}/${f}" ] && cp "${REPO}/${f}" "${WT_DIR}/${f}"
done

# Install dependencies per detected stack, quietly. Customize for your repo.
log "installing dependencies (log: /tmp/gw-worktree-setup.log)"
(
  cd "$WT_DIR"
  if   [ -f pnpm-lock.yaml ]; then pnpm install --frozen-lockfile
  elif [ -f yarn.lock      ]; then yarn install --frozen-lockfile
  elif [ -f package.json   ]; then npm install
  fi
  [ -f uv.lock ] && uv sync
  # Monorepo layout: one level of subdirs too.
  for d in */; do
    [ -f "${d}package.json" ] && (cd "$d" && { [ -f pnpm-lock.yaml ] && pnpm install --frozen-lockfile || npm install; })
    [ -f "${d}uv.lock" ] && (cd "$d" && uv sync)
  done
) >> /tmp/gw-worktree-setup.log 2>&1 || log "dependency install had issues, see log"

# Fire gw's per-branch setup hook (create db, migrate) if gw + gw.toml exist.
if command -v gw >/dev/null 2>&1 && [ -f "${WT_DIR}/gw.toml" ]; then
  log "running gw setup hooks for the new branch"
  (cd "$WT_DIR" && gw doctor) >> /tmp/gw-worktree-setup.log 2>&1 || true
fi

log "worktree ready — run \`gw up\` inside it to start services"
# The one and only stdout line:
echo "$WT_DIR"
