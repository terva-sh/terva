# terva controllers — the control-plane protocol (`ctrlproto`)

`ctrlproto` is terva's **control-plane protocol**: the contract a frontend uses
to *drive and manage* a terva workspace — run turns, manage sessions, and
(additively) manage models, trust, lore, extensions, and templates. It is the
wire behind [`terva web`](web.md), and the substrate a future management plane
would use to command a fleet of terva instances.

It is the third of terva's three home-grown protocols, and deliberately shares
their shape:

| Protocol | Axis | Doc |
|---|---|---|
| **connproto** | host ↔ chat connector (Telegram, Discord, external) | [connectors.md](connectors.md) |
| **extproto** | host ↔ extension (subprocess, JSON-RPC) | [extensions.md](extensions.md) |
| **ctrlproto** | frontend ↔ workspace (drive & manage terva) | this doc |

The code lives in `packages/agent/ctrlproto` (always built, carrier-agnostic).
For the design rationale see the proposal:
`docs/proposals/control-plane-protocol.md`.

> **Status.** Out-of-process *programmatic* control and multi-instance
> management are **not available yet**. The only shipped carrier is the
> WebSocket one that backs `terva web` (a single trusted browser user, behind
> an auth gate). The stdio / CLI carrier, the extension-tunnel carrier, and the
> fleet controller are designed here but not built. See
> [Carriers & current state](#carriers--current-state).

## Interface-first, wire-second

The artifact that matters is a **Go interface**, `ctrlproto.WorkspaceService` —
"everything a frontend needs to drive and manage terva." The wire (`Frame` +
`Encode`/`Decode`) is *one serialization* of that interface; `ServeConn` adapts
any frame carrier onto a `WorkspaceService`.

This is why "migrate the TUI onto the control plane" does not re-introduce a
serialization cost: the TUI would bind the interface **in-process** (direct
method calls, no JSON), while web/stdio/extension clients use the wire
serialization of the *same* interface. Driving the TUI through the interface is
the protocol's **completeness test**, not a performance regression.

The wire is intentionally the connproto/extproto meta-architecture — a wire spec
+ an SDK-style interface + pluggable carriers + capability negotiation +
id-correlated commands and uncorrelated streamed events — with a richer,
non-chat vocabulary.

## Carriers & current state

The same `WorkspaceService` is bound to different transports:

| Carrier | Client | Status |
|---|---|---|
| **in-process** | the TUI / any embedding host — direct interface calls, no serialization | **shipped, the only TUI backend**: the TUI binds `Workspace` through the `modes.Carrier` seam (PR #14; the legacy direct driver has since been removed, `--tui-legacy` is a deprecated no-op). The `AgentFor` crutch is **gone** (remediation plan 4.1, complete — see `modes/carrier.go`): the TUI holds no `*core.Agent`, and its whole control path is ctrlproto — prompt dispatch, the event stream, approvals/asks, transcript rendering, `/context`, `/permissions`, `/settings`, `/clear`, `/model`, login, side chat. Honest caveat: file-based session fork/tree/export stays local (out of wire scope v1) |
| **WebSocket** | [`terva web`](web.md) — bidirectional, streaming, browser-native | **shipped** |
| **stdio** | a CLI, scripts, or a fleet controller (one agent per child) | designed, **not built** |
| **ext-tunnel** | extensions, over extproto (the `chat_open`/`chat`/`chat_close` envelope trick connproto already uses) | designed, **not built** |

Adding a carrier is a binding, not new protocol work.

## Method groups

The surface is split into four **capability-negotiated groups** with different
rates of change and audiences. A client declares which groups it speaks in its
hello; a minimal client (the mobile PWA) negotiates only the first two.

| Group | Carries | Authority |
|---|---|---|
| **conversation** | the event stream (out) + turn commands: prompt, queue, cancel, compact, clear, approve, answer, subscribe | frontend-driving |
| **session** | session lifecycle (list/create/resume/rename/delete), usage (incl. snapshots and reset credits), context breakdown + tree nodes, side chat, surfaces, i18n catalog | frontend-driving |
| **control** | host-reconfiguring management: models, trust, restart, reset-credit redemption — and (future) lore, prompt/system overrides, templates, jail | **categorically higher** — see [Security & authority](#security--authority) |
| **replay** | a recorded session's transport (`replay.control` / `replay.state`) and its `replay_state` broadcast | frontend-driving, but **optional**: it is off the base server hello — only a carrier backing a `ReplayController` advertises it, so a client that negotiates it is guaranteed the group is served |

Keeping the groups separable is what lets one protocol serve a focused mobile
client, a full desktop control panel, and a fleet controller without any of them
implementing surface they don't use.

## The wire

### Frame envelope

One JSON object per message (a WebSocket message boundary; LF-delimited on a
stream carrier). A frame is a discriminated union over `kind`:

| `kind` | Direction | Purpose | Populated fields |
|---|---|---|---|
| `hello` | both, once | capability handshake | `hello` |
| `cmd` | client → server | a command | `id`, `sess?`, `method`, `params?` |
| `resp` | server → client | the response to a `cmd` | `id`, then exactly one of `result` / `error` |
| `event` | server → client | a streamed, uncorrelated event | `sess`, `event` |

`id` (a `uint64`) correlates a `cmd` with its `resp`; it is absent on events and
hellos. `sess` addresses the session a frame concerns — **addressing rides the
envelope from day one**, so a mobile-subset client ("I speak conversation +
session for *this* session") and a future fleet controller ("I address N
agents") need no reframe. An empty `sess` means the workspace's default session.

### Handshake & capability negotiation

The client sends its hello first; the server replies with its own. The
**contract** is the intersection of the two — the groups and features both
sides speak, at `min(protocol)`:

```jsonc
// client → server
{"kind":"hello","hello":{
  "role":"client","protocol":1,"agent":"terva-web","version":"0.115.1",
  "groups":["conversation","session","control"],
  "features":["images","resolve-events"]}}

// server → client
{"kind":"hello","hello":{
  "role":"server","protocol":1,"agent":"terva","version":"0.115.1",
  "groups":["conversation","session","control"],
  "features":["images","resolve-events"],
  "locale":"en"}}
```

New capabilities land as **additive feature strings**, not protocol bumps — an
old client simply never uses a feature a newer host advertises. `Protocol` bumps
only on a breaking envelope change, which negotiation is designed to avoid.

| Feature | Meaning |
|---|---|
| `images` | inbound image attachments on `prompt` |
| `image-data` | outbound image payloads: image blocks in snapshots and message / tool-result events keep raw `data` alongside `mime_type`+`bytes`, so the client renders real pixels. Without it the carrier strips `data` at the connection boundary (size-only blocks — inlined payloads are opt-in, not a default). In-process carriers bypass serialization and always see the full form |
| `resolve-events` | the `permission_resolved` / `ask_resolved` multi-client dismissal events |
| `context-tree` | `context.get` carries the hierarchical `ContextBreakdown.Tree` (section/turn/message outline), lazily expanded with `context.node`. A client that doesn't see it falls back to the flat message list |
| `restart` | the daemon will serve `control.restart` (advertised only under `--allow-restart`; `--web-allow-restart` is an accepted alias) |

The server also advertises its active UI language (`locale`, BCP-47) so a client
can fetch the matching string catalog (`i18n.catalog`); server-originated display
text on the wire is already localized to it.

### Commands

Every command is a `cmd` frame; the server answers with a `resp` (a bare ok, a
`result` payload, or an `error`). `sess` rides the envelope, not the params.

**conversation**

| Method | Params | Effect |
|---|---|---|
| `prompt` | `{text, images?}` | start a turn; returns when *accepted*, progress streams to subscribers (`busy` → `error`) |
| `queue` | `{text}` | enqueue for the next safe boundary of the running turn (the interject / multi-device story) |
| `queue.set` | `{texts}` | replace the whole pending queue (edit/cancel queued messages); empty clears |
| `cancel` | — | interrupt the active turn; no-op when idle |
| `compact` | — | summarize + replace the transcript; clients get a fresh snapshot |
| `clear` | — | wipe the transcript (no summary), same session |
| `approve` | `{call_id, decision}` | resolve a `permission_request` (first client wins) |
| `answer` | `{ask_id, answer}` | resolve an `ask_request` (first client wins) |
| `subscribe` | — | stream events for `sess`; the server sends a `snapshot` first |
| `unsubscribe` | — | stop the stream |

**session**

| Method | Params → Result | Effect |
|---|---|---|
| `sessions.list` | → `{sessions:[SessionInfo]}` | list sessions, newest activity first |
| `sessions.create` | `{…CreateOpts}` → `{session}` | mint a session (honors `persona`) |
| `sessions.resume` | → `{session}` | load a persisted transcript |
| `sessions.rename` | `{title}` | set the display nickname |
| `sessions.delete` | — | remove a session + transcript |
| `usage.get` | → `{usage}` | cumulative tokens / cost |
| `usage.snapshot` | `{refresh?}` → `{usage}` | the provider's usage windows / credits; `refresh` pulls from the provider's usage endpoint and blocks on the fetch |
| `usage.resets.list` | → `{supported, resets?}` | the consumable usage-reset credits and their status — read-only (redeeming one is `usage.resets.consume`, in **control**) |
| `context.get` | → `{breakdown}` | the `/context` size view (prompt, tools, ext context, transcript vs window) |
| `context.node` | `{id, op?}` → `{node}` | resolve one context-tree node by its opaque id: expand it a level deep, or run a node-named reveal (feature `context-tree`) |
| `sidechat.open` | → `{id}` | freeze a snapshot of the session for an off-transcript side chat (the `/btw` overlay) |
| `sidechat.ask` | `{id, prior?, question}` → `{text}` | one tool-less completion against the frozen snapshot; the client carries the prior turns, so the daemon holds no per-turn state |
| `sidechat.close` | `{id}` | release the snapshot (closing an unknown id is not an error) |
| `surfaces.list` | → `{surfaces}` | auxiliary panes (context, usage, extension panels) |
| `surface.get` | `{id}` → `{surface}` | one pane's content |
| `surface.action` | `{id, action, args?}` | act on a pane (e.g. forward a keypress; `extensions`/`mcp` `toggle` takes `{name, enabled, scope: global\|project}`; `tasks` takes `spawn {task, model?, provider?, persona?}` / `stop`/`remove`/`resume` `{id}` / `send {id, text}` — actions return no payload, so a spawned agent's id arrives via the next `tasks` fetch) |
| `i18n.catalog` | `{lang?}` → `{catalog}` | the effective web string catalog (session-independent) |

**control**

| Method | Params | Effect |
|---|---|---|
| `models.list` | → `{models}` | models the workspace can switch to |
| `models.switch` | `{model, provider?}` | switch the model backing `sess`, live, for the next turn (emits `session_updated`); `provider` qualifies ids that exist under several providers |
| `models.favorite` | `{provider, model, on}` | pin/unpin a favorite (persisted to config) |
| `control.trust` | `{parent?}` | grant Workspace Trust to the cwd; brings project extensions/lore/permission rules live across open sessions |
| `control.untrust` | — | revoke it, tearing project content back down |
| `control.restart` | — | Tier-1 self-restart (re-exec into the installed binary); `unsupported` unless `restart` was advertised |
| `usage.resets.consume` | `{id}` → `{reset, windows_reset?}` | redeem a usage-reset credit. Irreversible and it spends a scarce provider grant, so it sits here rather than beside the read-only `usage.resets.list`; the host confirms with the user before issuing it |

**replay** (optional — served only by a carrier backing a `ReplayController`,
which advertises the group in its hello; otherwise `unsupported`)

| Method | Params → Result | Effect |
|---|---|---|
| `replay.control` | `{action, position?, multiplier?, unit?}` → `{state}` | drive a recorded session's transport: `play` / `pause` / `step` / `seek` / `turn` / `speed`. Only the field the action needs is read |
| `replay.state` | → `{state}` | the current transport state: `{playing, position, total, speed, mode}` (`mode` = `effective` \| `raw`) |

Implementations may return `unsupported` for control methods they don't yet
serve — jail, lore CRUD, prompt/system overrides, and templates are shaped in
the interface but not implemented.

Example — a model switch and its broadcast:

```jsonc
{"kind":"cmd","id":12,"sess":"20260704-231713","method":"models.switch",
 "params":{"model":"deepseek-v4-pro"}}
{"kind":"resp","id":12}                                   // bare ok
{"kind":"event","sess":"20260704-231713",
 "event":{"type":"session_updated","info":{ /* fresh title/model/cost */ }}}
```

### Events

Events are `event` frames addressed to a `sess`, uncorrelated with any command.

The **conversation stream** reuses core's canonical `WireEvent` vocabulary
*verbatim* — text deltas, tool-use start/args/end, tool calls & results,
progress, usage, turn boundaries, errors, compaction. Its JSON is byte-identical
to what every other terva surface emits; see the wire schema in
[docs/rpc.md](rpc.md). ctrlproto adds only the **control-plane events** a client
must render and answer:

| Event `type` | Payload | Meaning |
|---|---|---|
| `permission_request` | `permission:{call_id, tool, preview?}` | approve a tool call (resolve with `approve`) |
| `ask_request` | `ask:{ask_id, question, options?, allow_custom?}` | a mid-turn question (resolve with `answer`) |
| `permission_resolved` | `resolved:{call_id}` | another client / a remembered decision answered it — dismiss the dialog (feature `resolve-events`) |
| `ask_resolved` | `resolved:{ask_id}` | the question was answered |
| `snapshot` | `snapshot:{…}` | sent only to a **new subscriber**: transcript, pending permissions/asks, queue, skills, `busy` — render history before the live stream |
| `session_updated` | `info:{SessionInfo}` | metadata changed (title, model, cost, `trusted`) — refresh headers & the session list |
| `queue_updated` | `queued:[…]` | the pending queue changed (absent = cleared) |
| `surface_updated` | `surface_id` | a pane's content changed — re-fetch with `surface.get` |
| `surfaces_changed` | — | the set of panes changed — re-list |
| `locale_changed` | `locale` | the daemon's UI language changed — re-fetch catalogs & re-render |
| `notice` | `notice:{level, text, ext?, kind?, data?}` | a one-shot, ephemeral message (not persisted, not replayed) |
| `replay_state` | `replay:{playing, position, total, speed, mode?}` | a recorded session's transport moved (play/pause/seek/speed) — every client's scrubber re-syncs (group `replay`) |

**Kinded notices.** A notice may carry a machine-readable `kind` plus a
string-map `data` payload. `text` always stands alone, so a client that
doesn't recognize a kind renders the text and loses nothing; a kind-aware
client can filter, route, or re-render — a single-user surface shows the
notice inline, a fleet control plane might aggregate the same kind across
many daemons. Kinds are additive protocol surface, documented on their
constants in `ctrlproto/event.go`. Current kinds:

| kind | data keys | meaning |
|---|---|---|
| `prompt_rebuilt` | `scope` (system \| tools \| both), `reason` (approval-mode \| auto-swarm \| extension-reload \| mcp-toggle \| trust \| tool-withdrawal \| extension-context \| chat-connect \| chat-disconnect), `context_tokens?` | the session's pinned prompt prefix changed, so the provider prompt cache is invalidated and the next turn re-reads ~`context_tokens` tokens uncached. Emitted only on a real diff — an identical rebuild is silent. The extension-driven reasons (`tool-withdrawal`, `extension-context`) are suppressed to a host log when they fire before the first turn — a startup policy assertion invalidates no cache. Informational, never blocking: the run loop pins the prefix per turn, so the change lands at the next turn regardless. |

Example — a tool-approval round-trip:

```jsonc
{"kind":"event","sess":"s1","event":{"type":"permission_request",
 "permission":{"call_id":"call_01","tool":"bash","preview":"rm -rf build/"}}}
{"kind":"cmd","id":20,"sess":"s1","method":"approve",
 "params":{"call_id":"call_01","decision":{"allow":true}}}
{"kind":"resp","id":20}
{"kind":"event","sess":"s1","event":{"type":"permission_resolved",
 "resolved":{"call_id":"call_01"}}}    // other clients dismiss their dialog
```

### Error codes

A failed command returns a `resp` with an `error:{code, message}`. Codes are
stable wire strings (not localized):

| Code | When |
|---|---|
| `busy` | a turn is already running on the session |
| `no_session` | the addressed session does not exist |
| `not_found` | a named resource (model, surface) does not exist |
| `bad_request` | malformed params |
| `unsupported` | the method isn't served (an unimplemented control call, or an un-negotiated group) |
| `unauthorized` | the auth gate rejected the caller |
| `internal` | an unexpected server-side failure |

## Multi-client fan-out

A session's event stream fans out to **every** subscriber. The permission and
ask requests are *broadcast*, and the **first client to answer wins** — a late
answer for an already-resolved call is ignored, and the `*_resolved` events tell
the other clients to dismiss the dialog they were showing. This is the
multi-device story: your phone and your laptop both watch the same turn, either
can approve a tool call, and the other's prompt clears. A client that
(re)subscribes mid-turn gets a `snapshot` that restores the parked dialogs and
queue, so a suspended tab reconnecting doesn't leave a turn invisible.

## Security & authority

The conversation and session groups are frontend-driving. The **control group is
host-reconfiguring** — switch your model, edit your trust, restart the process —
which is a categorically higher authority than a bounded chat tunnel. Two layers
apply:

- **Group negotiation** gates *presence*: `ServeConn` rejects any method whose
  group the client did not negotiate (`unsupported`). This is what lets a host
  hand a mobile client a conversation+session-only capability set.
- **The carrier's auth gate** is the security *boundary*. For `terva web` that
  is: a loopback bind with no auth by default (plus a `Host`-header check that
  defeats DNS rebinding), or a bearer token, or a trusted forward-auth header
  from a reverse proxy (Authentik) — honored only from a loopback peer. See
  [web.md](web.md). `control.restart` is additionally gated behind
  `--allow-restart` (the `restart` feature; `--web-allow-restart` is an accepted
  alias — the capability is not web-only).

**Per-group *authority* gating is a future requirement for the ext-tunnel
carrier.** When ctrlproto is tunneled over extproto, an extension that speaks the
conversation group must **not** thereby be allowed to reconfigure the host: the
control group has to hook terva's authority classes (`local-read` /
`local-data` / `workspace-mutation` / `process-execution` / `network-read` /
`external-mutation`). The group structure exists now so this slots in additively
rather than being bolted on under pressure — but it is not implemented, which is
one reason out-of-process control is not yet exposed.

## The management-plane horizon

Everything above is single-workspace today, but the design points at a
**management plane for multiple terva instances** — the reason addressing is in
the envelope from the start:

- A **fleet controller** is a control plane that holds many carriers (or one
  carrier multiplexing many agents), addressing each instance by the envelope's
  `sess`/agent id. No reframe is required — the id is already there.
- This ties into the existing swarm / agent-dispatch work, which already spawns
  terva agents as subprocesses. ctrlproto is the clean way to *observe and
  manage* them (versus fire-and-collect): one vocabulary to drive a whole fleet.
- Getting there is **carrier work, not protocol work** — the stdio carrier (a
  CLI / controller driving one agent per child) and the ext-tunnel carrier — plus
  the per-group authority gating above. Until those land, terva does **not**
  allow out-of-process programmatic control; the browser panel is the only
  frontend on the wire.

See the platform vision in
`docs/proposals/terva-platform.md`.

## See also

- [web.md](web.md) — `terva web`, the browser control panel (the first ctrlproto carrier)
- [connectors.md](connectors.md) — connproto, the chat-connector wire (the meta-architecture ctrlproto reuses)
- [extensions.md](extensions.md) — extproto, the host↔extension wire (a future ctrlproto carrier)
- [rpc.md](rpc.md) — the `core.WireEvent` JSON stream that ctrlproto's conversation events *are*
- `docs/proposals/control-plane-protocol.md` — the full design rationale and roadmap
- `packages/agent/ctrlproto` — the implementation (`doc.go`, `wire.go`, `methods.go`, `event.go`, `hello.go`, `service.go`, `serve.go`)
