# Agent instructions for this repo

gw is a Go CLI: a branch-aware local HTTPS gateway for git worktrees.

## Build / test

```bash
go build ./...
go vet ./...
go test ./...
```

All three must pass before committing. Go 1.22+, stdlib only — do not add
external dependencies without discussion (single-binary, zero-dep is a
design constraint; the planned exceptions are BurntSushi/toml and
smallstep/truststore, tracked in the README roadmap).

## Layout

- `cmd/gw/` — CLI entry point and subcommand dispatch.
- `internal/branchinfo/` — branch detection, DNS-safe slugs, deterministic port hashing.
- `internal/config/` — `gw.toml` loading and `{branch}`/`{slug}` template rendering.
- `internal/detect/` — `gw init` stack detection and hardcoded-URL scanning.
- `internal/certs/` — local CA + on-demand leaf certificate signing (`tls.Config.GetCertificate`).
- `internal/proxy/` — the HTTPS gateway daemon; Host-header routing.
- `internal/registry/` — hostname→port route table shared between `gw up` and the proxy (JSON file, mtime-cached).
- `internal/up/` — `gw up` orchestration: ports, env contract, hooks, process lifecycle.

## Claude Code plugin

The repo doubles as a Claude Code plugin and its marketplace:
`.claude-plugin/`, `skills/gw/SKILL.md`, `commands/setup.md`, `hooks/`.

When changing CLI commands, flags, or the injected env contract
(`PORT`, `GW_URL_*`, `GW_BRANCH`, …), update `skills/gw/SKILL.md` and the
README in the same commit — the skill is documentation that agents execute,
so drift breaks users. Validate with `claude plugin validate . --strict`.

## Manual end-to-end check

```bash
export GW_STATE_DIR=$(mktemp -d)   # keep test state out of ~/.gw
go build -o /tmp/gw ./cmd/gw
/tmp/gw proxy &                    # binds :443 (macOS needs no sudo) or :8443
# in a repo with gw.toml: /tmp/gw up, then curl -k the printed URL
```

Never run `gw trust` in automation — it modifies the system trust store and
prompts for sudo.
