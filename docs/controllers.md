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

> **Status.** Out-of-process *programmatic* control **is available**. Three
> shipped commands are ctrlproto clients of a running terva: `terva attach`
> (`attach_mode.go`), `terva ctl` (`ctlcmd.go`) and `terva ext config`
> (`extconfigcmd.go`). They dial through `ctrlproto/ctrlclient` over the
> WebSocket carrier — TCP **or a unix socket** — and authenticate with a token.
> `ctrlclient` is deliberately connection-count-agnostic and rendering-free, so
> a fleet or orchestration frontend can hold N of them.
>
> Still **not** built: the **stdio/pipe carrier** (a client connects to a
> daemon; it cannot `spawn` one and pipe it), the **extension-tunnel carrier**,
> and **multi-instance management** — one daemon serves one workspace, and the
> relay is design-of-record only. See
> [Carriers & current state](#carriers--current-state).
>
> **Authorization is all-or-nothing today.** Group negotiation gates which
> methods a client may *call*, but a valid token grants whatever groups that
> client declares — including `control` (trust, restart, approval mode).
> Per-group *authority* gating is designed and unimplemented, and it is the
> prerequisite for handing a token to anything less trusted than yourself. See
> [Security & authority](#security--authority).

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

### Optional controllers and the forwarder's completeness

Capability verbs beyond the base `WorkspaceService` hang off **optional
controller interfaces** — `CardsController`, `BackgroundsController`,
`CastController`, `NoteController`, `UserController`, … — which `ServeConn`
type-asserts at dispatch and answers `unsupported` for when a carrier does not
implement them. Two things implement these: the real daemon-side `Workspace`,
and `ctrlclient.Service`, the **wire-client forwarder** that lets a Go client
(the TUI, `terva attach`) drive a *remote* daemon through the same interface,
turning each call into a frame.

The forwarder does not implement every controller. The Stage/play-immersion
controllers — **cast, author's note, user persona, `turn.continue`, replay** —
are **web-only today**: no Go client drives them, so the forwarder deliberately
omits them (a future "TUI Stage" would add the relevant ones — cast most
plausibly, if it rides the swarm-spawn machinery). Which controllers the
forwarder serves is therefore a real decision, not an accident, and
[`forwarder_complete_test.go`](../packages/agent/ctrlproto/ctrlclient/forwarder_complete_test.go)
enforces it: it reads every `*Controller` interface from source and requires
each to be in exactly one bucket — **forwarded** (carrying the compile-time
`var _ ctrlproto.XController = (*Service)(nil)` assertion, which itself proves
the implementation) or on the test's **`notForwarded` allow-list with a
reason**. A newly-added controller fails the test until its author categorizes
it, which closes the silent gap where a forwarded-but-unasserted method — or a
never-forwarded one — would simply `unknown`/404 from a Go client with nothing
flagging it.

## Carriers & current state

The same `WorkspaceService` is bound to different transports:

| Carrier | Client | Status |
|---|---|---|
| **in-process** | the TUI / any embedding host — direct interface calls, no serialization | **shipped, the only TUI backend**: the TUI binds `Workspace` through the `modes.Carrier` seam (PR #14; the legacy direct driver has since been removed, `--tui-legacy` is a deprecated no-op). The `AgentFor` crutch is **gone** (remediation plan 4.1, complete — see `modes/carrier.go`): the TUI holds no `*core.Agent`, and its whole control path is ctrlproto — prompt dispatch, the event stream, approvals/asks, transcript rendering, `/context`, `/permissions`, `/settings`, `/clear`, `/model`, login, side chat. Honest caveat: file-based session fork/tree/export stays local (out of wire scope v1) |
| **WebSocket** | [`terva web`](web.md) (browser-native), and the out-of-process Go clients `terva attach` / `terva ctl` / `terva ext config` via `ctrlclient`. Bidirectional, streaming; dialable over TCP or a unix socket | **shipped** |
| **stdio** | a CLI, scripts, or a fleet controller (one agent per child) | designed, **not built** |
| **ext-tunnel** | extensions, over extproto (the `chat_open`/`chat`/`chat_close` envelope trick connproto already uses) | designed, **not built** |

Adding a carrier is a binding, not new protocol work.

## Method groups

The surface is split into six **capability-negotiated groups** with different
rates of change and audiences. A client declares which groups it speaks in its
hello; a minimal client (the mobile PWA) negotiates only the first two.

| Group | Carries | Authority |
|---|---|---|
| **conversation** | the event stream (out) + turn commands: prompt, queue, cancel, compact, clear, the transcript-revision verbs (edit/delete/retry/swipe), approve, answer, subscribe — and the directed-authorship verbs (`post.line`, `direct.turn`, `turn.advance`) and variant cleanup, which mutate a turn or run one | frontend-driving |
| **session** | session lifecycle (list/create/resume/fork/rename/delete/archive/restore/export), usage (incl. snapshots and reset credits), context breakdown + tree nodes, side chat, surfaces, i18n catalog, the workspace file listing, the read-only provider-credential view, and the model-backed advisory verbs (suggest, the doctors, next scene, realize) | frontend-driving |
| **control** | host-reconfiguring management: models (incl. per-model overrides and defaults), trust, restart, reset-credit redemption, the content library (cards, personas, user personas, backgrounds, groups, Worlds), World lore and settings — and (future) prompt/system overrides, templates, jail | **categorically higher** — see [Security & authority](#security--authority) |
| **auth** | MODEL-PROVIDER credential mutation: establish, repair, and revoke the credential terva uses to reach Anthropic / OpenAI / Kimi, plus forgetting a named endpoint | **categorically higher, and separate from control** — see below. **Optional**: off the base server hello; `terva web` advertises it only under `--web-allow-login`, and never on an unauthenticated listener |
| **secrets** | terva's **at-rest** encryption posture (what is sealed, what is still plaintext, which components hold a key) and the secret store's grant model | **categorically higher, and separate from auth** — see below. **Optional**: off the base server hello; `terva web` advertises it only under `--web-allow-secrets`, and never on an unauthenticated listener |
| **replay** | a recorded session's transport (`replay.control` / `replay.state`) and its `replay_state` broadcast | frontend-driving, but **optional**: it is off the base server hello — only a carrier backing a `ReplayController` advertises it, so a client that negotiates it is guaranteed the group is served |

`auth` is its own group rather than a corner of `control` because **the group is
the unit of authority gating**. An extension granted `control` can switch models
and edit lore; it must not thereby be able to replace your Anthropic token.
Reading credential state grants no authority and is not here — `auth.providers`
sits in `session`, and nothing on this wire ever hands a secret back out.

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
| `history-window` | windowed snapshots: `Snapshot.messages` carries the last 80 messages rather than all of them, plus the `epoch`/`base`/`total` it was cut at, and the client pages backward with `conversation.history`. Cut at the connection boundary per contract, next to where `image-data` is stripped — a client that does not negotiate it receives the whole transcript exactly as before, so there is no flag day. Worth having because a snapshot rides every `subscribe` **and the end of every turn**: free in-process (slices are shared), the entire conversation again over a phone's WebSocket. `(epoch, index)` is a message's stable identity — `epoch` bumps only on a wholesale replace (compact, `/clear`), never on an append — so a windowed client MERGES a snapshot into what it holds instead of rebuilding, and treats a new epoch as the signal to rebuild |
| `resolve-events` | the `permission_resolved` / `ask_resolved` multi-client dismissal events |
| `context-tree` | `context.get` carries the hierarchical `ContextBreakdown.Tree` (section/turn/message outline), lazily expanded with `context.node`. A client that doesn't see it falls back to the flat message list |
| `restart` | the daemon will serve `control.restart` (advertised only under `--allow-restart`; `--web-allow-restart` is an accepted alias) |
| `stage` | the daemon serves the Stage immersive chat/play app at `/stage/` (advertised under `--web-stage` or the `web_stage` config knob); a client reads it to offer an "open in Stage" link. See [web.md](web.md#stage-the-immersive-chatplay-surface) |

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
| `message.edit` | `{epoch, index, text}` | replace the message at `index` with same-role text (edit in place); epoch-guarded — `conflict` if the transcript shifted under the client |
| `message.delete` | `{epoch, index}` | remove the message at `index` (later messages shift down); epoch-guarded |
| `turn.retry` | `{epoch, guidance?, ignore_prior?}` | regenerate the last response, keeping the current one as a swipeable take; starts a turn (`busy` if one is already running). `guidance` steers that one generation ("shorter", "have her refuse instead") and the daemon shows the model the take being replaced unless `ignore_prior` says not to — omit it and the verb behaves exactly as before. The guidance rides a request-scoped cue and is never persisted: a regenerate's takes all share the prefix they were generated from, so writing it into that prefix would put it in front of takes that never saw it |
| `turn.swipe` | `{epoch, variant}` | make take `variant` of the tail span active — swipe among a regenerated turn's alternatives or a card's pre-seeded greetings; epoch-guarded |
| `turn.continue` | `{epoch}` | extend the trailing assistant message as a provider prefill — the merged text replaces it in place (a replace amend), not a new message. Optional (a `ContinueController`): `unsupported` where unserved, `bad request` when there is no trailing assistant or the provider can't continue a prefill (only Anthropic today; `SessionInfo.supports_continue` advertises it). Starts a turn (`busy` if one runs) |
| `approve` | `{call_id, decision}` | resolve a `permission_request` (first client wins) |
| `answer` | `{ask_id, answer}` | resolve an `ask_request` (first client wins) |
| `post.line` | `{actor?, text}` | commit an APPROVED directed line into the transcript — the "post" half of Stage's suggest → review → edit → post loop. Appends an assistant message attributed to `actor` (empty posts a narrator beat) and **never runs the model**: the text was drafted and approved already, so the daemon only appends, persists, and broadcasts. Optional (a `DirectController`); `CodeBusy` while a turn is in flight, bad request on empty text |
| `direct.turn` | `{text}` | run ONE turn steered by an out-of-character direction — the "apply-as-direction" disposition. Unlike `post.line` this DOES run the model: the direction rides the transcript as a visible `[Direction]` message (the convention `cast.speak` uses) and the model writes the next beat following it. Chat or play; optional (a `DirectController`); `CodeBusy` while a turn runs |
| `turn.advance` | — | run one turn on the transcript exactly as it stands — Stage's "▶ Advance". No params: the point is that nothing is injected. It differs from `direct.turn` in what it PERSISTS (advance writes only the model's reply; its stage cue is request-scoped), and from `turn.continue` in what it PRODUCES (continue extends the trailing assistant in place on Anthropic only, advance writes the next beat as a new message on every provider). The natural next step after a run of `post.line` calls. Optional (a `DirectController`) |
| `variants.prune` | `{epoch, index}` | collapse the message-scoped variant at `index` to its active take and close the position (prune-to-latest): the swipe marker goes away and the other takes stop being switchable. Epoch-guarded like the other revision verbs; bad request when `index` has no variants. Optional (a `VariantsController`) |
| `variants.drop` | `{epoch, index, variant}` | remove ONE take from the variant at `index`, keeping the rest swipeable; the position closes when a single take remains. Same epoch semantics; bad request when `index` has no variants or `variant` is out of range. Optional (a `VariantsController`) |
| `subscribe` | — | stream events for `sess`; the server sends a `snapshot` first |
| `unsubscribe` | — | stop the stream |

**session**

| Method | Params → Result | Effect |
|---|---|---|
| `sessions.list` | → `{sessions:[SessionInfo]}` | list sessions, newest activity first |
| `sessions.create` | `{…CreateOpts}` → `{session}` | mint a session. `CreateOpts` carries title/provider/model/persona and the immersive spec — `experience` (`chat`/`play`), `card`, `cast`, `greeting`, `background` — all persisted to session meta so a restart re-materializes what the session was |
| `sessions.resume` | → `{session}` | load a persisted transcript |
| `sessions.fork` | `{from_index}` → `{session}` | branch the frame's session at `from_index` into a NEW parent-linked child: it keeps messages `0..from_index` and diverges after; the parent is untouched (the wire story for `core.BranchSession`) |
| `sessions.rename` | `{title}` | set the display nickname |
| `sessions.generate_title` | → `{title}` | regenerate the title from the transcript with a one-shot model call — the on-demand sibling of the automatic `auto_title` pass, and the backfill for old untitled sessions. **BLOCKS on the model** (the same synchronous posture as `compact`), and an explicit request overwrites whatever title exists, manual renames included. The caller updates its own row from the result; a live session's other clients converge via `session_updated` |
| `sessions.delete` | — | remove a session + transcript |
| `sessions.archive` | → `{ArchivedSessionInfo}` | compress the transcript into the directory's archive: the session leaves every listing without leaving the disk — the third thing you can do to one, between keeping it in the picker forever and destroying it. The session is closed first if it is live. Optional (a `SessionArchiveController`): a replay carrier has no directory to move anything into and answers `unsupported` |
| `sessions.archived` | → `{sessions:[ArchivedSessionInfo]}` | what is in this directory's archive, newest first. Rows carry the same descriptive fields a live one does — title, model, message count, preview — because an archive of opaque ids is one nobody restores from; `bytes`/`original` are the two facts that exist only once archived. Read-only |
| `sessions.restore` | `{id}` → `{SessionInfo}` | decompress an archived transcript back into the sessions directory, where every listing sees it again. The id travels in **params, not the frame's `sess`**: `sess` is a live handle that subscriptions and turn routing key on, and an id resolving to no live session has no business being addressed as one |
| `sessions.discard_draft` | — | reclaim an unpromoted draft the moment the user navigates away without sending — a fresh Stage chat defers its greeting, so a character opened only for preview stays a meta-only draft. Closes its live session (and any extension subprocesses it holds) rather than waiting for shutdown or the next boot-prune. A guarded **no-op** on anything that is not an unpromoted draft, so it can never discard real work. Optional (a `DraftController`) |
| `sessions.state` | → `{composer?:ComposerDraft}` | the session's durable **client** state, which the daemon keeps beside the transcript at `<session>.state.json`. Today one tenant: `composer`, the message the user typed and did not send. Serving it here rather than in each front end is what makes it ONE draft — the TUI and the web panel see the same unsent text instead of each keeping a private copy. A session with no state file is not an error, it is an empty result. Read-only; optional (a `SessionStateController`) |
| `sessions.set_composer` | `{text, source?}` | write the composer tenant, leaving every other tenant untouched. Blank `text` **clears** it. `source` is `user` (the default) or `suggestion`, and it decides how the text comes back: the user's own words are restored into the composer, the model's are offered as a ghost to accept. A draft over the 64 KiB cap is **refused** — nothing is written, so a client can say the draft was not kept rather than store a prefix of it that looks whole. There is deliberately no `sessions.set_state`: a whole-document setter lets a client that names only the tenants it knows delete the ones it does not |
| `sessions.export` | `{format?}` → `{SessionExport}` | serialize the session for something outside terva. `markdown` (the default) renders the played scene as a readable story — YAML front matter, each turn under its speaker, lossy on purpose: no tool calls, no reasoning, no unchosen variants. `tervasession` is the lossless raw-JSONL round-trip. One verb with a format discriminator rather than a verb per format, matching `cards.export`: different audiences for the same act, not different acts. Unknown formats are refused rather than defaulted. Optional (an `ExportController`) |
| `usage.get` | → `{usage}` | cumulative tokens / cost |
| `usage.snapshot` | `{refresh?}` → `{usage}` | the provider's usage windows / credits; `refresh` pulls from the provider's usage endpoint and blocks on the fetch |
| `usage.resets.list` | → `{supported, resets?}` | the consumable usage-reset credits and their status — read-only (redeeming one is `usage.resets.consume`, in **control**) |
| `context.get` | → `{breakdown}` | the `/context` size view (prompt, tools, ext context, transcript vs window), plus `breakdown.cache` — the prompt-cache reading in provider-counted tokens: session and last-request hit rate, what caching saved (signed), and a per-request tail. Absent from a server that predates it, so key off presence, not zeroes |
| `context.node` | `{id, op?}` → `{node}` | resolve one context-tree node by its opaque id: expand it a level deep, or run a node-named reveal (feature `context-tree`) |
| `conversation.history` | `{before, limit?, epoch?}` → `{epoch, base, total, messages}` | page BACKWARD through the live transcript — the part a windowed snapshot (feature `history-window`) did not carry. `before` is the client's current `base`, so paging up is "give me what is above what I have"; `base: 0` in the reply means it has reached the top. Served from memory (the effective transcript is in the agent). `epoch` is the snapshot's `epoch`: a stale one gets `conflict`, because an index only names a message within the transcript it was taken from, and a compaction replaces that transcript. Distinct from `conversation.reveal`, which goes BEHIND a compaction to turns the model no longer has |
| `conversation.reveal` | `{ordinal}` → `{ordinal, prev_ordinal, total, messages}` | the turns a compaction summarized away, so the scrollback can keep them above the divider instead of losing them. `ordinal` is 0-based among the session's checkpoints; negative means the latest. The span EXCLUDES the tail the checkpoint kept, so it never overlaps the live transcript — a client prepends it, and never merges. Follow `prev_ordinal` to walk further back (`-1` = the beginning). `CodeNotFound` for an ephemeral session (no file to read). Read-only; in **session** because it grants no authority — it is the same transcript a subscriber already streams, only older |
| `sidechat.open` | → `{id}` | freeze a snapshot of the session for an off-transcript side chat (the `/btw` overlay) |
| `sidechat.ask` | `{id, prior?, question}` → `{text}` | one tool-less completion against the frozen snapshot; the client carries the prior turns, so the daemon holds no per-turn state |
| `sidechat.close` | `{id}` | release the snapshot (closing an unknown id is not an error) |
| `surfaces.list` | → `{surfaces}` | auxiliary panes (context, usage, extension panels) |
| `surface.get` | `{id}` → `{surface}` | one pane's content |
| `surface.action` | `{id, action, args?}` | act on a pane (e.g. forward a keypress; `extensions`/`mcp` `toggle` takes `{name, enabled, scope: global\|project}`; `tasks` takes `spawn {task, model?, provider?, persona?}` / `stop`/`remove`/`resume` `{id}` / `send {id, text}` — actions return no payload, so a spawned agent's id arrives via the next `tasks` fetch) |
| `i18n.catalog` | `{lang?}` → `{catalog}` | the effective web string catalog (session-independent) |
| `files.list` | `{dir?, recursive?, respect_gitignore?}` → `{files, truncated?}` | workspace files for a client's @-file picker, walked daemon-side under the working directory with the picker's usual semantics (gitignore filtering, `.git` pruning, entry/depth caps). `dir` selects a subdirectory in flat mode (`""` = the cwd; `..` escapes are rejected) and is ignored when `recursive` is set — a recursive listing always covers the whole tree. `truncated` reports a walk that stopped at the caps: still usable, just not exhaustive. Session-independent |
| `auth.providers` | → `{providers, can_login?}` | the workspace's **model-provider** credential state: who terva can log into, who it is logged into, and how — plus each provider's offered methods, an OAuth token's expiry and whether it has expired, and setup notes for providers terva stores no credential for at all. **Nothing it returns is a secret** — not a key, not a token, not a prefix of either; it is built to be safe on a screen someone else can see. In **session** rather than **control** because it grants no authority: a client that can read your transcripts already learns which provider you use from the first usage event. `can_login` reports whether this daemon serves the `auth` group, so a pane knows whether to offer a way in or only report what it finds. Not the panel's own bearer-token login — that answers "may YOU talk to this daemon", this answers "may this daemon talk to Anthropic". Session-independent |
| `suggest.reply` | `{note?, history?, target?, target_name?, target_voice?, target_card?, provider?, model?}` → `{draft}` | draft the player's next message in their voice from the session transcript and user persona — a composer aid that **never touches the transcript** (the user still sends it). `history` carries the prior Note→Draft rounds so the daemon holds no per-request state. `target` selects whose line to draft: `""`/`user` fills the composer, while `actor` (in a named character's voice) and `narrator` produce a line meant to be committed with `post.line` rather than typed. `target_card` voices a library card instead of a typed walk-on, superseding `target_voice`. Optional (a `SuggestController`) |
| `suggest.next_step` | `{on_demand?}` → `{line}` | one ephemeral completion on the session's own prefix, offering a single line the user might type next while they sit idle. **Records nothing** — not the ask, not the answer, in memory or on disk — and the answer reaches the calling client alone, so unlike `shell.result` it arms nothing for a later turn. Reasoning is turned off explicitly, which is what makes the output cap safe rather than a cap the model spends thinking. An empty `line` is an ordinary answer, not a failure. `on_demand` says the user ASKED for the suggestion (the TUI's `/nextstep`) rather than terva volunteering it while they sat idle: it selects a prompt variant and changes nothing else, because the idle framing tells the model as a fact that the user "has not asked you for anything", which on that path is false. Omitting it means the original caller — the idle trigger — so a client that predates the field behaves exactly as before. Optional (a `NextStepController`) |
| `sessions.doctor` | `{decisions?, focus?, promote?, provider?, model?}` → `{proposals, note?}` | run the session doctor (the Dramaturgi persona) over a live immersive session, returning **typed** proposals — `lore_entry`, `open_thread`, `cast_promotion`, `scene_state`, `lore_retire`, `scene_break` — each of which applies through a verb that already exists, so nothing is applied unless the author accepts it. `decisions` carries the verdicts on a prior round (empty on the first pass) so the doctor can revise or withdraw in light of a decline reason. `focus` narrows the run to what one message establishes; `promote` drafts exactly one cast promotion for a named walk-on; the two are mutually exclusive. Optional (a `DoctorController`) |
| `sessions.next_scene` | `{commit?, title?, summary?, opening?, world?, provider?, model?}` → `{title?, summary?, opening?, note?, session?, world_id?, world_name?}` | start the next scene of a played session. **Two phases, one verb**: `commit:false` (the default) PROPOSES a draft title, story-so-far recap, and cold-open beat with one bounded model call and creates nothing; `commit:true` CREATES the scene from those fields as the author edited them and spends nothing. The new session carries this one's live World state (roster, lore including the pinned scene-state card, coordination, backdrop, user persona), with the recap as always-on lore and the cold open standing in for the card greeting. `world_id` empty on a propose means the scenes would be grouped by nothing unless the author names one — that is what `world` on the commit is for. Optional (a `DoctorController`) |
| `sessions.realize` | `{commit?, proposal?, provider?, model?}` → `{proposal?, session?}` | turn a cartographer conversation into a playable world. The `next_scene` shape: PROPOSE re-reads the converged planning chat with one bounded Kartoittaja call and returns the finished structure — World, protagonist, NPC roster, lore, cold open, plus an attribution ledger of what the author gave versus what the model invented — creating nothing; COMMIT imports the roster as cards and seeds a play session with the protagonist as the bound **user persona** (not a roster card) and the cold open standing in for the greeting, spending nothing. A commit does not persist a library World — that stays the explicit `worlds.save` act. Optional (a `DoctorController`) |
| `workflows.list` | → `{runs:[WorkflowRunInfo]}` | every workflow run on the host, newest first: status (`incomplete`\|`running`\|`crashed`\|`done`\|`failed` — the first two earned by the heartbeat a running process restamps on its record), completed-of-total agents, in-flight count, cost, and whether resuming would replay real work (a `running` run is not resumable; a `failed` one can be). Session-independent; optional, served only by a `WorkflowsController` |
| `workflows.get` | `{id}` → `{run, script?, args?, results?}` | one run opened: the record, **the script as it ran** (recorded at launch, not re-read from disk), and each journaled result with its label and byte size. The id is path-validated — it names a directory under the run root and arrives from a network client |
| `shared.list` | → `{files:[SharedFileEntry]}` | the files this session's agent handed to the user with `share_file`, newest first: the record the transcript already carries (id, name, kind, mime, size, caption, expiry) plus `path`, where the bytes sit on the **daemon's** disk. A session that shared nothing returns an empty list, not an error. `path` is host-local and is what lets a client on the daemon's own machine open the file in a system viewer or put a real path on the clipboard; a client elsewhere must ignore it and `shared.fetch` instead, because a path from another machine names nothing it can reach. Optional (a `SharedFilesController`) |
| `shared.fetch` | `{id}` → `{id, name, mime?, data?}` | one shared file's bytes, so a client can preview it, save it, or hand it to a viewer — the only way a **remote** client reaches the content at all. Bounded by `MaxSharedFetchBytes` (8 MiB): the store holds files far larger, which are fine to list and fine over the web route's range requests but not fine inlined in a control frame both ends read whole. Past that bound a caller has `path` (same host) or the web `/shared/` route (any host). Read-only, and the id resolves only within the frame's own session |

**control**

| Method | Params | Effect |
|---|---|---|
| `models.list` | → `{models}` | models the workspace can switch to |
| `models.switch` | `{model, provider?}` | switch the model backing `sess`, live, for the next turn (emits `session_updated`); `provider` qualifies ids that exist under several providers |
| `models.favorite` | `{provider, model, on}` | pin/unpin a favorite (persisted to config) |
| `models.hide` | `{provider, model, on}` | hide a model from the pickers, or bring it back (persisted to config). The lowering counterpart to `models.favorite`'s raising, and needed for the same catalogue that made favorites necessary: pinning six models does nothing about the three hundred you still scroll past. It names **one model**, never a rule — the stored form is an ordered pattern list (`hidden_models`, last match wins), and a client that could send patterns would hide hundreds of models in a call that reads like it hides one. The daemon owns that translation, which is also what lets un-hiding rescue a model from a broad pattern the client never knew about. Hiding changes what the pickers **offer** and nothing else: the catalog is untouched, so a hidden model keeps its context window and cost data, a session already running on one carries on, and `models.list` still **sends** it (flagged `hidden`, with `hidden_by` naming the rule) so a client can offer "show hidden" and act on the row |
| `models.reasoning` | `{level}` | set the thinking depth backing `sess`, live, for the next turn, persisted to session meta so a daemon restart brings it back (emits `session_updated`). `level` is a ladder rung as the user types it — `off`, `minimum`, `low`, `medium`, `high`, `maximum`, `max` — or `inherit` (equivalently `""`) to drop the override and follow the global setting again. Raw rather than normalized: `off` and "no override" are different states, and which one a session is in decides whether a later change to the global setting moves it. Stands to the settings `reasoning` control exactly as `models.switch` stands to `models.set_default` — one live session, no config written |
| `models.set_default` | `{provider, model, scope}` | persist a model as the default for **new** sessions. Distinct from `models.switch`, which changes one live session and touches no config: this writes to disk and outlives the daemon, which is why it sits here rather than in **session**. `scope` is `global` (the user's config — the default in every workspace) or `project` (the workspace's project config, which takes effect only while that workspace is trusted) |
| `models.params` | `{provider, model}` → `{provider, model, has_override?, params}` | one model's editable overrides from `models.json` — context window, max tokens, temperature — each spec carrying its key, label, kind hint, rendered default, current override, bounds, and help. **The daemon describes the form and the client renders it**: nothing on the wire names `contextWindow` or `maxTokens`, so a provider that gains a knob costs a daemon change and nothing in either frontend. `has_override` is whether "reset to defaults" would actually do anything. `provider` is required — the same id can exist under an api-key provider and a subscription one, and editing "the other one" silently is the bug `models.switch`'s qualification already guards. Optional (a `ModelParamsController`) |
| `models.params.set` | `{provider, model, values}` | write the overrides. **Send every field the descriptor listed, not just the changed ones**: a blank value CLEARS that override, so a partial map would be ambiguous — "absent" and "cleared" would look the same, and a client omitting an untouched field would silently wipe it. Values cross as strings and the daemon parses; `""` and `"0"` are different answers, which a typed zero could not express |
| `models.params.reset` | `{provider, model}` | remove the model's `models.json` entry outright — back to inherited defaults |
| `models.tiers` | `{provider}` → `{provider, has_override?, rungs}` | one provider's **sub-agent tier ladder**: which model `swarm_spawn`'s `tier: weak` / `medium` / `strong` resolves to today, its display label, any thinking level pinned to the rung, and a `source` of `override` (the operator pinned it) or `built-in` (a family rule found it). **The resolved pick is the payload**, not the override — an empty `swarm_tiers` is the normal case and says nothing about whether the ladder is right. That distinction is not academic: google's medium and strong rungs once resolved to image-generation models while config was empty and every guard passed, and a client rendering only what config held would have shown three blank rungs. Optional (a `ModelTiersController`) |
| `models.tiers.set` | `{provider, rung, model?, reasoning?}` | pin one rung. **One rung per call**, unlike `models.params.set`'s whole-form rule — a rung is addressed by name, so "absent" cannot be confused with "cleared". Either field may be empty: a rung naming only `reasoning` means *the built-in model for this rung, but think this hard*, which is the cheapest way to build a ladder on a provider whose families terva already knows, and it stops a repeated id drifting from the built-in one. Both empty is a reset. `model` must exist in that provider's catalog — an unknown id would otherwise resolve to nothing and the sub-agent would quietly inherit the host model |
| `models.tiers.reset` | `{provider, rung?}` | drop one rung's pin, or the provider's whole entry when `rung` is omitted. An entry left with no rungs is removed rather than kept as an empty object, so `has_override` keeps answering "would a reset do anything" |
| `models.default_for` | `{card?, world?}` → `{provider, model, source}` | resolve the effective default model for a context, walking **Card → World → Workspace**. The single authority every "what's the default here?" surface consults, so a card's default propagates to the card doctor, the session seed, and any picker's fallback row identically; `source` names the rung that won, letting a card-scoped surface tell "this card has its own" from "inheriting". With neither field it is just the workspace default. `world` is accepted but has no rung yet — reserved so callers need no change the day one exists. Optional (a `CardModelController`) |
| `cardmodel.set` | `{card, provider?, model?}` | write a card's default model, or **clear it when both are empty** (fall back to the workspace default). A card's default is terva-owned metadata kept outside the card, like a group — a seed for a session started from it, not something stored on the card. Clearing a card that had none is not an error |
| `control.trust` | `{parent?}` | grant Workspace Trust to the cwd; brings project extensions/lore/permission rules live across open sessions |
| `control.untrust` | — | revoke it, tearing project content back down |
| `control.restart` | — | Tier-1 self-restart (re-exec into the installed binary); `unsupported` unless `restart` was advertised |
| `usage.resets.consume` | `{id}` → `{reset, windows_reset?}` | redeem a usage-reset credit. Irreversible and it spends a scarce provider grant, so it sits here rather than beside the read-only `usage.resets.list`; the host confirms with the user before issuing it |
| `cards.list` / `cards.get` | → `{cards}` / `{CardView}` | the character-card library — optional, served only by a `CardsController` |
| `cards.import` | `{bytes\|path\|url}` → `{CardView}` | import a CCv2 card (PNG or JSON) by upload, server path, or remote URL (fetched through the SSRF-guarded `egress` client); the avatar pixels are retained for the `/media/` route |
| `cards.edit` | `{id, fields}` → `{CardView}` | mutate a card's own fields and re-serialize (unknown `extensions` round-trip losslessly); a card stays untrusted **data** — an edit never widens what it can do |
| `cards.duplicate` | `{id, name}` → `{CardView}` | copy a card under a new name — portrait included — as a card of its own, so a design can be iterated on without overwriting the version that works. `name` is required and must differ from the original's: an id is a stem of the name plus a hash of the contents, so an unchanged name over unchanged contents names the card you copied, not a second one. A name that would land back on the original — or on any card already holding these exact contents — is **refused**, because the alternative is a write that reports success and creates nothing. The copy starts with an empty revision history; the caller picks the free name (Stage proposes `<name> (copy)` and counts upward) |
| `cards.delete` | `{id}` | remove a library card |
| `cards.export` | `{id, format?}` → `{CardExport}` | serialize a card for download — a CCv2 PNG (the current JSON embedded in the retained avatar via `card.WritePNG`) when it has one, else CCv2 JSON; `format` forces `png`/`json`, `""` auto-picks. Bytes ride base64 |
| `cards.lint` | `{id}` → `{findings}` | run the **deterministic** card lint over a stored card: each finding carries its rule, a severity (`warn` for a real problem, `info` for a fact worth surfacing), the offending field, and the offending snippet. No model runs — this is the static pass `cards.doctor` reads before proposing anything |
| `cards.favorite` | `{id, favorite}` | pin/unpin a card in the library browser |
| `cards.history` | `{id}` → `{revisions}` | the card's retained earlier revisions, newest first. Each entry carries an opaque `ref`, when it was saved, its size, the card's name **at that revision** (so the list still reads correctly after a rename), the CCv2 fields it differs from the saved card in — i.e. what restoring it would change — and whether restoring would also change the portrait (which lives outside the card document, so a revision that replaced only the image would otherwise read as identical). A card never edited has an empty list, which is a normal state |
| `cards.restore` | `{id, ref}` → `{CardView}` | put an earlier revision back. The restore itself snapshots the outgoing card first, so it is undoable in turn |
| `cards.revision` | `{id, ref}` → `{CardRevisionView}` | one revision's stored document in full, `raw` being the normalized card JSON in the shape `cards.get` returns — so a client diffs two documents of one shape |
| `cards.doctor` | `{id, decisions?, session?, steer?, provider?, model?}` → `{proposals, note?}` | the **LLM** card doctor: reads the card plus its deterministic lint and proposes concrete per-field edits, each with a severity, a rationale, the before/after text, and a `remove` flag for a proposal that CLEARS a field (which has to be said rather than inferred — an empty `after` on its own is indistinguishable from a model with nothing to offer). `decisions` carries the user's verdicts on the prior round so the doctor can revise or withdraw in light of a decline reason — the negotiation. `steer` is the author's standing instruction for the pass ("make her wearier", "cut the war backstory"), re-sent on each revise and taking priority over lint findings; distinct from a decision's `reason`, which answers one proposal after the fact. `session` switches to EDITOR mode, grounding proposals in a named immersive session's scene and World lore — promotion from play — rather than card-craft lint alone. Optional (a `DoctorController`) |
| `personas.list` / `personas.get` | → `{personas}` / `{PersonaView}` | the persona library — optional, served only by a `PersonasController` |
| `personas.create` / `personas.edit` | `{…}` → `{PersonaView}` | write a persona — **trusted-tier gated** (a charter shapes identity in the cached prefix), unlike the ungated card edits above |
| `personas.delete` | `{name}` | remove a persona from the user library |
| `userpersonas.list` | → `{personas}` | the saved **user** personas (name-sorted), one marked default if the user set one — the reusable "who I am in the story" identities (name, description, gender, pronouns). Distinct from `personas.*`, which shape a CHARACTER: a saved user persona is benign data with no charter and no authority, so these verbs are **ungated** beyond the workspace's own auth. Optional (a `UserPersonasController`) |
| `userpersonas.save` | `{ref?, name, description?, …}` → `{UserPersonaView}` | upsert a saved user persona by a slug of its name, preserving its default status. `ref` identifies **which** persona is being edited: send it and a changed `name` renames that persona; omit it and the name alone decides, which makes a rename indistinguishable from a create. A rename onto a name already taken is refused |
| `userpersonas.delete` | `{ref}` | remove a saved user persona; a missing one is a no-op |
| `userpersonas.set_default` | `{ref}` | mark one persona the default a new immersive session pre-fills, clearing the others — so a chat no longer opens as the literal "User". An **empty ref clears** the default entirely |
| `backgrounds.list` | → `{backgrounds}` | scene backdrops — optional, served only by a `BackgroundsController` |
| `backgrounds.import` | `{bytes\|path}` → `{BackgroundView}` | add a backdrop image |
| `backgrounds.delete` | `{id}` | remove a backdrop |
| `backgrounds.bind` | `{id}` | bind a backdrop to the frame's session (per-session, written to meta) |
| `backgrounds.generate` | `{prompt, negative_prompt?, size?, backend?}` → `{BackgroundView}` | paint a scene from a prompt via the session's image backend (the same registry `generate_image` uses), store it, and bind it to the session in one step; a bad request when no image backend is configured |
| `note.set` | `{text}` | set/clear the session's author's note — a live steering string injected into the uncached per-turn tail (no cache bust); optional, served only by a `NoteController` |
| `shell.result` | `{command, output}` | offer the result of a `!` shell escape the CLIENT ran to the session's next request — it rides the uncached per-turn tail once and is not written to the transcript. `output` is the raw merged text, never the client's ANSI-styled block. An empty `command` disarms. Optional, served only by a `ShellResultController`; in the conversation group because it grants strictly less than `prompt`, which is already there |
| `user.bind` | `{name?, description?, ref?}` | bind the session's **user persona** — who the user is *in the story* (distinct from `persona`, who the agent is). The description rides the free per-turn tail; a changed name re-bakes the `{{user}}` macro into the cached prefix (a deliberate rebuild). `ref` (a saved user-persona) is reserved and rejected for now. Optional, served only by a `UserController` |
| `cast.add` | `{name, ref}` | add/update a **play** session's cast member (actor name → persona name or card path); the ref is validated, then the `actor_spawn` tool + cast addendum rebuild. Optional, served only by a `CastController`; a bad request on a chat/coding session |
| `cast.remove` | `{name}` | drop a cast member and retire its warm actor; rebuilds as above |
| `cast.speak` | `{actor}` | user-directs ("pick who speaks"): direct the narrator to bring a cast member into the scene now — runs a normal turn (the narrator stays the source of truth and voices the actor via `actor_spawn`), so it returns once the turn is accepted, `CodeBusy` if one is running |
| `world.lore.put` | `{entry, replace?}` | add or update one **World lore** entry — authored keyed context every character on stage can see. An entry carries trigger `keys` (it injects when one appears in recent messages), `constant` (injects every turn instead — an entry needs one or the other, else it could never activate), `content`, and an `audience` naming who knows it (empty = everyone on stage; a named character's generation sees only what they are cleared for, while the narrator sees everything). Like the author's note, entries ride the **uncached per-turn tail**, so an edit takes effect next turn with no cache bust. Upserts by `entry.name`; `replace` names the entry this supersedes, so a rename edits in place. The `model` and `learned` fields are read-only on the wire — a user edit takes ownership and clears the model badge. State reads ride `SessionInfo.world_lore`; there is no list verb, matching how note and cast travel. Optional (a `WorldController`) |
| `world.lore.delete` | `{name}` | remove a World lore entry |
| `world.set` | `{coordination}` | the World's settings — today the meta-narrator's coordination mode: `""` (auto — the meta-narrator picks who answers), `off` (the bound character always answers), or `focus:<roster name>` (that character always does). Takes effect on the next turn; state reads ride `SessionInfo.coordination` |
| `worlds.list` | → `{worlds}` | the saved-World library, each with its roster, lorebook, per-character model pins, cover URL, and **member-session count resolved** so a shelf renders without N extra calls |
| `worlds.save` | `{name?, description?}` → `{WorldView}` | promote or update: lift the frame session's live World state — roster, pins, lore, coordination — into the library. A session not yet in a World creates one (`name` required) and is stamped a member; a member session updates its World in place (last-wins, `name` optionally renaming). **Explicit save-back, never live sync** |
| `worlds.delete` | `{id}` | remove a saved World. Member sessions keep their embedded copies and lose only the grouping |
| `worlds.update` | `{id, name?, description, cover?, remove_cover?}` → `{WorldView}` | edit a saved World's metadata without a session. The id never changes, so member-session grouping survives a rename; `name` empty keeps the current one (a World always has one). `description` is applied **verbatim** — the sheet editing it holds the full view, so it sends the current text back when leaving it unchanged, and `""` clears it. Setting both `cover` (PNG bytes, base64) and `remove_cover` is a bad request |
| `worlds.set_character_model` | `{id, character, provider?, model?}` | pin one roster character's World-scoped default model — the seed a new session in this World gives that actor. Empty `provider` AND `model` clears the pin (the character inherits again); the character must be on the roster. Sessionless, like `worlds.update` |
| `worlds.set_model` | `{id, provider?, model?}` | set the World's OWN default model — the `world` rung of the `card → world → workspace` ladder `models.default_for` resolves. Distinct from `worlds.set_character_model`, which pins one actor's voice: this is the floor a scene started here, a `worlds.doctor` run, and any unpinned character fall back to. Empty `provider` AND `model` clears it. The pick is catalog-validated, because an unresolvable default degrades silently down the ladder |
| `worlds.export` | `{id}` → `{WorldExport}` | bundle a saved World for download: one JSON carrying the World document, every roster character's card in its ordinary export form, and the cover image. Same download shape as `cards.export` (filename, MIME, base64 bytes) |
| `worlds.doctor` | `{id, sessions?, decisions?, steer?, provider?, model?}` → `{card_proposals, world_proposals, note?}` | the **ensemble** doctor (SD6): Dramaturgi reads every roster character's card *together* with the World's lorebook and proposes character edits beside world edits. Runs on a **saved World by id, with no session anywhere** — the World studio is a Library screen, and a doctor that demanded an open scene could not be reached from it. Two proposal families, kept apart because they apply through different verbs: `card_proposals` are per-field card edits applying through **`worlds.edit_character`**, which FORKS rather than rewriting the shared library card, so an accepted proposal cannot change a character another World is playing; `world_proposals` are typed entries applying through `worlds.lore.put`/`delete`, plus the kind only this doctor has — **`character_new`**, a character the cast is missing, seeded through `cards.import` + `worlds.add_character`. `sessions` names the scenes to read as evidence, **chosen** rather than implied: a World's evidence is its whole history of play, and which nights matter is a judgement. They share one budget pool divided by water-filling, so one long night cannot crowd out three short ones, and the server caps how many it will read at all. `steer` is the author's standing instruction and is **load-bearing here rather than decorative** — a World of one character is thin evidence, and "grow me a cast" is the question rather than a refinement of it. Every refusal is pre-spend, and the spend is booked to the workspace usage ledger (`$TERVA_HOME/usage.jsonl`) since there is no session file to hold the row. Optional (a `DoctorController`) |
| `worlds.import` | `{bytes\|path}` → `{WorldView}` | ingest a World bundle: each embedded card lands in the card library (idempotent by content), the roster is remapped to the ids **this** library assigned, and a **fresh World id is minted** — a bundle never collides with a local World. `bytes` (an upload) wins over `path` (a file on the daemon's disk); one must be present |
| `worlds.lore.put` | `{id, entry, replace?}` → `{WorldView}` | add or update one lore entry on a **saved** World — the sessionless twin of `world.lore.put`, taking the World's id where that one takes the session from the frame. Both share one upsert rule in the workspace, deliberately: a session seeds its book from the save and `worlds.save` writes it back, so two rules would mean promoting a book could reshape it. Unlike the session path it does **not** date the scene-state pin — that stamp measures staleness against a transcript, and a saved World has none |
| `worlds.lore.delete` | `{id, name}` → `{WorldView}` | remove one lore entry from a saved World. Refuses when no entry answers to the name, rather than reporting success for a no-op |
| `worlds.set` | `{id, coordination}` → `{WorldView}` | a saved World's coordination mode (W3), same three shapes as `world.set` and validated against the **saved** roster — the roster a new session in this World would actually start with |
| `worlds.add_character` | `{id, name, ref}` → `{WorldView}` | put a character on a saved World's roster: the sessionless `cast.add`. `ref` must resolve in the card library — a roster entry pointing at nothing is a part that cannot be cast, so it is refused before it is written rather than failing later at session build. Re-adding an existing name re-points it (how a swap is spelled); the model pin is keyed by roster **name**, so it survives a re-point |
| `worlds.remove_character` | `{id, name}` → `{WorldView}` | take a character off the roster, clearing their model pin with them — pins key by name, so a left-behind one would silently re-apply to whoever next took it. A `focus:` coordination naming the removed character falls back to auto, since focusing an uncastable part would route every turn nowhere. A World-scoped variant's origin record goes too, so the card returns to the shelf as an ordinary card rather than a hidden orphan |
| `worlds.edit_character` | `{id, character, card, also_library?}` → `{world, card_id, forked}` | edit a roster character's card **without the change escaping this World**. A roster holds a plain card ref and `cards.edit` rewrites a card IN PLACE (the content hash is minted at import and never re-derived), so one library card is shared by every World, every session, and the shelf — an edit accepted in one World would rewrite the character every other World is still playing. Instead the card is **forked** and the roster re-pointed; the original is never opened for writing. Content-addressing carries the scheme: an edit that changes the bytes mints a new id, and an edit that changes nothing resolves to the same one and is a no-op (`forked: false`), so a speculative apply cannot litter the library with a twin. `also_library` opts into the old behaviour explicitly, for a fix that belongs to the character everywhere rather than to this World's take on them; it is off by default because the surface driving this proposes edits to characters the author may be playing elsewhere |
| `worlds.create_character` | `{id, name, card}` | import a NEW card and roster it into this World in one operation, recording that the character was born here. One verb rather than `cards.import` + `worlds.add_character`: the pair can half-apply (card in the library, roster untouched), and provenance written by a client is provenance a client can forget. Distinct from `worlds.add_character`, which borrows an EXISTING card and deliberately claims no provenance. The origin record carries no `forked_from`, which is what keeps the card ON the shelf (badged) rather than hidden like a variant |
| `cardgroups.list` | → `{groups}` | the card groups, each with its **live** members (refs whose card still exists — stale ids are filtered out, so a count is just the member-list length). A group is a terva-owned membership bucket the library browses by, distinct from a card's embedded CCv2 tags (author labels inside the card) and from a World (which seeds sessions and carries roster/lore). These verbs only move ids between buckets and never touch a card, so they are ungated like `cards.*`. Optional (a `CardGroupsController`) |
| `cardgroups.save` | `{id?, name, color?}` → `{GroupView}` | create a group (empty `id`) or rename/recolour one (existing `id`), leaving its members untouched |
| `cardgroups.delete` | `{id}` | remove a group. Its member cards are untouched — a group is a view, not a container |
| `cardgroups.set_members` | `{id, members}` → `{GroupView}` | replace the member list; unknown refs are dropped. **The sole membership mutation** — adding or removing one card means sending the group's new full list |
| `sessiongroups.list` | → `{groups}` | the same membership bucket over **session ids**, reusing the `GroupView` shape; members are the live set. Distinct from a World, which also groups sessions but carries roster, lore, and coordination — a group is only a name and a member list. Unlike card groups these appear on both the Stage library and the control panel, but the verbs are identical. Optional (a `SessionGroupsController`) |
| `sessiongroups.save` | `{id?, name, color?}` → `{GroupView}` | create (empty `id`) or rename/recolour (existing `id`); members ride `set_members` |
| `sessiongroups.delete` | `{id}` | remove a group; its member sessions are untouched |
| `sessiongroups.set_members` | `{id, members}` → `{GroupView}` | replace the member list (session ids); unknown ids are dropped |

**auth** (optional — served only by a carrier backing an `AuthController`, which
advertises the group in its hello; otherwise `unsupported`. `terva web`
advertises it only under `--web-allow-login`, and refuses to on an
unauthenticated listener)

These CHANGE the credential terva uses to reach a model provider, which is why
they are not in **session** with the read-only `auth.providers`. **No verb here
returns a secret** — not now, not behind a flag. A credential goes in and never
comes back out: a client can see *that* Anthropic is logged in, and can replace
or revoke it, but nothing on this wire hands the token back.

| Method | Params → Result | Effect |
|---|---|---|
| `auth.login.start` | `{provider, method, local?}` → `{AuthFlowStep}` | begin a flow and return **what to put in front of the user**. `method` is `apikey` or `oauth`; the client does *not* choose the concrete OAuth variant, because only the daemon knows which can actually complete over this carrier — a browser on a phone can never reach the daemon's loopback, so those flows are not offered over the wire at all. `local` states that the caller's browser is on the daemon's own host (true only for an in-process carrier); it is a statement of fact, not a preference, and a remote client must never set it. The step's `kind` says what to render — `form` (collect `fields`, then submit), `display` (show the `url` and `user_code` and wait; the daemon is polling and completion arrives as an `auth_state` event), or `info` (read-only prose). `url` is **displayed, never auto-opened**: the daemon's browser is not the user's, and on a headless host there is none. The daemon owns field order, labels, and validation; the client owns only presentation — one descriptor ends the drift where the browser form and the TUI dialog ordered the same four fields differently |
| `auth.login.submit` | `{flow, values}` | complete a form step, `values` keyed by field name. **The one frame in ctrlproto that carries a secret, and it goes one way: in.** `flow` is required because a login is not a pure function of its inputs — the manual OAuth flow leaves a PKCE verifier on the daemon, and the code the user pastes back is only meaningful against the flow that minted it. With one user at one keyboard that is invisible; with a daemon serving a phone and a laptop, two logins started at once would exchange each other's codes. A submit against a superseded handle is refused (`CodeBusy`), not mis-exchanged |
| `auth.login.cancel` | `{flow}` | abandon a flow in progress |
| `auth.logout` | `{provider}` | clear a stored credential; `all` clears every one. `openai` and `openai-codex` are separate logins sharing one slot on disk — a platform API key and a ChatGPT subscription — and clearing one leaves the other standing |
| `auth.endpoint.remove` | `{id}` | forget a named openai-compatible endpoint: its entry in config.json's `endpoints`, and any key stored under that id. **Deliberately not a logout.** A logout forgets a secret and the provider is still there to sign back into; this forgets the operator's *definition* of a server — which host, which port, which context window. Making "sign out" do that silently would be a trap, so the two verbs stay apart even though the same pane offers both |

**secrets** (optional — served only by a carrier backing a `SecretsController`,
which advertises the group in its hello; otherwise `unsupported`. `terva web`
advertises it only under `--web-allow-secrets`, and refuses to on an
unauthenticated listener. The in-process TUI serves it unconditionally: the flag
exists to keep a *remote* peer from enumerating the host, and a user at that
terminal already has `terva secret status`)

terva's **at-rest** posture — what is encrypted, what is still plaintext, which
components hold a key — and the secret store's grant model. Separate from
**auth** by the argument that separated **auth** from **control**, one rung
further: `auth` writes the credential terva uses to reach a model provider,
while this reports on the key that opens *everything*, including material `auth`
never touches.

**No verb here returns a secret value**, and two things are deliberately absent.
**Rotation**, in either mode: `terva secret rotate` supersedes the key and
`--revoke` destroys it, so a bug in a client — or a hostile one — bricks the
install; it is rare and operator-initiated, so it stays on the CLI. And **`init`
/ `migrate`**, for a weaker form of the same reason: both rewrite every
secret-bearing file in the home, and a fresh install now seals itself, so the
on-ramp needs no wire verb. Both are trivial to add later and impossible to
un-ship.

| Method | Params → Result | Effect |
|---|---|---|
| `secrets.status` | → `{SecretsStatus}` | the whole posture as a struct — key state and permissions, the public recipient, retired-key count, per-file encrypted/plaintext state, the store's scopes and counts, config.json's secret locations, the registered components, the per-component **read verdicts** (`reads`), and the grants. **Shape only**: paths, modes, counts, names, states and reasons; no value anywhere, so it is safe to paste into an issue. `key.state` distinguishes `absent` (encryption was never on — `terva secret init` is right) from `missing` (ciphertext exists that this key was meant to open — minting a new one would strand it permanently), which is the one field a reader can act on wrongly. A component with `registered: false` holds sealed values that no registry entry claims a recipient for, so a rotation cannot re-seal it. `reads` is a **different axis**: whether the agent's own tools may read inside each component's directory, which a component earns by being verifiably free of plaintext secrets. A `reads` row with `enforced: false` is a verdict that is reported but not yet applied — a declaration requirement still in its grace period, surfaced so the change is visible before it bites. `grants[].expired` is computed by the **daemon**, because a client's clock is not the daemon's. This is the same struct `terva secret status` renders — one producer, so a pane and a terminal cannot disagree about what is encrypted |
| `secrets.list` | → `{scopes: [{scope, keys}]}` | the store's scopes and the **key names** under each. A key name is schema, not material — `bot_token` says what a slot is for and nothing about what is in it, which is the line that lets `list` exist while a `get` never will |
| `secrets.grant` | `{principal, scope, mode, ttl?}` | authorize `principal` against `scope`. `mode` is `use` (may ask the host to act with the secret, may not receive the material) or `read`. `ttl` is a **duration** (`720h`), not a timestamp: the daemon computes the deadline against its own clock, because a caller supplying an absolute time would be asserting agreement about *now* with a machine it may share neither a timezone nor an accurate clock with. Default is deny — there is no ambient tier a component reads without being named |
| `secrets.revoke` | `{principal, scope}` | withdraw one grant. Both halves are required: a principal may hold grants on several scopes, and dropping all of them because one was named would be a surprise in the direction that matters |
| `secrets.forget` | `{scope, purge?}` → `{component?, grants?, values?, remaining?}` | drop terva's record of a component: its registry entry and every grant naming it. **Never automatic** — "not seen for N days" is indistinguishable from a seasonal connector — and it has teeth beyond tidying: an uninstalled component never acks a new generation, so it pins every retired key in the ring open forever, and this is the unblock. `purge` additionally **deletes the values it stored**; without it they are left in place and counted in `remaining`, because a user unblocking a rotation would be badly surprised to lose a still-installed component's credential. The result says what was actually removed, so a forget against a typo'd scope does not read the same as one that worked |

**replay** (optional — served only by a carrier backing a `ReplayController`,
which advertises the group in its hello; otherwise `unsupported`)

| Method | Params → Result | Effect |
|---|---|---|
| `replay.control` | `{action, position?, multiplier?, unit?}` → `{state}` | drive a recorded session's transport: `play` / `pause` / `step` / `seek` / `turn` / `speed`. Only the field the action needs is read |
| `replay.state` | → `{state}` | the current transport state: `{playing, position, total, speed, mode}` (`mode` = `effective` \| `raw`) |

Implementations may return `unsupported` for control methods they don't yet
serve — jail, prompt/system overrides, and templates are shaped in the interface
but not implemented. (Lore CRUD was on that list until `world.lore.put` /
`world.lore.delete` shipped; the session's lorebook is live surface now.)

Note that `unsupported` covers two different things. Most of the verbs above are
**optional by controller**: `ServeConn` type-asserts the interface at dispatch
and answers `unsupported` when a carrier does not implement it — a replay
carrier has no card library, a test fake has no live session. That is a
permanent property of the carrier, not a build gap, and it is why the rows say
which controller backs them.

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
| `snapshot` | `snapshot:{…}` | sent only to a **new subscriber**: transcript, pending permissions/asks, queue, skills, `busy`, and the tail span's variant metadata (`tail:{span_start, variants, active}`, so a reconnecting client redraws the swipe arrows) — render history before the live stream |
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
| `prompt_rebuilt` | `scope` (system \| tools \| both), `reason` (approval-mode \| auto-swarm \| extension-reload \| mcp-toggle \| trust \| tool-withdrawal \| extension-context \| skill-reload \| chat-connect \| chat-disconnect), `context_tokens?` | the session's pinned prompt prefix changed, so the provider prompt cache is invalidated and the next turn re-reads ~`context_tokens` tokens uncached. Emitted only on a real diff — an identical rebuild is silent. The extension-driven reasons (`tool-withdrawal`, `extension-context`) are suppressed to a host log when they fire before the first turn — a startup policy assertion invalidates no cache. `skill-reload` is user-initiated (`/reload-skills`) and so notifies even pre-turn; it fires only when the reload changed the skill *manifest*, so editing a skill body costs nothing. Informational, never blocking: the run loop pins the prefix per turn, so the change lands at the next turn regardless. |

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
rather than being bolted on under pressure — but it is not implemented.

The consequence today is that **a token is all-or-nothing**: it grants whatever
groups the client declares. The shipped out-of-process clients (`terva attach`,
`terva ctl`, `terva ext config`) are safe because *you* run them and they carry
your own authority. Per-group authority gating is what would remove that
assumption — it is the prerequisite for handing a token to a tunneled extension
or a third-party orchestrator, neither of which is you.

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
  the per-group authority gating above. Out-of-process control itself already
  works — `terva attach` / `terva ctl` / `terva ext config` drive a running terva
  over the WebSocket carrier, and `ctrlclient` is built to be held N-at-a-time.
  What is missing for a *fleet* is those two carriers, the per-group authority
  gating that makes a token safe to delegate, and multi-workspace addressing:
  one daemon still serves one workspace.

See the platform vision in
`docs/ideas/terva-platform.md`.

## See also

- [web.md](web.md) — `terva web`, the browser control panel (the first ctrlproto carrier)
- [connectors.md](connectors.md) — connproto, the chat-connector wire (the meta-architecture ctrlproto reuses)
- [extensions.md](extensions.md) — extproto, the host↔extension wire (a future ctrlproto carrier)
- [rpc.md](rpc.md) — the `core.WireEvent` JSON stream that ctrlproto's conversation events *are*
- `docs/proposals/control-plane-protocol.md` — the full design rationale and roadmap
- `packages/agent/ctrlproto` — the implementation (`doc.go`, `wire.go`, `methods.go`, `event.go`, `hello.go`, `service.go`, `serve.go`)
