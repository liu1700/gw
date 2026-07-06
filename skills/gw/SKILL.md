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
| `GW_PORT_<SERVICE>` | `GW_PORT_WORKER=21387` | direct 127.0.0.1 access — the only address for `proxy = "none"` services |
| `GW_BRANCH` / `GW_SLUG` | `feature/auth` / `feature-auth` | naming, logging |
| `NODE_EXTRA_CA_CERTS` | `~/.gw/ca.pem` | Node adds the gw CA to its roots (additive) |
| `REQUESTS_CA_BUNDLE`, `SSL_CERT_FILE` | `~/.gw/ca-bundle.pem` | Python/OpenSSL trust the gw CA **and** public roots — the bundle combines them, so outbound HTTPS to public endpoints still verifies |

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
once per branch **before services start** (idempotent via marker file) — use
it for bootstrap work like creating databases, running migrations, or
minting an app's own certificates; `gw clean` runs teardown.

### Non-HTTP services: `proxy = "passthrough" | "none"`

By default gw terminates TLS and reverse-proxies HTTP. Services that can't
live behind that (mTLS servers that authenticate the *client's* certificate,
gRPC-over-TLS, anything speaking its own TLS) declare a mode:

```toml
[services.dataplane]
cmd = "orlop-server --listen 127.0.0.1:$PORT"
proxy = "passthrough"   # SNI-routed raw TCP splice — gw does NOT terminate
                        # TLS, so client certs reach the server intact

[services.worker]
cmd = "worker --listen 127.0.0.1:$PORT"
proxy = "none"          # supervised + isolated $PORT only, no routing;
                        # siblings reach it at 127.0.0.1:$GW_PORT_WORKER
```

Both kinds are still started/stopped/logged by `gw up -d` / `gw down` /
`gw logs` and get branch-hashed ports. A `passthrough` service must present
a certificate valid for its gw hostname (`$GW_URL` minus the scheme) — mint
one in `hooks.setup` with the app's own CA. Clients must connect with SNI
(any TLS client dialing the hostname does this automatically).

Backing services (Postgres/Redis) are NOT started by gw — the model is one
shared server, per-branch logical isolation via templated names. Plaintext
TCP protocols carry no hostname at all, which is why databases isolate by
*name* (`{branch_snake}`) instead of by route.

## Installing gw

If `gw` is not on PATH, install the prebuilt binary — do not assume Go is
available:

```bash
curl -fsSL https://raw.githubusercontent.com/liu1700/gw/main/install.sh | sh
```

It installs to `~/.local/bin` (override with `GW_INSTALL_DIR`) and prints a
PATH hint if needed. Verify with `gw version`.

## Troubleshooting

- URL not loading → `gw doctor`, then `gw list` to confirm the route exists.
- `502 no route` → the service isn't running; `gw up -d` in that worktree,
  then `gw logs` if it 502s again (the service may have crashed on boot).
- `508 loop detected` → the app is proxying to its own public hostname;
  point it at the sibling's `GW_URL_*` instead.
- `421 misdirected request` → the hostname belongs to a `proxy = "none"`
  service (connect to `127.0.0.1:$GW_PORT_<SERVICE>` instead), or to a
  `passthrough` service reached without SNI.
- TLS/cert error on a `passthrough` URL → that's the app's own certificate
  (gw splices, it does not terminate): the service must present a cert valid
  for its gw hostname — mint it in `hooks.setup` — and the client must trust
  the app's CA, not gw's.
- Cert warning in browser → `gw trust` hasn't been run (no sudo on macOS,
  sudo once on Linux; ask the user before running it).
- `:443 permission denied` (Linux) → proxy fell back to `:8443`; grant with
  `sudo setcap cap_net_bind_service=+ep $(which gw)`. macOS 10.14+ binds
  :443 without root.
- Real domain not resolving → needs a wildcard A record
  `*.dev.example.com → 127.0.0.1`, or local dnsmasq.

## Reporting gw bugs

If gw **itself** misbehaves — crashes, routes to the wrong branch/service,
serves a bad certificate, leaves stale routes, `gw up -d` loses processes —
as opposed to the user's app failing, tell the user and offer to file a bug
at https://github.com/liu1700/gw/issues. With their consent, use
`gh issue create --repo liu1700/gw` (or hand them the link) and include:
`gw version`, OS and version, the exact command run, and the relevant
`gw doctor` / `gw logs` output. Redact secrets (tokens, DATABASE_URL
credentials) from anything you paste.
