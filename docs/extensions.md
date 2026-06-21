# terva extensions

terva can be extended with custom slash commands by running an external
program as a subprocess and exchanging newline-delimited JSON over
its stdin/stdout. Extensions can be written in **any language** that
can read and write JSON lines from stdio — Go, TypeScript, Python,
Rust, shell with `jq`, anything.

Four phases shipped so far:

- **Phase 1**: slash commands + chat notifications.
- **Phase 2**: tools the LLM can call.
- **Phase 3**: lifecycle event subscriptions + tool-call interception
  for guardrail extensions.
- **Phase 4**: interactive extension-owned panels rendered inside terva.
- **Theme-only extensions**: ship `theme.json` without launching a
  subprocess. See [themes.md](themes.md).

## Quick start

The simplest extension is a script that prints a hello frame, reads
commands, and prints responses. Here's the whole thing in **Python**,
no SDK required:

```python
#!/usr/bin/env python3
# $TERVA_HOME/extensions/hello-py/hello.py
import json, sys, threading

def emit(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

emit({"type":"hello","name":"hello-py","version":"1.0.0","capabilities":["commands"]})
emit({"type":"register_command","name":"hellopy","description":"say hi (python)"})

for line in sys.stdin:
    msg = json.loads(line)
    if msg["type"] == "command_invoked":
        emit({"type":"command_response","id":msg["id"],"action":"prompt",
              "prompt": "Greet me very briefly. Add one emoji."})
    elif msg["type"] == "shutdown":
        emit({"type":"shutdown_ack"})
        break
```

Drop it in a directory with this `extension.json`:

```json
{
  "name": "hello-py",
  "version": "1.0.0",
  "exec": "./hello.py",
  "language": "python",
  "enabled": true
}
```

`exec` is required for protocol extensions. If an extension only ships
`theme.json` or `themes/theme.json`, no `exec` is required and terva does
not spawn a subprocess.

`chmod +x hello.py`, install:

```bash
terva ext install ./hello-py
```

Restart `terva`, type `/hellopy`, the agent greets you. Done.

## Built-in extensions

**terva ships with no extensions installed by default.** A fresh `terva install` (or `go install`) gives you a clean agent. Extensions are entirely opt-in: you install (or `--ext` for one run) only the ones you want.

The `examples/extensions/` directory in the repo is reference code, not a default install set. To use any of those:

```bash
# go-based examples need a build first
cd path/to/terva/examples/extensions/hello && go build -o hello .

# install (copies to $TERVA_HOME/extensions/hello/)
terva ext install path/to/terva/examples/extensions/hello

# or load straight from the repo for one terva session
terva --ext path/to/terva/examples/extensions/hello
```

Nothing is auto-installed and nothing reaches out to the network without your explicit action.

## Extension packs

A **pack** is a hosted manifest naming a set of extensions, so you can
install a useful starting set in one step instead of N `ext install`
calls:

```bash
terva ext pack install              # the built-in "core" pack (default)
terva ext pack install core         # same, explicitly
terva ext pack install https://example.com/team-pack.json
terva ext pack install ./pack.json  # a local manifest
```

A pack is just a **list of sources** — terva clones each one and spawns
it exactly as `ext install` does. It carries no binaries or checksums:
each extension owns its own bring-up (the recommended
[self-bootstrapping launcher](#recommended-a-self-bootstrapping-launcher)
compiles on first run, or downloads a verified release binary when no
compiler is present), so binary integrity is the extension's
responsibility, not terva's. An already-installed entry is skipped, so
re-running a pack is safe. Lifecycle afterwards is the normal per-extension tools
(`terva ext list` / `enable` / `disable` / `remove`) — a pack is a
starting point, not a managed set.

Installing from a non-built-in pack (a URL or file) prints the entries
and asks for confirmation first; `--yes` skips the prompt. The built-in
core pack ships with terva and installs without prompting.

A pack manifest is JSON:

```json
{
  "schema": "terva-extension-pack/v1",
  "name": "core",
  "description": "The terva core extension set.",
  "extensions": [
    { "name": "index", "source": "https://github.com/terva-sh/terva-ext-index.git", "ref": "v0.2.0" }
  ]
}
```

Each entry needs a `source` (git URL or local path). `ref` is an
optional branch or tag (absent → the repo's default branch); `name`
defaults to the source basename. See
`docs/plans/extension-packs.md` for the full schema.

### First-run offer

The very first time you start an interactive session with **no
extensions installed**, terva offers to install the core pack. It asks
at most once, only on an interactive terminal (never when input is
piped or in CI), and going through `install.sh` never triggers it. Say
no and it won't ask again; install later with
`terva ext pack install core`.

Suppress the offer entirely (e.g. for fleet provisioning) with user
config:

```json
{ "disable_core_pack_offer": true }
```

## Layout & discovery

terva scans two directories on startup, in this order:

1. **Project-local**: `./.terva/extensions/<name>/extension.json`
2. **Global**: `$TERVA_HOME/extensions/<name>/extension.json`

A project-local extension with the same name wins over a global one.
On macOS `$TERVA_HOME` defaults to `~/Library/Application Support/terva/`;
on Linux it's `$XDG_STATE_HOME/terva` or `~/.local/state/terva`.

Because each extension owns its own directory, the recommended place
for extension state is inside that directory itself (for example
`todos.json`, `settings.json`, or an auth/cache file used only by that
extension). The host also passes this path back in `hello_ack` as
`extension_dir` / `data_dir` so runtime code does not need to guess it.

Each extension owns its own subdirectory. The `extension.json`
manifest tells terva how to launch it:

```json
{
  "name": "weather",
  "version": "1.0.0",
  "exec": "./weather",
  "args": ["--mode", "daemon"],
  "language": "go",
  "description": "current weather for any city",
  "enabled": true
}
```

| field | meaning |
|---|---|
| `name` | required. how terva identifies the extension; must match what's sent in the `hello` frame. |
| `version` | optional. shown in `terva ext list`. |
| `exec` | required. path to the executable (relative to the manifest). |
| `args` | optional. extra argv passed to `exec`. |
| `language` | optional. informational only (`go`, `python`, `typescript`, ...). |
| `description` | optional. shown in `terva ext list`. |
| `enabled` | optional, defaults to `true`. set to `false` to disable without removing. |
| `permissions` | optional **bundle contribution**: suggested permission rules (see below). |

## Recommended: a self-bootstrapping launcher

For a **compiled** extension (Go, Rust, …) the strongly recommended
pattern is to point `exec` at a small launcher script — not at a binary
you commit to the repo — and let that script own the entire
build/download story. A fresh `terva ext install <git-url>` or an
[extension pack](#extension-packs) clones source with no binary in it;
the launcher is what turns that clone into something runnable.

This keeps bring-up **inside the extension**, which is exactly where
terva wants it. terva treats an extension as an opaque subprocess: it
clones the directory and spawns `exec`, and deliberately knows nothing
about toolchains, target platforms, or release URLs. The launcher is the
one place with the context to do build-or-download well — so that
responsibility lives there, not in the host.

The launcher should try, in order:

1. **Use the binary** if it's present and newer than the sources — just
   `exec` it. The fast path; no rebuild on every launch.
2. **Build** from source if a compiler is available (binary missing or
   stale). This is also what makes `terva update` work: it pulls new
   source, and the next launch rebuilds because the sources are now
   newer than the binary.
3. **Download** a prebuilt release binary for the host OS/arch when there
   is no compiler — and **verify its checksum** before trusting it.
   Binary integrity is the extension's job; terva does not verify it.
4. **Fail clearly**: if none of the above worked, print how to build it
   by hand, **disable itself** in the manifest so terva stops re-spawning
   it every session, and exit non-zero. The user builds it and runs
   `terva ext enable <name>`.

Pair it with a manifest whose `exec` is the launcher. Ship `enabled`
explicitly so step 4 has a field to flip:

```json
{ "name": "index", "exec": "./run.sh", "language": "go", "enabled": true }
```

A reference `run.sh` (POSIX sh — works on Linux and macOS; Windows is a
second-class target, so ship a `run.cmd` alongside or document a manual
build):

```sh
#!/usr/bin/env sh
set -eu
# Run from the extension's own directory so relative paths resolve no
# matter what cwd terva spawned us from.
cd "$(dirname "$0")"

NAME=index          # must match extension.json "name"
BIN=./index         # the built binary

# Print build instructions, disable in the manifest, and give up.
fail() {
  echo "$NAME: $1" >&2
  echo "Build it yourself:  (cd '$(pwd)' && go build -o '$BIN' .)" >&2
  echo "Then re-enable:     terva ext enable $NAME" >&2
  # Flip "enabled" to false so terva does not re-spawn this launcher on
  # every session until the binary exists.
  if command -v jq >/dev/null 2>&1; then
    tmp=$(mktemp) && jq '.enabled=false' extension.json >"$tmp" && mv "$tmp" extension.json
  else
    sed -i.bak 's/"enabled"[[:space:]]*:[[:space:]]*true/"enabled": false/' extension.json && rm -f extension.json.bak
  fi
  exit 1
}

# Download + checksum-verify a release binary for this host. Returns
# non-zero (so the caller falls through to fail) if anything is missing.
download_release() {
  command -v curl >/dev/null 2>&1 || return 1
  os=$(uname -s | tr '[:upper:]' '[:lower:]')          # linux | darwin
  arch=$(uname -m)
  case "$arch" in x86_64|amd64) arch=amd64 ;; arm64|aarch64) arch=arm64 ;; *) return 1 ;; esac
  base="https://github.com/OWNER/REPO/releases/latest/download"   # per-extension
  asset="${NAME}_${os}_${arch}"
  curl -fsSL "$base/$asset"        -o "$BIN"        || return 1
  curl -fsSL "$base/$asset.sha256" -o "$BIN.sha256" || return 1
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c "$BIN.sha256" >&2 || return 1
  else
    shasum -a 256 -c "$BIN.sha256" >&2 || return 1
  fi
  rm -f "$BIN.sha256"; chmod +x "$BIN"
}

# 1. Fast path: a fresh binary already exists -> hand off.
if [ -x "$BIN" ] && [ -z "$(find . -name '*.go' -newer "$BIN" -print 2>/dev/null | head -n1)" ]; then
  exec "$BIN" "$@"
fi

# 2. Build from source when a compiler is present.
if command -v go >/dev/null 2>&1; then
  if go build -o "$BIN" . >&2; then
    exec "$BIN" "$@"
  fi
  fail "build failed — see the errors above"
fi

# 3. No compiler: download a verified release binary.
if download_release; then
  exec "$BIN" "$@"
fi

# 4. Nothing worked.
fail "no Go compiler and no verified release download available"
```

`exec` matters: it **replaces** the shell with your binary, so terva's
stdin/stdout pipes connect straight to it — the launcher must never sit
between terva and the extension on the wire. Everything the launcher
prints goes to **stderr**, which terva routes to
`$TERVA_HOME/logs/ext-<name>.log`; stdout is reserved for the JSON
protocol, so build chatter there would corrupt the wire (note the `>&2`
on `go build` and the verify step).

## Bundle contributions

An installed extension directory is also a declarative bundle — it can
contribute data alongside its executable:

- **Skills**: a `skills/` directory beside `extension.json` joins
  skill discovery (`skills/<name>/SKILL.md`, same format as
  [skills.md](skills.md)). Bundle skills rank after the user's own
  skill directories, so they can never shadow a deliberately-authored
  skill, and a disabled extension contributes nothing.
- **Suggested permission rules**: a `permissions` array in the
  manifest (same shape as [permissions.md](permissions.md) rules).
  Like project rules, the extension layer may only *restrict*: `deny`
  and `ask` are honored, `allow` is dropped with a warning — installing
  a bundle can tighten the posture but never grant tool access the
  user didn't. Evaluated after project rules, before user rules.

Hooks and MCP server declarations are deliberately **not**
bundle-contributable: both mean running additional programs, and that
stays an explicit user-config decision (see [hooks.md](hooks.md),
[mcp.md](mcp.md)).

## Lifecycle

1. **Discovery**: terva reads every `extension.json` in the search dirs.
2. **Spawn**: enabled extensions are launched as subprocesses. stderr
   redirects to `$TERVA_HOME/logs/ext-<name>.log` (one file per
   extension, append-mode). The child environment is the host's minus
   loader/interpreter injection vars (`LD_*`, `DYLD_*`, `PYTHONPATH`,
   `NODE_OPTIONS`, `JAVA_TOOL_OPTIONS`, `BASH_ENV`, …) — an extension
   that needs one of those must set it itself for its own children.
   `PATH`, `HOME`, API keys, and everything else pass through.
3. **Hello handshake**: the extension sends a `hello` frame; terva
   replies with `hello_ack` containing the protocol version, the
   active provider/model/cwd, and the extension's own data directory
   so it can persist files beside its manifest.
4. **Registration**: the extension sends `register_command` frames.
   First-come-first-served: a name already taken by a built-in or by
   a previously-loaded extension is silently shadowed (logged in the
   extension's own log file).
5. **Runtime**: terva dispatches `command_invoked` frames when the
   user runs a registered command; the extension responds with
   `command_response`. Extensions can also push `notify` frames at
   any time. Panel-capable extensions may open an interactive panel,
   receive key events, and push redraws while the panel is focused.
6. **Shutdown**: when terva exits, it sends `shutdown` and waits up to
   2s for the extension to send `shutdown_ack`. Holdouts are
   SIGTERM'd, then SIGKILL'd.

A crashing extension does not bring down terva. The slash command it
owned simply stops working until the extension is fixed and terva is
restarted.

## Context contributions

An extension can contribute to what the **model** sees, under host
control (see [the design](plans/archive/extension-context-cards.md)): static
guidance folded into the system prompt (`register_context`), live
per-turn cards (`context_card`), and a status-line segment
(`status_segment`). Run `/context` to see exactly what's injected.

The static block is normally set once during registration. An extension
that needs to **swap it mid-session** — a memory store loading this
project's notes on `session_start`, say — sends `refresh_context`
(protocol 3, declare `RequireProtocol(3)`): the host replaces the block
and rebuilds the cached system prompt so it takes effect on the next
turn. It stays a *snapshot* — it changes only when the extension sends
the frame, not every turn — so the prompt cache survives. The per-block
budget is a few KB; the host trims anything larger.

Installing an extension is consent to run it, but you can opt one out of
injecting into the model's context — per user **or** per project — with
`disable_context_extensions` in `config.json`:

```json
{"disable_context_extensions": ["noisy-ext"]}
```

A project's `.terva/config.json` may add to this list but never remove
from it (restrict-only union with the user layer), so a directory can
run terva with a stricter context posture. The disabled extension's
tools, commands, and panels keep working — only model-context injection
is suppressed.

## Wire format

All frames are one JSON object per line. Top-level `type` is the
discriminator. Optional `id` correlates request frames with their
responses.

### Frame size limits

There is a per-frame maximum of **4 MiB** (`extproto.MaxFrameBytes`) in
both directions. Oversized frames are handled gracefully, never fatally:

- A frame larger than the cap on the read side (either direction) is
  **skipped and logged**, and reading continues — one oversized frame
  never takes the extension or the host's reader down.
- The host caps the args it puts in a single `tool_call` frame at
  **1 MiB** (`extproto.MaxToolCallBytes`, comfortably below the read
  cap). If the model produces a larger tool argument, the call comes
  back to the model as a normal `is_error` tool result ("arguments are
  N bytes; the limit is …") instead of being sent — so an oversized
  argument can't kill an extension. Keep individual tool results and
  context contributions well under these limits.

### Extension → host

#### `hello` (required, first frame)

```json
{"type":"hello","name":"weather","version":"1.0.0",
 "capabilities":["commands","tools","panels"]}
```

#### `register_command`

```json
{"type":"register_command","name":"weather",
 "description":"current weather for a city"}
```

#### `register_tool`

Registers a tool the LLM can call. `schema` is a JSON Schema object
describing the tool's args (the same shape Anthropic and OpenAI accept).

```json
{"type":"register_tool","name":"weather",
 "description":"Get the current weather for a city.",
 "schema":{
   "type":"object",
   "properties":{"city":{"type":"string"}},
   "required":["city"]
 }}
```

The optional `"read_only": true` field declares the tool side-effect
free (the MCP `readOnlyHint` analog). Annotated tools are admitted in
the `plan` approval mode and auto-allowed in `auto-edit` (see
[permissions.md](permissions.md)); unannotated tools are treated as
mutating. Lying here only cheats your own user's policy. Old hosts
ignore the field; old extensions never send it — fully additive.

#### `host_tool_call` (protocol 3)

The reverse of `tool_call`: an extension asks the host to run one of the
**host's own** tools (read, grep, bash, an MCP tool…) and sends back a
`host_tool_result` correlated by the extension's `id`. It exists so an
extension can orchestrate host tools without a model round-trip — e.g. a
code-execution extension whose sandboxed script calls `read`/`grep`/`bash`
as functions, collapsing a multi-step pipeline into one turn.

```json
{"type":"host_tool_call","id":"c1","name":"read","args":{"path":"README.md"},"silent":true}
// → {"type":"host_tool_result","id":"c1","content":[{"type":"text","text":"…"}]}
```

The host runs the tool under the **same permission gate** a model call
uses — an extension gains reach, never authority — and refuses
extension-owned tools, so a `host_tool_call` cannot recurse back into an
extension (only built-in and MCP tools are reachable). `silent` is a hint
not to surface the call in the UI. Declare `RequireProtocol(3)`; a host
that doesn't support it answers with an error result.

#### `list_sessions` / `read_session` (protocol 3)

Read-only, project-scoped access to past session transcripts, so an
extension can index prior conversations — e.g. a session-search store
building an FTS index. `list_sessions` returns the active project's
sessions; `read_session` returns one transcript flattened to role+text
(the shape a text index wants, not the full tool-call structure).

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
it:

```go
e.Tool("worktree_list", "List worktrees.", schema, handler, ext.ReadOnly())
```

Tool names live in the same namespace as built-in tools (`read`,
`write`, `edit`, `bash`, `skill`). Conflicts are silently shadowed by
the built-in.

#### `ready`

Sentinel telling terva "all initial registrations are flushed". Send it
right after your last `register_*` frame so the host can build the
agent's tool registry without racing the registration window.

```json
{"type":"ready"}
```

#### `tool_result`

Reply to a `tool_call` from the host. `content[]` is a list of
message blocks; each block is `{"type":"text","text":"..."}` or
`{"type":"image","mime_type":"image/png","data":"<base64>"}`. Set
`is_error: true` to mark the call as failed.

```json
{"type":"tool_result","id":"...",
 "content":[{"type":"text","text":"Berlin: 16°C, fog"}]}
```

#### `subscribe`

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
`transcript_compacted`. (The host advertises the exact set it emits in
`hello_ack.supported_events`; subscribing to a name an older host doesn't
emit is harmless — it simply never fires.)

`run_end` fires once when the agent finishes a whole prompt — every step,
tool loop, and the at-close gate done. It's the per-prompt bookend to
`user_message`, distinct from the per-*step* `turn_end` (which fires
repeatedly inside a tool loop). Use it to act when the agent goes idle:
summarize the exchange, run a post-turn check, or flush state. The Go SDK
exposes it as `OnRunEnd`.

`compact_start` fires when the host is *about to* compact the transcript —
the pre-event paired with `transcript_compacted` (post). The `text` field
carries a short human-readable reason. Because compaction runs a slow LLM
summarization, a handler has time to read the full session (`read_session`)
and harvest detail before it's summarized away — the window the post-event
misses. The Go SDK exposes it as `OnCompactStart`.

`user_message` fires for every genuine user prompt — the initial submit
and any queued follow-ups — the symmetric counterpart to
`assistant_message`. Use it to harvest intent for a memory store or feed
a session index. The host's synthetic at-close gate nudge is **not**
delivered (it's a host re-prompt, not the user's words). The Go SDK
exposes it as `OnUserMessage`.

`workspace_changed` fires once at the end of each agent run with the net
set of files the turn touched, in a `files` array of
`{"path":"...","change":"added|modified|deleted"}` (workspace-relative,
slash-separated paths, sorted). A run that changed nothing fires no event.
The host derives it by diffing the workspace at run boundaries — honoring
`.gitignore` and pruning `.git` — so it catches `bash` side effects and
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
memory extension re-injects its notes here via `refresh_context` — the
same thing it does on `session_start`. It's a fire-and-forget signal,
purely additive and opt-in: subscribe to receive it, and a host too old
to emit it simply never fires it (your extension keeps its
session-boundary refresh). The Go SDK exposes it as `OnCompaction`.

`session_start` (protocol 2+) carries the active session's identity —
`session_id`, `session_path`, `session_title` — plus `cwd` and
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

#### `event_intercept_response`

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

#### `command_response` (reply to `command_invoked`)

```json
{"type":"command_response","id":"...","action":"prompt",
 "prompt":"Show today's weather for Berlin in one line."}
```

`action` is one of:

- `"prompt"` — submits `prompt` as a fresh user message; the agent
  runs a turn against it.
- `"insert"` — inserts `insert` into the editor at the cursor without
  submitting.
- `"display"` — appends `display` to the chat as a one-shot styled
  note. No model call, nothing written to the transcript.
- `"open_panel"` — opens an extension-owned interactive panel inside
  terva. The panel content lives in `open_panel`.
- `"noop"` — the extension handled it itself (e.g. it pushed
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

#### `panel_render` (one-way, while a panel is open)

Pushes a fresh frame for an already-open panel.

```json
{"type":"panel_render","panel_id":"todos-main",
 "title":"Todos",
 "lines":["□ ship panel api","✓ persist state"],
 "footer":"↑/↓ navigate - a add - x complete - esc close"}
```

#### `panel_close`

Closes a previously-open panel.

```json
{"type":"panel_close","panel_id":"todos-main"}
```

#### `notify` (one-way, any time)

```json
{"type":"notify","level":"info",
 "message":"refreshed cache (12 entries)"}
```

`level` is one of `info`, `success`, `warn`, `error`. The note shows
up below the transcript with the extension's name in brackets. Notes
are one-shot: they clear automatically when the user sends their next
prompt (and on `esc` / `/clear`).

#### `clear_notes` (one-way, any time)

Removes every note this extension previously pushed via `notify` /
`display`. Use it for transient status lines (e.g. an approval prompt)
so they do not stack up; notes from other extensions are untouched.

```json
{"type":"clear_notes"}
```

In `--mode rpc`, this surfaces to the host as an `ext_clear_notes`
event (alongside `ext_notify` / `ext_display`).

#### `submit_slash` (one-way, any time)

Submits a slash command to the host's TUI as if the user had typed it.
Typically emitted from a `panel_key` handler — e.g. Enter on a selected
row to switch the host with `/cd <path>`. `text` must start with `/`.

```json
{"type":"submit_slash","text":"/cd /repo/.worktrees/feature-x"}
```

Interactive-mode only: the host ignores it in `-p` / `--json` / `rpc`
(no TUI to submit into). Reserved for opt-in extensions that the user
has installed and trusts — it lets an extension drive any host command,
so it is not something a casual extension should reach for. From the Go
SDK this is `e.SubmitSlash("/cd " + path)`.

#### `shutdown_ack`

Sent in response to `shutdown`. Extension should exit promptly after.

### Host → extension

#### `hello_ack`

```json
{"type":"hello_ack","protocol_version":2,
 "terva_version":"0.0.7","provider":"anthropic",
 "model":"claude-opus-4-7","cwd":"/Users/pat/Developer/terva",
 "extension_dir":"/Users/pat/Developer/terva/.terva/extensions/todos",
 "data_dir":"/Users/pat/.terva/ext-data/todos",
 "supported_events":["session_start","turn_start","turn_end","tool_call",
   "tool_result","assistant_message","transcript_compacted"]}
```

Sent immediately after `hello`. The extension can use these fields to
decide which commands to register (e.g. only register a Python tool
on macOS, only register a model-specific shortcut for opus, etc.).

`supported_events` lists the lifecycle events this host can emit — a
finer-grained capability signal than `protocol_version`. Use it to adapt
or warn (the Go SDK exposes `Host().Emits("transcript_compacted")`); it's
**absent on an older host** that doesn't advertise, which you should read
as "unknown" and handle by subscribing optimistically and degrading if
the event never fires, rather than gating on it.

`extension_dir` is the **read-only install dir** — the extension's code
and any defaults/assets it ships. `data_dir` is the **writable state
dir**, `$TERVA_HOME/ext-data/<name>`, kept separate so a read-only or
system install still works and code never mixes with data. Persist your
state (e.g. `todos.json`, caches, scoped auth tokens) under `data_dir`.

> **Note:** `data_dir` used to alias the install dir. It now points at
> the separate `ext-data` location. The Go SDK's `Host().DataFS()` layers
> `data_dir` over `extension_dir` (read-through, copy-on-write), so a file
> written under the old location is still read until it's next written —
> a no-flag-day migration. Use `DataFS` for both "ship a default, let the
> user override" and reading legacy state. For per-project state, use
> `Host().ProjectDataDir()` (`data_dir/projects/<project_id>`, scoped by
> the `project_id` on `session_start`).

#### `command_invoked`

```json
{"type":"command_invoked","id":"...",
 "name":"weather","args":"berlin"}
```

`args` is everything the user typed after the command name, trimmed.

#### `tool_call`

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

#### `event`

Lifecycle notification for events the extension subscribed to via
`subscribe`. One-way — no response expected.

```json
{"type":"event","event":"turn_start","step":1}
{"type":"event","event":"tool_call",
 "tool_id":"...","tool_name":"read","tool_args":{"path":"foo.go"}}
{"type":"event","event":"turn_end","stop":"end_turn"}
```

#### `event_intercept`

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

#### `panel_key`

Sent while an extension-owned panel is focused. `key` is a normalized
name (`up`, `down`, `left`, `right`, `enter`, `esc`, `tab`, `pageup`,
`pagedown`, `home`, `end`, `backspace`, `delete`, `rune`). For
`key:"rune"`, `text` carries the typed character.

```json
{"type":"panel_key","panel_id":"todos-main","key":"down"}
{"type":"panel_key","panel_id":"todos-main","key":"rune","text":"x"}
```

#### `panel_close`

Sent when the user closes the focused panel from terva (for example with
Esc or Ctrl+C). The extension should treat this as the panel lifetime
ending and stop sending `panel_render` updates for that `panel_id`.

```json
{"type":"panel_close","panel_id":"todos-main"}
```

#### `shutdown`

Sent during graceful terva exit (or `/reload-ext` once that lands).
Reply with `shutdown_ack` and then exit.

## Managing extensions from the CLI

```
terva ext list                    list installed extensions and their state
terva ext install <path|git-url>  copy / clone into $TERVA_HOME/extensions/
terva ext upgrade <name>...       fast-forward-pull an installed extension's git checkout
terva ext remove <name>           delete an extension directory
terva ext enable <name>           re-enable a disabled extension
terva ext disable <name>          disable without removing
terva ext logs <name> [-f]        cat / tail the extension's stderr
```

`terva ext install <path>` does a recursive copy; `<git-url>` does a
shallow clone. Both validate that the destination contains an
`extension.json` and roll back if not.

### Disabling extensions by config (per user or project)

`terva ext disable <name>` is a global toggle (it flips the manifest).
For policy that travels with a directory, `config.json` has two
restrict-only lists, by extension name:

```json
{
  "disable_extensions": ["terva-tasks"],
  "disable_context_extensions": ["noisy-ext"]
}
```

- **`disable_extensions`** — the extension is **never loaded**: not
  spawned, no tools/commands/panels/context. The strong "I don't want
  this running here" switch.
- **`disable_context_extensions`** — the extension loads normally, but
  its **model-context** contributions (`register_context` / cards) are
  suppressed; tools, commands, panels, and status still work.

A project's `.terva/config.json` may **add** to either list but never
remove from it (restrict-only union with the user layer): a cloned repo
can keep an extension from running in its directory, but can never make
one run that the user didn't install, nor re-enable one the user
disabled. Both compose with the manifest toggle and with `--ext` — any
one of them disabling wins.

## Loading an extension for one run

For iteration on a working copy, skip the install + reload cycle
and load straight from disk for one terva session:

```
terva --ext ./my-extension        # short form: -e ./my-extension
terva --ext ./a -e ./b            # repeatable
```

`--ext` paths take precedence over installed extensions of the same
name, so you can shadow an installed copy with a work-in-progress
version without uninstalling first. Nothing is copied or persisted;
the extension dies with terva like any other subprocess.

## SDKs

Writing the wire protocol by hand is fine for one-off scripts, but
for anything bigger the SDKs handle the boilerplate.

### Go — `packages/agent/ext`

```go
package main

import (
    "encoding/json"
    "terva.sh/terva/packages/agent/ext"
)

func main() {
    e := ext.New("hello", "1.0.0")

    // Slash command
    e.Command("hello", "say hi", func(args string) ext.Response {
        return ext.Prompt("Greet me in one short sentence.")
    })

    // LLM-callable tool
    e.Tool("weather", "Current weather for a city.",
        json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
        func(args json.RawMessage) ext.ToolResult {
            var in struct{ City string `json:"city"` }
            json.Unmarshal(args, &in)
            return ext.TextResult(in.City + ": sunny")
        })

    e.Run()
}
```

Build with `go build -o hello .`, drop the binary + an `extension.json`
into `$TERVA_HOME/extensions/hello/`.

The SDK has four interceptor hooks, all optional:

```go
// e is the *ext.Extension returned by ext.New(...).

// Refuse calls or rewrite args before they run.
e.InterceptToolCall(func(tool string, args json.RawMessage) (bool, string) {
    if tool == "bash" { /* inspect args, return false, reason */ }
    return true, ""
})

// Richer variant: returns ToolCallDecision so you can also rewrite
// args via ModifiedArgs.
e.InterceptToolCallX(func(tool string, args json.RawMessage) ext.ToolCallDecision {
    return ext.ToolCallDecision{
        ModifiedArgs: json.RawMessage(`{"command":"echo GUARDED"}`),
    }
})

// Block the next turn before the model is called.
e.InterceptTurnStart(func(step int) ext.TurnStartDecision {
    if time.Now().Hour() < 9 { return ext.TurnStartDecision{Block: true, Reason: "outside business hours"} }
    return ext.TurnStartDecision{}
})

// Scrub or rewrite the assistant's final text before the user sees it.
e.InterceptAssistantMessage(func(text string) ext.AssistantMessageDecision {
    return ext.AssistantMessageDecision{
        ReplaceText: strings.ReplaceAll(text, "SECRET", "[redacted]"),
    }
})
```

See:
- `examples/extensions/hello/` — slash commands
- `examples/extensions/clock/` — slash commands in plain Node, no SDK
- `examples/extensions/weather/` — LLM-callable tool
- `examples/extensions/guard/` — event subscriptions + tool-call
  interception (refuses dangerous bash patterns)
- `examples/extensions/todo/` — interactive persistent panel + tool
- `examples/extensions/scratchpad/` — source-run TypeScript commands + tool

### Hot reload

Type `/reload-ext` in the TUI to tear down every running extension
subprocess, re-read the manifests from disk, and respawn the set.
The agent's tool registry is rebuilt automatically, so freshly-
registered extension tools become callable without restarting terva.
Useful while developing an extension: edit, save, `/reload-ext`,
done. Explicit `--ext` paths are remembered and reloaded alongside
discovered extensions.

### TypeScript / Python

These SDKs aren't in the main repo yet; the wire format is small
enough that a `~30 line` raw script gets you started in either
language. See the [Quick start](#quick-start) Python example for the
shape. SDK packages will land in follow-up commits.

## Security

Extensions run with **the user's full filesystem and network
permissions**. Treat installing an extension the same as installing
any other binary on your machine.

`terva ext install <git-url>` clones from any URL you give it. There's
no sandbox in v1; if you need isolation, install only extensions you
trust or run terva under your platform's sandboxing tool (`bwrap` /
`sandbox-exec` / AppContainer).

## Roadmap

Phase 1 (shipped):
- [x] subprocess lifecycle + hello handshake
- [x] `register_command` + `command_invoked`
- [x] `notify` + `clear_notes`
- [x] `terva ext` CLI

Phase 2 (shipped):
- [x] `register_tool` + `tool_call` + `tool_result`
- [x] `ready` sentinel for safe agent-registry build timing
- [x] tool result attribution surfaces extension name in details

Phase 3 (shipped):
- [x] event subscriptions (`session_start`, `turn_start`, `turn_end`,
      `tool_call`, `assistant_message`)
- [x] tool-call interception (block before execution)

Phase 4 (shipped):
- [x] interception for `turn_start` and `assistant_message` (in
      addition to `tool_call`)
- [x] modify tool args mid-flight via `modified_args`
- [x] rewrite user-visible assistant text via `replace_text`
- [x] `/reload-ext` slash command (hot-reload without restarting terva)

Future (no firm timeline):
- [ ] TypeScript and Python SDK packages (currently the wire format
      is stable enough to hand-roll, see the Python quick-start)
- [ ] HTTP / WebSocket transport variants (today: subprocess stdio)
- [ ] per-extension permission scopes (today: full user privileges)

## Installing and managing extensions

```bash
terva ext install <path|git-url>   # copy / clone into $TERVA_HOME/extensions/
terva ext list                      # show installed extensions
terva ext logs <name> [-f]          # cat or tail the extension's stderr log
terva ext enable <name>             # re-enable a disabled extension
terva ext disable <name>            # disable without removing
terva ext upgrade <name>...         # fast-forward-pull just these extensions
terva ext remove <name>             # delete an extension directory
```

For development, point `terva --ext <path>` at a working directory and skip the install step entirely. Repeatable; takes precedence over installed extensions of the same name.

### Updating extensions

`terva ext upgrade <name>...` upgrades just the named extensions, and
`terva update` refreshes the terva binary **and** every installed
extension at once. Both run the same per-extension logic (below) — `ext
upgrade` is the targeted form when you only want to bump one or two.
Per-extension behaviour:

- Disabled extensions are skipped.
- Extensions without a `.git/` directory (installed by `terva ext install ./local-path`) are skipped — there is no remote to pull from.
- For the rest, terva stashes any dirty worktree state (including untracked runtime files like `todos.json` or `config.json`), runs `git pull --ff-only`, and pops the stash. If the pop produces conflicts, the conflict markers are left in place and you'll see a warning.
- Diverged branches, offline pulls, or any other git failure are reported as `failed` and the next extension is processed. `terva update` itself never aborts because of an extension.
- terva does **not** run any build step (`go build`, `npm install`, `make`) after the pull — building stays the extension's job. The recommended way to handle this is a [self-bootstrapping launcher](#recommended-a-self-bootstrapping-launcher): `terva update` pulls new source, and the next launch (or `/reload-ext`) rebuilds automatically because the sources are now newer than the binary. An extension that instead commits a prebuilt artifact (binary, transpiled JS) just keeps working from the pulled copy. Either way, if you need to force a rebuild now, do it manually and `/reload-ext`.

### Theme-only extensions

An extension may ship only a theme: `extension.json` plus `theme.json` (or `themes/theme.json`) and no executable. terva loads it without spawning a subprocess and shows it in `/settings` with source information. See [docs/themes.md](themes.md).
