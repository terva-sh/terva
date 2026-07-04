# Proposal — `terva web`: a control panel + conversation surface for a self-hosted terva

- **Status:** IN PROGRESS. The v1 backend + client have landed behind the
  `terva_web` build tag — the conversation + session method groups over a
  WebSocket, the in-process `Workspace`, and the Preact PWA. See
  [docs/web.md](../web.md) for how to run it. Still open: the control-group
  surfaces (lore / extensions / prompt overrides / templates) and loading
  installed extensions into web-mode agents.
- **Date:** 2026-07-03
- **Scope:** a new `packages/agent/modes/web/` package (sibling to
  `modes/interactive` and `packages/agent/acp`), a `terva web` subcommand gated
  behind build tag `terva_web` (excluded from `min`, exactly like the built-in
  connectors), an embedded PWA served via `go:embed`, and a small amount of
  `args.go`/`build.go`/config wiring for the daemon. **No core engine changes
  required for v1** — this rides the existing agent-event seam.
- **Origin:** the six-agent feasibility review (2026-07-03) established that the
  core engine is cleanly frontend-agnostic and multi-session-safe — three
  independent frontends (the TUI, the chat/connector stack, and ACP) already
  drive it through one small seam. This specs the fourth frontend: a browser
  **control panel** for a single-user self-hosted terva, replacing the author's
  OpenClaw deployment.
- **Prereq reading:** docs/proposals/control-plane-protocol.md (ctrlproto — the
  protocol this mode is the first consumer of); docs/proposals/terva-platform.md
  (the multi-user horizon this is the first rung toward).

## TL;DR

`terva web` is **the TUI's reach, over the wire, plus a control panel** — a rich
browser UI for one person running one workspace of terva on a box they own,
reachable from anywhere they can hit the page. The thesis is in the phrase
*"view and manage terva without system access"*: chat is table stakes; the point
is being able to switch sessions and models, and later edit lore, toggle
extensions, override the prompt, and swap templates, **without SSHing in**.

It drives the engine at the agent-event seam
(`Resolve → NewAgent → PromptWithPolicy(sink)` + `Confirmer`/`Asker`), **not**
through the chat connector path (which is chat-shaped and discards tool-call
fidelity). It speaks **ctrlproto** over a WebSocket. v1 is a tight, shippable
OpenClaw replacement — persistent chat, session switching with nicknames, model
switching, a PWA usable on mobile, and usage review — and every one of those
maps onto a primitive that already exists. The control-plane-heavy surfaces are
deliberately v2+.

## Why now

The author runs a self-hosted AI web assistant (OpenClaw) on a dedicated
LXC/VM and wants to replace it with terva, so that the constant companion that
remembers work, keeps them on task, and helps get things done *is* terva —
reachable from any device, not just a terminal. The feasibility review showed
the engine is ready; the only thing missing is a frontend that renders in a
browser and a protocol to drive it. This proposal is the frontend; ctrlproto is
the protocol.

Framing it as a **control panel** (not "web chat") is the decision that shapes
everything downstream: the web client is a first-class peer to the TUI, so it
should drive the same rich event stream and eventually the same management
surface — which is why the protocol work (ctrlproto) is split into its own
proposal and designed to outlive this mode.

## Non-goals (v1)

- **Not multi-user / multi-tenant.** One user, one workspace, one daemon. The
  multi-tenant story lives in docs/proposals/terva-platform.md and is
  explicitly deferred — but the protocol underneath is designed not to foreclose
  it (addressing + capability negotiation in the envelope from day one).
- **Not a code-execution sandbox for untrusted users.** The security boundary is
  the VM perimeter + the auth gate, not the in-process jail (see Security).
- **Not the full control plane.** Lore CRUD, extension management, prompt
  overrides, and templates are v2+. v1 is chat + sessions + models + PWA.
- **Not an OIDC/identity provider.** Auth is fronted by a reverse proxy
  (Authentik), not built into the binary (see Auth).

## Deployment model

The target deployment is the one the author will actually run first, and the
design is tuned to it:

- **A dedicated LXC/VM**, `terva web` run as a **systemd service**, pinned to a
  single project/workspace directory (`--cwd` / the service's `WorkingDirectory`).
- **Project-pinned, but tools can reach outside.** The daemon defaults its
  working directory to the pinned project; `/jail` and `/unjail` are exposed in
  the UI so terva can reach beyond the project dir when a task needs it. Sessions
  are keyed by the daemon's launch cwd (`core.SessionsDir` / `CWDHash`), so
  wandering tools do not fragment the session namespace — one project = one clean
  session list.
- **The jail is a guardrail, not the boundary.** The in-process sandbox is
  documented in-tree as "a speed bump for the model, not a security boundary"
  (bash can escape it; it inherits the process environment). On a dedicated VM
  that is exactly the right role for it — a courtesy rail you can flip off from
  the panel — while the VM perimeter and the auth gate do the real work.
- **Durable, lossless restart.** Because it replaces an always-up service,
  sessions must persist so a daemon restart (deploy, crash, reboot) loses no
  history. The engine already decouples persistence behind `On*` callbacks
  (`OnMessageAppended`/`OnUsage`/`OnTranscriptCompacted`); v1 wires them to the
  existing session JSONL under `$TERVA_HOME`. Graceful shutdown drains in-flight
  turns.

## Architecture

`modes/web` is a **sibling to `modes/interactive` and `acp`**, not a connector.
This is the load-bearing architectural choice:

- **Drive the agent-event seam directly.** Construct one `core.Agent` per session
  via `Resolved.NewAgent()`, run turns with `agent.PromptWithPolicy(ctx, text,
  images, sink)`, and implement `core.Confirmer` (tool approval) and `core.Asker`
  (mid-turn questions) against the socket — parking the turn on a channel resolved
  by a client message, exactly as `acp/permission.go`'s confirmer does today.
- **Do not route through `chat.Loop`/connproto.** That path is chat-shaped: it
  drops tool-call events on the floor and buffers replies to send only at
  turn-end. A control panel wants the opposite — full tool-call/diff/cost
  fidelity, streamed. The connector stack is the wrong altitude for this
  frontend.
- **ACP is the template.** `acp/agent.go` (a per-session `AgentFactory`),
  `acp/translate.go` (AgentEvent → client notifications), and
  `acp/permission.go` (channel-parked approval) are a near-complete blueprint;
  `terva web` is that shape with a WebSocket carrier and ctrlproto framing instead
  of JSON-RPC-over-stdio.
- **Multi-device from day one.** Even single-user, the panel will be open on a
  desktop, a phone, and a pinned tab simultaneously. The `core.Agent` is
  single-flight (`ErrBusy` on concurrent `Prompt`), so the daemon owns **one
  agent per session** and **broadcasts its event stream (pub/sub) to N connected
  clients**; input from any client is enqueued (`QueueMessage` already exists).
  Building request/response first would force a rearchitecture — so the fan-out
  is in the v1 design.
- **It consumes ctrlproto.** All of the above is expressed as the conversation +
  session method groups of the control-plane protocol; `modes/web` is a carrier
  binding (WebSocket) of that interface. See the protocol proposal.

## Build & packaging

- **Build tag `terva_web`**, excluded from `min` — same optionality model as the
  Discord/Telegram connectors and `terva_acp`. The `min` binary stays small; the
  full build embeds the panel.
- **PWA embedded via `go:embed`** so the binary is self-contained with zero
  runtime external dependencies (terva already uses `go:embed` for the builtin
  persona crew). The SPA is built once at release time (platform-independent) and
  embedded; goreleaser gets a web-asset build step for the tagged artifacts only.
- **`terva web`** subcommand: `--addr` (default `127.0.0.1:PORT`), `--cwd`
  (pinned workspace), auth flags (below), jail default.

## v1 scope — and where the work actually is

The OpenClaw must-match list, and the encouraging finding that each item is
mostly assembly over an existing primitive:

| Feature | Existing primitive it rides | New work |
|---|---|---|
| Persistent chat | `core.Session` JSONL + `On*` persistence callbacks | wire the sink to WS; render events |
| Session switching | `OpenSession` / `SessionsDir` / `SetMessages` | list + resume UI; ctrlproto session group |
| Session **nicknames** | `SessionMeta` title + rename rows already in the format | expose rename in UI + protocol |
| Model switching | `SetClientAndModel` (swaps client+model live) | model dropdown from the catalog; ctrlproto command |
| Usage review | `EvUsage` streams; `cost.go` tracks per-session cost | render it — the data already flows past |
| PWA / mobile subset | (client-side) | manifest + service worker; capability toggle |

**So v1's real work is the transport + the PWA shell + the `modes/web` wiring —
not core surgery.** Usage review is listed by the author as nice-to-have, but
because `EvUsage`/`cost.go` already produce the numbers, it is nearly free and
worth pulling into v1.

Deferred to v2+ (the control-plane groups): lore CRUD, extension
enable/configure/install, prompt overrides, and templates/profiles. These need
the control method group and, in the case of extensions, a reload story (see
Open questions).

## Auth

**Front it; do not build it.** Putting OIDC into the Go binary fights the
lean-binary philosophy and would metastasize. The author already runs Authentik,
which has a forward-auth / proxy provider built for exactly this:

- **Primary mode:** bind terva to localhost, put Authentik (or Caddy /
  oauth2-proxy) in front doing OIDC, and trust an `X-Forwarded-User`-style
  header. terva's "identity" is single-user, so this is a gate ("keep strangers
  out"), not an identity system.
- **Fallback mode:** a minimal in-binary **bearer token** for the proxy-less case
  (Tailscale, quick remote access). ~50 lines, not a framework.
- terva must **refuse to bind a non-loopback address without an auth mode
  selected** — fail closed, because the endpoint can run code as you (below).

## Security posture

Be honest about the stakes: once reachable "wherever I can hit the webpage," this
is a **remote endpoint that can run `bash` and (later) install extensions as
you**. The auth gate is not a nicety — it is the entire application-layer security
boundary, and a leaked token is your whole box. Consequences baked into the
design:

- Strongly prefer **Tailscale/WireGuard + forward-auth** over raw public exposure.
- The VM perimeter is the real isolation; the jail is a guardrail.
- **Tool-approval policy is a knob, not a constant.** Frictionless yolo is fine on
  the LAN; a session that originated remotely should be able to require approvals.
  v1 can ship a single daemon-wide policy; the per-session/per-template policy is
  a natural v2 addition once templates exist.

## The PWA (mobile)

"Accessible wherever I can hit the webpage" wants a **PWA**, not just a
responsive page — installable to the phone home screen, with a service worker.
The mobile build does **not** need the full control panel; the advanced surfaces
(lore, extensions, prompt overrides) can be hidden behind a toggle or simply
omitted on small viewports, keeping the on-the-move experience focused on chat +
session/model switching. Web push (turn-complete, and later idle nudges) is the
expansion that turns a tab into a companion-in-your-pocket — deferred to a later
phase but worth designing the notification hooks for.

## Phased ladder

- **Phase 0 — the spike.** One agent, one project, WebSocket, streaming chat +
  tool-call rendering + approvals over ctrlproto's conversation group. Proves the
  seam outside a terminal.
- **Phase 1 — the daily driver (the OpenClaw replacement).** Session list /
  switch / nicknames, model dropdown, usage, the auth gate, durable sessions,
  cancel/queue, and the PWA shell. This is the ship target.
- **Phase 2 — the control plane.** Lore CRUD, extension enable/configure,
  prompt overrides, templates/profiles — the ctrlproto control group.
- **Phase 3 — companion polish.** Web push, idle nudges (reuse the existing
  `--idle-nudge`/`--idle-prompt` primitive), memory-continuity views.

## Open questions

1. **Extension hot-enable vs reload.** Extensions spawn as subprocesses at daemon
   startup. Enabling a newly-installed extension mid-run likely needs an
   ext-manager reload/restart, not a live hot-add. v1 sidesteps this (extension
   management is v2), but Phase 2 must choose: graceful in-place ext-manager
   reload (nicer, more work) vs. "settings saved — restarting" (simple, brief
   blip). The always-up-companion goal argues for eventually doing the graceful
   reload.
2. **Templates apply at session creation, not mid-session.** The system prompt is
   baked at `NewAgent`; model swap is live (`SetClientAndModel`) but persona /
   prompt swap is not. Design templates as session-creation-time first; a
   mid-session `SetSystem` core mutator is a deliberate later change, not a v1
   assumption.
3. **Project switching.** v1 is one daemon = one workspace. Multiple projects =
   multiple daemons on different ports, or a later workspace-switcher that
   re-`Resolve`s against a new cwd (which ripples through session keying and the
   jail root). Decide when a second project actually shows up.
4. **Where does the general-assistant home base point?** If terva is the constant
   life companion (not a code repo), the pinned cwd may be `~` or a notes dir,
   with code projects reached into on demand. This is a deployment choice, not a
   code one, but it interacts with (3).
