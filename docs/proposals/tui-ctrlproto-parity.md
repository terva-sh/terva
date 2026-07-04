# TUI ↔ ctrlproto parity check — what the wire must gain to host the TUI

- **Status:** ANALYSIS / design. No implementation. This is the "completeness
  test" gate named in the control-plane proposal — an inventory of the full
  interactive-TUI capability surface measured against what `ctrlproto` +
  `Workspace` + the web client express today, plus the migration-gap list the
  eventual TUI-on-ctrlproto port must close.
- **Date:** 2026-07-04
- **Scope:** read-only audit. The TUI migration itself is explicitly a **later
  PR**, not this one. This doc is the map that later PR follows.
- **Prereq reading:** docs/proposals/control-plane-protocol.md (the wire; its
  "TUI migration = the completeness test" roadmap item is what this operationalizes),
  docs/proposals/web-surfaces.md (the surfaces system most TUI panes map onto),
  docs/proposals/terva-web.md (first consumer), docs/proposals/terva-platform.md
  (the fleet endgame the shared interface unlocks).

> **Update (landed in PR #9, after the audit).** Three of the gaps below were
> closed as foundational ctrlproto work while the wire is still pre-freeze:
> `clear` (conversation group), `control.trust` / `control.untrust` +
> `SessionInfo.trusted` (control group — closes the "web shows gated project
> scopes but can't grant trust" incoherence), and `CreateOpts.Persona` is now
> honored on session create. The matrix rows and tier lists below are marked
> **✅ SHIPPED (PR #9)** where this applies; the analysis is otherwise as-audited.

## TL;DR

The **conversation** and **session** groups are effectively at parity — the hard,
high-frequency interactive core (streaming, tool display, approvals, mid-turn
asks, the message queue, compaction, cancel) is already fully expressed on the
wire, because `ctrlproto.Event` embeds `core.WireEvent` verbatim and the two
control-plane events (`permission_request` / `ask_request`) were designed for
exactly the TUI's two blocking modals.

The gaps are concentrated in the **control group's unbuilt intent** (trust, jail,
prompt overrides, templates, persona/mode switching, login/logout) plus a cluster
of **TUI-only session ops** (clear, export/import/fork/tree, cd) and **side-modes**
(`/btw`, `/study`, rescue, `!shell`). Most management dialogs (permissions,
extensions, MCP, lore, settings, tasks, context, ext panels) already have a
web-surface analog and port cleanly.

Seven capabilities are **shape-hard** over request/response+events (they aren't
just missing verbs): synchronous modal input semantics, the multi-step login
state machine, `!shell` local execution, the `/btw` scratch-conversation, the
rescue picker, host/filesystem mutations (trust/jail/migrate), and status-line
scripts. These need deliberate protocol design, not a mechanical port.

**Bottom line:** the TUI can migrate onto the in-process carrier incrementally.
Nothing in the hot conversation path blocks it. The port is gated on landing the
control group (already the plan) and on making a handful of deliberate
protocol-shape decisions, none of which need to happen in the current web PR.

## Method

Two independent read-only recon sweeps of the worktree
(`.claude/worktrees/terva-web`):

1. **TUI surface** — every built-in slash command (`modes/slash_registry.go`),
   every overlay/dialog (`modes/overlay_registry.go` `buildOverlays()`), the
   turn-interaction surface (streaming/tool-display/approvals/asks/queue/cancel/
   compact), the mode/identity axis (regular/chat/play/cards/personas/cast/swarm),
   TUI-only chrome, and the startup/config surface.
2. **ctrlproto/web surface** — the `WorkspaceService` interface + method groups
   (`ctrlproto/service.go`, `methods.go`), the event + hello vocabulary
   (`event.go`, `hello.go`), the surface kinds and which are actually emitted
   (`workspace_surfaces.go`), the `Workspace` impl's method coverage, and the
   Preact client's actual feature usage (`web/client/src/app.tsx`).

Each capability below is classified by target home:
**(a)** conversation group · **(b)** session group · **(c)** control group or a
surface · **(d)** pure client chrome (no wire analog needed).

Status legend: **✅ covered** (wire verb/surface exists and is exercised) ·
**🟡 partial** (exists but narrower than the TUI, or web-only-so-far) ·
**❌ gap** (no wire method/surface/event) · **⚠️ shape-hard** (a gap that also
needs non-trivial protocol design, not just a new verb).

---

## Part 1 — Parity matrix

### Conversation group — ✅ at parity

The interactive hot path. `ctrlproto.Event` embeds `core.WireEvent`
(`event.go:14`), so every streaming event (text deltas, tool-use start/args/end/
call/result/progress, usage, turn_start/turn_end/done, error, user_message) is
already on the wire unchanged. The two blocking modals map to the two purpose-built
control events.

| TUI capability | Source | Wire | Status |
|---|---|---|---|
| Prompt dispatch (+images) | `interactive_input.go:204`, `interactive_turn.go:210` | `MethodPrompt` + `Images` (`methods.go:64`) | ✅ |
| Streaming output (typewriter pacer) | `interactive_turn.go:474`, `handleEvent` `:453` | embedded `core.WireEvent`; pacing is client chrome | ✅ (a)+(d) |
| Tool-call live display | `EvToolUseStart/Args/End/Call/Result/Progress` `:492` | same events; Full/Minimal/Hidden cycling is client-side | ✅ (a)+(d) |
| Tool approval modal (5-way) | `confirm_dialog.go`, `confirmOptions` | `MethodApprove` + `EventPermissionRequest`/`Resolved`; `Decision{Allow,RememberTool,RememberAll,PersistTool}` maps the 5 options cleanly | ✅ (a) |
| Mid-turn ask (`ask_user_question`) | `question_dialog.go` | `MethodAnswer` + `EventAskRequest`/`Resolved`; `AskRequest.Options/AllowCustom` | ✅ (a) |
| Message queue (type-while-busy) | `turnEngine`, `claimOrQueue` | `MethodQueue` + `MethodQueueSet` + `EventQueueUpdated` + `Snapshot.Queued` | ✅ (a) |
| Queue edit/cancel (Alt+Up pop) | `keymap.go:276` | `queue.set` (replace whole queue) | ✅ (a) |
| Turn cancel (Esc) | `keyEsc` `keymap.go:172` | `MethodCancel` | ✅ (a) |
| `/compact` manual | `runCompact(...,false)` | `MethodCompact` | ✅ (a) |
| Auto-compact (pre/post-turn, 413 retry) | `shouldAutoCompact` `:441` | daemon-side policy, fires transparently — no client verb needed | ✅ (a) |
| Continue-on-open-work nudge | `cli.go:446` `ag.ContinueOnStop` | already host-agnostic (shared by web) | ✅ (a) |

**One semantic flag (⚠️ but not blocking):** in the TUI, confirm/question **block
the agent goroutine on a Go channel** until answered. Over the wire this becomes
request → broadcast → **first-responder-resolves** (any subscriber can answer,
first-answer-wins). For a single in-process TUI carrier this is behaviourally
identical; it only diverges with multiple concurrent clients. The in-process
carrier can preserve exact single-answerer semantics.

### Session group — ✅ mostly, with named gaps

| TUI capability | Source | Wire | Status |
|---|---|---|---|
| `/new` fresh session | `startNewSession` | `sessions.create` (resets in place TUI-side) | ✅ (b) |
| `/sessions` resume picker | `session_dialog.go` | `sessions.list` + `sessions.resume` | ✅ (b) |
| Session rename (in-picker `r`) | `sessionDialogAction.Renamed` | `sessions.rename` | ✅ (b) |
| Session delete | *(TUI has no delete)* | `sessions.delete` exists — **web-only capability** | 🟡 (b) |
| `/context` breakdown | `context_dialog.go` | `context.get` + `context` surface | ✅ (b) |
| `/usage` windows | `usage_dialog.go` | `usage.get` (folded into `context` surface) | ✅ (b) |
| `/jump` to past turn | `jump_dialog.go` | client-side over snapshot (scroll = chrome); the fork-branch selection is a gap | 🟡 (b)/(d) |
| `--continue`/`--resume` preload | `interactive.go:899` | `sessions.resume` + `EventSnapshot` | ✅ (b) |
| **`/clear`** transcript wipe | `slashClear` `ag.SetMessages(nil)` | `clear` (conversation) — empties + durable empty checkpoint | ✅ SHIPPED (PR #9) |
| **`/session export`** | `session_ops_dialog.go`, `cli.go:2362` | **no analog** | ❌ (b) |
| **`/session import`** | `session_ops_dialog.go` | **no analog** | ❌ (b) |
| **`/session fork`** | `jump_dialog.go` `pendingFork` | **no analog** | ❌ (b) |
| **session tree / branch switch** | `session_tree_dialog.go` | **no analog** (only flat list) | ❌ (b) |
| **`/cd <path>`** change cwd | `slashCD` (hidden) | **no analog** | ❌ (b/c) |

### Control group + management surfaces

Most **management dialogs already have a web-surface analog** — those port
cleanly. The gaps cluster in the control group's unbuilt intent (the `hello.go:18`
comment already names lore/extensions/prompt overrides/templates/jail as the
group's intended scope).

| TUI capability | Source | Wire | Status |
|---|---|---|---|
| `/model` pick/switch | `model_dialog.go` | `models.list` + `models.switch` | ✅ (c) |
| Model favorite (star) | `modelDialogAction` | `models.favorite` | ✅ (c) |
| `/permissions` inspect + revoke | `permissions_dialog.go` | `permissions` surface + `surface.action` | ✅ (c) |
| `/extensions` toggle/config/log | `extensions_dialog.go` | `extensions` surface (toggle); config-form + log-viewer not yet | 🟡 (c) |
| `/mcp` toggle/log | `mcp_dialog.go` | `mcp` surface (toggle); log-viewer not yet | 🟡 (c) |
| `/lore` list/edit | `lore_view.go` | `lore` surface + `save`/`delete` action | ✅ (c) |
| `/settings` (all items) | `settings_dialog.go`, `interactive_settings.go` | `settings` surface + `set` action (incl. approval-mode switch) | ✅ (c) |
| `/swarm` dashboard | `swarm_dialog.go` | `tasks` surface — **but** TUI subcommands (new/kill/logs/send/resume/attach) far richer than stop/remove/resume/send | 🟡 (c) |
| Extension slash commands | `interactive.go:1160` | `commands` surface + `run` action + `EventNotice` | ✅ (c) |
| Extension-owned panels | `ext_panel_dialog.go` | `ext:<ext>:<panel>` surfaces + key/action/close | ✅ (c) |
| Ambient status segments | `interactive_render.go:502` | `status` surface (ext segments only; rich meters TUI-local) | 🟡 (c)/(d) |
| Tier-1 self-restart | *(web-first)* | `control.restart` (gated by `--web-allow-restart`) | ✅ (c) |
| **Model promote-to-default** (project/global) | `modelDialogAction` Ctrl+D | **no analog** | ❌ (c) |
| **Per-model config edit** | `model_edit_dialog.go` | **no analog** | ❌ (c) |
| **`/login`** | `login_dialog.go` | **no analog** — ⚠️ multi-step + browser callback | ⚠️ (c) |
| **`/logout`** | logout picker | **no analog** | ❌ (c) |
| **`/jail` / `/unjail`** | `Sandbox.Lock/Unlock` | **no analog** (`hello.go:18` names it as intended) | ❌ (c) |
| **`/trust` / `/untrust`** | `slashTrust`/`slashUntrust` | `control.trust` / `control.untrust` + `SessionInfo.trusted` | ✅ SHIPPED (PR #9) |
| **`/reload-ext`** | slash | **no analog** (web reloads via `SetOnReload`; no explicit verb) | ❌ (c) |
| **`/connect`** (chat bridges) | `connect_dialog.go` | **no analog** | ❌ (c) |
| **`/migrate`** (zot→terva) | `migrate_dialog.go` | **no analog** — one-time host op | ❌ (c) |
| **Log viewer** (ext/mcp tail) | `log_dialog.go` | **no analog** (no log surface) | ❌ (c) |
| **Skills inspector** | `skills_dialog.go` | only `Snapshot.Skills` (autocomplete list); no inspect view | 🟡 (c) |
| **Persona at session-create** | `--persona` at launch | `CreateOpts.Persona` now honored on create (validated → CodeBadRequest on unknown); `Template` still reserved | ✅ SHIPPED (PR #9) |
| **Persona / mode switch at runtime** | fixed at launch | `SessionInfo.Persona` read-only; no switch-on-existing-session verb | ❌ (c) |

### Modes / identity — fixed at launch, not switchable on the wire

`--chat`/`--play`/`--card`/`--cast`/`--greeting`/`--no-tools` are CLI flags parsed
at boot (`args.go`). ctrlproto has **no mode/persona/cast method** — a session
inherits whatever the daemon launched with. `CreateOpts` carries `Persona`/
`Template` fields on the wire but the impl drops them. Cast/actor dispatch
(`actor_spawn`, `--play`, `.terva/cast.json`) surfaces only as tool calls in the
stream; there's no wire control for it. **Status: ❌ (c)** for runtime switching;
the underlying turns are ✅ (a) since they're just prompts+tool-calls.

### Pure client chrome — no wire analog needed (✅ (d))

Status-line meters/rendering, user status-line scripts (local shell), scroll/jump/
auto-follow, paste UX (the image *payload* rides `PromptParams.Images`; the marker
UX is chrome), tool-display cycling (Ctrl+T/O), theming, spinner, keybindings,
slash/@-file autocomplete popups (fed by `BuiltinSlashCommands()` + `Snapshot.Skills`),
welcome/update/changelog banners. These stay in whatever client renders the wire.

---

## Part 2 — Migration-gap list (what ctrlproto must gain)

Grouped by the work each needs, roughly in dependency order.

### Tier 1 — new verbs, mechanically straightforward

Plain additions to an existing group; no new protocol shape.

- ~~**`clear`** (conversation) — `ag.SetMessages(nil)`. Sibling to `compact`.~~
  **✅ SHIPPED (PR #9)** — empties the live agent + writes a durable empty
  checkpoint (`AppendCompaction(nil)`) so a resume also starts fresh.
- **`sessions.export` / `sessions.import`** (session) — serialize/deserialize a
  transcript to/from a path or blob.
- **`/reload-ext`** verb (control) — the web already reloads via `SetOnReload`;
  this just names it.
- **`models.promote`** (control) — persist a model as project/global default
  (Ctrl+D today).
- **`models.config.get`/`set`** (control) — per-model overrides (`models.json`).
- **Honor `CreateOpts.Persona`/`CreateOpts.Template`** — ✅ **Persona SHIPPED
  (PR #9)** (threaded through `buildSession`, validated up front); `Template`
  still reserved. Still open: `personas.list` + a persona-switch verb for runtime
  change on an existing session.
- **Skills inspector surface** — promote `Snapshot.Skills` into a read `skills`
  surface (name+description+body), mirroring `/skills`.
- **Log-viewer surface** — a `logs` surface (or an action on extensions/mcp) that
  tails ext/mcp logs.
- **Richer tasks/swarm actions** — extend the `tasks` surface with spawn + tier
  config + persona-dispatch selection to match `/swarm`'s subcommands.

### Tier 2 — host/state mutation verbs (design the authority story)

These reconfigure the host, so they belong in the control group **and** must slot
into the per-group authority gating the control-plane proposal already mandates
for the ext-tunnel carrier.

- ~~**`trust.set {on, parent?}` / `untrust`**~~ **✅ SHIPPED (PR #9)** as
  `control.trust {parent}` / `control.untrust`, lifting the ACP
  `TrustWorkspace`/`UntrustWorkspace` shape: persist via `TrustPath`/`UntrustPath`,
  flip `extMgr.SetProjectTrusted` + reload, rebuild tools, re-discover lore, and
  broadcast `session_updated`. State on `SessionInfo.trusted`.
- **`jail.set {on|off}`** — `Sandbox.Lock/Unlock`. Already named in the
  control-plane proposal's config section.
- **`cd {path}`** — change session cwd; rebuilds tool sandbox roots.
- **Prompt / system overrides** — `hello.go:17` names this as intended control
  scope; no verb yet.

### Tier 3 — ⚠️ shape-hard (need protocol design, not just a verb)

These don't fit request/response+events cleanly. Each needs a deliberate decision
before porting.

1. **Synchronous modal input semantics.** Confirm/question block the agent
   goroutine on a channel; the wire is broadcast + first-responder. The
   in-process carrier can preserve single-answerer semantics; multi-client needs a
   "claim" concept. *Decision: does the in-process carrier special-case, or does
   the protocol grow claim/lock?*
2. **`/login` + `/logout`.** Multi-step state machine (method → provider →
   browser/paste-code → done) with an OS browser launch and async OAuth callback.
   Needs either a login *sub-protocol* (stateful, multi-round) or an out-of-band
   auth flow the client drives and the daemon observes. Auth is currently
   daemon-side/out-of-band by design.
3. **`!shell` escape.** Runs a subprocess in cwd honoring the local sandbox,
   output parked below the transcript, never entering the model conversation.
   No natural remote analog — inherently local execution. *Likely stays a client-
   local capability of the in-process carrier, not a wire verb.*
4. **`/btw` side-chat.** A second, throwaway conversation against a frozen snapshot
   of the same session — no "scratch conversation" concept exists in ctrlproto.
   Needs either an ephemeral-session concept or a dedicated side-turn verb.
5. **Rescue picker.** Couples in-turn error classification + a model switch + a
   re-prompt into one modal. Needs a recoverable-error event carrying candidate
   models, then a resolve verb (switch+retry).
6. **Host/filesystem one-time ops (`/migrate`).** Tied to the local process/
   filesystem. `control.restart` is precedent for a host op on the wire; migrate
   is lower priority (one-time).
7. **Status-line scripts.** Arbitrary local shell feeding UI atoms — inherently
   local; stays client-side.

### Not gaps — already handled

Auto-compact (daemon policy), the continue-on-open-work nudge (host-agnostic),
image **input** (wire-ready via `Image`/`FeatureImages`/`toImageBlocks`; only the
PWA *UI* is missing — a TUI carrier reuses the existing inbound plumbing), and all
pure chrome.

---

## Part 3 — Recommended staging for the eventual TUI port

The port is validation of the interface, not a big-bang rewrite. Suggested order
(each stage keeps the TUI shippable):

1. **In-process carrier + conversation group first.** Route the TUI's turn loop
   (prompt/stream/tool-display/approve/answer/queue/cancel/compact) through the
   in-process `WorkspaceService`. This is the highest-value proof: the hot path is
   already at parity, so it's a wiring exercise, and it validates the embedded
   `core.WireEvent` stream end-to-end with a demanding client.
2. **Session group.** Move `/sessions`/`/new`/rename over; add `clear` +
   export/import so nothing regresses.
3. **Management surfaces.** Point the TUI's permissions/extensions/mcp/lore/
   settings/tasks/context dialogs at `surface.get`/`surface.action` (the web
   already proves these render).
4. **Tier-2 control verbs** (trust/jail/cd/prompt-overrides) with the authority
   gating.
5. **Tier-3 shape-hard items** last, each with its own mini-design — or keep the
   truly-local ones (`!shell`, status scripts) as client-local capabilities the
   in-process carrier exposes directly without a wire round-trip.

The completeness test the control-plane proposal calls for is passed when stages
1–3 carry the daily-driver TUI with no feature regression; tiers 4–5 are the long
tail.

## Explicitly out of scope

No TUI migration work happens in the current web PR — the user was explicit it is
"not in this PR, but after our web work." This doc is the map, not the move. The
immediate web PR continues to grow the surfaces/control coverage on its own track;
each capability it adds (permissions/lore/mcp editing, extension commands, self-
restart) is also a TUI-migration down-payment, since the TUI will consume the same
verbs.
