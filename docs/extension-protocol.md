# The terva extension frame reference

Every frame an extension and the host exchange, in both directions, with the
protocol version each one arrived in. The schema is pinned by golden tests in
`packages/agent/extproto`, and the corpus those tests read is published, so a
conformance suite can point at it rather than copy frames by hand.

For writing an extension, the manifest, discovery, configuration, the SDKs, and
managing what is installed, see [extensions.md](extensions.md). You do not need
this page to write one against the Go SDK; you need it to write one in another
language, or to see what actually went over the wire.

All frames are one JSON object per line. Top-level `type` is the
discriminator. Optional `id` correlates request frames with their
responses. The schema is pinned by golden tests in
`packages/agent/extproto`; breaking it means bumping
`extproto.ProtocolVersion`, never a rename sweep, because third-party
extensions are deployed independently of terva and cannot be recompiled
in lockstep (see [fork.md](fork.md)).

**The golden corpus is published** at
`packages/agent/extproto/testdata/golden.jsonl`: one
`{"name":…,"dir":…,"frame":…}` object per line, where `frame` is the
exact bytes terva emits, carried as a JSON string so it survives the
envelope intact. Point your conformance suite at that file rather than
copying frames by hand: a copy drifts silently, and a fetch cannot. It
covers every frame type in the protocol, and a test refuses to let it
fall behind: a frame added to `extproto.go` without a corpus entry
fails on the commit that adds it.

It pins the **wire spec**, which is not the same as what a host actually
sends: [`panel_resize`](#panel_resize) has a corpus entry and no
emitter. A corpus entry tells you the exact bytes to expect *if* the
frame arrives, never that it will.

`dir` is `ext_to_host`, `host_to_ext`, or `both`. An extension only ever
*encodes* `ext_to_host` frames, so the useful split is to assert those
byte-exact against the corpus and the rest on decode.

Two things to know before byte-comparing:

- terva encodes with Go's `encoding/json`, which escapes `<`, `>` and
  `&` where most encoders emit them literally. Both are valid JSON and
  every reader here accepts either, so this never matters on the live
  wire, where real frames carry those characters constantly, but the corpus
  is deliberately kept free of them so byte-exact comparison stays
  meaningful across languages.
- Map-valued fields (`config` on `hello_ack` and on a `config_update`
  event) are emitted with their **keys sorted**, because Go marshals maps
  that way. A language whose map preserves insertion order has to sort
  to reproduce these bytes.

The corpus also pins the shapes that are easy to guess wrong. Several
array fields carry no `omitempty`, and Go marshals a nil slice as
`null`, not `[]`: `secret_keys` for an extension holding no secrets is
`{"keys":null}`, and a not-found `session_data` is `{"messages":null}`.
Model those as nullable.

## Frame size limits

There is a per-frame maximum of **4 MiB** (`extproto.MaxFrameBytes`) in
both directions. Oversized frames are handled gracefully, never fatally:

- A frame larger than the cap on the read side (either direction) is
  **skipped and logged**, and reading continues. One oversized frame
  never takes the extension or the host's reader down.
- The host caps the args it puts in a single `tool_call` frame at
  **1 MiB** (`extproto.MaxToolCallBytes`, comfortably below the read
  cap). If the model produces a larger tool argument, the call comes
  back to the model as a normal `is_error` tool result ("arguments are
  N bytes; the limit is …") instead of being sent, so an oversized
  argument can't kill an extension. Keep individual tool results and
  context contributions well under these limits.

## Extension → host

### `hello` (required, first frame)

```json
{"type":"hello","name":"weather","version":"1.0.0",
 "capabilities":["commands","tools","panels"]}
```

The optional `"min_protocol": 3` field is the lowest host `protocol_version`
this extension can run against, the wire behind `RequireProtocol(n)`. Zero
(the default, and what every pre-negotiation extension sends) means "no
minimum", so old extensions and old hosts interoperate unchanged. When set, a
host below it refuses to load the extension with a clear message instead of
letting it misbehave against a wire it doesn't fully speak. Declare it only
when your extension genuinely can't function without that protocol level;
otherwise feature-detect on `Host().ProtocolVersion` and degrade.

### `bootstrap` (optional, before `hello`)

```json
{"type":"bootstrap","message":"compiling extension"}
```

Sent by a **launcher**, zero or more times, before `hello`, to say "still
working". Each frame restarts the hello deadline, so the host measures
*silence* rather than total elapsed time, and shows your message so a long
build reads as progress instead of a hang. An absolute ceiling (10 minutes)
still applies.

This is not an SDK feature and cannot be: the SDK isn't running yet, which is
exactly the difficulty. A launcher that builds before it can `exec` the real
extension emits it with one `printf`:

```sh
#!/bin/sh
if [ ! -x ./weather ] || [ weather.go -nt ./weather ]; then
  printf '%s\n' '{"type":"bootstrap","message":"building weather"}'
  go build -o weather . || exit 1
fi
exec ./weather
```

Emit one before the build starts, and another every so often if the build is
long enough that the host could time out between reports. Purely additive: a
host that doesn't know the frame sees a malformed `hello` and skips the
extension, which is what it did with a slow build anyway.

### `register_command`

```json
{"type":"register_command","name":"weather",
 "description":"current weather for a city"}
```

### `register_tool`

Registers a tool the LLM can call. `schema` is a JSON Schema object
describing the tool's args (the same shape Anthropic and OpenAI accept).

```json
{"type":"register_tool","name":"weather",
 "description":"Get the current weather for a city.",
 "schema":{
   "type":"object",
   "properties":{"city":{"type":"string"}},
   "required":["city"]
 },
 "authority":"network-read"}
```

The optional `"read_only": true` field declares the tool side-effect
free (the MCP `readOnlyHint` analog). Annotated tools are admitted in
the `plan` approval mode and auto-allowed in `auto-edit` (see
[permissions.md](permissions.md)); unannotated tools are treated as
mutating. Lying here only cheats your own user's policy. Old hosts
ignore the field; old extensions never send it. The field is fully additive.

The optional `"authority"` field is its **richer successor**: the tool's effect
class, one of `local-read`, `local-data`, `workspace-mutation`,
`process-execution`, `network-read`, `external-mutation` (see the
[authority classification](standard-tools.md#authority-classification)). It says
what `read_only` cannot: a `network-read` tool reads nothing locally yet must
not be auto-allowed as read-only. Also additive/optional: empty means the host
falls back to the `read_only` bool, and an unknown value is treated as
side-effecting (the safe default). From the Go SDK: `ext.WithAuthority(...)`,
the counterpart to `ext.ReadOnly()`.

The optional `"essential": true` field marks a **load-bearing** tool that must
stay advertised to the model every turn, even when the host runs with [lazy tool
visibility](standard-tools.md#lazy-tool-visibility-lazy_tools) and would otherwise
defer your tools behind an `activate_tools` call. Use it for the tool your static guidance
([`register_context`](#register_context-protocol-2)) tells the model to reach for
before others. "Search the index before reading a file" only works if the search
tool is in the tool list when the model first wants to read, not sitting deferred
while the prompt that names it is already in context. Only the tools you mark stay
eager; the rest of your tools still lazy-load, so a big extension keeps the context
savings for the tools the model rarely needs. The host **caps** how many tools one
extension may mark essential (currently 3); excess ones load deferred and the
drop is logged, so an extension can't quietly pin its whole surface
always-visible and defeat lazy mode. Additive/optional: an old host ignores it (the tool lazy-loads as
before) and a host with lazy mode off advertises everything anyway, so it is a
no-op there. `essential` is visibility only: the tool is still permission-gated
exactly as before when actually called. From the Go SDK: `ext.Essential()`.

The optional `"display"` object tells a rich client how to draw the **card** for
your tool's calls. Unlike the three fields above it changes nothing about
permissions, visibility, or what the model sees: it is presentation, and only
presentation.

```json
{"type":"register_tool","name":"weather",
 "schema":{"type":"object"},
 "display":{
   "subject":"{city}",
   "body":"table",
   "redact":["api_key"]
 }}
```

`subject` is a template over your tool's **top-level argument keys**, written as
`{key}`. A client that does not have one guesses a subject from a priority list
of key names (`command`, `path`, `query`, `title`, …) and falls back to counting
("1 argument") when none of them matches, which is what an extension tool with
domain-specific arguments usually gets. The template says which argument names
the call, and in what order. A key your arguments do not carry fills as empty,
and the result is substituted as **text**, never as markup.

`body` names one of the renderers the client already has: `text`, `json`, `diff`,
or `table`. It is a name, not a format string. A name the host does not know is
dropped at registration (and logged to your extension's log); a name the host
knows but the client does not falls back to `text`. `table` means
**tab-separated** output, and a result with no tab in it renders as plain lines
rather than as one lopsided column.

`redact` names argument keys whose values the card masks even when the reader
expands the arguments. It **adds to** the client's own key-name denylist
(`token`, `secret`, `password`, `api_key`, `authorization`, …) and cannot shrink
it, so it is how you protect a credential your tool called something that list
would never guess. It cannot unprotect one.

**The hint is data, and that is the point.** A client that evaluated rendering
code an extension supplied would be running third-party code in the user's
browser against their transcript. A template plus a closed set of renderer names
gives you the useful part with none of that, which is also why the set is closed
and why an unknown value degrades instead of failing.

The host bounds what it will carry: a subject over 200 bytes is dropped whole
(truncating one mid-`{key}` would render a stray brace), and at most 16 redact
keys are kept. Every drop is written to your extension's log, so a hint that
came back looking generic tells you why. A malformed hint never costs you the
registration: the tool still registers and the model can still call it.
Additive/optional: an old host ignores the field, an old client never asks for
it, and both keep working. From the Go SDK:
`ext.WithDisplay(ext.ToolDisplay{Subject: "{city}"})`.

### `set_withdrawn_tools` (protocol 4)

Hides (and later restores) tools **this extension registered** from the
model for the session, the tool analog of `refresh_context`. It is a
wholesale snapshot of the names to hide, so the same frame doubles as the
restore path and re-sending an unchanged set is a free host-side no-op.

```json
{"type":"set_withdrawn_tools","all":true}              // hide all my tools
{"type":"set_withdrawn_tools","tools":["gitlog"]}      // hide just these
{"type":"set_withdrawn_tools","tools":[],"all":false}  // restore everything
```

`all:true` hides every tool the extension registered (`tools` ignored);
otherwise the listed tools are hidden and any name that isn't one of this
extension's own is ignored: you can't hide a built-in or another
extension's tool. A withdrawn tool leaves both the callable registry and the
system-prompt tool list but stays registered, so restoring needs no
re-registration (and the `/extensions` dialog still shows it in the count).
Send this only at a session boundary (see
[Responsible use](extensions.md#responsible-use-context--tools)): the host pins
the tool set per turn, so a mid-turn change applies on the next turn. Additive:
a protocol-3 host ignores the frame and the tools stay visible, so
feature-detect with `Host().ProtocolVersion >= 4` rather than declaring
`RequireProtocol(4)`.

### `register_context` (protocol 2)

Declares the extension's **static** context block: guidance the host wraps,
bounds, attributes, and folds into the cached system-prompt addendum. Sent
during the register phase, like `register_command` / `register_tool`. See
[Context contributions](extensions.md#context-contributions).

```json
{"type":"register_context","text":"House style: prefer table-driven tests."}
```

### `refresh_context` (protocol 3)

`register_context` you can send **mid-session**: the host swaps the block,
rebuilds the cached system prompt, and the change lands on the next turn. The
block stays a *snapshot*: it changes only when you send this frame, never per
turn, so re-snapshot at a boundary (`session_start`, `transcript_compacted`)
rather than churning the prompt cache. Empty `text` clears the block. Same host
wrapping, attribution, and byte bound as `register_context`. Declare
`RequireProtocol(3)`.

```json
{"type":"refresh_context","text":"Project notes for acme-api: …"}
```

### `context_card` / `context_card_clear` (protocol 2)

A **dynamic** block the host injects at the cache-free tail each turn and never
persists, the channel for live state (a task list, a build status) that would
otherwise invalidate the cached prefix. Set or replace by `id`; `label` is a
short header for the host's wrapper, `priority` orders multiple cards (lower
first), and `blocking` marks open work, which makes the host append a soft
"review before closing" nudge to the card's injection.

```json
{"type":"context_card","id":"todos","label":"Open work",
 "text":"□ ship panel api\n✓ persist state","priority":10,"blocking":true}
{"type":"context_card_clear","id":"todos"}
```

### `status_segment` (protocol 2)

Sets (or replaces, by `id`) a short segment in the host's status line.
Host-rendered and **not** model-facing: the adjacent channel to the context ones,
for the things a user should see but the model shouldn't pay for. An empty
`text` clears the segment.

```json
{"type":"status_segment","id":"weather","text":"Berlin 16°C"}
```

### `host_tool_call` (protocol 3)

The reverse of `tool_call`: an extension asks the host to run one of the
**host's own** tools (read, grep, bash, an MCP tool…) and sends back a
`host_tool_result` correlated by the extension's `id`. It exists so an
extension can orchestrate host tools without a model round-trip, as in a
code-execution extension whose sandboxed script calls `read`/`grep`/`bash`
as functions, collapsing a multi-step pipeline into one turn.

```json
{"type":"host_tool_call","id":"c1","name":"read","args":{"path":"README.md"},"silent":true}
// → {"type":"host_tool_result","id":"c1","content":[{"type":"text","text":"…"}]}
```

The host runs the tool under the **same permission gate** a model call
uses (an extension gains reach, never authority) and refuses
extension-owned tools, so a `host_tool_call` cannot recurse back into an
extension (only built-in and MCP tools are reachable). `silent` is a hint
not to surface the call in the UI. Declare `RequireProtocol(3)`; a host
that doesn't support it answers with an error result.

### `list_sessions` / `read_session` (protocol 3)

Read-only, project-scoped access to past session transcripts, so an
extension can index prior conversations. `list_sessions` returns the
active project's sessions; `read_session` returns one transcript
flattened to role+text.

> **Cross-session search now ships in core as the `session_search` tool,
> and a search extension built on this bridge is superseded by it.**
>
> The flattening is why. Role+text drops tool calls, their arguments, and
> their results. On a measured coding session that is **2% of the
> searchable bytes**, and the 98% dropped is where file paths, commands,
> and command output live. Searching one real project for a filename
> found **24 matches at full fidelity against 1** through this bridge,
> and that one was incidental. The bridge also cannot see swarm
> sub-agents at all: their transcripts live under the swarm state root,
> which `list_sessions` never enumerates.
>
> The bridge is unchanged and still supported. It remains the right
> surface for an extension that wants conversation *text* (topic
> clustering, summarisation, export). It is the wrong surface for
> recall, which is why that moved in-tree.
>
> The `session-search` extension is retired accordingly: it is in
> `supersededExtensions`, so an installed copy is **skipped at load**
> with a pointer to `terva ext remove session-search`, the same way
> `git-worktree` and `memory` were retired. Nothing is lost by not
> loading it: its state was an FTS index *derived* from the
> transcripts, and core keeps no index, so there is no adoption step;
> the transcripts were always the source of truth.
>
> Superseding keys on the EXTENSION name, not the tool name. A
> third-party extension under a different name may still register a
> tool called `session_search`; built-ins win that collision, so core's
> stays live.

```json
{"type":"list_sessions","id":"l1"}
// → {"type":"session_list","id":"l1","sessions":[{"session_id":"…","title":"…","messages":12,"mtime":…}]}
{"type":"read_session","id":"r1","session_id":"…"}
// → {"type":"session_data","id":"r1","messages":[{"role":"user","text":"…"},…]}
```

Cross-project reads are not granted here (a non-matching `project_id`
returns nothing), and a `session_id` that tries to escape the project's
session directory is refused. Declare `RequireProtocol(3)`; an
unsupported host returns an empty list / `not_found`.

From the Go SDK, pass `ext.ReadOnly()` as a trailing option to declare
it. `ext.WithAuthority(...)`, `ext.Essential()` and `ext.WithDisplay(...)` are
trailing options too, so a load-bearing read-only tool combines them:

```go
e.Tool("branch_list", "List branches.", schema, handler, ext.ReadOnly())

// A load-bearing search tool: stays advertised under lazy tool visibility so
// the guidance that tells the model to use it first isn't pointing at a
// deferred tool. See "Keep your model-facing footprint small" in extensions.md.
e.Tool("index_search", "Search the workspace index.", schema, handler,
    ext.WithAuthority(ext.AuthorityLocalRead), ext.Essential())

// How the card for this tool's calls is drawn. Presentation only: it changes
// nothing about permissions, visibility, or what the model sees.
e.Tool("weather", "Current weather for a city.", schema, handler,
    ext.WithDisplay(ext.ToolDisplay{Subject: "{city}", Body: ext.BodyTable}))
```

Tool names live in the same namespace as built-in tools (`read`,
`write`, `edit`, `bash`, `skill`, the `worktree_*` five). Conflicts are
silently shadowed by the built-in.

### `ready`

Sentinel telling terva "all initial registrations are flushed". Send it
right after your last `register_*` frame so the host can build the
agent's tool registry without racing the registration window.

```json
{"type":"ready"}
```

### `tool_result`

Reply to a `tool_call` from the host. `content[]` is a list of
message blocks; each block is `{"type":"text","text":"..."}` or
`{"type":"image","mime_type":"image/png","data":"<base64>"}`. Set
`is_error: true` to mark the call as failed.

```json
{"type":"tool_result","id":"...",
 "content":[{"type":"text","text":"Berlin: 16°C, fog"}]}
```

### `subscribe`

Declares which lifecycle events the extension wants to observe and
which it wants to intercept. Send once after `hello`, before `ready`.

```json
{"type":"subscribe",
 "events":["session_start","session_end","turn_start","tool_call","turn_end","user_message","assistant_message"],
 "intercept":["tool_call","turn_start","user_message","assistant_message"]}
```

Recognised event names: `session_start`, `session_end`, `turn_start`,
`turn_end`, `run_end`, `tool_call`, `tool_result`, `user_message`,
`assistant_message`, `workspace_changed`, `compact_start`,
`transcript_compacted`, `config_update`. (The host advertises the exact set it
emits in `hello_ack.supported_events`; subscribing to a name an older host
doesn't emit is harmless; it simply never fires.)

`run_end` fires once when the agent finishes a whole prompt, with every step,
tool loop, and the at-close gate done. It's the per-prompt bookend to
`user_message`, distinct from the per-*step* `turn_end` (which fires
repeatedly inside a tool loop). Use it to act when the agent goes idle:
summarize the exchange, run a post-turn check, or flush state. The Go SDK
exposes it as `OnRunEnd`.

`compact_start` fires when the host is *about to* compact the transcript:
the pre-event paired with `transcript_compacted` (post). The `text` field
carries a short human-readable reason. Because compaction runs a slow LLM
summarization, a handler has time to read the full session (`read_session`)
and harvest detail before it's summarized away, the window the post-event
misses. The Go SDK exposes it as `OnCompactStart`.

That read works from inside the handler. In the Go SDK, event handlers run on
their own serial lane rather than on the frame-reading loop, so a handler may
issue `read_session` (or any other ext→host request) and wait for the reply.
Until that lane existed the recommendation above deadlocked: the reply could
only be delivered by the loop the handler was blocking, so the extension went
wire-dead for 30 seconds and harvested nothing. If you write an SDK in another
language, dispatch event handlers off your read loop for the same reason.

`user_message` fires for every genuine user prompt, meaning the initial
submit and any queued follow-ups. It is the symmetric counterpart to
`assistant_message`. Use it to harvest intent for a memory store or feed
a session index. The host's synthetic at-close gate nudge is **not**
delivered (it's a host re-prompt, not the user's words). The Go SDK
exposes it as `OnUserMessage`.

`workspace_changed` fires once at the end of each agent run with the net
set of files the turn touched, in a `files` array of
`{"path":"...","change":"added|modified|deleted"}` (workspace-relative,
slash-separated paths, sorted). A run that changed nothing fires no event.
The host derives it by diffing the workspace at run boundaries (honoring
`.gitignore` and pruning `.git`), so it catches `bash` side effects and
external edits, not just the agent's own write/edit tools. Scoped to the
workspace root only; oversized trees disable it (it reports nothing rather
than walk an unbounded tree each turn). Use it to keep a code index fresh
or note edits in a memory store. Additive/opt-in; the Go SDK exposes it as
`OnWorkspaceChanged`, and the change list also rides the generic `Event`
as `Files`.

`transcript_compacted` fires after the host compacts the conversation
(auto, near the context limit, or via `/compact`), before the next model
turn. It's the moment to re-snapshot a frozen context block: compaction
summarizes away the tool-results that recorded mid-session writes, so a
memory extension re-injects its notes here via `refresh_context`, the
same thing it does on `session_start`. It's a fire-and-forget signal,
purely additive and opt-in: subscribe to receive it, and a host too old
to emit it simply never fires it (your extension keeps its
session-boundary refresh). The Go SDK exposes it as `OnCompaction`.

`session_start` (protocol 2+) carries the active session's identity
(`session_id`, `session_path`, `session_title`) plus `cwd` and
`project_id`. Unlike the `cwd` in the hello handshake (frozen at launch),
these refresh on **every** `session_start`, including after a `/cd`, so
an extension follows the working directory instead of going stale.
`project_id` is the host's stable, collision-proof key for the cwd (a
readable, flattened path plus a short hash); use it to scope per-project
state without reinventing the keying. The SDK refreshes `Host().CWD` /
`Host().ProjectID` from these before any handler runs, and an `OnSession`
handler receives them on the `Session`. A no-session start (session
closed / `--no-session`) leaves `cwd`/`project_id` empty and the SDK
keeps the last known value (closing a session doesn't move the cwd).

`session_end` is the bookend to `session_start`, carrying the same
identity fields for the session that is ending. It fires for the
*outgoing* session just before a switch or close announces the next one,
and once more for the active session at host shutdown (the session_end is
queued ahead of the shutdown frame on the same FIFO outbox, so a healthy
extension sees it before exiting). Use it to flush a memory store or index
the just-finished session. It is **best-effort**: a hard kill (SIGKILL)
skips it, so persist incrementally and treat it as a flush point, not a
durability guarantee. Additive/opt-in; the Go SDK exposes it as
`OnSessionEnd`.

`config_update` fires when the user changes **this** extension's config (the
`/extensions` config dialog). The new resolved values ride the event's `config`
field, the same shape `hello_ack` carries, so an extension re-reads its
settings live instead of asking the user to restart terva. Fire-and-forget and
gracefully degrading (an older host never emits it, and the initial values still
arrive in `hello_ack` regardless). The Go SDK exposes it as `OnConfig`.

Interceptable events:

- `tool_call`: block the call (model sees `reason` as the tool
  error) or rewrite args via `modified_args`.
- `turn_start`: block the turn before the model is called. Useful
  for rate-limiting and business-hour gates. `reason` is shown to
  the user as a status line. No rewrite supported.
- `user_message`: block a prompt via `block` (it's neither recorded
  nor sent; `reason` is shown to the user), or rewrite the prompt the
  model sees via `replace_text` (the rewrite IS what lands in the
  transcript). Runs on the initial prompt and on queued follow-ups,
  so a guard can't be bypassed by typing while the agent is busy.
  Useful for input guardrails, secret redaction, and prompt
  augmentation. The Go SDK exposes it as `InterceptUserMessage`.
- `assistant_message`: suppress the message via `block`, or rewrite
  the user-visible text via `replace_text`. The model's original
  text stays in the transcript so the model sees what it actually
  said on subsequent turns.

### `event_intercept_response`

Reply to an `event_intercept` from the host. All fields default to
"allow, pass through unmodified".

| field | meaning |
|---|---|
| `block` | `true` refuses the action. For `tool_call`, `reason` is shown to the model; for `turn_start` / `user_message` / `assistant_message`, `reason` is shown to the user. |
| `reason` | refusal text (on block) or pass-through note. |
| `modified_args` | for `tool_call`: rewritten JSON args the tool will actually see. Must be a valid JSON object. Ignored when `block` is true. |
| `replace_text` | for `user_message`: replaces the prompt the model receives (the rewrite also lands in the transcript). For `assistant_message`: replaces the user-visible text while the model's original output stays in the transcript. Ignored when `block` is true. |

Missing the response within 5s is treated as "allow" (i.e. an
unresponsive extension never stalls the agent). When multiple
extensions subscribe to the same event, they're consulted serially;
the first `block` wins and rewrites (args / text) chain: each
subsequent interceptor sees the previous one's output.

```json
{"type":"event_intercept_response","id":"...",
 "block":true,"reason":"refused: matches danger pattern \"rm -rf\""}

{"type":"event_intercept_response","id":"...",
 "modified_args":{"command":"echo GUARDED: ls"}}

{"type":"event_intercept_response","id":"...",
 "replace_text":"[redacted]"}
```

### `command_response` (reply to `command_invoked`)

```json
{"type":"command_response","id":"...","action":"prompt",
 "prompt":"Show today's weather for Berlin in one line."}
```

`action` is one of:

- `"prompt"`: submits `prompt` as a fresh user message; the agent
  runs a turn against it.
- `"insert"`: inserts `insert` into the editor at the cursor without
  submitting.
- `"display"`: appends `display` to the chat as a one-shot styled
  note. No model call, nothing written to the transcript.
- `"open_panel"`: opens an extension-owned interactive panel inside
  terva. The panel content lives in `open_panel`.
- `"noop"`: the extension handled it itself (e.g. it pushed
  `notify` frames or kicked off background work). terva doesn't change
  the UI in response.

Example:

```json
{"type":"command_response","id":"...","action":"open_panel",
 "open_panel":{
   "id":"todos-main",
   "title":"Todos",
   "lines":["□ ship panel api","✓ persist state"],
   "footer":"↑/↓ navigate - a add - x complete - esc close"
 }}
```

If `error` is non-empty, terva renders it as a red status line
regardless of `action`.

### `open_panel` (one-way, any time)

Opens an interactive panel **without** a command invocation to hang it off:
the spontaneous twin of the `open_panel` action inside `command_response`. Send
it from a tool handler, an event handler, or any background goroutine. The
payload is the same panel spec.

```json
{"type":"open_panel","panel":{
   "id":"todos-main",
   "title":"Todos",
   "lines":["□ ship panel api","✓ persist state"],
   "footer":"↑/↓ navigate - a add - x complete - esc close"}}
```

### `panel_render` (one-way, while a panel is open)

Pushes a fresh frame for an already-open panel.

```json
{"type":"panel_render","panel_id":"todos-main",
 "title":"Todos",
 "lines":["□ ship panel api","✓ persist state"],
 "footer":"↑/↓ navigate - a add - x complete - esc close"}
```

### `panel_close`

Closes a previously-open panel.

```json
{"type":"panel_close","panel_id":"todos-main"}
```

### `notify` (one-way, any time)

```json
{"type":"notify","level":"info",
 "message":"refreshed cache (12 entries)"}
```

`level` is one of `info`, `success`, `warn`, `error`. The note shows
up below the transcript with the extension's name in brackets. Notes
are one-shot: they clear automatically when the user sends their next
prompt (and on `esc` / `/clear`).

### `clear_notes` (one-way, any time)

Removes every note this extension previously pushed via `notify` /
`display`. Use it for transient status lines (e.g. an approval prompt)
so they do not stack up; notes from other extensions are untouched.

```json
{"type":"clear_notes"}
```

Under `terva rpc`, this surfaces to the host as an `ext_clear_notes`
event (alongside `ext_notify` / `ext_display`).

### `submit` (one-way, any time)

Queues a plain model prompt in the interactive host, as if the user had typed
and sent it. The host routes it through its submit-or-queue path, so it is safe
to send while a turn is running; it lands at the next boundary rather than
racing the active turn. Empty/whitespace `text` is ignored.

```json
{"type":"submit","text":"summarize what changed in the last turn"}
```

Interactive-mode only (the RPC loop has no editor and takes its prompts from
the client, so it ignores this). The Go SDK has no helper for it yet, so send the
frame directly, or use a `command_response` with `action:"prompt"` when the
prompt is the answer to a slash command.

### `submit_slash` (one-way, any time)

Submits a slash command to the host's TUI as if the user had typed it.
Typically emitted from a `panel_key` handler, as with Enter on a selected
row to switch the host with `/cd <path>`. `text` must start with `/`.

```json
{"type":"submit_slash","text":"/cd /repo/.worktrees/feature-x"}
```

Interactive-mode only: the host ignores it in `-p` / `--json` / `rpc`
(no TUI to submit into). Reserved for opt-in extensions that the user
has installed and trusts: it lets an extension drive any host command,
so it is not something a casual extension should reach for. From the Go
SDK this is `e.SubmitSlash("/cd " + path)`.

### `register_connector` (protocol 5, experimental)

Declares the [connector role](extensions.md#connector-role-experimental) during
the register phase. No payload: capabilities travel in the inner hello at
session-open time. Refused (logged, ignored) unless the manifest
declares `"connector": true`.

```json
{"type":"register_connector"}
```

### `chat` / `chat_down` (protocol 5, experimental)

The extension side of the connector tunnel. `chat` carries one frame of
the CONNECTOR protocol verbatim (see the frame reference in
[connector-protocol.md](connector-protocol.md); this wire never mirrors that
vocabulary); `chat_down` ends the session from the extension side, with
`error` set when the connector engine died (the process and its tools
live on) or without for an orderly teardown. `id` on both is the
session id from the host's `chat_open`.

```json
{"type":"chat","id":"s1","frame":{"type":"message","chat_id":"c1","user_id":"u1","text":"hi"}}
{"type":"chat_down","id":"s1","error":"auth revoked"}
```

### `shutdown_ack`

Sent in response to `shutdown`. Extension should exit promptly after.

## Host → extension

### `hello_ack`

```json
{"type":"hello_ack","protocol_version":2,
 "zot_version":"0.0.7","terva_version":"0.0.7","provider":"anthropic",
 "model":"claude-opus-4-7","cwd":"/Users/pat/Developer/terva",
 "extension_dir":"/Users/pat/Developer/terva/.terva/extensions/todos",
 "data_dir":"/Users/pat/.terva/ext-data/todos",
 "supported_events":["session_start","turn_start","turn_end","tool_call",
   "tool_result","assistant_message","transcript_compacted"]}
```

Sent immediately after `hello`. The extension can use these fields to
decide which commands to register (e.g. only register a Python tool
on macOS, only register a model-specific shortcut for opus, etc.).

`zot_version` and `terva_version` carry the same host version string, and the <!-- rename:keep -->
old key is **always** on the wire, a frozen wire field kept for compatibility
with extensions written against the pre-fork protocol, and pinned by golden
tests (see [fork.md](fork.md)). Read `terva_version` and fall back to
`zot_version`. <!-- rename:keep -->

`supported_events` lists the lifecycle events this host can emit, a
finer-grained capability signal than `protocol_version`. Use it to adapt
or warn (the Go SDK exposes `Host().Emits("transcript_compacted")`); it's
**absent on an older host** that doesn't advertise, which you should read
as "unknown" and handle by subscribing optimistically and degrading if
the event never fires, rather than gating on it.

`extension_dir` is the **read-only install dir**: the extension's code
and any defaults/assets it ships. `data_dir` is the **writable state
dir**, `$TERVA_HOME/ext-data/<name>`, kept separate so a read-only or
system install still works and code never mixes with data. Persist your
state (e.g. `todos.json`, caches, scoped auth tokens) under `data_dir`.

> **Note:** `data_dir` used to alias the install dir. It now points at
> the separate `ext-data` location. The Go SDK's `Host().DataFS()` layers
> `data_dir` over `extension_dir` (read-through, copy-on-write), so a file
> written under the old location is still read until it's next written, so
> the migration needs no flag day. Use `DataFS` for both "ship a default, let the
> user override" and reading legacy state. For per-project state, use
> `Host().ProjectDataDir()` (`data_dir/projects/<project_id>`, scoped by
> the `project_id` on `session_start`).

### `command_invoked`

```json
{"type":"command_invoked","id":"...",
 "name":"weather","args":"berlin"}
```

`args` is everything the user typed after the command name, trimmed.

### `tool_call`

Sent when the LLM invokes a tool the extension registered. `args` is
the parsed JSON object the model produced; the extension is
responsible for validating/coercing it.

```json
{"type":"tool_call","id":"...","name":"weather",
 "args":{"city":"Berlin"}}
```

Reply with `tool_result` within the host's tool timeout (default 60s).
Missing the timeout surfaces an error to the model and the call is
marked as failed.

### `event`

Lifecycle notification for events the extension subscribed to via
`subscribe`. One-way, with no response expected.

```json
{"type":"event","event":"turn_start","step":1}
{"type":"event","event":"tool_call",
 "tool_id":"...","tool_name":"read","tool_args":{"path":"foo.go"}}
{"type":"event","event":"turn_end","stop":"end_turn"}
```

### `event_intercept`

Sent when terva wants to give the extension a chance to block, modify,
or annotate a lifecycle event before it happens. Reply with
`event_intercept_response` within 5s; missing the deadline is
treated as "allow".

Payload fields depend on the event:

```json
// tool_call: includes the tool id, name, and parsed args
{"type":"event_intercept","id":"...","event":"tool_call",
 "tool_id":"...","tool_name":"bash",
 "tool_args":{"command":"rm -rf /tmp/foo"}}

// turn_start: includes the step number
{"type":"event_intercept","id":"...","event":"turn_start",
 "step":3}

// assistant_message: includes the assembled text
{"type":"event_intercept","id":"...","event":"assistant_message",
 "text":"here is your api key: sk-ant-..."}
```

### `panel_key`

Sent while an extension-owned panel is focused. `key` is a normalized
name (`up`, `down`, `left`, `right`, `enter`, `esc`, `tab`, `pageup`,
`pagedown`, `home`, `end`, `backspace`, `delete`, `rune`). For
`key:"rune"`, `text` carries the typed character.

```json
{"type":"panel_key","panel_id":"todos-main","key":"down"}
{"type":"panel_key","panel_id":"todos-main","key":"rune","text":"x"}
```

### `panel_resize`

Tells the extension the panel's drawing area changed, so a renderer can re-wrap
its `lines` to the new `width` / `height` (in cells).

```json
{"type":"panel_resize","panel_id":"todos-main","width":80,"height":24}
```

Honest caveat: the frame is part of the wire spec, but **no host currently emits
it**, because panels are re-rendered on the extension's own `panel_render`
cadence.
Handle it defensively if you like; don't wait for it.

### `panel_close`

Sent when the user closes the focused panel from terva (for example with
Esc or Ctrl+C). The extension should treat this as the panel lifetime
ending and stop sending `panel_render` updates for that `panel_id`.

```json
{"type":"panel_close","panel_id":"todos-main"}
```

### `chat_open` / `chat` / `chat_close` (protocol 5, experimental)

The host side of the connector tunnel, sent only after this extension
registered the [connector role](extensions.md#connector-role-experimental) AND
a chat consumer selected it by name. `chat_open` starts a session (the
extension answers by starting its connector engine, whose first output
is the inner connproto `hello` wrapped in a `chat` frame); `chat`
carries one connector-protocol frame verbatim; `chat_close` ends the
session host-side, so tear the engine down and confirm with `chat_down`;
the process keeps serving tools. Frames whose `id` isn't the live
session's are stale stragglers: drop them.

```json
{"type":"chat_open","id":"s1"}
{"type":"chat","id":"s1","frame":{"type":"connect"}}
{"type":"chat_close","id":"s1"}
```

### `shutdown`

Sent during graceful terva exit (or `/reload-ext` once that lands).
Reply with `shutdown_ack` and then exit.
