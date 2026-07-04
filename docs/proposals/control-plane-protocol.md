# Proposal — ctrlproto: terva's control-plane protocol (interface-first)

- **Status:** IN PROGRESS. The v1 slice has landed in `packages/agent/ctrlproto`
  (always-built, carrier-agnostic): the `WorkspaceService` interface, the framed
  envelope + hello/capability negotiation, the `AgentEvent`-based `Event` union,
  and the generic `ServeConn` loop — with a WebSocket carrier (`terva web`) as
  the first consumer. Stages D+ remain: the control group, the TUI in-process
  carrier, the ext-tunnel carrier, and the fleet.
- **Date:** 2026-07-03
- **Scope:** a new **Go interface** — the control plane / workspace service — that
  expresses "drive and manage a terva agent (or a set of them)", plus **one wire
  serialization** of it (`ctrlproto`) and a set of **pluggable carriers**. v1
  implements the *conversation* and *session* method groups over a WebSocket
  carrier and an in-process carrier, consumed by `terva web`. The *conversation*
  group reuses the existing `core.AgentEvent` vocabulary
  (`packages/core/events.go` + `wire.go`) verbatim.
- **Origin:** `terva web` needs a bidirectional protocol, and none of the four
  wires we already have fit its shape: **ACP** is editor-shaped, **connproto** is
  chat-shaped (and discards tool fidelity), **extproto** is host↔extension, and
  **raw JSON-RPC** is too thin to carry a domain. Rather than invent a one-off web
  wire, this specs a **general control plane** designed to eventually drive the
  TUI, tunnel over extproto, and command a fleet — so the effort lands once and
  reaches every future frontend.
- **Prereq reading:** docs/connectors.md + docs/proposals/connector-protocol-v2.md
  (the carrier / negotiation / framing **meta-architecture** this deliberately
  reuses); docs/proposals/terva-web.md (first consumer);
  docs/proposals/terva-platform.md (the fleet / multi-user endgame this unlocks).

## TL;DR

The control plane is defined as a **Go interface first, a wire second.** The
interface (a `WorkspaceService`) is "everything a frontend needs to drive and
manage terva": run turns, manage sessions, and — additively — manage models,
lore, extensions, prompt overrides, and templates. `ctrlproto` is one
serialization of that interface; the same interface has **multiple carriers**:

- **in-process** → the TUI (no serialization — this is why interface-first
  matters),
- **WebSocket** → `terva web`,
- **stdio** → a CLI / fleet controller,
- **ext-tunnel** → extensions (the same `chat_open`/`chat`/`chat_close` trick
  connproto already uses over extproto).

We reuse the `AgentEvent` vocabulary for the streaming conversation half and
design a new bidirectional command envelope for the rest, organized into
capability-negotiated **method groups** (conversation / session / control). v1
ships only conversation + session over WS; everything else grows additively.

## Why a new protocol (why not skin one we have)

Reusing an existing wire here is "skinning a rabbit with a spork" — wrong shape,
wrong tool. Concretely:

| Candidate | Why it doesn't fit |
|---|---|
| **ACP** (editor protocol, `terva_acp`) | Editor-shaped: session/prompt/permission are there, but the control plane (lore/extension/template/model management) is entirely outside its domain. Extending it that far is semi-forking an editor standard to carry things it was never meant to. Worth *studying* (its permission/prompt round-trip is well-solved) — not reusing. |
| **connproto** (chat wire) | Chat-shaped: it drops tool-call events and buffers replies to turn-end. Its **vocabulary** is wrong for a control panel. But its **architecture** is exactly right — see below. |
| **extproto** (host↔extension) | A different axis (host talks to extensions). It is a future *carrier* of ctrlproto (via tunneling), not the protocol itself. |
| **raw JSON-RPC** | Just a framing convention — no streaming-event ergonomics, no domain vocabulary, no capability model. We'd be building ctrlproto on top of it anyway. |

**But reuse the connproto *playbook*.** connproto's vocabulary is wrong for us;
its meta-architecture is the proven template we should copy wholesale:

- a **wire spec** + an **SDK interface** (`connsdk.Transport`) + **pluggable
  carriers** (stdio proxy, in-process `connlocal`, ext-tunnel),
- **capability / feature negotiation** in the hello handshake (grow additively via
  feature strings, not version-lock),
- **framing**: id-correlated commands with responses, uncorrelated streamed
  events.

We built this layering once already for connectors. ctrlproto is the same layering
with a richer, non-chat vocabulary.

## The core principle: interface-first, wire-second

> Define the control plane as a Go interface. The wire is one serialization of
> that interface. Different clients pick different carriers of the **same**
> interface.

This single decision dodges the two traps in the "make it the universal protocol"
vision:

1. **The TUI-migration goal must not re-introduce a CPU regression.** The
   streaming CPU creep we already root-caused came from re-parsing tool-call JSON
   in the render hot path. If "migrate the TUI onto the protocol" meant
   serialize→deserialize every event in-process, we'd rebuild that cost by
   construction. Interface-first means the TUI migrates to the **interface** and
   calls the in-process implementation directly — **zero serialization** — while
   web/stdio/ext clients use the *wire* serialization of the same interface.
   "Drive the TUI through the control plane" becomes the protocol's
   **completeness test** (if the interface expresses everything the TUI needs,
   it's complete) instead of a performance regression.

2. **"New protocol" tempts a from-scratch design.** Interface-first plus the
   connproto playbook means most of the structure is inherited, and the vision
   falls out as *carriers* rather than rewrites (see Carrier model).

The interface is the artifact that matters; the wire is generated from / mirrors
it. Naming: the wire is **`ctrlproto`** (parallels `connproto`/`extproto`); the
Go interface name (`WorkspaceService` / `ControlPlane`) matters more than the
wire's.

## Method groups

The interface is organized into three groups with different rates of change and
different audiences. A client negotiates which groups it speaks; a minimal client
(the mobile PWA) implements only the first two.

| Group | What it carries | v1? |
|---|---|---|
| **conversation** | the `AgentEvent` stream (out) + turn commands: prompt, cancel, queue, answer-an-ask, approve-a-tool (in) | **yes** |
| **session** | list / create / resume / rename / delete / switch sessions; usage / cost readout | **yes** |
| **control** | models (list/switch beyond the basic), lore CRUD, extension enable/configure/install, prompt overrides, templates/profiles, jail toggle | v2+ |

Keeping these as separable groups is what lets the same protocol serve a
focused mobile client, a full desktop control panel, and a fleet controller
without any of them implementing surface they don't use.

## Carrier model

Reusing the connproto carrier pattern, the same interface is bound to transports:

- **in-process carrier** — the TUI (and any embedding host). Direct interface
  calls, no serialization. The completeness test lives here.
- **WebSocket carrier** — `terva web`. Bidirectional, streaming, browser-native.
  The v1 target.
- **stdio carrier** — a CLI, scripts, or a fleet controller driving one agent per
  child process. (This is also where an ACP-compat shim could live if we ever want
  editor interop, without polluting the core vocabulary.)
- **ext-tunnel carrier** — extensions, via the identical `chat_open` / `chat` /
  `chat_close` envelope trick connproto already uses over extproto. This is how a
  future extension builds a whole new frontend (see terva-platform.md).

"Expose the control plane over the extension protocol, like chat is" is therefore
not new protocol work — it's one more carrier binding.

## Envelope & framing

Reusing connproto's framing philosophy, with two forward-compat additions baked
in from day one even though v1 is single-user / single-agent:

- **Framed messages** (WS frames or LF-delimited JSON depending on carrier) with a
  `type` discriminator; **commands carry an `id`** for request/response
  correlation; **events are uncorrelated** and streamed.
- **Addressing in the envelope.** Every frame carries a **session/agent id**. v1
  only ever has one workspace and a handful of sessions, but putting addressing in
  now is nearly free and is exactly what makes the mobile-subset client ("I speak
  conversation + session for *this* session") and the fleet controller ("I address
  N agents") work later **without a reframe**. Retrofitting addressing into a
  shipped protocol is the painful path.
- **Capability negotiation in `hello`.** Each side declares which **method groups**
  and which **features** it speaks; the intersection is the contract. New
  capabilities land as additive features (the connproto v2 approach) rather than
  version bumps. A host that gains lore CRUD advertises it; an old client simply
  never uses it.

## Conversation group (reuse, don't reinvent)

The streaming half is the hard part, and it already exists as one vocabulary
shared with core and the TUI: `core.AgentEvent` (`events.go`) with
`wire.EventToWire` (`wire.go`) as its ready JSON serialization —
`EvTextDelta`, `EvToolUseStart/Args/End`, `EvToolCall`, `EvToolResult`,
`EvToolProgress`, `EvUsage`, `EvAssistantMessage`, `EvTurnEnd`, `EvDone`,
`EvError`, `EvCompactStart/End`. ctrlproto's conversation events **are** these,
carried on the wire.

The new part is the small set of **inbound turn commands**:

- `prompt {text, images}` → drives `PromptWithPolicy`,
- `cancel` → cancels the turn `ctx`,
- `queue {text}` → `QueueMessage` (the fan-out / multi-device story),
- `answer {ask_id, ...}` → satisfies a `core.Asker` round-trip,
- `approve {call_id, decision, scope}` → satisfies a `core.Confirmer` round-trip
  (the channel-parked pattern from `acp/permission.go`).

## Session group

Session lifecycle, most of which already has primitives (`OpenSession`,
`SessionsDir`/`CWDHash`, `SessionMeta` title + rename rows, `SetMessages`):

- `sessions.list` → id, nickname/title, created/updated, model, cost summary,
- `sessions.resume {id}` / `sessions.create {template?}` / `sessions.switch {id}`,
- `sessions.rename {id, nickname}` (the format already supports rename rows),
- `sessions.delete {id}`,
- `usage.get {id}` → per-session cost/tokens from `cost.go` + `EvUsage` history.

## Control group (v2+)

The management surface that makes this a *control panel*, deferred past web v1 but
shaped now so it slots in additively:

- `models.list` / `models.switch {id}` (switch is live via `SetClientAndModel`),
- `lore.*` — list / read / write / validate entries (files on disk; the daemon
  writes them and re-`Resolve`s),
- `extensions.*` — list / enable / disable / configure / install (subject to the
  reload story — see terva-web.md open questions),
- `prompt.override` — persona / intro override / additive charter (session-creation
  time; mid-session needs a `SetSystem` core mutator),
- `templates.*` — save / list / apply a named `{persona + model + prompt-override +
  lore set + extension set + approval policy + experience mode}` bundle,
- `jail.set {on|off}` — the guardrail toggle.

## Config threading (a cheap forward-compat down-payment)

Today config resolves through process globals (`LoadConfig()` is called at leaves
like `effectiveApprovalMode`; `TervaHome`/`pinnedGlobalHome` are process-wide).
For a single-user daemon that is **correct and fine** — do not fix multi-tenancy
now. But have the workspace service **hold a `Config` value and thread it** rather
than calling globals at the leaf. That is single-user-correct today and the
cheapest possible down-payment on the fleet / multi-tenant future (where each
agent needs its own config), and it pays down exactly the debt the feasibility
review flagged.

## v1 slice / staging

- **Stage A — envelope + hello.** Framing, id-correlation, addressing,
  capability negotiation; WS carrier + in-process carrier. Golden-frame tests (the
  connproto v2 discipline).
- **Stage B — conversation group.** `AgentEvent` stream out; prompt / cancel /
  queue / answer / approve in. Confirmer + Asker over the wire (the
  `acp/permission.go` channel-parked pattern). This is the Phase-0 spike in
  terva-web.md.
- **Stage C — session group.** list / resume / create / switch / rename / delete +
  usage. This completes the terva-web.md Phase-1 daily driver.
- **Stage D+ — control group.** models / lore / extensions / prompt / templates /
  jail, landed additively as features, gated by capability negotiation.
  *(Shipped so far: `models.list/switch/favorite`, `control.restart`,
  `control.trust` / `control.untrust` — workspace-trust grant/revoke that brings
  project extensions/lore/permission rules live across open sessions, with the
  state on `SessionInfo.trusted`. Also `clear` in the conversation group and
  `CreateOpts.Persona` honored on session create. Still open: jail, prompt/system
  overrides, templates, persona/mode switching on an existing session, login.)*

## Roadmap (beyond web v1)

- **TUI migration = the completeness test.** Once the interface expresses
  everything the interactive loop needs, migrate the TUI to the **in-process
  carrier**. No serialization on the hot path; one code path for "drive terva"
  across every frontend. Sequence this *after* web v1 — it is validation, not a
  blocker.
- **ext-tunnel carrier.** Expose ctrlproto over extproto so extensions can build
  whole frontends. **This must be per-method-group authority-gated** (see below).
- **Fleet.** A dedicated control plane that holds many carriers (or one carrier
  multiplexing many agents), addressing each by the envelope's session/agent id.
  This is the substrate for the multi-user / SillyTavern horizon in
  terva-platform.md, and it ties into the existing swarm/agent-dispatch work
  (which already spawns terva agents as subprocesses — ctrlproto is the clean way
  to *observe and manage* them versus fire-and-collect).

## Security & authority

- The conversation + session groups are frontend-driving; the **control group is
  host-reconfiguring** — switch your model, edit your lore, rewrite your prompt,
  spawn sessions. That is a categorically higher authority than the bounded chat
  tunnel.
- **When ctrlproto is tunneled over extproto, the control group must be
  authority-gated per method group**, hooking the existing authority classes
  (`local-read` / `local-data` / `workspace-mutation` / `process-execution` /
  `network-read` / `external-mutation`). An extension that speaks the conversation
  group is not thereby allowed to reconfigure the host. Bake the *assumption* of
  per-group gating into the design now so it is not bolted on under pressure later.
- Over the WS carrier for `terva web`, the auth gate (terva-web.md) is the
  boundary; there is no per-group gating for the single trusted user, but the
  group structure means we *could* later hand a mobile client a
  conversation+session-only capability set.

## Open questions

1. **Serialization format.** JSON everywhere (debuggable, matches connproto) vs.
   a compact binary (msgpack/CBOR) for the WS carrier's high-frequency
   `EvTextDelta` stream. Lean JSON for v1; measure before optimizing.
2. **One `hello` or per-group handshakes.** Single negotiated hello (simpler) vs.
   lazily negotiating a group when first used (more flexible for the ext-tunnel
   carrier). Lean single hello for v1.
3. **How much ACP compatibility, if any.** A stdio ACP-compat shim would let
   editors drive terva-web's backend. Worth it only if editor interop is a real
   goal; otherwise it's surface we don't need. Deferred — the carrier model keeps
   it addable without touching the core vocabulary.
4. **Interface granularity for the fleet.** Does a fleet controller address N
   agents through one `WorkspaceService` with an agent-id parameter, or hold N
   single-agent services? The envelope carries the id either way; the Go interface
   shape is the open call.
