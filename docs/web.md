# `terva web` — the browser control panel

`terva web` serves a browser UI for a self-hosted terva: chat with full
tool-call fidelity, switch and nickname sessions, switch models, and watch
usage — from any device that can reach the page. It is the TUI's reach, over
the wire, plus a control panel.

It is an **opt-in build** (build tag `terva_web`), excluded from the `min`
binary exactly like the `terva acp` mode and the chat connectors. The full
install one-liner and `just install` include it.

The protocol it speaks is the `ctrlproto` control plane —
[docs/controllers.md](controllers.md) is the reference. The design record
(`docs/proposals/terva-web.md`), the protocol rationale
(`docs/proposals/control-plane-protocol.md`), and the platform horizon
(`docs/proposals/terva-platform.md`) live in the development repository, not
the public release tree.

## Running it

```bash
just install            # full build (includes terva_web)
terva web               # serves http://127.0.0.1:8730 (loopback, no auth)
```

Open the address in a browser. On mobile, use *Add to Home Screen* — it is an
installable PWA.

Flags:

| Flag | Default | Meaning |
|---|---|---|
| `--web-addr` | `127.0.0.1:8730` | listen address |
| `--web-token` | — | require `Authorization: Bearer <token>` (or `?token=` on the socket) |
| `--web-auth-header` | — | trust this forward-auth header as the authenticated user (only from loopback / a trusted proxy — see [Auth](#auth)) |
| `--web-trusted-proxy` | — | IP/CIDR(s) allowed to assert `--web-auth-header` (comma-separated; loopback is always allowed) |
| `--web-insecure` | off | permit a non-loopback bind with **no** auth mode (dangerous — open to any source) |
| `--web-insecure-cidr` | — | grant **no**-auth access to these source IP/CIDR(s) only (comma-separated; loopback always allowed) — the scoped, safer form of `--web-insecure` for a trusted overlay network (see [Auth](#auth)) |
| `--allow-restart` | off | enable Tier-1 self-restart (see [Self-restart](#self-restart)); `--web-allow-restart` is the accepted older spelling |

Standard flags apply too: `--cwd` pins the workspace, `--model` / `--provider`
pick the default, `--yolo` runs without approval prompts, `--jail` / `--no-jail`
set the sandbox default.

> **Bind address.** `--web-addr 0.0.0.0:8730` binds all IPv4 interfaces — the
> reliable form for reaching the panel over an IPv4 overlay (tailscale, most
> LANs). A bare `--web-addr :8730` binds the IPv6 wildcard (`[::]`), which is
> dual-stack on a typical host but will NOT accept IPv4 on a host configured
> `net.inet6.ip6.v6only=1`; prefer `0.0.0.0:PORT` unless you specifically want
> IPv6. Either way a non-loopback bind needs an auth mode or `--web-insecure-cidr`.

## Auth

terva's identity is single-user, so the auth here is a **gate** ("keep
strangers out"), not an identity system. It fails closed: binding a non-loopback
address with no auth mode is refused unless you pass `--web-insecure`.

- **Front it (recommended).** Bind loopback and put a reverse proxy that does
  real auth in front — the author runs [Authentik](https://goauthentik.io/)
  forward-auth. Point terva at the header the proxy sets:

  ```bash
  terva web --web-addr 127.0.0.1:8730 --web-auth-header X-Forwarded-User
  ```

  The forward-auth header is a **proxy assertion**, so terva honors it **only
  from a peer it can identify**: a loopback connection (the proxy runs on the
  same host — the shape above) or an IP inside a `--web-trusted-proxy` CIDR.
  Otherwise the header is forgeable by anyone who can reach the port directly.
  For that reason a **non-loopback bind under header auth requires
  `--web-trusted-proxy`** (naming the proxy's address) and is refused without it:

  ```bash
  # remote proxy fronting a non-loopback bind → name its network
  terva web --web-addr 0.0.0.0:8730 --web-auth-header X-Forwarded-User \
            --web-trusted-proxy 10.0.0.0/24
  ```

- **Bearer token (proxy-less).** For quick remote access over Tailscale/WireGuard:

  ```bash
  terva web --web-addr 0.0.0.0:8730 --web-token "$(openssl rand -hex 24)"
  ```

  The browser can't set request headers on a WebSocket handshake, so the panel
  passes the token as a `?token=` query parameter on the socket URL; the same
  token also works as `Authorization: Bearer` for non-browser clients. A token in
  a URL can leak into a fronting proxy's access logs, browser history, and the
  `Referer` header (terva's own logs never record it — auth failures log the path
  only). For a hardened setup prefer forward-auth (no token in the URL) or a
  native client sending the `Authorization` header.

- **Trusted network, no per-request auth.** To expose the panel over a private
  overlay (Tailscale/WireGuard/VPN) and let the *network* be the boundary — no
  token, no proxy — scope no-auth access to the overlay's source range with
  `--web-insecure-cidr` instead of the blanket `--web-insecure`:

  ```bash
  # reachable only from tailnet peers (100.64.0.0/10); everyone else gets 403
  terva web --web-addr 0.0.0.0:8730 --web-insecure-cidr 100.64.0.0/10
  ```

  Requests are admitted only when the **source IP** is loopback or inside a named
  range; the `Host` check is relaxed for those peers so reaching the panel by its
  real overlay IP/name works, and [self-restart](#self-restart) is permitted
  (the range bounds who can trigger it). This is strictly narrower than
  `--web-insecure`, which admits *any* source. There is still **no per-user auth
  inside the range** — anyone who can source-spoof into it, or any device on the
  overlay, has full owner access — so use it only where you trust the network;
  layer a token or forward-auth on top for anything shared.

**DNS-rebinding defense.** In no-auth mode terva also requires the request's
`Host` to be a loopback name, so a malicious web page can't rebind its own
hostname to `127.0.0.1` and drive your local panel through your browser.
Authenticated modes don't restrict `Host` (proxy hostnames vary); a
`--web-insecure-cidr` peer inside the range is likewise unrestricted (a loopback
*source* still gets the check, so your local browser stays protected).

> **The auth gate is the whole application-layer boundary.** Once reachable, this
> endpoint can run `bash` as you. Prefer Tailscale/WireGuard + forward-auth over
> raw public exposure; the VM perimeter is the real isolation, and the in-process
> jail is a guardrail, not a boundary.

## Deployment

The intended shape is a dedicated LXC/VM running `terva web` as a systemd
service, pinned to one project directory:

```ini
# /etc/systemd/system/terva-web.service
[Service]
ExecStart=/usr/local/bin/terva web --web-addr 127.0.0.1:8730 --web-auth-header X-Forwarded-User
WorkingDirectory=/srv/workspace
Restart=always
```

Sessions persist under `$TERVA_HOME`, so a restart (deploy, crash, reboot)
loses no history. The daemon defaults its working directory to the pinned
project; the panel can flip the jail off so tools can reach beyond it when a
task needs to.

### Unix socket

`--web-addr unix:/path/to/terva.sock` serves the same HTTP + WebSocket stack
on a filesystem socket instead of TCP. The socket is created `0600` and the
file's permissions **are** the auth boundary — no token dance for a same-user
client (a `--web-token`, if set, is still enforced on top; the IP-based
options are meaningless here). Browsers can't dial filesystem sockets, so
this form is for `terva attach unix:/path/to/terva.sock` and programmatic
ctrlproto clients; a stale socket left by a crash is cleared on the next
start, and a live daemon's socket is refused rather than stolen.

### systemd socket activation

When `LISTEN_FDS` names the process (a `.socket` unit started the service),
the passed socket — unix or TCP, whichever the unit declares — is served and
`--web-addr` is ignored. The daemon starts on the first connection rather
than at boot, and Tier-1 self-restart re-adopts the inherited socket across
the exec (the pid is unchanged, so `LISTEN_PID` stays valid):

```ini
# ~/.config/systemd/user/terva-web.socket
[Socket]
ListenStream=%t/terva.sock
SocketMode=0600

[Install]
WantedBy=sockets.target
```

```ini
# ~/.config/systemd/user/terva-web.service
[Service]
ExecStart=/usr/local/bin/terva web --allow-restart
WorkingDirectory=%h/workspace
```

`systemctl --user enable --now terva-web.socket`, then
`terva attach unix:/run/user/1000/terva.sock` — the first attach spawns the
daemon.

## Self-restart

With `--allow-restart` (also accepted as `--web-allow-restart`, its original
web-only spelling), `terva web` can restart itself into the
currently-installed binary — no external supervisor — to pick up a new build.
It is a Tier-1 restart: it re-execs the same executable (via `exec(2)`) with the
original arguments and environment, so the PID is preserved and the process
image is replaced in place. Because install is atomic (`go install` /
`just install-dev` rename over the same path), the next restart runs the new
code.

It is **off by default** and refused outright on a *blanket* insecure listener —
if you pass `--web-insecure` (open to any source) with no `--web-token` /
`--web-auth-header`, restart stays disabled (a stranger must never be able to
re-exec the daemon). A `--web-insecure-cidr` listener bounds who can reach it to
a trusted source range, so restart **is** permitted there. It is unix-only
(`exec(2)`); running from a `go run` temp binary is rejected with a clear error.

Two ways to trigger it, both funneling through the same path:

- **From the browser** — a **Restart terva** control appears in the **Settings**
  pane (arm, then confirm). It calls the `control.restart` ctrlproto method.
- **From the agent** — a `terva_restart` tool. It is registered only when the
  flag is set and **prompts for your approval** in the browser before running in
  every gating approval mode (it is left unclassified in the permission model, so
  the default `workspace`/`ask`/`auto-edit` modes all confirm it). This is the
  loop where terva edits its own code, reinstalls, and relaunches on the new
  build with a human in the middle.

  > **Caveat: `--yolo` skips this prompt too.** yolo bypasses the approval gate
  > for *every* tool, so with self-restart enabled **and** `--yolo`, the agent can
  > re-exec the daemon without confirmation. That was a deliberate v1 choice
  > (yolo means "run freely"); combine the two only when that's acceptable.

On restart the daemon broadcasts a "terva vX is restarting — reconnecting
shortly" notice (naming the outgoing build), then replaces the image after a
brief flush delay. The version is visible on both sides of the hop: stderr
logs the running build with the restart request and the new image logs
`self-restart complete — was vX, now vY` on boot (the prior version rides the
exec env), while in the browser the Settings pane shows the build serving the
panel and a toast announces the version change once the client reconnects to a
different build. Sessions persist to disk per-message, and the PWA
auto-reconnects and restores from the on-disk snapshot, so no history is lost —
but an **in-flight turn is interrupted** (Tier 1 does not preserve active tool
calls). Prefer restarting while idle.

## Attaching the TUI

`terva attach [URL]` connects the interactive TUI to a running `terva web`
daemon as a second client — same sessions, same live stream as the browser
panel, with the PWA's reconnect/resync discipline. See docs/tui.md
§"Attaching to a running daemon".

## Building the client

The panel is a Preact + Vite PWA under `packages/agent/web/client/`. Its build
output (`client/dist/`) is **committed** and embedded via `go:embed`, so
`go build -tags terva_web` and the release pipeline need no Node.js. After
changing anything under `client/src`, rebuild and commit:

```bash
just web-build     # npm ci + vite build -> client/dist (commit the result)
```

The client source is organized by dependency direction:

- `src/platform/` contains Preact-free protocol, conversation-state, and
  image-policy modules;
- `src/features/` contains reusable Preact features and feature-local behavior
  such as conversation attachments, interactions, sessions, and model UI;
- `src/ui/` contains small shared presentation primitives plus browser and
  formatting helpers; and
- `src/app.tsx` remains the control-panel composition root while its product
  surfaces and orchestration are split incrementally.

Keep transport calls in the composition/controller layer and pass typed data and
callbacks into visual components. Run `just web-test`, the client `typecheck` and
`i18n-check` scripts, and `just web-build` after source changes.

## Languages

The panel follows terva's operator language (the `TERVA_LANG` env var — the
legacy `ZOT_LANG` spelling still works <!-- rename:keep --> — else config
`language`; the OS `LANG` is *not* consulted. See
[docs/localization.md](localization.md)), and you can change it from **Settings →
Language**: it switches the daemon's active language live, saves it as the
default, and broadcasts to every open tab (each re-fetches its catalog + panes
and re-renders — server-rendered titles/labels included). It's a single active
language for the daemon, not a per-browser preference; already-generated
conversation text and a running session's baked system prompt stay as they were —
new sessions and freshly rendered UI pick up the change.

Two halves cooperate:

- **Server-originated text** (surface/pane titles, settings labels and options)
  is localized server-side with `i18n.T` before it reaches the wire, to the
  daemon's active language. Conversation content is already localized by the core
  agent. The client renders all of this verbatim.
- **Client-owned chrome** (buttons, placeholders, tooltips, empty states) is
  translated in the browser through a small English-as-key `t()` / `tn()` layer
  (`client/src/i18n.ts`, mirroring the Go `i18n` package — English source strings
  are the keys, plurals via `Intl.PluralRules`; English is the implicit fallback).
  The daemon advertises its locale in the ctrlproto hello (`Hello.locale`); the
  PWA shows its bundled catalog immediately, then fetches the daemon's **effective**
  catalog (`i18n.catalog`) and overlays it, so operator edits win.

### Translating the web panel

The web strings are a first-class catalog in the `terva locale` workflow — the
same one used for the TUI and CLI (see [docs/localization.md](localization.md)).
They live in their own `web/` file, English-as-key like the main UI catalog:

```
terva locale init <lang>      # seeds web/<lang>.json alongside the rest
# … edit $TERVA_HOME/locales/web/<lang>.json …
# reload the browser → the daemon re-reads the overlay and serves it; the panel
# re-renders in your edits (server-rendered titles refresh too, via re-Configure)
terva locale export <lang>    # writes web/<lang>.export.json to PR back
```

`terva locale list` / `diff` report web coverage (`[web done/total]`); `validate`
checks a `web/<lang>.json` against the web reference. Because the client fetches
the effective (embedded ⊕ `$TERVA_HOME/locales/web` overlay) catalog on every
connect, a browser reload is the whole check loop — no daemon restart.

Shipped translations live in `packages/i18n/locales/web/<lang>.json`; the client
bundle is a build-time mirror (regenerated by `just web-build`) for offline /
first-paint. The client's English reference (`web/en.json`) is extracted from the
`t()`/`tn()` calls by `scripts/i18n-extract.mjs`, the client-side twin of
`cmd/terva-i18n-lint`. Finnish ships as the reference translation.

## Session titles

A new session is named from the first line of your opening message as soon as
the first turn finishes, and the name is pushed to every open tab live (no page
refresh). To have terva write a short, specific title with a one-shot model call
instead, set it in `config.json` (off by default so no extra tokens are spent):

```json
{
  "auto_title": true,
  "auto_title_model": "anthropic/claude-haiku-4-5-20251001"
}
```

`auto_title_model` is optional — leave it out to title with the session's own
model. Either way you can always rename a session by hand; a manual name is
never overwritten by the automatic pass.

You can also generate a title **on demand**: the ✨ button next to rename in
the session drawer (or `g` in the TUI's `/sessions` picker) runs one bounded
model call over the conversation — the latest compaction summary when one
exists, plus the most recent exchanges — and works regardless of the
`auto_title` setting, including as backfill for old untitled sessions. An
explicit generate does replace whatever name is there: you asked for it.
Long sessions get better titles this way than from their opening line.

With `auto_title` on, titles also **refresh automatically after each
compaction** — the moment a session has provably outgrown the name its
opening earned. Only machine-generated titles refresh; a session you named
by hand keeps its name (title provenance is tracked in the transcript, so
this holds across restarts too).

## Panes

Auxiliary views live in a **pane host** — a collapsible right rail on desktop, a
full sheet on mobile — with a switcher across the top. Toggle it with the ⊞
button in the top bar; a tab lists each available pane (see
docs/proposals/web-surfaces.md). Today's panes:

- **Usage** (below) — one pane: the live usage picture (gauge, cost,
  subscription windows) *and* the context-size breakdown that explains where the
  usage goes. It refreshes in place as turns complete, no tab-switching needed.
- **Tasks** — the background-agent (swarm) dashboard: one row per agent with a
  status badge, live activity, an expandable transcript tail, and stop / resume /
  remove actions. It's workspace-global (shows every agent, including ones
  detached from a prior run) and appears when auto-swarm is on or any agents
  exist. Live-updated by a poller (the swarm has no push). The agent can spawn
  tasks via `swarm_spawn` when auto-swarm is enabled in `/settings`.
- **Raati** — the deliberation board (`/raati`,
  docs/raati.md): convene a three-unit panel on a
  question and watch it deliberate live — blind round, cross-examination,
  then the verdict landing as kanji before settling into your language, with
  the tally and the minority report. Workspace-global; the convene form picks
  a convening profile (built-ins: `triage`, `counsel`, `code-review`,
  `ethics`), the decision class and the rigor level (0 *kaiku*: the workspace binding;
  1 *kuoro*: the provider's tier ladder; 2 *käräjät*: cross-provider seats
  from the user config's `raati.level2`); each block shows its seat's
  binding. The record persists under `$TERVA_HOME/raati/`. Pushed live (the
  coordinator has a real event feed — no poller). With `raati.convene_tool`
  enabled, the **agent** can convene a panel too (`raati_convene`, always
  behind the approval gate) — its deliberation renders on this same board.
- **Settings** — every setting the daemon exposes, rendered from one
  server-side surface (`workspace_settings.go`) that the TUI's `/settings`
  renders too, so the two never drift. Each row states when it bites: **approval
  mode** (per-session, live, not saved — a security posture); **thinking**
  effort, **auto-condense** (steps / turns / off) and **language** (all live, and
  saved as the default — language switches every open tab, see Languages below);
  **auto-title**, **temperature**, **theme**, **inline images**, **lazy tool
  loading** (advertise core tools first, let the agent pull tool groups in with
  `activate_tools`), **recursive file search**, **respect .gitignore**, **lore**
  (keyed context), **swarm worktrees**, and the first-run **core tool pack**
  offer; plus **auto-swarm** as two nested toggles: *background sub-agents* (the
  `swarm_spawn` tool) and, under it, the *proactive-delegation nudge* (default on
  — off keeps the tool but drops the system-prompt push). The ones marked
  *applies to new sessions* (the swarm toggles, lazy tools, lore, …) are baked at
  session construction. Config writes are concurrency-safe.
- **Commands** — every slash command an extension registered, as clickable
  buttons grouped by extension. The web has no command line, so a command is a
  button, not a `/name` you type (the TUI keeps the slash prompt). Running one
  applies its response the same way the TUI does: it can open a panel, submit a
  prompt to the model, or post a one-shot note back into the conversation
  (`display`/`error`; `insert` degrades to a note since there's no shared
  composer to fill). The pane appears whenever any loaded extension has
  commands. User-driven slash commands such as `/skill` are a separate composer
  autocomplete surface; this pane contains only the extension-provided set.
- **Extensions** — a management pane listing the session's installed + loaded
  extensions with a health rollup: a status badge (running / stopped / disabled /
  gated), version, scope, language, tool + command counts, and — for one that
  failed — the tail of its log as the reason. It mirrors the TUI's extensions
  dialog and appears when any extension is loaded. Each has an **enable/disable
  toggle**: it persists to the project config and applies **live** — the
  subprocess starts/stops and the agent's model-facing tool set is rebuilt on the
  spot (so a toggled-on extension's tools reach the model and a toggled-off one's
  stop lingering), no session restart. Gated (untrusted-project) extensions show
  no toggle; an extension disabled in its own manifest stays off until re-enabled
  there.
- **Lore** — the authored keyword-triggered context ([lore](localization.md))
  inspector + editor: each entry shows its name, trigger keywords (or an *always*
  badge for constant entries), source, and content. You can **create, edit, and
  delete user lore** from here — the form (name + comma-separated keywords, or
  "always active" + content + **scope**) writes a `<slug>.md` file to user
  (`$TERVA_HOME/lore`) or, in a **trusted** workspace, project (`.terva/lore`)
  scope (validated through the real parser; project scope is offered only when
  trusted, since project lore is trust-gated on load). Edits appear in the pane
  immediately. **Keyword-
  triggered** entries also take effect **live** — the running session re-wires its
  per-turn lore, so the next turn sees the edit. **Constant ("always active")**
  entries are baked into the system prompt at build, so those apply to **new
  sessions** (kept that way on purpose, so an edit doesn't reset the prompt
  cache). Only web-managed user entries are editable — entries from extension
  bundles, character cards, or other tiers are read-only.
- **Chat** — the chat-bridge manager (the TUI's `/connect`). Lists every
  registered chat service — the compiled-in telegram and discord connectors plus
  any connector extensions (tagged `extension`, and `dev` where applicable) —
  each either **not configured** (with a pointer to `terva bot setup`) or a
  **connect** / **connect & pair** button. Once connected it shows the connector,
  the bot's `@username`, the paired user (or *awaiting `/start` from your phone*),
  and which session is being mirrored, with **disconnect** and **mirror this
  session** actions — the bridge is bound to one session and does *not* follow
  whichever session a tab happens to be showing, so rebinding is explicit.
  Workspace-global, like the bridge itself. A running `terva bot` daemon already
  polling the service blocks connecting (both consumers would race each update
  and one always loses); the pane says so and names the pid. The pane is offered
  whenever any chat service is registered, so it can explain "not configured"
  rather than silently going missing. See [connectors.md](connectors.md).
- **MCP** — the Model Context Protocol server manager. `terva web` now starts the
  configured MCP servers (user `config.json` + a trusted project's) once for the
  daemon and merges their tools into every session, so MCP tools are usable in
  the browser. The pane lists each server with a status badge (running / stopped
  / disabled / gated / failed), scope, tool count, and any startup error, and an
  enable/disable toggle. Because the servers are shared across sessions, toggling
  one restarts/stops it and rebuilds **every** open session's tool set live.
- **Permissions** — the approval inspector, headed by the **Workspace trust**
  state with a **Trust workspace** / **Untrust** control. Trust is workspace-
  global (keyed on the cwd) and gates whether project-scoped content loads at all
  — project extensions, project lore, project permission rules — so granting it
  here is what unlocks the *project* scope in the other panes. Trusting brings
  that content **live** across every open session (project extensions reload,
  tool sets rebuild, project lore becomes visible/editable); project skills and
  context baked into the system prompt take effect on the next session. Untrust
  tears it back down. Backed by the ctrlproto control verbs `control.trust` /
  `control.untrust`; the state rides on `SessionInfo.trusted`. Below trust: the
  session's approval mode (its
  setter lives in Settings), the compiled **rules** (tool → allow / deny / ask,
  with source), and the session's live **"always-allow" grants** — the decisions
  you accrued by answering "always allow" in approval prompts. Each grant has a
  **Revoke** button (and *Revoke all* clears a blanket allow-all). You can also
  **add and remove rules**: an add form (tool name or `mcp_*` glob + allow/deny/
  ask + optional args regex + **scope**) writes to your **user** `config.json` or
  the **project** `.terva/config.json`, and user/project rules get a **×** to
  delete. Edits persist and apply **live** — the rule takes effect on the next
  tool call across every open session (a deny rule blocks immediately). Project
  rules are restrict-only: they can't grant **allow** (the self-approval ban), so
  the form drops that option for project scope. Extension/builtin rules aren't
  config and stay read-only.
- **Extension panels** — an extension that opens a panel appears as a live pane
  and disappears when it closes; a **Status** pane aggregates extension status
  segments. A panel can be plain text lines *or* a rich **widget tree**: an
  extension sends a semantic vocabulary (heading, text, meter, keyvalue, table,
  list, group, note, action, divider) via the SDK's `OpenPanelWidgets` /
  `RenderPanelWidgets`, and the panel renders it natively — meters as bars,
  actions as buttons that call back to the extension — no per-extension client
  code. The extension also sends text `Lines` as the TUI fallback. A panel can
  be opened spontaneously (on a session event) or by running the owning
  extension's command from the **Commands** pane.

## Usage

The top bar shows a live **context meter** — the last turn's real input+cache
tokens against the model's window, as a small bar + percent that turns amber
past 70% and red past 85% (the TUI status bar's `ctx` gauge). Click it (or the
cost number, or the **Context breakdown** button in the ⓘ popover) to open the
single **Usage** pane. Everything below lives on that one pane — the size
breakdown is really just a clarification of where the context usage goes, so the
once-separate usage and context views are merged. The pane refreshes in place as
turns complete:

- **Provider / model** — the active provider and model backing the session.
- **Context gauge** — real last-turn tokens vs the window (falls back to a byte
  estimate before the first turn).
- **Cumulative usage** — session input/output/cache tokens and cost, with a
  `(sub)` marker for OAuth/subscription credentials.
- **Subscription windows** — for providers that report them (e.g. Codex's 5h +
  weekly), each as a meter with a reset countdown (the status bar's usage
  meters; populated once the provider returns usage, i.e. after a turn).
- **Next-request size breakdown** — system prompt (with the extension-guidance
  share broken out), tool definitions, ephemeral extension context, and the
  transcript per message (largest flagged). terva has no tokenizer, so these
  are bytes with ~bytes/4 token estimates — enough to finger a bloat source.

It mirrors the TUI's `/context` plus the status-bar usage meters.

## Tool calls

The tool-display button in the top bar cycles four levels (the choice is
remembered): **box** (full name/args/result), **grouped** (a run of tool calls
between replies collapses to a single "N tool calls" line — with a summary of
which tools and any failures — that expands to the full boxes on click),
**minimal** (one greyed line per call), and **hidden**. Grouped is the answer to
a dozen tools burying a reply; the TUI has the same four levels on its `ctrl+t`
cycle, and both sides summarize a run identically (one shared format, pinned by
golden fixtures).

## Slash commands

Type `/` in the composer for an autocomplete menu of **user-driven** commands —
operator chrome, distinct from the extension **Commands** pane (which is
buttons). Arrow keys navigate, Tab/Enter selects (a command with no argument runs
immediately; one that takes an argument primes `/name ` and keeps focus), Esc
dismisses. A message that merely starts with `/` but isn't a known command still
sends as a normal prompt.

| Command | Does |
|---|---|
| `/compact` | Summarize + replace the transcript to reclaim context (the TUI's `/compact`; the daemon otherwise auto-compacts near the window limit). Refused mid-turn; a already-minimal transcript reports a benign note. |
| `/clear` | Wipe the transcript with **no** summary — start over in the same session. Unlike compact it keeps nothing; the durable session file gets an empty checkpoint (old rows stay for audit). Refused mid-turn. |
| `/skill <name> [task]` | Prime the model to load a skill — it rewrites to *Use the "name" skill for: task* and sends it, so the model calls the `skill` tool. After `/skill ` the menu autocompletes skill names. |
| `/model [id]` | Switch to a model by id, or open the model picker. |
| `/context` | Open the Usage pane. |
| `/raati` | Open the deliberation board. |
| `/new` | Start a new session. |
| `/help` | List the commands. |

Only the daily-driver subset is exposed; TUI-only chrome (`/jail`, `/paste`, …)
and things with dedicated web UI (settings, sessions) are not. `/compact` and
`/clear` are each backed by a ctrlproto conversation method (`compact` / `clear`);
the rest are existing methods or client-side transforms.

## @-file mentions

Type `@` (at the start of the message or after a space) for workspace-file
completion — the daemon lists its own tree over ctrlproto (`files.list`,
gitignore-filtered, advertised as the `files-list` hello feature; on an older
daemon the stage simply doesn't appear). Matching is substring-then-
subsequence over the whole relative path; selecting a file inserts the path,
selecting a directory keeps the token live and narrows into it. The listing
is fetched lazily on the first `@` and re-fetched on a 30-second TTL, so
keystrokes never ride the wire.

**Tab** shell-completes the token in place — segment-wise prefix completion
to the unique candidate (a directory gains `/` and stays live) or the
longest common prefix, bash dot-name rules included. Tab never commits;
Enter applies the highlighted row. The TUI's `@`-picker Tab runs the same
semantics — both implementations are pinned to one shared golden-fixture
file, so they cannot drift.

## Queued messages

Sending while a turn is running queues the message (shown as a dashed bubble).
Before the agent consumes it you can **edit** it in place (✎) or **remove** it
(×); the change is pushed to the queue and broadcast to every open tab.

## Scope

v1 is the daily-driver: chat, session switching + nicknames, model switching
(searchable picker with per-model favorites), a context breakdown, usage, and
the PWA. Extensions load per session (like ACP), so lore, hooks, tool
approvals, and extension tools all work. Management surfaces have since landed
on top of that — lore editing and extension enable/disable both ship (see the
Lore and Extensions panes above), as do MCP, permissions, and the chat bridge.
Still deferred (further `ctrlproto` control group): prompt overrides and
templates.
