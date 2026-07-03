# Connector extensions: one process, both roles

Status: IMPLEMENTED (`explore/ext-connector-duplex`, PR #7). The
tunnel (extproto 5), the service-aware TUI `/connect`, crash respawn,
and the whole connproto v2 program riding through it all shipped; this
doc is kept as the design record — the ask, the wire reasoning, the
consent layering, and the assessment. Author/user reference:
docs/connectors.md ("Connector extensions") and docs/extensions.md
("Connector role").
Depends on: extension protocol v4, external connectors (chat-connectors.md phase 4)

## The ask

Let a single subprocess be BOTH an extension (the agent calls into it:
tools, commands, context) AND a connector (it triggers the agent:
inbound chat messages start turns). Today these are two deliberately
separate stacks — `extproto`/`extdriver`/`ext` and
`connproto`/`chat/external`/`connsdk` — so a Telegram bridge that also
wants to expose a `telegram_search_history` tool must ship two
processes, two manifests, two supervisors, and share state through the
filesystem.

The value of one process is shared state: the connector half and the
tool half see the same live connection, the same credentials, the same
in-memory caches. A chat bridge whose tools can act on the service
(search history, list rooms, pin a message) is the canonical shape.

## What the gap actually is

A close read of both stacks shrinks the problem considerably:

- The extension wire is ALREADY bidirectional. Extensions push
  unprompted frames today (`notify`, `open_panel`, `context_card`,
  `refresh_context`, `set_withdrawn_tools`, `submit_slash`) and make
  reverse requests (`host_tool_call`). The single-writer outbox and
  ID-correlated pending maps in `extdriver` are a working multiplexer.
- Everything above the connector transport is already unified.
  `chat.Loop` (bot daemon) and `chat.Bridge` (TUI mirror) consume one
  `chat.Connector` interface; in-process and out-of-process connectors
  are indistinguishable above `chat.Register`.
- Extensions already load in bot mode (`setupNonInteractiveExtensions`
  in `botRun`).

So the missing piece is narrow: **a typed chat-message stream and a
host→child send-with-ack path on the extension wire, plus a
`chat.Connector` adapter that makes an extension process show up in the
chat registry.** Not a new protocol, not a merged supervisor.

## Prior art (web research, 2026-07)

- **Claude Code "channels" — the direct precedent.** A channel is an
  MCP stdio server that is both a tool provider and an event source:
  the role is opted in by a handshake capability, events are
  fire-and-forget notifications injected as host-tagged context,
  replies go through an ordinary tool, and consent for the event role
  is SEPARATE from tool consent (being configured is not enough; the
  server must also be named per session). Official Telegram/Discord/
  iMessage plugins ship exactly as "connector+extension in one
  process". Their stated security doctrine: "an ungated channel is a
  prompt injection vector — gate on sender identity, not room
  identity."
- **MCP core is moving the other way** (2026 drafts): server-initiated
  requests are being replaced by data-in-results round trips (MRTR),
  sampling deprecated. But the driver is stateless multi-instance HTTP
  deployment — none of which applies to a host-spawned stdio child.
  For local subprocess plugins the duplex-channel pattern is alive and
  well (LSP, ACP, Claude Code channels). A Triggers & Events WG is
  chartered to standardize the event-source role as an MCP extension;
  worth watching, nothing shipped.
- **Non-AI blueprints agree.** Matrix application services, Slack apps
  (Events API + Web API, one manifest), Home Assistant integrations
  (entities + services + triggers on one bus), n8n packages (trigger
  node + `usableAsTool` action node in one package): mature ecosystems
  converge on one logical component with role-scoped channels and
  separate authorization per role.
- **Named failure modes** to design against: echo loops (a bridge
  relays the agent's own outbound back as inbound — Matrix bots reply
  `m.notice` precisely so other bots ignore them); backpressure when
  events arrive mid-turn (LangGraph names the option space: reject /
  enqueue / interrupt / rollback; Claude Code queues and batches);
  and the trust shape — a dual-role plugin is structurally Willison's
  "lethal trifecta" in one package (it injects untrusted content AND
  ships the tools that can exfiltrate), so role consent and sender
  gating are load-bearing, not polish.

## Design space

**A. Connector role as an extension capability (chosen).** One
process, one wire (`extproto`), role declared in the manifest and
registered at runtime; the host adapts it into a `chat.Connector`.
Matches the Claude Code channels shape and terva's existing seams.

**B. One process, two channels.** Spawn once, speak `extproto` on
stdio and `connproto` on an extra fd/socket. Keeps protocols pure but
doubles lifecycle/identity stitching, needs new transport plumbing,
and loses the ordered-single-writer property that the v2 "session_start
before first tool_call" guarantee rides on. The Matrix/Slack shape is
right for networked services; wrong for a host-spawned child.

**C. New unified protocol v-next.** A superset "plugin protocol"
subsuming both. Maximum conceptual cleanliness, maximum migration
cost: two frozen wire-compat invariants (`zot_version` in both hellos,
golden-frame tests, two independent version-negotiation styles) for a
benefit A already delivers additively.

Choice: **A**, with two rules borrowed from the host-owned-triggers
camp: the event role gets separate consent, and the HOST decides how
an inbound message becomes a turn (the existing `chat.Loop` gate and
queue — never the plugin).

## Wire design (extproto v5) — second pass: the tunnel

> The first pass mirrored connproto's vocabulary as `chat_*` frames on
> the extension wire. It worked (CI-green, e2e-proven), but it created
> exactly the maintenance shape we must not ship: TWO implementations
> of the connector wire per side (frames defined twice, session state
> machines written twice, `connsdk.Transport` cloned as
> `ext.ChatTransport`), where every future connproto feature —
> reactions, edits, threads, presence — would have to be hand-mirrored
> into extproto, the driver, the adapter, and the SDK. Both protocols
> are still growing; the second pass replaces the mirror with a tunnel.

The extension protocol never learns chat vocabulary. It adds ONE
role declaration and a four-frame envelope; every chat semantic rides
inside as an opaque, verbatim `connproto` frame:

Registration (register phase, alongside `register_tool` — no payload;
capabilities travel in the inner hello):

```json
{"type":"register_connector"}
```

The envelope (session-scoped; `id` is a host-minted session id so a
close/reopen can never interleave stragglers into the next session):

| type | dir | purpose |
|---|---|---|
| `chat_open` | host→ext | start a session: the extension boots its connector engine, whose first output is the inner connproto `hello` |
| `chat` | both | one connproto frame, verbatim, in `frame` (RawMessage; neither side's extension layer parses it) |
| `chat_close` | host→ext | end the session host-side; the engine unwinds, the process and its tools live on |
| `chat_down` | ext→host | the engine exited — with `error` (receive stream died permanently) or without (orderly teardown) |

The INNER protocol is the connector protocol, complete: `hello` /
`hello_ack` (so connproto's own negotiated versioning applies inside
the tunnel — connproto can go to v2 with **zero** extproto changes),
`connect`/`connected`/`connect_error`, the `message` stream, `send*`/
`result`, `warn`, `shutdown`. `hello_ack.data_dir` is the extension's
data dir, so attachment flow is byte-identical to a standalone
connector's.

What `chat_down` replaces: connproto signals "connector broken" by
process exit + the proxy's restart budget. A dual-role process must not
die (or be blindly bounced — the model's tool registry would desync)
because its connector half failed, so the failure gets an envelope
frame and the tools keep serving.

Versioning: everything here degrades gracefully on an old host (the
frames are ignored; the extension's tools still work — the same
"tools work, channel messages won't arrive" split Claude Code chose).
Bump `ProtocolVersion` to **5** purely for feature detection
(`Host().ProtocolVersion >= 5`), the same rationale as v4's bump. No
`RequireProtocol(5)` needed for the role itself; an extension that is
USELESS without the connector role may still declare it.

Wire-compat invariants untouched: no changes to `hello`/`hello_ack`
fields; `zot_version` bridge unaffected (in BOTH protocols — the inner
hello_ack carries it too); golden-frame tests pin the envelope in
extproto and the vocabulary in connproto, with no overlap.

## Host design

**One session core.** The host side of a connproto session —
handshake + version negotiation, frame dispatch, pending-send
correlation, attachment ingestion/containment, warns — lives in ONE
place: `chat/connhost.Session`, extracted from `external.Proxy`. It
speaks through a two-method carrier interface (`FrameConn`:
`ReadFrame`/`WriteFrame`), so it neither knows nor cares what moves
its frames:

- `chat/external.Proxy` composes it over a child process's stdio and
  keeps everything process-shaped: spawning, the crash/restart budget,
  log files, reaping. (The proxy's whole test suite passed unchanged
  against the refactor.)
- `chat/extconn.Conn` composes it over the extension tunnel.

**Manifest.** `extension.json` gains `"connector": true`. The flag is
the visible consent surface: the host refuses a `register_connector`
frame from an extension whose manifest doesn't declare the role (a
tool-only extension cannot quietly grow into a message source after
install). Manifest-declared but never registered is fine (e.g. not
configured yet).

**extdriver.** Stays dependency-light AND semantics-free: it gates the
role (manifest consent), then only moves bytes. `Extension.OpenChat()`
mints the session id, sends `chat_open`, and returns a `ChatTunnel`
(the `FrameConn` for this carrier); the readLoop routes `chat` payloads
to the live tunnel by session id (bounded queue; overflow kills the
session loudly — a dropped inner frame would desync the protocol, so
there is no silent drop) and `chat_down`/process-exit close it. No
pending maps, no chat types, no `chat` import.

**chat/extconn.** `extconn.Conn` implements `chat.Connector`:
resolve the live extension via the bound Host → `OpenChat()` →
`connhost.Session` over the tunnel (hello → connect → receive).
Service registration happens at CLI startup next to
`external.RegisterDiscovered`: scan installed extension manifests for
`"connector": true` and `chat.Register` a service per name (required
because `runBotCommand` resolves the service by name BEFORE `botRun`
spawns extensions). The live extension process is bound at `Connect`
time via `extconn.BindHost(manager)` after each mode's extension
discovery. `Connect` fails with a clear error when the extension
didn't load or never registered the role.

**Lifecycle asymmetries, resolved explicitly:**

- *Restart*: external connectors get a crash budget (3/60s); dual-role
  processes now get THE SAME budget with a host-mediated respawn.
  What made the v1 design refuse this — "respawning would desync the
  model's tool registry" — turned out to be wrong for the crash case:
  extension tools dispatch BY NAME through the driver's live index at
  call time (`exttool` → `InvokeTool`), so a respawn of the same binary
  re-binds the agent's existing wrappers with no registry surgery. The
  machinery: `extensions.Manager.RestartExtension` (StopByName reaps
  the crashed entry — the loaded set keeps it otherwise — then the
  ApplyOne spawn path re-registers everything and fires onReload), and
  `extconn` exposes it via the optional `RestartHost` interface. Two
  failure classes share one budget in `Receive`: session death with a
  reason (`chat_down` — process alive, tools serving) only REOPENS the
  session (the engine re-dials via a fresh transport, standalone
  parity for the fatal-receive → exit → respawn loop); a reasonless
  end (process death) respawns first. Where the bound host cannot
  respawn (a bare driver), process death stays immediately permanent
  with an error saying so.
- *Spawn*: the extension Manager owns the process in every mode. The
  connector adapter never spawns; it attaches. (`terva bot setup`
  /`status`/`reset` verbs: v1 delegates to the extension's own config
  flow — `/extensions config` fields in the manifest — rather than the
  connproto verb convention.)
- *Attachments*: inbound file paths must resolve inside the
  extension's data_dir (symlink-escape containment, read-and-delete) —
  now literally `external.Proxy`'s code, shared via `connhost`, not a
  port of it.

## Trust & safety

The bot daemon defaults to yolo approval, and this feature hands
turn-initiation to any installed extension that declares a manifest
flag. Defenses, layered:

1. **Separate consent per role.** Manifest `"connector": true`
   (install-time visibility) + the role only ACTIVATES when a chat
   consumer explicitly selects it: `terva bot run --connector <ext>`
   or a TUI bridge connect. An extension never becomes a message
   source as a side effect of being installed. This is `--channels`,
   terva-shaped.
2. **Global-only.** Project-local extensions never get the connector
   role, trusted or not — a cloned repo must not be able to declare
   itself a message source. (Discovery-time gate, same class as the
   workspace-trust RCE fix.)
3. **Sender gating is host-owned.** Inbound messages flow through the
   existing `chat.Loop` gate: pairing (first `/start` claims),
   allowlist, `/stop`. The plugin cannot bypass it — the gate sits
   between the adapter and the agent. Gate on sender identity, not
   chat identity.
4. **Host-stamped provenance.** The host knows which extension a
   message came from (process identity, not frame content); anything
   surfaced to the model or logs is attributed by the host.
5. **Reach, not authority.** The connector role grants message
   delivery INTO the gate and reply delivery out. It does not touch
   the tool-approval ladder, `host_tool_call` policy, or authority
   classes.
6. **Echo-loop hygiene** is the transport author's job (filter own
   outbound; the SDK docs must say so), and the Loop's single-flight
   queue bounds the blast radius (one turn at a time, batched queue).

## SDK design

There is no second SDK. The author surface IS `connsdk.Transport` —
the same interface a standalone connector implements — and the engine
that drives it is `connsdk.Serve`, the standalone SDK's protocol loop,
exported and run over an in-process pipe pair whose lines the `ext`
run loop pumps into `chat` envelopes:

```go
e := ext.New("chatterbox", "0.1.0")
e.Tool("history_search", ..., handler)      // extension half, unchanged
e.Connector(connsdk.Capabilities{MaxTextLen: 4096},
	func(s connsdk.Session) (connsdk.Transport, error) {
		return newTransport(s.DataDir), nil   // per-session, lazy
	})
e.Run()
```

Moving a connector between the two packagings is a ~5-line change of
`main` (`connsdk.Main(cfg)` ⇄ `ext.New(...).Connector(...)`); the
transport code — and the wire it produces — is identical.

The engine dials and receives on its own goroutines, so a slow service
call never blocks a concurrent `tool_call` on the same stdin — the
reentrancy case: a turn started by this connector may call this same
extension's tools mid-turn; that must not deadlock, and has a test.

One engine improvement fell out of the unification (fixed once,
benefits both packagings): `connsdk.Serve` used to notice a fatal
transport receive death only when the HOST next sent a frame — it sat
blocked on stdin. It now selects on the reader and the receive-error
signal, so a dead transport ends the session immediately: prompt
restart-budget for standalone connectors, prompt `chat_down` for
extensions.

## Turn semantics

Inherited wholesale from `chat.Loop`: single turn in flight, arrivals
queue, `/stop` cancels, idle-nudge unchanged, ONE agent + ONE session
per bot process. This proposal adds a message *source*, not new turn
semantics. (Per-chat sessions, interactive-TUI wake — see future work.)

## What this does NOT do (scope discipline)

- `connproto` stays. Pure connectors (no tools) should stay on the
  simpler protocol; nothing migrates, nothing deprecates.
- No merged supervisor, no shared manifest format, no protocol v-next.
- No general "wake the agent" primitive in interactive sessions
  (`submit_slash` already exists; a `submit_prompt` analog is future
  work with its own consent story).
- No permission-relay-over-chat (Claude Code's channel permission
  capability). Powerful, scary, separate proposal.

## Future work

- ~~TUI bridge to a connector extension~~ — done: `/connect` is
  service-aware (one picker row per configured service, provenance
  tags, `/connect <name>` direct form); a connector extension bridges
  into the live TUI session like any compiled-in connector.
- ~~Dual-role restart semantics~~ — done: crash respawn under the
  proxy's budget (see the *Restart* lifecycle note above);
  `Manager.RestartExtension` + `extconn.RestartHost`.
- ~~Per-chat session identity~~ — done: connproto v2 stages A
  (message/chat identity), B (group admission), and C (per-chat
  sessions) all shipped; see the staging list in
  docs/proposals/connector-protocol-v2.md.
- Event-source generalization: the inner `message` frame is one event
  kind; a webhook/cron/file-watch extension wants the same wake path
  with a different payload. The tunnel makes this a CONNPROTO
  conversation (grow the inner protocol once, both packagings get it).
  → deliberately HELD: docs/decisions/0002-connector-event-sources-hold.md
  records why, the MCP context, and what reopens it.
- Idle-nudge per-connector config/cap (open item; unchanged here).
- Graduation out of experimental: the envelope is frozen in practice
  (zero changes across the whole connproto-v2 build-out), but the
  label stays until a real connector ships over the tunnel — the
  loopback demo proves the wire, not the packaging, and the in-tree
  Discord connector deliberately dogfoods connproto over the
  in-process carrier instead. Candidates: the Matrix connector, or
  migrating a built-in to the extension packaging. Graduating is then
  one docs commit: drop the experimental markers in extensions.md and
  the envelope rides the extension wire's normal negotiation compat.

## Assessment (the requested pushback, grounded)

The idea survives grounding well: the pattern is proven in production
by Claude Code channels, echoed by every mature plugin ecosystem
(Matrix/Slack/HA/n8n), and terva's architecture is unusually ready for
it — the extension wire already multiplexes bidirectional traffic and
the chat stack already abstracts transports. The additive version
(role-as-capability) carries low regret: if it turns out wrong,
`connproto` still exists and nothing was migrated onto the new frames.

Two honest cautions, neither fatal:

1. **The security surface is the feature.** One flag turns a
   tool-provider into a prompt-injection source feeding a
   yolo-approval agent that also holds the exfiltration tools. The
   defenses above (role consent + explicit activation + global-only +
   host-owned gate) are the actual deliverable; the frames are easy.
   If any of the four is dropped for convenience later, this becomes
   terva's biggest hole.
2. **Restart semantics are genuinely worse for dual-role processes** —
   this was the second caution, and it is now RESOLVED rather than
   accepted: the "can't bounce without desyncing the tool registry"
   fear was wrong for the crash case (tools dispatch by name through
   the live driver index), so dual-role processes get the standalone
   crash budget with a host-mediated respawn. The residual truth in
   the caution: a respawn that registers a DIFFERENT tool set (an
   upgraded binary mid-session) is registration drift the onReload
   hook surfaces but the bot-mode agent won't fully absorb; crash
   recovery assumes the same binary comes back.
