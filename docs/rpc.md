# terva RPC

`terva rpc` runs the agent runtime as a subprocess that speaks newline-delimited JSON on stdin and stdout. Use it from any language that can spawn a process and read/write its pipes — Go, TypeScript, Python, Rust, shell, anything.

For a Go program embedding the runtime in-process, use the `packages/agent/sdk` SDK instead. The wire format below IS the SDK's type set: the **event stream** on every surface (`terva --json`, this RPC stream, the SDK's `Event`, swarm event logs) is generated from one serializer (`core.WireEvent`), so consumers can share parsing code. The RPC layer adds a few frames of its own on top of that stream — the `response` command acks, `hello`, the `get_*` result payloads, and `compact_done` — which are RPC-specific, not `core.WireEvent`.

## Quick start

```bash
# spawn terva rpc; talk to it from a shell
( echo '{"id":"1","type":"prompt","message":"hello"}'; sleep 5 ) \
  | terva rpc --provider anthropic
```

You'll see one JSON object per line on stdout: a response acknowledging the prompt, a stream of events (`text_delta`, `tool_call`, `tool_result`, `usage`), then `done`.

## Process model

- One `terva rpc` process serves **one cwd, one model, one session**.
- For multiple projects, spawn multiple processes.
- Concurrency: at most one prompt or compact in flight at a time. A second one queues until the first finishes; aborting fires immediately.
- The process exits when stdin closes.

## Flags

`terva rpc` accepts the same flags as the other modes: `--provider`, `--model`, `--cwd`, `--api-key`, `--base-url`, `--system-prompt`, `--append-system-prompt`, `--reasoning`, `--max-steps`, `--no-tools`, `--tools`, `--session <path>` (opt into a durable, resumable session — see below).

[Extensions](extensions.md) and [MCP servers](mcp.md) load on the same lifecycle as every other mode, with the same flags — `--ext DIR` (repeatable), `--extensions a,b` (allowlist), `--no-ext`; `--mcp git,jira` (restrict-only), `--no-mcp`. Their tools join the registry like any other, and an extension's notes surface on the stream as the `ext_notify` / `ext_display` / `ext_clear_notes` events (the RPC loop has no editor, so an extension's `submit`/`insert` are no-ops).

Tool calls run unconfirmed by default (headless is yolo). `--no-yolo`, or `--approval MODE`, installs the [permission](permissions.md) gate — but RPC has no interactive prompt, so a call that would need confirmation is **refused** with a model-readable reason rather than asked (`plan` still runs read-only tools; explicit `allow`/`deny` rules apply as written). A gate that will refuse says so in a one-line note on stderr at startup.

To **confirm** instead of refuse, opt into an approval carrier that fills the gate out-of-band:

- `--rpc-approvals` — answer prompts over this JSON-RPC wire. A tool needing confirmation arrives as a request the driver replies to over the same connection; a driver that never answers keeps the safe refuse-by-default rather than hanging.
- `--approval-socket <path>` — route the gate through a local MCP approval bridge at a Unix socket (terva's own MCP client). The transport-opaque sibling of `--rpc-approvals`.
- `--approval-http <addr>` — route the gate through a Streamable-HTTP MCP permission endpoint (a remote orchestrator). The networked sibling of `--approval-socket`.

A backend sets **one** carrier, never several; each fails closed, leaving the refuse-by-default in place if it can't start.

Session persistence is **opt-in** in RPC mode. By default the process holds an in-memory transcript for the life of the connection (`get_messages` reads it) and never touches disk — the embedding application owns any persistence. Pass `--session <path>` to make the session durable and **resumable**: the prior transcript at that path is restored on start (or the file is created fresh on first run), and every message, usage row, and post-compaction transcript is written as it happens. A process pointed at the same `--session` path continues the conversation instead of starting blank — which is how a supervised worker (`terva rpc` under the swarm) is revived with its history intact after its process dies. Without `--session`, nothing is persisted, exactly as before.

## Auth

If the environment variable `TERVACORE_RPC_TOKEN` is set on the spawned process, the first line on stdin **must** be a `hello` command containing the matching token (the pre-rename spelling `ZOTCORE_RPC_TOKEN` is still honored, so existing embedders keep working unchanged): <!-- rename:keep -->

```json
{"id":"0","type":"hello","token":"shared-secret"}
```

A **wrong** token is fatal: the response carries `success:false` with `invalid token` and the process exits. A first frame that is **not** a `hello` is not — it gets `success:false` with `auth required: send hello with token first` and the read loop continues, refusing every command the same way until a valid `hello` arrives. Without `TERVACORE_RPC_TOKEN` set, no auth is required (the spawning process is implicitly trusted; if it can spawn `terva` it can also read your `auth.json` directly).

## Wire format

Every line in either direction is one JSON object terminated by `\n`. Object boundaries follow newline boundaries — no multi-line JSON.

### Frame types

| `type` | Direction | Description |
|---|---|---|
| any command (`prompt`, `abort`, ...) | client → server | Request |
| `response` | server → client | Reply to one command, correlated by `id` |
| any event (`text_delta`, `tool_call`, ...) | server → client | Stream notification (no `id`) |

## Commands

All commands share an optional `id` field; if present, the matching `response` echoes it. Use `id` to correlate replies with requests, especially when several requests are in flight.

### `hello`

```json
{"id":"0","type":"hello","token":"shared-secret"}
```

Response:

```json
{"type":"response","id":"0","command":"hello","success":true,
 "data":{"protocol_version":1,"version":"0.0.4","provider":"anthropic","model":"claude-opus-4-5"}}
```

Required as the first message when `TERVACORE_RPC_TOKEN` is set; optional otherwise.

### `prompt`

```json
{"id":"1","type":"prompt","message":"fix the failing test","images":[]}
```

Optional `images` is `[{"mime_type":"image/png","data":"<base64>"}]`.

Response is immediate (the turn is starting):

```json
{"type":"response","id":"1","command":"prompt","success":true,"data":{"started":true}}
```

Then a stream of event objects (see below) until the turn ends with `{"type":"done"}`.

### `abort`

Cancel the active prompt or compact.

```json
{"id":"2","type":"abort"}
```

Response: `{"type":"response","id":"2","command":"abort","success":true}`.

If the turn was streaming, the next events you see will be a `turn_end` with `stop:"aborted"` then `done`.

### `compact`

Summarise the current transcript into one synthetic user message. Same lifecycle as `prompt` (immediate response, then events).

```json
{"id":"3","type":"compact"}
```

On success it emits:

```json
{"type":"compact_done","summary":"<text>","strategy":"warm",
 "usage":{"input":412,"output":880,"cache_read":11904,"cache_write":0,"cost_usd":0.021}}
```

`strategy` is `"cold"` (the bespoke summarizer — its own system prompt, no tools,
the transcript flattened into one block, matching nothing the provider has
cached), `"warm"` (the `cache_aware_compaction` engine feature: the conversation's
own prompt prefix, so the transcript is served from cache), or
`"warm_fallback_cold"`, which adds a `fallback_reason` — `tool_use` (the model had
its tools live, as it must, and used one instead of summarizing),
`rejected_too_large`, `provider_unavailable` (the provider was down for the whole
of the transient-retry ladder — a fact about the provider, not about the warm
arm), `error`, or `empty_summary`.

`usage` is the summarization call's own spend, summed across both attempts when a
warm one fell back. **This is the only way to tell whether a warm compaction
actually hit the cache**: one that missed produces the same summary and the same
transcript and raises no error, differing only in that `cache_read` is ~0 and the
tokens were billed at full price. Unless you ran with `--session`, RPC persists no
session, so there is no row to check afterwards — if you are measuring, measure here.

On a no-op, `summary` is empty (the keep-tail already covers the whole transcript,
so there was nothing to compact). A failure emits the canonical
`{"type":"error","error":"<message>"}`.
Every outcome — success, no-op, failure, or cancellation — then terminates with
exactly one `{"type":"done"}`, identical to `prompt`, so a generic event loop can
key on `done` for both operations. `compact_done` is a result event, not the
terminal one.

Compaction also happens automatically as part of `prompt` (the same
core turn policy every run mode gets): before the model call when a
transcript is already past ~85% of the context window, after a clean
turn that pushed it past the threshold, and as a retry step when the
provider rejects a request as too large (HTTP 413). These surface on
the stream as `compact_start` / `compact_end` events inside the
prompt's request lifecycle (before its `done`), so clients need no
special handling — a failed automatic compaction is non-fatal and
rides the `compact_end` event's `error` field.

### `get_state`

Snapshot of the runtime.

```json
{"id":"4","type":"get_state"}
```

Response data:

```json
{
  "provider": "anthropic",
  "model": "claude-opus-4-5",
  "cwd": "/Users/pat/Developer/terva",
  "message_count": 12,
  "busy": false,
  "usage": {"input": 1234, "output": 567, "cache_read": 890, "cache_write": 0, "cost_usd": 0.0123}
}
```

### `get_messages`

Full transcript.

```json
{"id":"5","type":"get_messages"}
```

Response data: `{"messages": [<message>, ...]}`. See **message shape** below.

### `clear`

Drop the entire transcript. Equivalent to the `/clear` slash command.

```json
{"id":"6","type":"clear"}
```

### `set_model`

Switch model within the same provider.

```json
{"id":"7","type":"set_model","model":"claude-sonnet-4-5"}
```

Response data: `{"model":"claude-sonnet-4-5"}` — the model now backing the session.

Cross-provider swaps require relaunching `terva rpc` with the new `--provider`. So do cross-*endpoint* ones: the swap is in-place on the live client, which captured its base URL immutably at construction, so a model whose `models.json` `baseUrl` differs from the current model's is **rejected** (`model "..." routes to a different endpoint; restart the rpc session to switch`) rather than quietly firing requests at the old endpoint.

### `get_models`

List models known for the current provider.

```json
{"id":"8","type":"get_models"}
```

Response data: `{"models":[{"id":"...","provider":"...","context_window":200000,"desired_context_window":0,"context_surcharge_at":0,"max_output":8192,"reasoning":true}, ...]}`.

`context_window` is the model max — the hard ceiling. `desired_context_window` is the working window that drives auto-compaction (0 = use the max); `context_surcharge_at` is the input-token count above which the provider bills a higher rate (0 = no surcharge tier), which is the natural cost-safe value for the desired window. See [models.md](models.md).

### `ping`

Health check.

```json
{"id":"9","type":"ping"}
```

Response: `{"type":"response","id":"9","command":"ping","success":true,"data":{"pong":true}}`.

## Events

Stream notifications during a `prompt` or `compact`. None carry an `id`.

| `type` | Fields | Meaning |
|---|---|---|
| `turn_start` | `step` | Beginning of one model call (max-steps loop iteration) |
| `user_message` | `message` | The submitted prompt as it was added to the transcript (see Message shape) |
| `user_message_rejected` | `text`, `rejected` | A `BeforeUserMessage` guard refused the prompt: it never reached the model. `text` is the human-facing reason; `rejected` is the blocked prompt itself, in full — clients quote a truncated stub. On the initial-prompt path a `done` follows |
| `assistant_start` | (none) | About to receive assistant streaming |
| `text_delta` | `delta` | Partial assistant text. Concatenate to build the full reply |
| `tool_use_start` | `id`, `name` | The model began streaming a tool call |
| `tool_use_args` | `id`, `delta` | Partial tool-argument JSON |
| `tool_use_end` | `id` | Tool-argument streaming finished |
| `tool_call` | `id`, `name`, `args` | The model wants to call a tool |
| `tool_progress` | `id`, `text` | Optional progress line from the tool while it runs |
| `tool_result` | `id`, `is_error`, `content`, optional `lines_added` / `lines_removed` | Tool finished; the line-change counts (present on edits) feed the status-bar Δ segment |
| `assistant_message` | `message` | Final assistant message after the model turn ends (see Message shape) |
| `usage` | `usage`, `cumulative` | Per-turn + cumulative tokens / cost, each `{input, output, cache_read, cache_write, cost_usd}` |
| `turn_end` | `stop`, optional `error` | One model call finished. `stop` is `end`, `tool_use`, `length`, `error`, or `aborted` |
| `done` | (none) | The sole terminal event of a `prompt` or `compact` — exactly one per operation, on every outcome (success, no-op, error, or cancellation) |
| `error` | `error` | Error message under the canonical `error` field (prompt failures and explicit-`compact` failures alike) |
| `compact_done` | `summary` | Result of an explicit `compact` (summary text; empty on a no-op). Not terminal — a `done` follows |
| `compact_start` | `text` | An automatic, policy-driven compaction began inside a `prompt` (`text` carries the reason) |
| `compact_end` | optional `error` | The automatic compaction finished (before the prompt's `done`); empty `error` means success. Not terminal |
| `stall` | `stall` (`axis`, `tool`, optional `detail`, optional `rung`) | The stuck-loop detector acted on a repeating model. `axis` is `spin` (same call) or `churn` (same failure). `rung` is what it did: absent/`1` nudged, `2` held off, `3` refused to dispatch the call, `4` ended the turn (`detail` then carries why). Informational, and the turn continues — except at `rung` 4, which is followed by `done` |
| `escalation` | `escalation` (`reason`, `tool`, `from_model`, `to_provider`, `to_model`, `auto`, `disposition`, optional `detail`) | The hatch resolved a model escalation (rung 3). `disposition` is `switched`, `declined`, `stopped`, or `failed`; `detail` carries a failure's cause. Informational |
| `ext_notify` | `extension`, `level`, `message` | An extension raised a note (its `notify` frame). RPC-specific, not a `core.WireEvent` |
| `ext_display` | `extension`, `text` | An extension asked to show text (its `display` frame) |
| `ext_clear_notes` | `extension` | An extension cleared the notes it had raised |

The three `ext_*` events are the RPC surface of the extension host hooks — the RPC loop has no TUI to draw notes in, so they go on the stream for the client to render. Unlike the rest of the table they are driven by the extension, not the turn, so they can arrive outside a `prompt` / `compact`. See [extensions.md](extensions.md).

## Message shape

Used by `get_messages` and inside `user_message` / `assistant_message` events — with one difference between the two paths.

```json
{
  "role": "user",
  "content": [<content_block>...],
  "time": "2026-04-19T11:30:00Z",
  "synthetic": true
}
```

`synthetic` rides the **event** path only (it is a `core.WireMessage` field): `true` marks a host-injected message (e.g. the at-close continue-on-open-work nudge) rather than one the user typed, so a client can render it as a system note instead of a user bubble; it is omitted when false. `get_messages` serialises the transcript through its own encoder and emits `role`, `content`, and `time` **only** — never `synthetic`. A client that needs the distinction must take it from the live event stream.

### Content block types

```json
{"type": "text", "text": "..."}
{"type": "image", "mime_type": "image/png", "bytes": 12345}
{"type": "tool_call", "id": "toolu_xyz", "name": "read", "args": {"path": "..."}}
{"type": "tool_result", "call_id": "toolu_xyz", "is_error": false, "content": [<content_block>...]}
{"type": "reasoning", "reasoning_id": "...", "summary": "...", "encrypted_content": "..."}
```

Image bytes are reported as a count rather than embedded base64 to keep transcript dumps small. Tool results may nest text and image blocks. A `reasoning` block carries assistant chain-of-thought metadata; some providers (e.g. OpenAI Codex with thinking enabled) require its `encrypted_content` replayed on follow-up requests, so it must survive a wire round-trip.

## Reference clients

See `examples/rpc/` for working implementations in:

- `shell` — `bash` + `jq` one-liner
- `python` — `subprocess.Popen` + `json.loads` per line
- `node` — `child_process.spawn` + `readline`
- `go` — direct subprocess wrapper (the `packages/agent/sdk` SDK is the in-process Go API)

## Versioning

The `protocol_version` field in the `hello` response is the major version of this schema. Backwards-incompatible changes bump it. The set of supported events and commands within a major version only grows.
