# Connector protocol v2: from externalized telegram bot to a general chat wire

Status: IMPLEMENTED (as-built spec). Every non-frozen stage — A, B, C,
D, E, G, H, I — shipped on `explore/ext-connector-duplex` (protocol 2,
negotiated; each stage's entry in the staging list below records what
shipped and where it deviated from the sketches). Stage F (event
sources) stays a sketch BY DESIGN — the hold, its MCP context, and the
reopen criteria live in
docs/decisions/0002-connector-event-sources-hold.md. The design
sections are preserved as written (Rev 2, 2026-07-02: added
Interactions, Speaker identity, Work-stream threads, and the
Discord/Matrix deep-dive receipts); where a section's sketch and the
shipped wire differ, the staging entry is authoritative.
Prereq reading: docs/connectors.md (the maintained frame reference),
docs/proposals/connector-extensions.md (the tunnel that makes this the
single wire for both connector packagings).

## Why now

connproto v1 is the original built-in Zot telegram connector, turned
inside-out: the frames are the exact seam that bot happened to have. It
achieved its goal — connectors as plugins, in any language, without
touching terva core — and the tunnel rework then made it the ONLY chat
wire (standalone process or extension-bundled, one vocabulary). That
consolidation is what makes a v2 design pay off: every improvement lands
once and reaches every connector, every packaging, and the TUI bridge
and bot daemon alike.

This is also a RESET, not a patch: terva grew capabilities the v1 wire
cannot carry to chat at all. The tool-approval ladder exists everywhere
except bot mode (which is yolo-only because chat has no way to answer a
question); AskUserQuestion has no chat rendering; personas and the
`--play` cast want distinct speakers in one chat; agent dispatch wants a
thread per work stream. The v2 vocabulary is designed against those
consumers, not just against other chat services' feature lists.

The costs of the telegram-DM inheritance are now visible whenever a
connector for anything else gets written:

## What v1 cannot say (the inventory)

Wire-level, from the code as it stands:

1. **Messages have no identity.** `message` carries chat_id / user_id /
   username / reply_to / text / attachments — no `id`, no timestamp.
   Worse, the one identity-adjacent field is doing double duty: the
   telegram connector fills INBOUND `reply_to` with the message's own
   telegram message_id (so the host can reply-thread), while OUTBOUND
   `reply_to` means "the message I'm replying to". Nothing can
   reference a message later: no reactions, no edit tracking, no
   "which message did the bot just send" (send `result` carries no id
   either).
2. **No edit or delete events.** A service-side edit either re-delivers
   as a fresh message (echo/duplication) or the connector drops it.
3. **Chats are opaque tokens.** No kind (DM vs group vs thread vs
   broadcast), no title, no thread id. The host cannot distinguish a DM
   from a 500-person group — which is why the trust model is stuck at
   single-user DM pairing, and why the idle-nudge seeds "chat id =
   user id" (true only for telegram DMs).
4. **Attachments are images, period.** The wire says mime_type + path,
   but the host ingests every attachment into `provider.ImageBlock`;
   voice notes, documents, video are silently image-shaped or useless.
   No captions, no filenames, no duration.
5. **No text entities.** Mentions (crucially: "the bot was mentioned"),
   code spans, links — all flattened into plain text. Mention-gated
   group behavior is unimplementable.
6. **No reactions, receipts, presence, inbound typing.** Outbound
   typing exists (telegram's `sendChatAction`, generalized); nothing
   flows the other way.
7. **Capabilities are four outbound-ish booleans** (max_text_len,
   typing_refresh_ms, sends_images, sends_files), and only the
   CONNECTOR declares any — the host's hello_ack advertises nothing,
   so a connector cannot feature-detect its host.
8. **Outbound verbs: send / send_image / send_file / typing.** No
   edit-own-message (the primitive behind progressive/streaming
   replies), no react, no delete.

Host-level, inseparable from the wire gaps:

9. **One transcript per daemon.** `chat.Loop` owns a single
   `core.Agent`; every admitted message from any chat feeds one
   conversation. ChatID only routes the reply. Two chats = interleaved
   context soup, which is fine only because pairing makes multi-chat
   rare.
10. **The gate is one user id.** First `/start` claims the bot;
    everyone else is refused. There is no notion of an approved CHAT,
    an owner-vs-participant distinction, or a mention-only mode — the
    building blocks of group admission.

## Design principles

- **One vocabulary, negotiated.** connproto already has range-based
  version negotiation (hello carries [min,max], host picks). v2 is a
  bump to protocol 2 used as a feature-detection anchor; every v2
  construct is ALSO individually capability-flagged, and the host
  advertises its own capabilities in hello_ack (new, additive field).
  The rule stays extproto's: fire-and-forget additive frames degrade
  gracefully (unknown frames are logged and ignored on both sides
  today); anything a sender must not emit blind rides a capability
  flag; semantic changes to existing fields are what version bumps are
  for.
- **The wire describes; the host decides.** Chat kinds, entities, and
  message ids go on the wire; admission policy, session keying, and
  what to do with an edit stay host-owned. A connector must never gain
  authority by describing a message differently (the trust posture of
  the extension tunnel, kept).
- **Telegram remains implementable in an afternoon,** and the loopback
  example must stay trivial: every v2 feature is optional, and a
  connector that declares nothing behaves exactly like v1.
- **Don't invent what the ecosystem converged on.** Where Matrix,
  Discord, Slack, and Telegram agree on a shape (message ids, edit
  events referencing them, thread-as-reply-chain vs thread-as-channel),
  take the convergent shape. Where they diverge, take the simplest
  thing the chat.Loop can honor.

## The v2 vocabulary

(Survey-informed; see "Prior art notes" at the end.)

### Message identity (the keystone)

```json
{"type":"message","id":"m-8841","ts":1751469000123,
 "chat":{"id":"c1","kind":"group","title":"ops"},
 "user_id":"u1","username":"drew",
 "reply_to":"m-8790",
 "text":"…","entities":[…],"attachments":[…]}
```

- `id`: REQUIRED in v2 — stable and unique within its chat, opaque to
  the host. (Chat-scoped is the ecosystem floor: telegram message_ids
  and Slack ts are per-chat; Discord/Matrix global ids satisfy it
  trivially. Connectors for services without ids mint their own; the
  loopback example will.) Dedup of service-side redelivery — Slack
  webhook retries and kin — is the CONNECTOR's job, keyed on this id;
  the stdio/tunnel carrier itself is ordered and reliable, which is
  why v2 does not stamp every frame with an event id the way
  HTTP-delivered protocols must (a deliberate divergence; revisit only
  if a non-stdio carrier ever lands).
- `ts`: unix milliseconds, when the service says the message happened.
- `reply_to` now means in-reply-to in BOTH directions. Migration: the
  telegram connector moves its message_id from `reply_to` to `id`; the
  host falls back to treating inbound `reply_to` as a reply token when
  the peer speaks protocol 1 (the Loop only ever echoed it back, so
  compat is contained in one spot).
- send `result` gains `message_id` — the id of what was just sent —
  which is the prerequisite for outbound edits and reactions, and for
  the host to recognize its own messages if a service echoes them.

### Chats become objects

`chat.kind`: `dm | group | thread | channel` (channel = broadcast-ish,
read-mostly). `chat.title` optional, display-only. `thread_id` optional
on messages for services with container threads (Slack thread_ts,
Discord thread-channels); services with reply-chain threads (telegram,
Matrix) just use `reply_to` — reply and thread are orthogonal, per the
ecosystem consensus.

One membership frame, for the BOT's own admission only:

```json
{"type":"chat_membership","chat":{"id":"c9","kind":"group","title":"ops"},
 "change":"added","by_user_id":"u1","by_username":"drew"}
```

(`added | removed`.) Every platform delivers this (telegram
`my_chat_member`, Slack `member_joined_channel`, Discord guild events,
Matrix invites), and it is the trust hook group admission wants: the
owner hears "the bot was added to <chat> by <user> — /approve?" the
moment it happens instead of at the first awkward message. Full
rosters and member-change streams stay out of v2 — the host learns
participants lazily from messages. (A `chat_info` request/response
pair is reserved for later; nothing in the v2 host needs it.)

### Entities (minimum viable markup)

```json
"entities":[{"kind":"bot_mention","offset":4,"length":9},
            {"kind":"mention","offset":20,"length":5,"user_id":"u9"},
            {"kind":"code","offset":31,"length":12}]
```

Offsets in Unicode code points over `text`. `bot_mention` is the
load-bearing kind (group mention-gating); `mention`, `code`, `link` are
the cheap rest. Everything else (bold/italic/etc.) is deliberately NOT
modeled — the agent reads plain text fine; formatting fidelity is not
worth an entity zoo. Outbound stays plain text in v2: the connector
renders to its service's format (as telegram already does).

### Attachments grow kinds

```json
"attachments":[{"kind":"voice","mime_type":"audio/ogg","path":"…",
                "name":"…","size":38112,"duration_ms":4200,
                "caption":"listen to this"}]
```

`kind`: `image | audio | voice | video | document | sticker`. The host
ingests images as today; other kinds land as file references under the
connector's data dir that the AGENT can reach with its normal tools
(read, bash) — provenance-labeled, gate-approved content, not silently
dropped. Captions join `text` (as telegram users expect). The
containment rule (must resolve inside the data dir, read-and-delete for
images; non-image files are MOVED into a per-message subdir and cleaned
after the turn) is unchanged in spirit.

### Edits, deletes, reactions

Inbound (connector → host), all optional-by-capability:

| frame | fields | host default behavior |
|---|---|---|
| `message_edited` | `id`, `ts`, `text`, `entities` | if the edited message is still queued (un-turned), replace it in place; otherwise inject a short system note "user edited an earlier message: …" |
| `message_deleted` | `id` | drop from queue if queued; otherwise ignore (no retroactive transcript surgery) |
| `reaction` | `message_id`, `user_id`, `key`, `removed` | on the bot's own messages: system note; on others: ignore (v2) |

Outbound (host → connector), gated on connector capabilities:

| frame | fields | purpose |
|---|---|---|
| `edit` | `id` (corr), `message_id`, `text` | fix or progressively update the bot's own message — the streaming-reply primitive; answered by `result` |
| `react` | `id` (corr), `message_id`, `key`, `remove` | lightweight ack without a turn; answered by `result` |
| `delete` | `id` (corr), `message_id` | retract the bot's own message; answered by `result` |

Reaction semantics, pinned by the deep dives: `key` is an OPAQUE STRING,
not "an emoji" — Matrix allows any string (and custom-image keys are
`mxc://` URIs under MSC4027); Discord custom emoji key on the id, not
the name (which nulls when the emoji is deleted). Unicode emoji is the
only interoperable subset and what terva emits. Removal is first-class
(`removed: true`) because Matrix models un-reacting as a REDACTION of a
separate event and Discord delivers a distinct thin REMOVE payload —
recomputing by absence doesn't work. And reactions are a LOSSY signal
channel: both platforms can drop events across connection gaps
(Matrix's server doesn't aggregate annotations; Discord loses gateway
events unless Resume succeeds), so nothing security-relevant may hinge
on a reaction stream alone — see attestation under Interactions. Edits
always reference the ORIGINAL message id (Matrix forbids edit-of-edit;
the connector owns latest-wins collapsing), and `min_edit_interval_ms`
in capabilities tells the host how fast it may stream edits (Discord
practice: ~1/s).

Explicitly skipped in v2: inbound typing, read receipts, presence.
High-noise, low-agent-value; the capability namespace leaves room. (One
reserved outbound nicety: `status {text}` — Discord bots can set a real
custom status, a coarse GLOBAL "working on X" lamp. Reserved, not core:
it's per-bot, not per-chat, and no other consumer needs it yet.)

### Interactions: ask and answer (approvals as a wire primitive)

The single highest-leverage v2 addition. terva constantly needs a
small, gated answer from a specific human: the tool-approval ladder
(bot mode is yolo-only today precisely because chat cannot answer a
question), AskUserQuestion, group admission, pairing confirmation. v1
can only shout text into the void. v2 makes "ask a constrained
question, get an attributed answer" one primitive — and the connector,
not the host, decides how to render it with the best UI its service
has:

```json
→ {"type":"ask","id":"a1","chat_id":"c1","reply_to":"m-12",
   "text":"terva wants to run `rm -rf build/` — approve?",
   "options":[{"key":"approve","label":"Approve","style":"affirm","hint":"👍"},
              {"key":"deny","label":"Deny","style":"deny","hint":"👎"}],
   "restrict_to":["u1"],"expires_ms":120000}
← {"type":"result","id":"a1","message_id":"m-90"}
← {"type":"answer","ask_id":"a1","key":"approve",
   "user_id":"u1","username":"drew","attestation":"attested"}
→ {"type":"ask_close","id":"a2","ask_id":"a1","outcome":"approved by drew"}
```

- **Option keys, never widgets, on the wire.** The Discord connector
  renders buttons (custom_id = `ask_id:key` — interactions arrive with
  no intent, survive restarts, and identity is server-attested); the
  Matrix connector pre-seeds reactions with the `hint` emoji (the
  Draupnir pattern) or renders an MSC3381 poll for multi-choice; the
  telegram connector uses an inline keyboard; a bare service gets
  numbered text options with the connector parsing the reply.
- **`attestation` on every answer**: `attested` (the platform proves
  who answered — Discord interactions, Matrix poll responses, telegram
  callback queries) vs `best_effort` (reaction streams, parsed text).
  Host policy consumes this: an allow-once tool approval may accept
  `best_effort`; allow-always or anything durable requires `attested`
  — and the riskiest approvals can simply refuse chat and require the
  TUI. (The connector already owns the channel, so chat approval
  ultimately trusts the connector binary; attestation defends against
  PLATFORM-level weakness — reaction churn, gateway loss, spoofable
  text — not against a malicious connector. Same trust doctrine as
  everywhere else in this stack: per-connector policy caps what is
  approvable over chat at all.)
- **`restrict_to` is enforced late and twice.** No platform can hide a
  button per-user (visibility = clickability on Discord); the
  connector filters answers server-side (Discord: ephemeral "not for
  you" reply), and the HOST re-filters regardless — the wire contract
  is "answers only from these ids", not "only these ids see it".
- **First valid answer wins; the host closes.** Users churn (un-react,
  re-react, revote — Matrix polls are latest-wins, reactions toggle
  freely), so the host treats the first answer from an allowed
  responder as THE answer and immediately sends `ask_close`, whose
  `outcome` the connector renders into the message (Discord:
  UPDATE_MESSAGE disabling the buttons, "approved by drew" — the audit
  trail lives in the channel, not just the log). `ask_close` with no
  answer (timeout, turn cancelled) withdraws the controls the same
  way.
- **E2EE nuance from the Matrix dive**: reaction keys ride CLEARTEXT
  even in encrypted rooms, so options rendered as reactions expose
  only their hint emoji — never encode sensitive content in `hint` or
  `key`; the (encrypted) message `text` carries the substance.
- **The v1/absent-capability floor is host-side**: when a connector
  declares no `asks` feature, the host sends the question as a plain
  message ("reply 1 to approve, 2 to deny") and interprets the next
  message from an allowed responder — `best_effort` by definition.
  Approvals-over-chat therefore work with EVERY connector from day
  one; capabilities only upgrade the UX and the attestation.

What this unlocks, concretely: `terva bot` grows real permission modes
(the yolo default becomes a choice instead of a necessity); group
admission's `/approve` becomes an ask in the owner's DM fired by
`chat_membership`; AskUserQuestion renders natively in chat and play
modes; pairing can confirm instead of first-come-first-claimed.

### Speaker identity (the cast on the wire)

Personas and the `--play` cast want DIFFERENT characters speaking in
one chat. Outbound `send` gains an optional speaker:

```json
{"type":"send","id":"s1","chat_id":"c1","text":"The airlock hisses open.",
 "speaker":{"key":"kaiku","name":"Kaiku","avatar_path":"…"}}
```

- `key` is stable across the session and does real work on both major
  platforms: Discord connectors keep ONE managed webhook per channel
  (the PluralKit/Tupperbox pattern — per-message `username`/
  `avatar_url` overrides, `MANAGE_WEBHOOKS` required, ≤80-char names,
  no "clyde"/"discord" substrings) and Matrix connectors emit MSC4144
  per-message profiles (whose `id` enables retroactive profile
  updates) with the standard displayname-prefix fallback, upgrading to
  appservice ghost users where the deployment allows.
- Capability is three-valued: `speaker:full` (name + avatar),
  `speaker:name_only`, or absent — in which case the HOST prepends
  `**Kaiku:** ` and sends plain text, so the cast works on every
  connector immediately and connectors stay dumb.
- Platform constraints ride the contract, not the host's imagination:
  Discord webhook messages cannot be real replies, cannot carry
  interactive components, and never reach DMs — therefore **asks
  always come from the bot principal, never from a speaker**, and a
  speaker message's `reply_to` may degrade to a quote-embed or prefix.
  `result.message_id` is still returned (`?wait=true`) so edits and
  reactions work on speaker messages.

### Work-stream threads (outbound)

Dispatch/swarm work wants a thread per task so a busy chat stays
readable. One request/response pair:

```json
→ {"type":"thread_start","id":"t1","chat_id":"c1",
   "from_message_id":"m-12","name":"refactor: extract session core"}
← {"type":"result","id":"t1","chat_id":"c1.t-99"}
```

The result's `chat_id` is a NEW chat of kind `thread` (with
`parent_id` set on its messages); everything else — sends, edits,
asks, speakers — just targets it like any chat. Mapping is natural
everywhere it exists: Discord Start-Thread-from-Message (messages
inside arrive as ordinary events with the thread as channel and
auto-archive keeps the guild under its active-thread cap), Slack
`thread_ts`, Matrix `m.thread` relations targeting the root (the
connector synthesizes the `is_falling_back` reply fallback and mints
the composite chat id). Flat services (telegram non-forum) simply
don't declare `threads_out`, and the host keeps everything in the main
chat with prefixes.

### Capability exchange, both directions

Feature detection is a flat string set (the XMPP disco / Matrix
unstable_features shape — extensible without a boolean per release),
riding next to v1's numeric limits, which stay:

```json
"capabilities":{"max_text_len":4096,"typing_refresh_ms":5000,
  "min_edit_interval_ms":1000,
  "sends_images":true,"sends_files":true,
  "features":["message_ids","edits_in","edits_out","reactions_in",
              "reactions_out","entities","chat_kinds","chat_membership",
              "attachments:voice","attachments:document",
              "asks","asks:attested","speaker:full","threads_out"]}
```

hello_ack (host) gains the same `features` array meaning "what I will
CONSUME" — the telegram allowed_updates / Discord intents pattern, so a
connector never pushes events nobody asked for and can skip work an
old host would discard. Rule: a side may only EMIT an optional frame
the peer declared; everything else is exactly v1. Unknown frames and
unknown fields remain logged-and-ignored, never fatal — that, plus the
feature sets, is what lets third-party connectors version freely.

Open (post-v2, surfaced by the first live agent report): a **render
advisory** — some way for a connector to describe what markdown its
service renders (Discord and telegram both drop tables), so the host
can brief the model per service instead of the generic "simple
markdown only" line the chat-context intro carries today. Candidate
shape: a `renders` capability list ("tables", "headings", …); needs a
second consumer before it earns a wire slot.

### Event sources (sketch only — deliberately not v2 wire)

Webhooks, schedules, and file-watches want the same wake path as chat
messages but are not messages: no user, no chat, different gate. The
shape on this wire, when it comes, is one frame kind:

```json
{"type":"event","source":"webhook","kind":"github.push",
 "ts":…,"data":{…},"summary":"push to main by drew"}
```

routed through a DIFFERENT host gate: per-source consent config (never
pairing), rate caps, and a queue-behind-turn posture — an event never
preempts. The one-envelope-many-event-types shape is what n8n, Home
Assistant, Matrix appservice transactions, and Bot Framework
Activities all converged on, and it is the direction MCP's Triggers &
Events WG is heading (chartered 2026-03-24, AWS + Anthropic leads;
verified 2026-07: still "Ideating", incubation repo has no schemas
yet). Don't block on the WG; DO stay alignable — explicit
subscription/opt-in (our `features` exchange), per-chat ordering (the
carrier gives it), idempotency (connector-owned dedupe). This stays a
sketch until either the WG ships shapes worth adopting or a concrete
terva event source forces the issue; the tunnel means whatever lands
here reaches extension-bundled connectors for free. **The full hold
record — including the MCP context on both sides and the concrete
reopen criteria — is
docs/decisions/0002-connector-event-sources-hold.md; start there
before reopening this.**

## Host design (the other half)

The wire above is useless without two chat-stack evolutions. Both are
separable from the wire and from each other, and both keep the current
single-user-DM behavior as the zero-config default.

### Group admission (gate v2)

Pairing generalizes from "one allowed user id" to an OWNER plus an
approval surface:

- First `/start` in a DM still claims the bot; that user is the owner.
  DM behavior is exactly today's.
- In a chat of kind ≠ dm, the bot is silent-by-default. The owner
  admits a chat with `/approve` spoken IN that chat (or from the DM:
  `/approve <chat-id>`); `/revoke` reverses. Admission state persists
  next to pairing.
- Per-chat mode: `mention` (default — respond only to messages carrying
  a `bot_mention` entity or replying to the bot) or `all`. Group
  content is still untrusted input; the mention gate is UX, the
  owner-approval gate is the security boundary. Non-owner group members
  get REACH (their messages start turns in approved chats) but never
  AUTHORITY (no /approve, /stop stays owner-only, tool policy
  unchanged).

### Approvals over chat (the permission ladder reaches bot mode)

The ask/answer primitive is wire plumbing; the policy lives here. The
bot daemon and the TUI bridge gain a chat-approval mode where a tool
call that needs consent posts an ask to the OWNER (in the chat that
started the turn, or the owner's DM for group-originated turns) and
blocks the turn on the answer, exactly like the TUI's permission
prompt. Policy knobs, all host-owned:

- which permission decisions may be made over chat at all (default:
  allow-once yes; allow-always / trust-grants require `attested`
  answers or the TUI; some classes never leave the TUI);
- who may answer: the owner, always — `restrict_to` plus host-side
  re-filtering; group members never approve anything;
- timeout behavior: an expired ask denies (fail-closed) and says so.

This is what retires "bot mode = yolo" as a structural fact. It also
gives AskUserQuestion a chat rendering (options map 1:1) and turns
group admission into an ask fired by `chat_membership` instead of a
command the owner has to know.

### Per-chat sessions (Loop v2)

`chat.Loop` keys conversations by `chat.id`:

- One `core.Agent` per active chat (shared model/persona/system prompt,
  separate transcripts), created lazily, LRU-bounded (default ~8 live;
  eviction compacts and drops). `/status`, `/stop`, queue, and
  auto-compaction all become per-chat; the drain stays single-flight
  GLOBALLY at first (one provider stream at a time — simple, and the
  queue semantics users already know), with per-chat concurrency as a
  later knob.
- The idle nudge remains owner-DM-only.
- The TUI Bridge is explicitly NOT per-chat (it mirrors one session by
  design); it keeps first-chat-wins and ignores the rest.

## Staging (each stage lands alone, CI-green, docs updated)

- **A. Identity foundations — SHIPPED** (with two implementation
  deviations from the sketches above, chosen for wire hygiene: flat
  additive `chat_kind`/`chat_title` fields instead of a nested `chat`
  object, keeping `chat_id` primary and every v1 golden frame
  byte-identical; and connector features are AUTHOR-DECLARED in
  `Capabilities.Features` rather than derived — the transport doesn't
  exist yet at hello time). Protocol 2 negotiated via the existing
  [min,max] handshake; hosts normalize the v1 own-id-in-reply_to shape
  onto `Message.ID`, so consumers only ever see v2 semantics. The
  telegram built-in, telegram-ext, discord (D1), and the loopback
  example all fill identity; discord also implements the
  `connsdk.MessageIDSender` upgrade (`result.message_id`).
- **B. Group admission — SHIPPED** (deviations and notes recorded):
  entities and chat_membership ride features `"entities"` /
  `"chat_membership"`; gate v2 lands exactly as sketched — DMs
  unchanged, non-DM chats silent-by-default, owner `/approve [all]`
  in-chat (leading @bot mention tolerated, since mention-gated
  services only deliver addressed messages) or `/approve <chat-id>
  [all]` from the DM, `/revoke` both places, admission persisted at
  `$TERVA_HOME/chat/admissions-<service>.json` and shared by the bot
  daemon and the TUI bridge. Mention mode accepts the transport's
  bot_mention entity OR a host-side `@username` scan, so entity-less
  connectors still gate correctly (reply-to-bot detection is the
  recorded gap). Group `/start` cannot claim an unpaired bot —
  pairing stays a DM act. The admission ask shipped with it: a
  membership `added` event fires one owner-DM ask
  (approve/approve-all/ignore, restrict_to owner, fail-closed) and a
  `removed` event auto-revokes. Two tightenings the group work forced
  on earlier stages: the loop only adopts DM chats as the owner
  channel (idle nudges and owner asks can no longer wander into a
  group), and AskTarget sends group-originated approval questions to
  the owner's DM instead of the group (implementing the sketch's
  "owner's DM for group-originated turns"). Discord dogfood:
  bot_mention entities from the mentions array (located at the raw
  `<@id>` token; unlocated for reply-pings) — which composes with the
  no-privileged-intent posture, since mentioning messages are exactly
  the guild messages Discord delivers content for — and guild
  join/leave mapped onto the SYSTEM channel (approving it admits that
  channel only; Discord never reports who added the bot). Recorded
  follow-ups: telegram native doesn't emit membership or entities yet
  (its mention-gating rides the @username scan), and per-guild
  admission semantics on Discord remain an open design question.
- **C. Per-chat sessions — SHIPPED** (deviations recorded): the Loop
  gains an agent factory — DMs keep the primary agent (and its
  persisted session, unchanged), every other chat lazily mints its own
  agent with the same model/prompt/extension hooks and a separate
  transcript, LRU-bounded (default 8). Eviction DROPS rather than
  compacts-and-drops (per-chat transcripts don't persist, so
  compaction buys nothing — recorded deviation). /status and /stop are
  per-chat: status reports the chat's own context/cost/queue, stop
  cancels the running turn only when it belongs to that chat and drops
  only that chat's queued prompts. The drain stays single-flight
  globally as sketched; credential refresh fans out to every live and
  future agent. The idle nudge stays owner-DM-only (the stage-B
  tightening already pinned that), and the TUI Bridge is untouched by
  design. Per-chat session PERSISTENCE (group transcripts surviving a
  restart) is the recorded open item.
- **D. Edits & reactions — SHIPPED** (deviations recorded): the six
  frames land with `chat_id` on every one (the sketch omitted it;
  Discord's REST needs channel+message, and hosts shouldn't need a
  global message index) and `username` on inbound reactions. Feature
  strings split by direction — "edits_in|out", "deletes_in|out",
  "reactions_in|out" — with `min_edit_interval_ms` in capabilities.
  Host defaults as tabled, with one deviation: the too-late-to-replace
  cases become NOTES the chat's next prompt carries rather than
  injected system turns (an edit or reaction spam stream must not be
  able to drive the agent). Reactions on the bot's own messages are
  recognized via a bounded ring of result.message_id values the
  session recorded from its own sends and asks. The outbound trio
  rides chat.Editor/Reactor/Deleter through all three carriers;
  consumers (streaming replies via edit — the big one — and
  reaction acks) remain open items. Discord D5: MESSAGE_UPDATE
  (content-less partials dropped) / MESSAGE_DELETE / reaction
  add-remove via the still-unprivileged reaction intents, own-toggle
  echo hygiene, custom emoji keyed on ID, and REST
  edit/delete/add-remove-reaction outbound at a declared 1/s edit
  pace.
- **E. Attachment kinds — SHIPPED**: the attachment object grows
  kind/name/size/duration_ms/caption behind "attachment_kinds"
  (unlabeled = image, the v1 read). Hosts ingest images exactly as
  before; other kinds are MOVED into a per-message directory under
  the connector's data dir (containment preserved), surface to the
  agent as a labeled file manifest on the prompt, and are cleaned
  after the turn — including when a queued message is withdrawn by
  /stop or message_deleted. Captions join the text when not already
  present. Jail note: bot mode is unjailed by default so the agent
  can read the staged paths; a --jail'd bot cannot reach them
  (recorded). Discord ingests all kinds with mime-mapped labels
  (voice arrives as "audio" — Discord flags voice on the message, not
  the attachment); telegram stays images-only (recorded follow-up
  with its membership/entity gaps).
- **F. Events — HELD by design** (the only unshipped stage). Hold
  record, MCP context, and reopen criteria:
  docs/decisions/0002-connector-event-sources-hold.md.
- **G. Interactions — SHIPPED** (with D2; deviations from the sketches
  above, recorded here): the ask frame's command `id` doubles as the
  ask's identity (no separate ask_id field on the way out; answers and
  ask_close reference it as `ask_id`), and `ask_close` is a command
  with its own id + result like every other host command. The feature
  string is `"asks"`, author-declared like the stage-A features (the
  attestation grade rides each answer rather than a second feature).
  On the SDK, `Asker.Ask(ctx, a, deliver)` takes a per-ask deliver
  func and returns the rendered message id — no global answer sink,
  and the transport routes clicks by the ask id it embedded in its
  widget state. restrict_to is enforced THREE times in practice:
  service-side (Discord's ephemeral "not for you"), SDK-side before
  the frame leaves the process, and host-side at answer routing.
  Host half shipped: `connhost.Session.Ask` (first-valid-answer-wins,
  fail-closed timeout/cancel/session-death, close-with-outcome),
  `chat.Loop.Ask` with the numbered-text fallback floor (next matching
  message from an allowed responder; non-matching messages flow on as
  prompts), and `chat.ChatConfirmer` wiring the whole thing into the
  bot's ConfirmGate — `--approval ask/workspace` is now usable in bot
  mode, with allow-always requiring an ATTESTED answer (a parsed-text
  "always" downgrades to allow-once, with a note saying so). Not yet
  implemented from the sketch: per-decision-class policy (which
  permission classes may be approved over chat at all) — today the
  approval MODE decides what asks, and every ask goes to the paired
  owner; class-level knobs remain open. AskUserQuestion and group
  admission over asks also remain open consumers.
- **H. Speaker identity — SHIPPED** (with D3; deviations recorded):
  `send.speaker{key,name,avatar_path}` gated by the three-valued
  grade exactly as sketched ("speaker:full" / "speaker:name_only" /
  absent), with the host prefix fallback living in
  `connhost.Session.Send` — senders never check capability, they just
  set `Outgoing.Speaker`. The SDK adds the optional
  `SpeakerSender.SendAsSpeaker` (returns the message id) plus a
  defensive SDK-side prefix downgrade for a transport that declared
  the feature but lost the interface. Discord D3 ships NAME-ONLY: one
  managed "terva cast" webhook per channel (found by name before
  created — restarts must not accumulate toward the 15-per-channel
  cap), per-message username overrides sanitized to Discord's rules
  (≤80 chars, "clyde"/"discord" wedged with a ZWJ), reply_to dropped
  (webhooks cannot reply), and webhook failure (DMs, missing
  MANAGE_WEBHOOKS) degrading to the prefixed plain send so a cast
  line is never lost. speaker:full (avatars) is deferred: per-message
  webhook avatars need hosted URLs, not local paths — upgrading needs
  either an avatar-upload channel or per-speaker webhooks, recorded
  in the plan. The wire consumer (the --play cast actually sending
  per-actor messages over chat) is the open item — actor output today
  surfaces through tool results; wiring the cast engine onto
  Outgoing.Speaker is where this stage pays off.
- **I. Work-stream threads — SHIPPED** (with D4): `thread_start`
  (feature "threads_out") with the new thread chat id riding the
  result's `chat_id`; `chat.Threader` on every carrier gated by
  `Capabilities().ThreadsOut`; Discord maps to Start-Thread-from-
  Message (or a standalone public thread when no anchor is given,
  names capped at 100 chars) and resolves inbound thread messages to
  kind "thread" via the Guilds-intent channel cache. The consumers —
  dispatch/swarm surfaces threading their work streams — remain the
  open item, same shape as the cast for stage H.

A is a prerequisite for everything; B–E and G–I are mutually
independent. Nothing requires an extproto change at any stage — the
tunnel carries all of it opaquely (that was the point).

**Proving it**: an in-tree Discord connector is the designated
reference consumer — gated like telegram (out of the minimum build),
speaking connproto even when compiled in (an in-process carrier), and
holding a dogfood contract: a v2 stage merges only once the Discord
connector exercises it. Plan: docs/plans/discord-connector.md.

## Prior art notes (survey, 2026-07)

Ecosystems surveyed: Telegram Bot API, Slack Events API, Discord
Gateway, Matrix client-server + appservice, XMPP XEPs, Microsoft Bot
Framework Activities; n8n / Home Assistant / MCP for non-chat
triggers. What the vocabulary above leans on:

- **Message identity**: all four majors assign stable ids and deliver
  edits/deletes as SEPARATE events referencing them (telegram
  `edited_message`, Slack `message_changed`/`message_deleted`
  subtypes, Discord `MESSAGE_UPDATE`/`MESSAGE_DELETE`, Matrix
  `m.replace` relations + redactions) — never as re-sent messages.
  Edits carry full replacement content everywhere but Discord (which
  may send partials); v2 takes full-content. Id scope floor is
  per-chat (telegram ints, Slack ts); Discord snowflakes / Matrix
  event_ids satisfy it trivially. Telegram cannot deliver deletes to
  ordinary bots — one more reason `message_deleted` is optional.
- **Reply vs thread**: orthogonal concepts on every platform — pointer
  (`reply_to_message_id`, `message_reference`, `m.in_reply_to`) vs
  container (Slack `thread_ts`, Discord thread-channels, Matrix
  `m.thread`). Hence `reply_to` + `thread_id` as separate fields.
- **Chat model**: every platform ships a chat/conversation object with
  a kind enum (telegram `Chat.type`, Slack `channel_type`, Discord
  channel types, Bot Framework `conversationType`) and delivers the
  bot's OWN membership changes (`my_chat_member`,
  `member_joined_channel`, guild events, Matrix invites) — the
  `chat_membership` frame's precedent.
- **Content**: plain text body + typed attachments + annotation-layer
  markup is the consensus; markup ENCODING is the least-converged area
  (entity offsets vs HTML vs two markdown dialects), which is why v2
  models only entities that carry meaning the host acts on
  (bot_mention above all) and keeps text plain.
- **Reactions** are the one social signal all four both deliver and
  accept (telegram `message_reaction` — opt-in and admin-gated in
  groups, another reason capability flags matter). Typing-in, read
  receipts, presence are patchy (Matrix has all three, telegram none)
  — hence excluded, capability namespace reserved.
- **Capabilities**: advertised feature sets (Matrix
  versions/capabilities, XMPP disco feature URIs, MCP initialize) and
  consumer-declared interest (telegram `allowed_updates`, Discord
  intents, Slack event subscriptions) are the two live patterns; v2
  uses both — feature strings on hello, consumed-features on
  hello_ack. Degradation is per-feature and silent, never
  connection-fatal, everywhere.
- **Group admission**: three layers on every platform — human admits
  the bot per chat; content minimized by default in groups (telegram
  privacy mode delivers only commands/replies/mentions; Discord's
  MESSAGE_CONTENT intent gate imposes mention-only on unapproved
  bots); membership events feed the bot's own tracking. Gate v2 is
  exactly this stack with terva's owner on top.
- **Non-chat triggers**: one envelope, many event kinds is the
  converged automation shape (n8n trigger nodes, Home Assistant
  trigger platforms, Matrix appservice `transactions` batches, Bot
  Framework Activity types). MCP Triggers & Events WG (chartered
  2026-03-24) is still Ideating with no schemas — tracked, not
  blocked on.

Known divergences, on purpose: no per-frame event ids (stdio/tunnel
carriers are ordered and reliable; service-redelivery dedupe is the
connector's job, keyed on message id), no outbound rich formatting
contract, no rosters/state sync, no presence class.

### Rev-2 deep dives: Discord and Matrix receipts

What the second research round pinned down (feeding Interactions,
Speaker identity, and Work-stream threads above):

**Discord** (API v10, verified against live docs 2026-07):

- Components need no application-command scope — buttons ride any bot
  message; clicks arrive as INTERACTION_CREATE with **no intent
  required** and carry server-attested `member`/`user` identity. 3s to
  first callback, 15min followup token, but `custom_id` clicks on old
  messages still fire — approval state can survive restarts. The
  UPDATE_MESSAGE callback (type 7) is the "approved by X, buttons
  disabled" audit edit. This is why `attestation: attested` exists and
  why buttons are the approval gold path.
- Reactions: pre-seeding is the classic pattern but costs real time
  (~250ms/reaction community bucket; ≤20 emoji/message), needs
  ADD_REACTIONS + READ_MESSAGE_HISTORY, and REMOVE payloads are thin
  (no member object). Gateway gaps lose events; approval bots
  reconcile via Get Reactions before finalizing — hence reactions as
  `best_effort`.
- Per-message identity: Execute Webhook `username`/`avatar_url`
  overrides (PluralKit/Tupperbox run whole multi-identity products on
  this) — one cached webhook per channel, no DMs, no real replies, no
  interactive components on non-app webhook messages, name ≤80 chars
  minus "clyde"/"discord". `?wait=true` returns the message id.
- Threads: Start-Thread-from-Message; messages inside are ordinary
  MESSAGE_CREATE with the thread as channel_id + `parent_id` linkage;
  thread-per-task is an established agent-bot pattern (gpt-discord-bot
  et al.); auto-archive keeps guilds under the active-thread cap.
- Streaming: bot `content` caps at 2000 chars (embeds 4096); reference
  implementations edit at ~1/s — the `min_edit_interval_ms` hint.
  Typing expires after 10s and Discord's docs bless exactly the
  bot-computing case; presence (custom status, type 4) is global
  per-bot, not per-chat — hence `status` reserved, typing kept.
- MESSAGE_CONTENT stays privileged (>10k-user review threshold), but
  interactions and DMs and mentions bypass it — one more push toward
  ask-buttons and mention-gated groups.

**Matrix** (spec v1.11–1.15 era):

- Reactions are `m.reaction` events (`m.annotation` relation, opaque
  `key`); the server does NOT aggregate them (recount via /relations
  after sync gaps); duplicates are rejected server-side; removal is a
  REDACTION of the reaction event; keys ride cleartext in E2EE rooms.
  Pre-seeded self-reactions are the Draupnir management-room pattern.
- Polls (MSC3381): accepted but blocked-from-stable behind extensible
  events; unstable org.matrix.msc3381.* prefixes in production;
  Element-family support only; poll responses ARE encrypted and
  latest-wins per user — the `attested` multi-choice upgrade where
  available.
- Per-message profiles (MSC4144): open, implementations landing
  (bridges emit it + prefix fallback; Element doesn't render yet);
  its stable `id` is the wire's `speaker.key`. Appservice ghost users
  are the heavyweight correct multi-identity path; a plain bot should
  never mutate its own member state per message.
- Edits: `m.replace` with full `m.new_content`, same sender/type,
  edits-of-edits illegal — every edit targets the ORIGINAL id;
  latest-wins collapsing is connector-side. Redaction strips message
  content entirely (tombstone metadata only).
- Threads: `m.thread` relations all point at the ROOT; the sender
  synthesizes `is_falling_back` reply fallbacks for non-threaded
  clients — exactly the shape `thread_start`'s composite chat id wraps.
- E2EE bots in 2026: rust-crypto (matrix-bot-sdk) works with an
  unverified-device shield; matrix-nio still rides deprecated libolm;
  common practice is bimodal (rust-crypto or unencrypted rooms).
  Decryption is CONNECTOR-side; the wire stays plaintext (an
  `encrypted` provenance flag is reserved).
- Delivery: /sync long-poll is push-grade and portable — the right
  default for a personal agent; appservice mode is the upgrade for
  ghost identities, not for latency.
