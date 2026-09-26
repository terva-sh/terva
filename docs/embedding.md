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

`core.New(client, model, opts...)` is the agent loop with nothing chosen for
you. Each choice is an option: `WithAssembler` for the frame each request is
built from, `WithTools` for the tool registry, and `WithGate` for the
permission gate, the one option `New` requires. `New` returns an error for a
configuration it cannot build, such as a missing gate. `WithComponent` attaches
a component such as `stall`, `lazytools` or `contextpressure` through each hook
it implements. `Agent.Run` starts a turn from an input: `core.PromptInput` for
a user message, `core.ContinueInput` to resume without one. `New` is the only
constructor. The agent's settings are private: a host changes one through a
setter such as `SetModel`, and reads one back through a getter such as
`Model()` or `Client()`, and both take the agent's lock. Subscription usage
and resets belong to the client, so pass `Agent.Client()` to
`provider.ClientUsage` and its siblings. The wire takes
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
drawn, a minor release may change anything you use. The wire holds no model
catalog. Build one (`provider.NewRegistry`) and pass it to the agent with
`core.WithCatalog` and to its client with `provider.WithCatalog`. An agent or
client given none knows the built-in models and nothing else.

The engine writes text of its own: notes and refusals to the model, and
messages its turns report. It renders that text in English, or through the
translator `i18n.Use` installs for the whole process (package
`packages/core/i18n`). To run two agents in different languages, give each
its own translator with `core.WithTranslator`. The `stall`, `lazytools` and
`shellresult` components then speak the agent's language too. Two kinds of
text do not follow it:

- Compaction prompts and the `contextpressure` note are policy, not
  translation. They come from the agent's `CompactionPolicy`, which can also
  word the note as a `contextpressure.Noter`. Without one, they come from the
  neutral English in `compactprose`. For another language there, give each
  agent a policy in that language.
- Text from core's helper functions that your host calls itself, such as
  `core.ClassifyRecoverable`, still uses the process-wide translator.

A client sends its requests through `http.DefaultTransport`. To route them
through your own proxy, transport or test server, give the client your HTTP
client with `provider.WithHTTPClient`. For GitHub Copilot and for Vertex, that
also carries the exchange that turns your credential into a short-lived token.
Each client keeps its own token cache. The one request it does not carry is the
usage poll of the OpenRouter, DeepSeek and Kimi clients.

**Pick it when** terva's conventions are not the ones you want: you want to
decide what the model sees on each request, keep transcripts in your own store,
or expose only tools you wrote.
