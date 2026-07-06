# gw

**Branch-aware local HTTPS gateway for git worktrees. For humans and AI
agents.**

**Your E2E vehicle**

[![ci](https://github.com/liu1700/gw/actions/workflows/ci.yml/badge.svg)](https://github.com/liu1700/gw/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/liu1700/gw)](https://github.com/liu1700/gw/releases)
[![License: MIT](https://img.shields.io/github/license/liu1700/gw)](LICENSE)

Every git worktree gets trusted HTTPS URLs derived from its branch name —
run your full frontend + backend stack in five worktrees at once, with zero
port conflicts and zero per-branch configuration.

```diff
- localhost:3000        # main ✓ ... feature/auth: EADDRINUSE
- localhost:3001?       # which branch was this again?
+ https://web.myapp.localhost                # main worktree
+ https://web.feature-auth.myapp.localhost   # feature/auth worktree
+ https://api.feature-auth.myapp.localhost   # its backend, same pattern
```

Parallel agent sessions (`claude --worktree`, one branch per agent) all
fight over `localhost:3000`. gw takes ports out of the picture: one proxy
on `:443` routes by hostname, a local CA mints a certificate for any branch
subdomain on demand, and `gw up` starts your services with the right env
everywhere. Each branch gets isolated cookies, its own database, and URLs
that agents can't hardcode wrong.

## Use it with your agent

### Claude Code plugin

This repo is also a Claude Code plugin (and its own marketplace):

```
/plugin marketplace add liu1700/gw
/plugin install gw@gw-marketplace
```

Then, in any repo: `/gw:setup` — Claude installs gw, detects your stack,
writes `gw.toml`, fixes hardcoded `localhost:PORT` references, and brings
your services up on branch URLs.

The plugin ships three things:

- **Skill** — in any repo with a `gw.toml`, Claude starts services through
  `gw up` (never a raw `npm run dev`), reads `GW_URL_*` env vars instead of
  hardcoding ports, and debugs with `gw doctor`.
- **`/gw:setup`** — guided onboarding for any repo.
- **Worktree hooks** — `claude --worktree` sessions get dependencies and
  per-branch databases on create, and teardown on remove.

Non-interactive (scripts, dotfiles):

```bash
claude plugin marketplace add liu1700/gw && claude plugin install gw@gw-marketplace
```

### Day to day: "start the services"

You're working in a worktree and tell Claude:

> start the services for this worktree

With the plugin installed, Claude runs `gw up -d` and replies with this
branch's URLs:

```
gw: services for branch feature/auth up (detached, pid 47210)
  web      → https://web.feature-auth.myapp.localhost
  api      → https://api.feature-auth.myapp.localhost
```

Three properties make this safe to say from any worktree:

- **Detached** — services keep running after the agent session (or your
  terminal) exits; the proxy is started automatically if it isn't up.
- **Scoped** — only this worktree's branch is affected. Other worktrees,
  and whatever is running on your main checkout, are untouched.
- **Idempotent** — asking twice just reprints the URLs.

"stop the services" → `gw down` (again, only this worktree's).
"show me the logs" → `gw logs`.

### Any other agent

Copy this prompt to your agent (it's short — read it before pasting):

```text
Set up gw (https://github.com/liu1700/gw) in this repository so every git
worktree runs its dev stack on branch-specific HTTPS URLs with no port
conflicts:

1. If `gw version` fails, install the prebuilt binary (no Go required):
   curl -fsSL https://raw.githubusercontent.com/liu1700/gw/main/install.sh | sh
   (installs to ~/.local/bin; make sure that's in PATH).
2. Run `gw init` at the repo root. Review the generated gw.toml — it detects
   Next.js/Vite/Nuxt/FastAPI/Flask/Django and fills in dev commands.
3. `gw init` lists hardcoded localhost:PORT references. Replace them with
   the injected env vars: frontend uses NEXT_PUBLIC_GW_URL_API (or
   GW_URL_API server-side); backend CORS allows os.environ["GW_URL_WEB"].
4. Run `gw trust` to trust the local CA — on macOS it uses my login keychain
   (no sudo); on Linux it needs sudo once. Ask me before running it.
5. Run `gw up -d` (it starts the proxy automatically, detaches from your
   session, and prints the URLs) — then report my URLs.
6. Commit gw.toml so every worktree and teammate shares it.

Rules from now on: in this repo, always start dev servers with `gw up -d`
(stop with `gw down`, logs with `gw logs`), never directly; never hardcode
localhost:PORT in code, tests, or docs — use the GW_URL_* env vars; run
`gw doctor` first when a URL doesn't load. If gw itself misbehaves (not my
app), offer to file an issue at https://github.com/liu1700/gw/issues with
`gw version`, OS, and the `gw doctor` / `gw logs` output.
```

## Install

Prebuilt binary (macOS / Linux, no Go required):

```bash
curl -fsSL https://raw.githubusercontent.com/liu1700/gw/main/install.sh | sh
```

Installs to `~/.local/bin` (override with `GW_INSTALL_DIR`). Or download an
archive from the [releases page](https://github.com/liu1700/gw/releases).
With Go 1.22+ you can also `go install github.com/liu1700/gw/cmd/gw@latest`.

## Quick start

```bash
cd your-repo
gw init        # detect stack, write gw.toml, flag hardcoded localhost URLs
gw trust       # one-time: trust the local CA (no sudo on macOS; sudo once on Linux)
gw up -d       # start everything, proxy included → prints your URLs
```

New worktree — no extra ceremony, `gw.toml` is already in git:

```bash
git worktree add ../myapp-auth feature/auth
cd ../myapp-auth && pnpm i && gw up -d
#   web → https://web.feature-auth.myapp.localhost
#   api → https://api.feature-auth.myapp.localhost
```

Both branches are now live side by side: two browser tabs, separate
cookies/localStorage, separate databases if you configured hooks (below).

## How it works

- **Hostname routing.** One proxy on `:443` (fallback `:8443`) maps
  `{service}.{branch-slug}.{domain}` to the right local port via a shared
  route table. It serves HTTP/2 to the browser (with HTTP/1.1 fallback) and
  transparently upgrades WebSocket connections to your service.
- **gw terminates inbound TLS; your service speaks plain HTTP.** The gateway
  owns the certificate for the branch hostname, so your dev server should
  bind **plain HTTP** on `127.0.0.1:$PORT` — drop your own `--ssl`/cert
  flags. gw handles HTTPS at the edge; double-terminating TLS just breaks the
  handshake. (Outbound HTTPS from your app is unaffected — the injected CA
  bundle trusts both gw and the public roots.)
- **Certificates on demand.** `gw trust` creates a local CA (mkcert-style).
  The proxy signs a leaf certificate for whatever server name arrives, so
  any branch subdomain gets a green lock with no wildcard cert to manage.
- **Deterministic ports + env contract.** `gw up` hashes
  `(branch, service)` to a stable port, probes if taken, and injects:

| Variable | Example | Used for |
|---|---|---|
| `PORT`, `HOST` | `24094`, `127.0.0.1` | what your dev server binds |
| `GW_URL` | `https://web.feature-auth.myapp.localhost` | the service's own URL |
| `GW_URL_<SERVICE>` | `GW_URL_API=https://api.feature-auth…` | calling sibling services |
| `NEXT_PUBLIC_GW_URL_<SERVICE>`, `VITE_GW_URL_<SERVICE>` | same value | browser-side code (Next.js / Vite) |
| `GW_PORT_<SERVICE>` | `GW_PORT_WORKER=21387` | direct 127.0.0.1 access (`proxy = "none"` services) |
| `GW_BRANCH`, `GW_SLUG` | `feature/auth`, `feature-auth` | naming, logging |
| `NODE_EXTRA_CA_CERTS` | `~/.gw/ca.pem` | Node trusts the gw CA (added to its roots) |
| `REQUESTS_CA_BUNDLE`, `SSL_CERT_FILE` | `~/.gw/ca-bundle.pem` | Python/OpenSSL trust the gw CA **plus** public roots (combined bundle, so outbound HTTPS still works) |

Plaintext TCP (Postgres, Redis) carries no hostname, so gw isolates those
by *name* instead: one shared Postgres, one database per branch, templated
into `DATABASE_URL` and managed by setup/teardown hooks. TLS-speaking
services *do* carry a name (SNI) — route those with
[`proxy = "passthrough"`](#non-http-services-mtls-grpc-raw-tcp).

## gw.toml

Committed to git — one file, every worktree, every teammate.

```toml
domain = "myapp.localhost"

[services.web]
cmd = "pnpm dev"
dir = "frontend"

[services.api]
cmd = "uv run uvicorn main:app --port $PORT --host 127.0.0.1"
dir = "api"

[env]
DATABASE_URL = "postgres://localhost:5432/myapp_{branch_snake}"

[hooks]
setup = "createdb myapp_{branch_snake} 2>/dev/null; cd api && uv run alembic upgrade head"
teardown = "dropdb myapp_{branch_snake}"
```

Templates: `{branch}` (raw name), `{slug}` (DNS-safe: `feature-auth`),
`{branch_snake}` (`feature_auth`, for database names). `hooks.setup` runs
once per branch **before services start** — bootstrap work (create the
database, run migrations, mint your app's certificates) goes here;
`gw clean` runs teardown (drop the branch database, etc.).

## Non-HTTP services (mTLS, gRPC, raw TCP)

The default mode terminates TLS at the gateway and reverse-proxies HTTP.
That's fatal for a server that authenticates the *client's* certificate —
termination strips it. Two per-service opt-outs fix that:

```toml
[services.dataplane]
cmd = "orlop-server --listen 127.0.0.1:$PORT"
proxy = "passthrough"   # SNI-routed raw TCP splice: gw never terminates TLS,
                        # so mutual TLS works end to end through the gateway

[services.worker]
cmd = "worker --listen 127.0.0.1:$PORT"
proxy = "none"          # no routing at all: gw supervises the process and
                        # isolates its port; reach it at 127.0.0.1:$GW_PORT_WORKER
```

Both are supervised like any other service (`gw up -d` / `gw down` /
`gw logs`, branch-hashed ports), they just aren't HTTP-proxied. A
`passthrough` service presents its **own** certificate — make it valid for
the service's gw hostname (mint it in `hooks.setup`, which runs before
services start; the hook sees `GW_URL_*`/`GW_SLUG`). Clients that dial the
hostname get SNI for free, and their client certificates arrive at your
server untouched.

This is what lets gw host per-branch e2e environments for mTLS storage
servers, gRPC-over-TLS backends, or anything that needs a cert/seed
bootstrap before boot — not just web + HTTP-API stacks.

## Commands

```
gw init      detect your stack, generate gw.toml, flag hardcoded URLs
gw trust     create the local CA and trust it (no sudo on macOS)
gw up -d     start this worktree's services, detached; starts the proxy if
             needed; idempotent (omit -d to run in the foreground)
gw down      stop this worktree's detached services
gw logs      show logs from this worktree's detached services
gw list      show active routes across all branches
gw proxy     run the HTTPS gateway in the foreground (-d detached, stop)
gw doctor    verify the end-to-end path for this branch (CA trust, proxy,
             DNS, each service's HTTPS response); non-zero exit on any failure
gw clean     run teardown hooks for the current branch
```

## Real domains

The default `<repo>.localhost` domain needs no DNS setup — browsers resolve
`.localhost` natively. To use a real domain (production-like hostnames for
OAuth callbacks, secure cookies, domain-pinned frontends):

1. Add a wildcard DNS record: `*.dev.example.com  A  127.0.0.1`
   (public DNS pointing at loopback is fine), or run local dnsmasq with
   `address=/dev.example.com/127.0.0.1`.
2. Set `domain = "dev.example.com"` in `gw.toml`.
3. `gw doctor` verifies the chain end to end.

## Troubleshooting

Run `gw doctor` first — it verifies the whole chain for the current branch
(CA trust, proxy, DNS, each service's real HTTPS response) and prints the
specific cause and fix. The map below is the same reasoning by hand:

- URL not loading → `gw doctor`, then `gw list` to confirm the route exists.
- `502 no route` → the service isn't running; `gw up -d` in that worktree,
  then `gw logs` if it 502s again.
- App loads but "can't reach the backend" → is the backend actually up?
  Check `gw logs`. A service that crashes on boot is reported by `gw up -d`
  (non-zero exit, names the service) and by `gw doctor` — if `gw list` shows
  no route for it, it died; the log has the reason.
- WebSocket stuck on "connecting…" → the WS URL your frontend uses must
  include the **path** (e.g. `wss://api.<branch>.<domain>/ws`, not just the
  host); build it from `GW_URL_API`. A 403 on the upgrade that lacks *your
  app's* response headers is your app's own auth, not gw.
- A CLI tool (curl, wget) can't resolve `*.<domain>.localhost` → browsers
  resolve `.localhost` natively, but some resolvers don't. Add an
  `/etc/hosts` entry (`127.0.0.1  web.feature-auth.myapp.localhost`) or use a
  real domain with a wildcard record (see [Real domains](#real-domains)).
- `508 loop detected` → the app is proxying to its own public hostname;
  point it at the sibling's `GW_URL_*` instead.
- `421 misdirected request` → that hostname is a `proxy = "none"` service
  (connect to `127.0.0.1:$GW_PORT_<SERVICE>` instead), or a `passthrough`
  service reached without SNI.
- TLS/certificate error on a `passthrough` URL → that's your app's own cert,
  not gw's: connections are spliced straight through. Make the service
  present a cert valid for its gw hostname (mint it in `hooks.setup`), and
  point clients at the app's CA.
- Certificate warning → `gw trust` hasn't been run yet. Firefox keeps its
  own store — import `~/.gw/ca.pem` in Settings → Certificates if needed.
- Proxy on `:8443` instead of `:443` (Linux) →
  `sudo setcap cap_net_bind_service=+ep $(which gw)`. macOS binds `:443`
  without root.

## Uninstall

gw touches your system trust store, so removal is documented:

```bash
# macOS (login keychain; add sudo + System.keychain if you used the fallback)
security delete-certificate -c "gw local CA" ~/Library/Keychains/login.keychain-db
# Linux
sudo rm /usr/local/share/ca-certificates/gw-local-ca.crt && sudo update-ca-certificates

rm -rf ~/.gw                    # CA key, route table, logs, pidfiles
rm $(which gw)                  # typically ~/.local/bin/gw
```

## Comparison

- **[portless](https://github.com/vercel-labs/portless)** — same core idea
  (named `.localhost` URLs, worktree-aware), npm-based, frontend-focused.
  gw is a single Go binary and adds the backend half: multi-service repos
  (`gw.toml`), an env contract wiring frontend↔backend per branch, real
  domains beyond `.localhost`, and per-branch database lifecycle hooks.
- **mkcert + Caddy** — you can assemble the same thing by hand; gw is that
  assembly with git-branch awareness built in and one command to run it.
- **docker compose per worktree** — full runtime isolation, but you pay for
  image builds and volume lifecycle to solve what is at heart an addressing
  problem. gw only fixes addressing; compose still works underneath if you
  need it.

## Non-goals

gw does not run your database, manage tmux panes, or build containers. It
does one thing: give every process in every branch a coherent,
branch-scoped set of addresses — and make those addresses work over HTTPS.

## Status

Early (v0.2.x). Works today: init/trust/up (-d)/down/logs/list/proxy/
doctor/clean, detached services with stale-route pruning, TLS-passthrough
and unproxied service modes (mTLS/gRPC/raw TCP), prebuilt binaries
for macOS/Linux, the Claude Code plugin. Roadmap: start the proxy at login
(launchd/systemd units), full TOML via BurntSushi/toml, truststore via
smallstep/truststore, `gw wt <branch>` one-shot worktree bootstrap,
Windows, Homebrew tap.

Found a bug? [Open an issue](https://github.com/liu1700/gw/issues) with
`gw version`, your OS, and `gw doctor` / `gw logs` output.

## License

MIT
