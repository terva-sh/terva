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
| ACP JSON-RPC | 16 MiB | `acp.acpMaxFrameBytes` | skip frame |
| `terva rpc` NDJSON | 16 MiB | `rpc.rpcMaxFrameBytes` | skip frame + error frame to caller |
| Web WebSocket message | 16 MiB | `web/conn.go` `maxFrameBytes` | **connection closed** (WebSocket `SetReadLimit`, not lineframe) |

The 16 MiB tier (ACP, rpc, web) carries model prompts/results, which run larger
than a control frame; the 4 MiB tier is the historical carrier ceiling.

## Provider event streams (network-facing, reject)

A provider's `text/event-stream` response is the one framed reader that must
**not** skip. Its lines are not independent messages — they are deltas of a
single assistant message — so dropping one would punch an invisible hole in the
model's output. `provider/sse.go` therefore builds a reject policy on
`lineframe.ReadFrame` and reports *why* the stream ended.

| Boundary | Limit | Where | On exceed |
| --- | --- | --- | --- |
| SSE event line | 10 MiB | `provider.maxSSELineBytes` | **abort the stream**, permanent `ErrStreamLimit` |

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
| Model-turn image-recovery rounds | 16 | `core.maxImageRecoveryRounds` | stop peeling images off the turn |
| TUI clipboard paste | 2 MiB | `tui.maxPasteBytes` | paste truncated |

## Test coverage

Directly exercised: the `lineframe` bounds (`packages/lineframe/lineframe_test.go`
— oversized-frame recovery, exact-limit boundary, custom limit), the SSE reject
policy and its retry classification (`provider/provider_test.go` — over-limit
line is permanent, transport failure is transient and wrapped), the ACP/rpc
read loops through their package tests, the `read`/`grep` truncation paths
(`tools/*_test.go`), and the error-sidecar bound (`core/session_error_test.go`).

Boundaries without a dedicated bound-checking test — worth adding as they're
touched — include the image-gen body caps, the character-card cap, the Discord
attachment cap, and the WebSocket frame limit.
