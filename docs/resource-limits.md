# Resource limits

Every place terva reads a payload it does not itself produce — a wire frame from
a subprocess or socket, an HTTP response body, a file the model asked to read,
a pasted blob — is bounded. This page is the single inventory of those bounds:
what the limit is, where it lives, and what happens when a payload exceeds it.

Keep it current when you add or change a boundary. A limit that isn't written
here is a limit nobody can audit.

## The two stances

Boundaries fall into two behaviors, and the right one depends on who is on the
other end:

- **Recoverable (skip / truncate, keep going).** A single over-limit payload
  from an untrusted or buggy peer must not tear down a long-lived connection
  carrying many other messages. Multiplexed wire protocols read through
  `lineframe.Reader`, which drains an over-limit frame and continues;
  file/paging reads truncate with a "continue with offset=…" hint.
- **Reject (fail this operation).** A payload that can't be safely truncated —
  an image to decode, a manifest to parse, a character card, or one frame of a
  stream whose frames are *all one response* — is rejected whole rather than
  half-processed. Skipping there would be silent corruption.

Both stances read through [`packages/lineframe`](../packages/lineframe):
`ReadFrame` is the bounded primitive, `Reader` layers skip-and-continue on top,
and a reject-policy caller uses `ReadFrame` directly. The choice is per-boundary
and is about what a dropped frame *means* there, not about how much you trust
the peer.

`bufio.Scanner` supports neither: one token past its buffer returns `ErrTooLong`
and the scanner is *permanently* done — and it reports that only through `Err()`,
which callers forget to check, so an over-limit line silently becomes a clean
end-of-stream. That failure mode is why every peer-facing reader moved to
`lineframe`; do not reintroduce a raw `Scanner` on one.

## Wire frames (peer-facing, recoverable)

All newline-delimited JSON wire protocols read through `lineframe.Reader`, so an
over-limit frame is skipped (and, where a log/error channel exists, reported)
and the connection survives.

| Boundary | Limit | Where | On exceed |
| --- | --- | --- | --- |
| lineframe default | 4 MiB | `lineframe.DefaultMaxBytes` | skip frame, continue |
| connector carriers (connproto) | 4 MiB | `connproto.MaxFrameBytes` → default | skip + warn |
| extension stdio (extproto) | 4 MiB | `extproto.MaxFrameBytes` → default | skip + log |
| extension tool-call args (host → ext) | 1 MiB | `extproto.MaxToolCallBytes` | returned to the model as a tool error |
| MCP server stdout | 4 MiB | `mcp` (default) | skip frame; that response times out, connection lives |
| MCP streamable-HTTP / SSE | 4 MiB | `mcp/mcp_http.go` `readSSE` (default) | skip frame + warn to the server's log; stream survives |
| terva's own MCP approval server | 4 MiB | `mcpbridge` `server.run` (default) | skip frame + warn to stderr |
| worker / swarm child stdout+stderr | 4 MiB | `worker`, `swarm` runners (default) | skip line, surfaced in the transcript |
| swarm inbox socket | 4 MiB | `swarm.Listener.readLoop` (default) | skip frame + warn |
| `terva-mcp-bridge` relay (both halves) | 8 MiB | `bridgeMaxFrameBytes` | downstream: skip + warn; upstream: **reject** (see below) |
| ACP JSON-RPC | 16 MiB | `acp.acpMaxFrameBytes` | skip frame |
| `terva rpc` NDJSON | 16 MiB | `rpc.rpcMaxFrameBytes` | skip frame + error frame to caller |
| Web WebSocket message | 32 MiB | `web/conn.go` `maxFrameBytes` | **connection closed** (WebSocket `SetReadLimit`, not lineframe) |

The 16 MiB tier (ACP, rpc) carries model prompts/results, which run larger than
a control frame; the 4 MiB tier is the historical carrier ceiling. The bridge
sits between them at 8 MiB because an MCP tool result legitimately carries a
screenshot or a whole-file read, and it is one constant across both halves of
the relay — it used to be written twice, and a limit written twice is a limit
that drifts. The web
carrier sits above both at 32 MiB, and advertises `Hello.MaxUploadBytes`
(≈24 MiB, the base64-inflated file that fits inside one) so a client can refuse
an oversized file *before* sending it — an over-limit frame is not an error, it
closes the socket, and the request then dies with a generic dead-socket message
that names nothing the user can act on.

## Single-payload readers (reject)

A provider's `text/event-stream` response is the clearest case of a framed reader
that must **not** skip. Its lines are not independent messages — they are deltas of a
single assistant message — so dropping one would punch an invisible hole in the
model's output. `provider/sse.go` therefore builds a reject policy on
`lineframe.ReadFrame` and reports *why* the stream ended.

| Boundary | Limit | Where | On exceed |
| --- | --- | --- | --- |
| SSE event line | 10 MiB | `provider.maxSSELineBytes` | **abort the stream**, permanent `ErrStreamLimit` |
| bridge upstream SSE response | 8 MiB | `terva-mcp-bridge` `readResponseFrame` | error naming the frame limit |
| worker approval request | 4 MiB | `worker.handleApprovalConn` (default) | **deny** (fail closed) |
| bridge approval reply | 4 MiB | `mcpbridge.server.ask` (default) | error; the tool call fails |

The last three connections each carry exactly ONE frame, so a skipped frame is
not a gap in a stream — it is the whole answer. The bridge's upstream reader is
why this row exists: it used to skip silently and then return whatever frame
came *before*, so an over-limit tool result was relayed to terva as the
preceding progress notification, with no error anywhere.

Gemini is the client most likely to meet the ceiling: it streams a complete
`GenerateContentResponse` per line, base64 inline image bytes included, where
the other providers send small deltas.

The classification matters as much as the bound. An over-limit line is
*deterministic* — the server re-sends the identical event on every attempt — so
`NewStreamLimitError` sets `Transient: false`. Marking it transient (which is
what the old discarded-`Err()` path did, by falling through to
`NewStreamDeathError`) spends the whole retry budget, and the input tokens for
each attempt, to fail in the same place and then blame the network. A genuine
mid-stream transport failure keeps `Transient: true` via `NewStreamReadError`
and now carries its cause instead of being laundered into a generic truncation.

## Local files (trusted, large caps)

Session transcripts and exports are terva's own files, so they read with a big
cap rather than the recoverable skip — a truncated transcript row would be data
loss, not a defended attack surface.

| Boundary | Limit | Where | On exceed |
| --- | --- | --- | --- |
| Session JSONL row (import/export) | 20 MiB | `core/session_portable.go` Scanner buffer | scan error (import/export fails loudly) |
| Error-sidecar entry | 4 KiB | `core.maxSidecarErrorLen` | truncated with a byte-count marker (after secret redaction) |

## Model-facing file reads (tools)

The `read`/`grep` tools bound what a single call pulls into the model's context.
These truncate and tell the model how to page for more.

| Boundary | Limit | Where | On exceed |
| --- | --- | --- | --- |
| `read` in-memory file load | 10 MiB | `tools.maxReadFileBytes` | tail beyond the cap not loaded (noted in output) |
| `read` output slice | 50 KiB | `tools.maxReadBytes` | truncated + `continue with offset=` hint |
| `read` line count | 2000 | `tools.maxReadLines` | truncated + hint |
| `read` inline image | 5 MiB | `tools.maxImageBytes` | rejected with a size error |
| `grep` per-file scan | 10 MiB | `tools.maxReadFileBytes` | stops at the cap |

## In-engine scripting (`jsengine`)

A script running on the embedded JS engine ([scripting.md](scripting.md)) pulls
tool results across into a VM. These bounds are the engine's defaults, in
`packages/agent/jsengine`; a caller may tighten them per run via
`jsengine.Limits`.

| Boundary | Limit | Where | On exceed |
| --- | --- | --- | --- |
| binding return, one host call | 1 MiB | `jsengine.defaultMaxBindingBytes` | rejected, catchable in-script |
| host calls per run | 50 | `jsengine.defaultMaxHostCalls` | rejected, catchable in-script |
| printed output | 32 KiB | `jsengine.defaultMaxOutputBytes` | truncated, `Result.Truncated` set |
| JS call-stack depth | 2048 frames | `jsengine.defaultMaxCallStack` | run fails |

The binding-return cap **rejects rather than truncates**, unlike the file reads
above, because the two stances differ by consumer: a truncated `read` result
reaches a model that can see the "continue with offset=" hint and page, whereas
a truncated string handed to a script is indistinguishable from a short one and
silently corrupts what the script computes from it.

Run time is not listed because the engine takes it from the caller's context —
for `code_execution` that is 30 s, raisable to 120 s per call.

The engine does **not** meter VM heap: no pure-Go interpreter can
([decision 0008](decisions/0008-one-static-binary.md) chose a cgo-free binary).
So the import path is the only bound available, and it is the product of two
rows above: at most host calls × binding return, 50 MiB, enters a run from the
host. Nothing bounds what the script then allocates itself; the context
deadline is the backstop.

## Network response bodies (HTTP, `io.LimitReader`)

Every outbound HTTP response is read through an `io.LimitReader` so a hostile or
runaway server can't OOM the process.

| Boundary | Limit | Where | On exceed |
| --- | --- | --- | --- |
| Provider error body (for the error message) | 4 KiB | `provider/retry.go` | truncated (message only) |
| Image decode for resize | 64 MP (≈8000×8000) | `provider/anthropic_image.go` | rejected |
| Image-gen response — OpenAI | 64 MiB | `imagegen/openai.go` | truncated → decode fails |
| Image-gen response — ComfyUI | 128 MiB | `imagegen/comfyui.go` | truncated → decode fails |
| Image-gen response — Automatic1111 | 128 MiB | `imagegen/a1111.go` | truncated → decode fails |
| Extension pack manifest | 256 KiB | `extpack.maxPackManifestBytes` | rejected |
| Discord attachment download | 8 MiB | `modes/discord/transport.go` `maxAttachmentBytes` | truncated |

## Other user input

| Boundary | Limit | Where | On exceed |
| --- | --- | --- | --- |
| Character card (PNG import) | 8 MiB | `card.maxCharaBytes` | rejected |
| Web file attachment (per file) | 100 MB | `attach.MaxBytes` | rejected whole (413) |
| Web file attachment (whole staging area) | 2 GiB | `attach.CapBytes` | oldest evicted, sweeper |
| Shared file, agent → user (per file) | 100 MB | `attach.MaxBytes` | rejected whole (tool error) |
| Shared file (whole share area) | 2 GiB | `attach.ShareCapBytes` | oldest evicted, sweeper |
| Model-turn image-recovery rounds | 16 | `core.maxImageRecoveryRounds` | stop peeling images off the turn |
| TUI clipboard paste | 2 MiB | `tui.maxPasteBytes` | paste truncated |

## Test coverage

Directly exercised: the `lineframe` bounds (`packages/lineframe/lineframe_test.go`
— oversized-frame recovery, exact-limit boundary, custom limit), the SSE reject
policy and its retry classification (`provider/provider_test.go` — over-limit
line is permanent, transport failure is transient and wrapped), the ACP/rpc
read loops through their package tests, the `read`/`grep` truncation paths
(`tools/*_test.go`), and the error-sidecar bound (`core/session_error_test.go`).

Also directly exercised: the attachment caps
(`packages/agent/attach/attach_test.go` — the per-file limit rejects whole and
leaves nothing behind, the exact-limit boundary is accepted, and the sweeper's
TTL/size/grace rules), the same for the outbound direction
(`packages/agent/attach/share_test.go`, which also pins that each store sweeps on
its OWN policy — a share still young at the inbound TTL is the case that breaks
if the constants are ever read globally), and both routes' refusals
(`packages/agent/web/upload_test.go` — over-cap, cross-origin, unauthenticated,
sessionless; `shared_test.go` — unauthenticated, unresolvable, and the inline
allowlist).

Also directly exercised: the `jsengine` bounds
(`packages/agent/jsengine/jsengine_test.go` — the binding-return cap both
caught in-script and left uncaught, the host-call budget across both binding
kinds, output truncation, and stack overflow). One test deliberately pins a
**gap** rather than a bound: the return cap measures strings only, so a
structured return passes uncounted, and that is recorded as known behaviour
instead of left to surprise someone.

Boundaries without a dedicated bound-checking test — worth adding as they're
touched — include the image-gen body caps, the character-card cap, the Discord
attachment cap, and the WebSocket frame limit.

## A note on the attachment staging area

The per-file cap is the only one here that a user meets routinely, and it is a
**reject**, not a truncate: half an export or half a database dump is silent
corruption, and unlike a wire frame there is no long-lived stream to keep alive
by skipping it. The route streams the multipart part straight to disk rather
than letting `FormFile` materialize it first, so a 100 MB upload is written
once, into a directory terva owns and sweeps — not twice, with the first copy
left in `os.TempDir`.

The area's own bound is enforced by a sweep rather than at write time (24h TTL,
then oldest-first eviction over `CapBytes`), with a one-hour grace window that
protects a just-staged file. Without it, a burst of uploads could evict the very
files the message being composed is about to reference — the one deletion here
that waiting cannot undo.

The outbound area (`$TERVA_HOME/shared`, files the agent handed the user with
`share_file`) runs the same machinery with a **7-day** TTL. The asymmetry is
deliberate rather than an oversight: an uploaded file has done its job the moment
the agent has read it, while a shared one IS the deliverable, and the obvious way
to want it is to reopen the session days later. The size cap and grace window are
unchanged — the backstop is about not letting a runaway agent fill the disk, and
that concern does not care which way the files were going. Each store carries its
own `attach.Policy` and starts its own sweeper, so neither retention can silently
become the other's.

Reading a shared file back over the **control plane** (`shared.fetch`, the verb
a non-web client uses because it has no HTTP route) is bounded separately at
**8 MiB**, and the bound is checked by a stat before the read rather than after.
It is not a limit on what may be shared: the store takes far larger files, the
web route serves them with range requests, and a local client has the path. It
is a limit on what may be inlined into a single wire frame, which both ends read
into memory whole — so the refusal names the file and points at the path
instead. The TUI applies a tighter **2 MiB** ceiling to the image previews it
pulls automatically, for a different reason: a card that is merely on screen has
not asked you to wait for it.

A preview is fetched **once per share**, with the claim taken before the request
goes out. A card is re-rendered on every frame, so an unclaimed fetch would
become one request per repaint; a failure is remembered too, which is what stops
a swept file from being asked for forever.
