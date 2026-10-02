# The terva connector frame reference (protocol 1 and 2)

The wire protocol a connector speaks: every frame, in both directions, for
versions 1 and 2. This is the maintained reference, and the schema it describes
is pinned by golden tests in `packages/agent/connproto`.

For installing a connector, running the bridge, or writing one with the Go SDK,
see [connectors.md](connectors.md). You do not need this page to use a connector
that already exists; you need it to write one.

One JSON object per LF-terminated line; frames up to 4 MiB are
accepted. The cap is RECOVERABLE, not fatal, in either direction: an
over-limit frame is skipped with a warning and the stream continues, so
a runaway text field or an oversized caption costs you that one frame
rather than the session. Do the same on your side, because a reader that dies
on an over-limit line turns a recoverable event into a reconnect. The
schema is pinned by golden tests in `packages/agent/connproto`;
breaking it means bumping the protocol version. (Version 1 was
deliberately DM-shaped, because it began life as the built-in telegram bot's
seam. The design rationale for everything version 2 added lives in
`docs/proposals/connector-protocol-v2.md`, now an as-built spec; THIS
file is the maintained frame reference.)

**The golden corpus is published** at
`packages/agent/connproto/testdata/golden.jsonl`: one
`{"name":…,"dir":…,"frame":…}` object per line, where `frame` is the
exact bytes terva emits, carried as a JSON string so it survives the
envelope intact. Point your conformance suite at that file rather than
copying the frames by hand: a copy drifts silently, and a fetch cannot.
Note that terva encodes with Go's `encoding/json`, which escapes `<`,
`>` and `&` where most encoders emit them literally. Both are valid
JSON and every reader here accepts either, so this never matters on the
live wire, but it does mean the corpus is deliberately kept free of
those characters so byte-exact comparison stays meaningful across
languages.

`dir` is `conn_to_host` or `host_to_conn`. The two directions are not
symmetric in use: a connector only ever *encodes* `conn_to_host` frames,
so the useful split is to assert those byte-exact against the corpus and
the rest on decode. It is the same field
[extensions.md](extensions.md)'s extproto corpus publishes: one
envelope shape across both protocols, so a consumer speaking each needs
only one loader.

**Protocol 2 is live and additive.** The handshake negotiates the
highest version both sides speak (hello carries
`protocol_min`/`protocol_max`; hello_ack answers with the pick). At
protocol 2: `message` gains `id` (the message's OWN id, stable within
its chat, and REQUIRED), `ts` (unix ms), `chat_kind`
(`dm|group|thread|channel`), `chat_title`, and `scope_id` (the container
the chat belongs to, a Discord guild say; empty means scopeless, and it
is what lets the admission gate approve or revoke a whole scope at once);
`reply_to` means truly in-reply-to in both directions; `result` gains
`message_id` (the id of what a send created); and both sides exchange feature strings
(`capabilities.features` on hello = what the connector produces or
accepts; on hello_ack = what the host consumes). Everything past that
is gated by feature strings, never version bumps. Protocol-1
connectors keep the old shape, the message's own id riding
`reply_to`, which hosts normalize automatically, so nothing breaks
either direction.

The full feature-string vocabulary (declare only what you implement):

| feature | you provide | detailed below |
|---|---|---|
| `entities` | message markup, `bot_mention` above all | entities block |
| `chat_membership` | the bot's own admission events | entities block |
| `edits_in` / `deletes_in` / `reactions_in` | inbound edit/delete/reaction streams | edits block |
| `edits_out` / `reactions_out` / `deletes_out` | the outbound commands (+ `min_edit_interval_ms`) | edits block |
| `asks` | interactive questions with attributed answers | ask block |
| `speaker:full` / `speaker:name_only` | alternate outbound identities | speaker block |
| `threads_out` | opening work-stream threads | threads block |
| `chat_parents` | parent context on inbound thread messages | threads block |
| `typing_stop` | withdrawing the typing indicator on demand | the `typing` rule |
| `attachment_kinds` | labeled beyond-image attachments | attachment rules |

(`message_ids` and `chat_kinds` also appear in the wild as declared
features; hosts treat the protocol-2 identity fields as
presence-evident, so declaring them is informative, not load-bearing.)

The handshake, with the connector speaking first:

```json
→ {"type":"hello","name":"discord","version":"1.0.0","protocol_min":1,"protocol_max":2,
   "capabilities":{"max_text_len":2000,"typing_refresh_ms":8000,"sends_images":true,"sends_files":true,
     "features":["entities","edits_in","reactions_out","asks"]}}
← {"type":"hello_ack","protocol":2,"zot_version":"1.2.3","terva_version":"1.2.3",
   "data_dir":"$TERVA_HOME/connectors/discord/data",
   "capabilities":{"features":["entities","edits_in","reactions_out","asks"]}}
```

`zot_version` and `terva_version` carry the same host version string <!-- rename:keep -->
(terva kept zot's legacy key for compatibility when it forked; see
[fork.md](fork.md)). Read `terva_version` and fall back to
`zot_version`. The old key is deprecated and will be dropped after the <!-- rename:keep -->
connector-SDK deprecation window; the Go SDK already handles this for you.

Versioning is negotiated, not announce-only: hello carries
`protocol_min`/`protocol_max`, and terva refuses the spawn with a clear
error when its own version falls outside the range. A connector that
depends on protocol-2 message ids sends `protocol_min: 2`, so an older
host refuses it here rather than at `connect`. The Go SDK sets this from
`Config.ProtocolMin`. terva kills a
spawned connector that sends no hello within 3 seconds; a connector
carried inside an extension gets 5, measured after the extension
registers the connector role (itself allowed 3).

Connector processes start from a sanitized environment: terva strips
loader/interpreter injection vars (`LD_*`, `DYLD_*`, `PYTHONPATH`,
`NODE_OPTIONS`, `BASH_ENV`, …) before the spawn, same as extensions.
Everything else passes through: `PATH`, `HOME`, and the tokens your
connector reads.

The session, host to connector:

```json
{"type":"connect"}
{"type":"send","id":"42","chat_id":"...","reply_to":"...","text":"..."}
{"type":"send_image","id":"43","chat_id":"...","path":"/abs/file.png","caption":"..."}
{"type":"send_file","id":"44","chat_id":"...","path":"/abs/report.pdf","caption":"..."}
{"type":"typing","chat_id":"..."}
{"type":"typing","chat_id":"...","active":false}
{"type":"shutdown"}
```

Connector to host:

```json
{"type":"connected","id":"1234","username":"tervabot"}
{"type":"connect_error","error":"bad token"}
{"type":"message","chat_id":"...","scope_id":"...","user_id":"...","username":"...","reply_to":"...",
 "text":"...","attachments":[{"mime_type":"image/png","path":"<data_dir>/in/abc.png"}]}
{"type":"result","id":"42","error":""}
{"type":"warn","message":"gateway reconnecting"}
```

`warn` frames are **operator-facing, not user-facing**: they surface in
terva's own output (the terminal under `bot run`,
`$TERVA_HOME/logs/bot.log` under `bot start`) and never reach a chat.
Use `warn` for what an operator must see live (degraded auth, a
reconnecting gateway, a dropped attachment) and your own stderr, which
lands in `$TERVA_HOME/logs/connector-<name>.log`, for everything else.
Nothing you emit on either channel is visible to the humans in the
chat; to reach them, send a message. In the Go SDK, a transport sends a
`warn` with `Session.Warn`.

Interactive asks (protocol 2, feature `"asks"`) need two things. Declare the
feature in your hello `capabilities.features` AND implement the rendering, and
terva can pose constrained questions with the best widget your service
has. **Two kinds of question ride this one frame:**

- **Tool approvals**: the confirm gate asking whether a call may run.
  Fail-closed: an unanswered approval **denies** the call.
- **Agent questions**: `ask_user_question`, and the prefix-change
  guard's compaction offer. Fail-*open* by design: an unanswered
  question is a **dismissal**, so the agent decides for itself and the
  turn continues. A bot whose owner is asleep must not hang, and must
  not surface a failure the model would retry into a loop.

That difference is the only thing that distinguishes them on your side:
the frames are identical, so a connector implements `"asks"` once and
gets both.

```json
→ {"type":"ask","id":"a1","chat_id":"...","reply_to":"...",
   "text":"terva wants to run `rm -rf build/` — approve?",
   "options":[{"key":"approve","label":"Approve","style":"affirm","hint":"👍"},
              {"key":"deny","label":"Deny","style":"deny","hint":"👎"}],
   "restrict_to":["u1"],"expires_ms":120000}
← {"type":"result","id":"a1","message_id":"m-90"}
← {"type":"answer","ask_id":"a1","key":"approve",
   "user_id":"u1","username":"drew","attestation":"attested"}
→ {"type":"ask_close","id":"a2","ask_id":"a1","outcome":"Approve — @drew"}
← {"type":"result","id":"a2"}
```

Option KEYS ride the wire, never widgets: render buttons (Discord), an
inline keyboard (telegram), pre-seeded reactions, or numbered text, as
you choose. The ask's `id` doubles as its identity: answers reference
it as `ask_id`, and its `result` acknowledges the RENDERING
(`message_id` of the posted question), not the human. Send `answer`
frames whenever users interact, zero or more of them, until `ask_close`
tells you to withdraw the controls and render `outcome` into the
question message (the audit trail lives in the channel). Set
`attestation` honestly, and grade it by PROOF rather than by widget:
`"attested"` when your platform authoritatively proves who answered,
and only then. Button interactions and callback queries qualify, and
so does a signed event whose sender the platform authenticates (a
Matrix reaction does; document where your platform lands).
Use `"best_effort"` for anything parsed out of text, or otherwise
spoofable. Durable grants (allow-always) require attested answers, so
an inflated grade is a security lie. Filter `restrict_to` service-side
where you can (an ephemeral "not for you" beats silence); terva
re-filters regardless. Connectors WITHOUT the feature still work: terva
falls back to a numbered plain-text question and parses the next
matching reply, so approvals-over-chat reach every service from day one.
The feature only upgrades the widget and the attestation.

**One case where the fallback is the RICHER path, not the poorer one.**
The ask frame carries a fixed option list and returns one key, so a
question that wants a *written-in* answer, or one that lets the user
pick *several* options, cannot round-trip through the widget however
good your connector is. The numbered-text floor has no such limit, because
"reply 1,3" is several choices and free text is just text, so terva
routes those two kinds to the floor **even on a connector that declares
`"asks"`**. You will see a plain-text question where you expected your
buttons; that is deliberate.

The reasoning is worth stating, because it decides which way every
future degradation goes: narrowing the RENDERING is visible to anyone
reading the chat and costs a nicer widget, while narrowing the QUESTION
would hand the model one choice where the user wanted three, with
nothing recording the difference. The first is recoverable, the second
is invisible. The cost is attestation, because floor answers are
`best_effort`. That is safe here only because approvals never ask
these kinds, and approvals are the only decision that grants anything
durable. Do not build a policy that needs attested identity on top of a
multi-select.

Rendering these natively is tracked work and will arrive behind its own
feature string (select menus, modals), so nothing you implement today
changes.

Speaker identity (protocol 2, feature `"speaker:full"` or
`"speaker:name_only"`) exists because personas and the `--play` cast want
different characters speaking in one chat. Declare a grade and `send` may carry
an alternate identity:

```json
→ {"type":"send","id":"s1","chat_id":"...","text":"The airlock hisses open.",
   "speaker":{"key":"kaiku","name":"Kaiku","avatar_path":"..."}}
← {"type":"result","id":"s1","message_id":"m-90"}
```

Render it with whatever your service has. Discord keeps one managed
webhook per channel with per-message username overrides (the
PluralKit pattern); Matrix emits per-message profiles. `key` is stable
across the session and keys your per-speaker state; `avatar_path` (a
local file, same-host convention) only arrives at `speaker:full`
connectors. Platform limits are yours to absorb: webhook messages
can't be real replies (drop or quote `reply_to`), and asks NEVER carry
a speaker, because they come from the bot principal. Still return
`result.message_id`. Connectors with no grade never see the field:
terva prepends `**Name:** ` itself, so the cast works everywhere from
day one.

Entities and membership (protocol 2, features `"entities"` /
`"chat_membership"`) are the group-admission signals. `message` may
carry minimum-viable markup (offsets in Unicode code points over
`text`; `bot_mention` is the load-bearing kind, because it drives terva's
group mention-gating; offset and length both zero means "mentioned,
but not locatable in the text"):

```json
"entities":[{"kind":"bot_mention","offset":4,"length":9},
            {"kind":"mention","offset":20,"length":5,"user_id":"u9"}]
```

And the connector may report the BOT's own admission changing. That is the
hook that lets the owner approve a group the moment the bot lands in
it instead of at the first awkward message:

```json
{"type":"chat_membership","chat":{"id":"c9","kind":"group","title":"ops"},
 "change":"added","by_user_id":"u1","by_username":"drew"}
```

Re-announcing current membership on reconnect is safe, and is how you
self-heal a frame terva missed: a duplicate `added` for a chat terva
has already approved is a no-op, and one for a chat the owner has
already been asked about does not ask again. That suppression is per
host RUN and lives in memory, so the one cost of over-announcing is
that a chat the owner ignored will prompt again after terva restarts.

Both are optional enrichment: without entities terva falls back to
scanning for `@username`, and without membership events the owner
admits chats with `/approve` when the first message arrives. Group
admission itself is host policy (owner-only, silent-by-default,
mention-gated), so your connector stays deliberately dumb about trust.

Edits, deletes, reactions (protocol 2, features `"edits_in"` /
`"deletes_in"` / `"reactions_in"` inbound and `"edits_out"` /
`"reactions_out"` / `"deletes_out"` outbound; declare each side you
implement):

```json
← {"type":"message_edited","chat_id":"...","id":"m10","ts":1751469000123,"text":"fixed"}
← {"type":"message_deleted","chat_id":"...","id":"m10"}
← {"type":"reaction","chat_id":"...","message_id":"m-90","user_id":"u1","key":"👍","removed":false}
→ {"type":"edit","id":"e1","chat_id":"...","message_id":"m-90","text":"updated"}
→ {"type":"react","id":"r1","chat_id":"...","message_id":"m-12","key":"👀"}
→ {"type":"delete","id":"d1","chat_id":"...","message_id":"m-90"}
```

Edits always reference the ORIGINAL message id (collapse edit chains
latest-wins yourself), and `min_edit_interval_ms` in your capabilities
tells terva how fast it may stream edits.

**`chat_id` on these three frames is load-bearing.** terva matches
edits and deletes to queued and delivered messages on the compound key
(`chat_id`, `id`), and routes reaction notes by `chat_id`. Emit the
same `chat_id` the target message was delivered under, not merely a
chat that contains it. This bites platforms where one message is
addressable in more than one scope (a thread and its parent room, say):
the platform's edit/delete/reaction event often does not re-state the
sub-chat, so your connector must remember, or re-derive, the delivered
scope. A frame under the wrong chat id is not rejected. For edits and
deletes it silently misses the match: a queued message keeps its stale
text, and a deleted one can still become an agent turn. For reactions
the note is simply filed against the wrong conversation.

Reaction `key` is an opaque
string, and unicode emoji is the interoperable subset (what terva emits);
key custom emoji on their platform ID, not their name. Removal is
first-class (`removed: true`); never recompute it by absence. Apply
echo hygiene to reactions exactly as to messages: the bot's own
toggles must not come back inbound. Reactions are a LOSSY channel on
every surveyed platform, so terva treats them as context, never
authority. Host defaults: an edit that arrives before the message's
turn rewrites the queued prompt; after, it becomes a note on the
chat's next prompt; deletions withdraw queued prompts; reactions on
the bot's own messages become notes. Notes reach the model typed and
bracketed (`[chat event: message_edited] …`) so it reads them as
connector state rather than user text; no-op edits (embed unfurls) and
re-deliveries coalesce; and edits/deletes of the bot's OWN messages
(ask outcomes rendering, streaming edits) are dropped host-side, so a
connector that misses that echo case is still covered, **provided you
return `result.message_id`**. That field is the only thing that tells
terva which messages are the bot's, so omitting it costs you more than
correlation: your own ask-outcome re-renders come back as "the user
edited a message", and reactions stop becoming notes entirely, since
only reactions on the bot's own messages ever do. The record is bounded
to the most recent few hundred sends of a session, so the net covers
recent messages rather than your whole history. Each chat's
first prompt also carries a one-time `[chat context]` line (service,
chat title, and a formatting reminder that chat apps don't render
tables), and messages in multi-user chats are attributed
(`@name: …`) so the model knows who is speaking.

Work-stream threads (protocol 2, feature `"threads_out"`) use one
request/response pair to open a thread, so a busy chat stays readable:

```json
→ {"type":"thread_start","id":"t1","chat_id":"...",
   "from_message_id":"m-12","name":"refactor: extract session core"}
← {"type":"result","id":"t1","chat_id":"t-99"}
```

The result's `chat_id` is a NEW chat of kind `thread`; sends, asks,
and speakers target it like any chat, and messages inside it arrive as
ordinary `message` frames with the thread as their `chat_id`. Edits,
deletes, and reactions touching those messages must carry that
same thread `chat_id`, per the correlation rule above.
`from_message_id` anchors the thread where your service supports it
and may be absent. Flat services simply don't declare the feature.

At protocol 2, connectors can declare `chat_parents` and send the following fields when the host also declares it:

```json
{"type":"message","id":"m10","chat_id":"opaque-thread","chat_kind":"thread",
 "parent_chat_id":"parent-room","parent_chat_kind":"group","user_id":"u1","text":"hello"}
```

Both parent fields must be present and nonempty. The parent kind must be `dm`, `group`, or `channel`.
The parent ID must differ from the thread ID. The connector classifies the containing chat from service metadata.
The host never parses a thread ID to find its parent. `scope_id` remains a revocation grouping key and grants no access.

A DM thread admits only the paired owner. A group or channel thread inherits its parent's current mention/all policy.
The host saves the parent association and any explicit thread restriction, without copying the parent's grant.
The admission store uses a version-2 envelope and still reads both legacy map formats.
Older hosts reject that envelope and forget approvals, rather than interpreting thread restrictions as grants.
Owner-only `/approve` sets a mention restriction; `/approve all` removes that restriction. Neither command widens the parent's policy.
Owner-only `/revoke` mutes the thread until reapproval and leaves the parent unchanged. These restrictions survive restart.
Parent revocation stops its threads, cancels active turns, and discards queued prompts and held content.
Container removal revokes group/channel parent access and preserves each thread's own restrictions. Reapproving the parent restores inheritance.
DM-parent threads in the removed scope are muted individually. Threads outside that scope stay unchanged.
If persistence fails, revocation still stops runtime access. The host warns that a restart may restore access.
Mention-mode changes affect later and queued prompts. An active reply completes unless access is revoked.

Missing metadata retains explicit thread approval for legacy connectors. Invalid or unnegotiated metadata causes the host to drop the message.
After a connector upgrade reports parent context, an existing thread approval becomes a restriction and cannot bypass parent approval.
After the host saves a parent association, missing or conflicting metadata also causes a drop, including after restart.
Nonowners can create at most 256 automatic associations per parent, subject to a 4,096-association budget.
Standalone records consume no association slots. Owner messages and explicit owner-created records can still bind at capacity.
The host never evicts associations or restrictions to admit a new thread.
Thread IDs still identify conversations, outbound targets, and event correlation. Owner questions still target the canonical owner DM.

Rules:

- Answer `connect` with exactly one `connected` or `connect_error`,
  **within 30 seconds**. Budget that for the service dial and auth
  round-trip only: push anything long-polling (an initial sync, a
  gateway resume) into the session *after* `connected` goes out, or a
  warm reconnect will quietly eat the whole budget and read as a hang.
  Blowing it fails the bridge immediately on a first connect; during
  crash recovery it costs one of the three restarts allowed per 60
  seconds. `connect_error` means *permanently* broken (bad token);
  transient network trouble is yours to retry inside the session,
  surfaced via `warn`.
- Every `send`/`send_image`/`send_file` gets exactly one `result`
  echoing its `id` (empty `error` = success). terva times a send out
  after 30s. `typing` carries no id and gets no result; terva
  re-sends it every `typing_refresh_ms` while a turn runs. Declare
  `"typing_stop"` and one more `typing` with `"active":false` follows
  each reply, so withdraw the indicator then. That is for services whose
  indicator runs to a long server-side timeout (Matrix lingers up to
  30 s beside the delivered answer); one that clears itself within a
  few seconds needs neither the feature nor the frame. terva never
  sends the stop to a connector that did not declare it, because a
  connector that never learned the field reads it as one more start.
  `ask` and `ask_close` are commands with results too; only `answer`
  flows free.
- **Owner-directed frames may carry a chat id you never sent.** Until
  the owner's first inbound DM of a host run, terva addresses the
  frames aimed at the owner (the group-admission ask, tool approvals,
  the idle nudge) using the paired **user** id as the `chat_id`, so a
  freshly started bot can reach its owner cold. Where a user id is also
  a valid DM chat id (telegram) that costs you nothing; where it is not
  (a Matrix MXID is not a room id), resolve it to the owner's EXISTING
  DM chat, or fail the frame with a clear `result.error`. Don't create
  a room to satisfy one; the owner didn't ask for it. Failing is
  fail-closed, not fatal: an admission ask terva could not deliver does
  not count as asked, so it returns on the next `chat_membership added`
  for that chat once a real inbound DM has corrected the id. A tool
  approval that cannot be delivered is simply denied.
- **Attachments travel by path, both directions** (same-host
  assumption). Inbound: write the bytes under your `data_dir` and
  reference the path; terva takes ownership, so images are read and
  deleted and other kinds are moved into a per-message directory (still
  under `data_dir`) that the agent can read with its normal tools and
  that terva cleans after the turn. Paths outside `data_dir` are
  refused. Declare `"attachment_kinds"` and label each attachment
  (`kind`: image | audio | voice | video | document | sticker, plus
  `name`, `size`, `duration_ms`, `caption`, and captions join the message
  text host-side). Unlabeled attachments read as images, the v1
  assumption.
- On `shutdown` or stdin closing, exit promptly. The host gives the shutdown
  frame 200 ms to flush, then closes stdin without waiting for a blocked writer.
  It waits two seconds for exit, requests termination, then kills the process
  after one more second. Windows uses the final kill if termination is unsupported.
- Send deadlines cover writing the request and waiting for its result.
  Cancellation during a pipe write closes the stream and stops the child,
  because a partial frame cannot safely precede another request. The existing
  restart budget applies. The host does not resend the failed message.
- Crashes are loud, not silent: terva restarts a crashed connector with
  backoff and surfaces every attempt, but gives up after 3 crashes in
  60 seconds and reports the bridge as broken.
- **Reconnects recover, first connect discards.** On the first-ever
  connect after setup, discard history: never replay messages that
  predate that first connect into agent turns. On every later connect,
  deliver what arrived while you were down, bounded by whatever
  "recent" means on your platform. Silently eating a restart's worth of
  messages reads as a broken bridge, not as discipline. On the boundary
  a connector chooses between possibly delivering twice and possibly
  dropping once; terva does not yet dedupe re-deliveries, so the
  contract only names the trade-off. A duplicate is visible and
  recoverable; a silently swallowed message is neither.
- **Inbound messages queue, and the queue is bounded.** terva buffers a
  few hundred normalized messages between your stream and the agent
  loop; past that, overflow is dropped with a warning in terva's own
  output (not in your connector log, which only ever carries your
  process's stderr) and counted: the owner's in-chat `/status` reports
  `dropped inbound: N` for the process run, so a lossy afternoon shows
  up after the fact instead of only in a grep. Only `message` frames queue here: `result`,
  `answer`, `chat_membership`, and the edit/delete/reaction streams are
  dispatched directly and never contend for it. So pace a large
  recovery burst rather than emitting it in one batch, because the bound is
  generous for conversation and finite for a backlog.

Pairing state for external connectors persists host-side under
`$TERVA_HOME/connectors/<name>/pairing.json`; your process never sees
it.
