# Embedding terva

There are three ways to put terva inside another program. They differ in how
much of terva comes with it, and in who decides what the agent sees, which
tools it has, and where its transcript lives.

| | Over RPC | The SDK | The engine |
|---|---|---|---|
| Runs | `terva rpc` as a child process | `packages/agent/sdk` in your Go process | `packages/core` and `packages/provider` in your Go process |
| Language | Any that can spawn a process and read its pipes | Go | Go |
| Conventions | terva's, in full | terva's, minus what the SDK lists as yours | Yours |
| Configuration | `$TERVA_HOME`, the same as the CLI | `$TERVA_HOME/config.json`, env vars, and `sdk.Config` | Only the values you pass |
| Compatibility | The protocol's major version | The SDK's exported API | None yet |
| Start from | [rpc.md](rpc.md), `examples/rpc/` | `examples/sdk/` | `examples/harness/` |

## Over RPC

`terva rpc` is the whole of terva behind newline-delimited JSON on stdin and
stdout: the provider resolver, the tool set, extensions, MCP servers, sessions,
and the permission gate. [rpc.md](rpc.md) is the reference.

**What it costs.** A process per cwd, model and session, and a JSON hop for every
event. You get what terva's flags and config expose, and no more: the prompt, the
tools and the transcript format are terva's. A tool of your own has to be an
[extension](extensions.md) or an [MCP server](mcp.md).

**Pick it when** your program is not written in Go, when you want terva's
behavior as it is, or when you want the agent in a separate process so a crash
or a runaway tool cannot take your program down. The protocol is versioned, so
this is the most stable of the three.

## In-process, with terva's conventions: the SDK

`sdk.New` builds the same runtime the CLI does, through terva's resolver, and
hands you a `Runtime` with `Prompt`, `Cancel` and an event channel. The events
are the same type set as the RPC stream, so parsing code carries over.

**What it costs.** You take terva's harness whole. It reads
`$TERVA_HOME/config.json` and the environment for the provider, credentials and
the user's permission rules, and it builds terva's tool registry. The package
comment lists what the SDK does not wire: extensions, MCP servers, hooks, the
audit log and session persistence are yours, and the sandbox is opt-in.

**Pick it when** your program is in Go and terva's defaults suit you, but a
subprocess does not: you want to call it as a library, share memory with it,
or ship a single binary.

## In-process, with your own conventions: the engine

`core.NewAgent(client, model, assembler, tools, gate)` is the agent loop with
nothing chosen for you. You pass the provider client, the frame each request is
built from, the tool registry and the permission gate. The wire takes
credentials as values and reads nothing itself. The agent writes its
transcript to a `core.TranscriptStore` you attach, and resumes from a
`core.Transcript` you load. `core.MemoryTranscriptStore` keeps it in memory,
`packages/session` is terva's JSONL session file if you want that, and a store
of your own passes `packages/core/transcripttest`, the suite both of those
pass.
`examples/harness` is a complete agent built this way, and a test keeps it
from importing anything else of terva's.

**What it costs.** Every choice terva's harness makes is now yours to make:
where credentials come from, what the prompt holds, which tools exist and what
may run. The API is not stable yet: until the engine's stable packages are
drawn, a minor release may change anything you use. One piece is still
missing: a model catalog you pass rather than the wire's global one.

**Pick it when** terva's conventions are not the ones you want: you want to
decide what the model sees on each request, keep transcripts in your own store,
or expose only tools you wrote.
