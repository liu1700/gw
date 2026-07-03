---
description: Set up gw (branch-aware local HTTPS gateway) in this repository
---

Set up gw in the current repository so every git worktree can run its full
frontend + backend stack simultaneously over trusted HTTPS, with zero port
conflicts. Follow these steps in order:

1. Check gw is installed (`gw version`). If not, install it with
   `go install github.com/liu1700/gw/cmd/gw@latest` (requires Go 1.22+) and
   verify `gw version` works before continuing.
2. Run `gw init` from the repo root. Show the user the generated `gw.toml`
   and confirm the detected services and commands look right; edit the file
   if they correct anything.
3. If `gw init` flagged hardcoded `localhost:PORT` addresses, apply the
   suggested edits: frontend code should read `NEXT_PUBLIC_GW_URL_API` (or
   `GW_URL_API` server-side), backend CORS should read `GW_URL_WEB`. Show
   the user each diff before applying.
4. If the project has a database, offer to configure per-branch isolation:
   uncomment the `[env]` and `[hooks]` sections in `gw.toml` and adapt
   `DATABASE_URL` and the createdb/migrate commands to their stack.
5. Run `gw trust` (warn the user it will prompt for sudo once, to install
   the local CA into the system trust store).
6. Start the proxy if it isn't running (`gw doctor` will tell you), then run
   `gw up` and show the user their URLs.
7. Suggest committing `gw.toml` so every teammate and every worktree shares
   the config.
