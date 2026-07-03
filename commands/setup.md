---
description: Set up gw (branch-aware local HTTPS gateway) in this repository
---

Set up gw in the current repository so every git worktree can run its full
frontend + backend stack simultaneously over trusted HTTPS, with zero port
conflicts. Follow these steps in order:

1. Check gw is installed (`gw version`). If not, install the prebuilt
   binary (no Go required):
   `curl -fsSL https://raw.githubusercontent.com/liu1700/gw/main/install.sh | sh`
   and verify `gw version` works before continuing (it installs to
   `~/.local/bin` — make sure that's in PATH).
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
5. Run `gw trust` to trust the local CA (no sudo on macOS — it uses the
   login keychain; on Linux it prompts for sudo once). Tell the user before
   running it.
6. Run `gw up -d` (it starts the proxy automatically and detaches from this
   session) and show the user their URLs. If a URL 502s, check `gw logs`.
7. Suggest committing `gw.toml` so every teammate and every worktree shares
   the config.

If gw itself fails at any step (not the user's app), offer to file a bug at
https://github.com/liu1700/gw/issues with `gw version`, the OS, and the
failing command's output.
