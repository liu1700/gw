---
name: gw
description: >
  Run dev servers in git worktrees without port conflicts, using gw — the
  branch-aware local HTTPS gateway. Use this skill whenever the user mentions
  port conflicts, EADDRINUSE, "address already in use", running multiple
  branches or worktrees at once, parallel Claude Code sessions, testing
  frontend+backend per branch, needing local HTTPS or a real domain locally,
  or asks how to preview several features simultaneously. Also use it when
  starting dev servers in ANY repo that contains a gw.toml file — in that
  case, always start services through `gw up` instead of running `npm run
  dev` / `uvicorn` directly.
---

# gw — branch-aware local HTTPS gateway

gw gives every git worktree its own trusted HTTPS URLs derived from the
branch name. One proxy daemon, one committed `gw.toml`, zero per-branch
configuration:

| worktree | URL |
|---|---|
| main | `https://web.myapp.localhost` |
| branch `feature/auth` | `https://web.feature-auth.myapp.localhost` |

The proxy mints TLS leaf certificates on demand (signed by a local CA
installed via `gw trust`), so any branch subdomain gets a valid green-lock
cert with no configuration.

## Rule zero

**If the repo has a `gw.toml`, never start dev servers directly.** Do not run
`npm run dev`, `pnpm dev`, `uvicorn`, `flask run`, etc. yourself — run
`gw up -d` from the worktree root instead. It assigns deterministic ports,
injects the env contract, registers routes with the proxy (starting the
proxy if needed), and detaches: services keep running after your session
ends. It is idempotent and scoped to this worktree — other worktrees'
services are untouched. Starting servers manually recreates the exact port
conflicts gw exists to prevent.

When the user asks you to start services ("起一下服务", "start the dev
servers", …), run `gw up -d` and report the printed URLs. When they ask to
stop, run `gw down` (stops only this worktree's services).

When writing tests, scripts, or documentation in a gw-enabled repo, **never
hardcode `localhost:PORT`** — use the injected env vars below.

## Commands

```
gw init      # detect stack (Next.js/Vite/FastAPI/Flask/Django), write gw.toml,
             # and list hardcoded localhost URLs that must be parameterized
gw trust     # one-time: create local CA and trust it (no sudo on macOS; sudo on Linux)
gw up -d     # start ALL services for this worktree's branch, detached from your
             # session; auto-starts the proxy; idempotent (reprints URLs if running)
gw down      # stop this worktree's detached services (other worktrees unaffected)
gw logs      # show the detached services' logs — check here when something fails
gw list      # show active routes across all branches
gw doctor    # diagnose DNS / CA / proxy problems — run this first when URLs don't load
gw clean     # run teardown hooks for the current branch (e.g. drop its database)
gw up        # foreground variant (blocks, Ctrl-C stops) — only when the user
             # explicitly wants to watch logs live
```

Typical first-time flow in a repo: `gw init` → fix flagged hardcoded URLs →
`gw trust` → `gw up -d`.

Typical new-worktree flow: `git worktree add ../repo-auth feature/auth` →
install deps in it → `gw up -d`. Nothing else — the branch prefix, ports,
certs, and (if configured) per-branch database all happen automatically.

## Env contract (what `gw up` injects into every service)

| Variable | Example | Use for |
|---|---|---|
| `PORT`, `HOST` | `24094`, `127.0.0.1` | what the dev server must bind to |
| `GW_URL` | `https://web.feature-auth.myapp.localhost` | this service's own public URL |
| `GW_URL_<SERVICE>` | `GW_URL_API=https://api.feature-auth...` | calling sibling services |
| `NEXT_PUBLIC_GW_URL_<SERVICE>` | same value | browser-side code in Next.js |
| `VITE_GW_URL_<SERVICE>` | same value | browser-side code in Vite |
| `GW_BRANCH` / `GW_SLUG` | `feature/auth` / `feature-auth` | naming, logging |
| `NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE` | `~/.gw/ca.pem` | server-to-server HTTPS calls trust the local CA automatically |

When editing app code, replace hardcoded addresses like this:

```ts
// frontend
- const API = "http://localhost:8000";
+ const API = process.env.NEXT_PUBLIC_GW_URL_API;
```

```python
# backend CORS
- allow_origins=["http://localhost:3000"]
+ allow_origins=[os.environ["GW_URL_WEB"]]
```

## gw.toml reference

```toml
domain = "myapp.localhost"        # or a real domain with *.dev.example.com → 127.0.0.1

[services.web]                    # each service becomes <name>.<slug>.<domain>
cmd = "pnpm dev"
dir = "frontend"                  # optional, default "."

[services.api]
cmd = "uv run uvicorn main:app --port $PORT --host 127.0.0.1"
dir = "api"

[env]                             # templated, injected into every service
DATABASE_URL = "postgres://localhost:5432/myapp_{branch_snake}"

[hooks]
setup = "createdb myapp_{branch_snake} 2>/dev/null; cd api && uv run alembic upgrade head"
teardown = "dropdb myapp_{branch_snake}"
```

Templates: `{branch}` (raw), `{slug}` (DNS-safe, `feature-auth`),
`{branch_snake}` (`feature_auth`, for database names). `hooks.setup` runs
once per branch (idempotent via marker file); `gw clean` runs teardown.

Backing services (Postgres/Redis) are NOT started by gw — the model is one
shared server, per-branch logical isolation via templated names. HTTP
services are routed by hostname; raw-TCP services can't be (no Host concept
in the protocol), which is why databases isolate by name instead.

## Troubleshooting

- URL not loading → `gw doctor`, then `gw list` to confirm the route exists.
- `502 no route` → the service isn't running; `gw up -d` in that worktree,
  then `gw logs` if it 502s again (the service may have crashed on boot).
- `508 loop detected` → the app is proxying to its own public hostname;
  point it at the sibling's `GW_URL_*` instead.
- Cert warning in browser → `gw trust` hasn't been run (no sudo on macOS,
  sudo once on Linux; ask the user before running it).
- `:443 permission denied` (Linux) → proxy fell back to `:8443`; grant with
  `sudo setcap cap_net_bind_service=+ep $(which gw)`. macOS 10.14+ binds
  :443 without root.
- Real domain not resolving → needs a wildcard A record
  `*.dev.example.com → 127.0.0.1`, or local dnsmasq.
