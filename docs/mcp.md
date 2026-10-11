# MCP servers

terva can attach [Model Context Protocol](https://modelcontextprotocol.io)
servers as tool providers. A server is reached one of two ways: a **stdio**
subprocess terva spawns (newline-delimited JSON-RPC), or a remote **http**
endpoint terva POSTs to (Streamable HTTP). Either way its tools join the
registry under namespaced names, and everything downstream (the
[permission ladder](permissions.md), [hooks](hooks.md), plan mode's read-only
promise) applies to them like any other tool.

Scope, deliberately: **tools only.** Resources, prompts, sampling, and the
standalone stream for server-initiated messages can arrive later behind the
same seam. On SSE: the
*deprecated* transport is the 2024-era standalone HTTP+SSE (two endpoints),
which terva never implements; its replacement, **Streamable HTTP** (single
endpoint, a POST response that may upgrade to an SSE stream), is the live
remote-MCP wire and is exactly what the `http` transport speaks. The http
transport is build-tagged (default on); a `terva-min` / `terva_no_mcp_http`
build drops it and an `http` server then fails cleanly with "not compiled in"
(stdio servers are unaffected).

A server request still gets an answer. terva answers `ping` with an empty
result and every other method with JSON-RPC error `-32601` (method not found).
It ignores notifications. A server that stops reading its input while it sends
requests loses replies, because terva keeps reading the server's output rather
than wait on the server. Over `http`, terva reads
server requests from the SSE stream of a POST and sends each reply as another
POST. terva does not open the standalone GET stream, so it never sees a request
that a server sends only there.

**Trust boundary:** the gate is **Workspace Trust**, not the config
layer. The user config (`$TERVA_HOME/config.json`) is trusted, so its
servers always start. A project's `.terva/config.json` may declare
`mcp.servers` too, but they are spawned **only in a trusted workspace**
(`terva trust`, or `--trust` for one run): an untrusted cloned repo's
servers never start, so a clone alone still can't execute arbitrary
commands at session start. When the workspace is trusted the two sets
merge and the **user wins on a name collision**: a project may only *add*
servers, never shadow one you defined. (Hooks stay stricter: user config
only, no project surface at all.)

## Configuration

```json
{
  "mcp": {
    "servers": {
      "github": {
        "command": "github-mcp-server",
        "args": ["stdio"],
        "env": { "GITHUB_PERSONAL_ACCESS_TOKEN": "ghp_..." }
      },
      "filesystem": {
        "command": "npx",
        "args": ["-y", "@modelcontextprotocol/server-filesystem", "/data"],
        "timeout_ms": 30000
      },
      "internal-tools": {
        "transport": "http",
        "url": "https://mcp.internal.example/v1/mcp",
        "headers": { "X-Workspace": "${WORKSPACE_ID}" },
        "auth": { "bearer_env": "INTERNAL_MCP_TOKEN" }
      }
    }
  }
}
```

**stdio** (the default):
- `command` / `args`: the server process; bare names resolve via `PATH`.
- `env`: extra variables for the child. The base environment is the
  sanitized one every terva subprocess gets (loader/interpreter
  injection vars stripped; see [extensions.md](extensions.md)), and
  config `env` cannot re-introduce those keys or override `PATH`.
- Shutdown: terva closes the server's stdin and waits up to two seconds for
  the server and every process in its group to exit. If any remain, terva asks
  the group to stop, and kills the group one second later if any still remain.
  The server leads that group, so the real server behind a
  launcher such as `npx -y` or `uvx` stops with the launcher. On Windows terva
  can see the launcher alone. It stops the wait when the launcher exits, and
  the final kill ends the launcher alone, so a child that outlives the launcher
  keeps running. At exit terva stops every server at once and waits at most
  five seconds in total.
- Because each server leads its own process group, the terminal's `SIGINT`
  and `SIGHUP` do not reach it. If terva dies without its shutdown (`SIGKILL`,
  a crash), each server sees its stdin close, and a server that ignores that
  EOF keeps running. Commands from the `bash` tool and extensions behave the
  same way.

**http** (remote, Streamable HTTP):
- `transport: "http"` and `url`: the remote endpoint (both required; an http
  server must not also set `command`).
- `headers`: sent on every request. Values may reference `${ENV}`; a
  referenced-but-unset variable fails the server's startup rather than sending a
  broken request. **Put a credential in `auth.bearer_env`, not here.** If the
  server answers with a redirect to another host, the egress guard strips
  `Authorization` and nothing else, so a secret in a custom header rides the hop
  to that new host. Reading `${ENV}` keeps the value out of the config file. It
  does not protect the value in transit. After initialize, terva adds two
  headers of its own to every request: `Mcp-Session-Id` when the server
  assigned a session, and `MCP-Protocol-Version` with the version the server
  negotiated. A configured header of either name does not replace them.
- `auth.bearer_env`: names an env var whose value rides as
  `Authorization: Bearer <value>`. **Tokens are never inlined in config**: they
  live in the environment, the same posture as provider keys, so a shared or
  project config can't ship a secret (and can't point your token at a URL of its
  choosing unless that env is already set). It is also the only header the guard
  removes when a redirect crosses hosts, which is the other reason a token
  belongs here.

Common to both:
- `timeout_ms` bounds the full `tools/call`, including the send and response
  wait (default 60s).

Cancellation also covers waiting for another stdio writer. If cancellation
interrupts a pipe write, terva closes the connection and stops the server. The
server may have received part of the request, so terva does not retry it.
Restart the server from `/mcp` before using its tools again. A response timeout
after a complete write leaves the connection open; it does not undo the tool.

`--no-mcp` skips all servers for one run. `--mcp git,jira` is the
narrowing form: only the listed servers start (restrict-only: config
disables still subtract, and the `/mcp` dialog cannot live-enable an
excluded server for the run). Scope headless agents to what they need:
a chat bot exposed to a group room shouldn't carry every server your
TUI sessions use.

## OAuth servers: `terva-mcp-bridge`

The in-core `http` transport handles **static auth** (a bearer token or fixed
headers). Hosted SaaS MCP servers (Linear, Notion, GitHub remote, …) instead
require **OAuth 2.1**: a browser sign-in, a localhost redirect capture, and token
refresh. That service-shaped weight (a listening socket, a browser launch, a token
vault) is deliberately kept **out of core** and lives in a companion binary,
`terva-mcp-bridge`, which appears to terva as an ordinary **stdio** server:

```jsonc
{
  "mcp": {
    "servers": {
      "linear": {
        "command": "terva-mcp-bridge",
        "args": ["https://mcp.linear.app/mcp"]
      }
    }
  }
}
```

terva spawns it over stdio and routes `tools/list` / `tools/call` to it like any
other server; the bridge speaks Streamable HTTP + OAuth to the remote. Because the
bridge owns the HTTP upstream, this works **even in a `terva_no_mcp_http` build**, because
core needs no HTTP transport to reach a remote server through the bridge.

**One-time setup**, authorizing in a browser, which stores a refresh token:

```
terva-mcp-bridge login https://mcp.linear.app/mcp
```

Discovery (RFC 9728 / RFC 8414), dynamic client registration (RFC 7591), and the
authorization-code + PKCE flow are automatic. After that, the terva-spawned relay
refreshes tokens transparently, with no further prompts. Install it with
`go install ./cmd/terva-mcp-bridge` (an opt-in auxiliary binary, like the chat
connectors; it is **not** a chat connector, since its wire to terva is plain MCP stdio).

- Tokens persist under `$TERVA_HOME/mcp-bridge/<host>/tokens.json` at `0600`; the
  bridge owns them. `--client-id ID` uses a pre-provisioned client (skips dynamic
  registration).
- `--bearer-env VAR` makes the bridge a static-auth relay too (no OAuth), so one
  mechanism can cover every remote server if you prefer; in-core `http` stays the
  lighter path for static auth.
- After initialize, each request that the bridge sends to the remote server
  carries `MCP-Protocol-Version`. The value is the version that the remote
  server negotiated with the bridge, the same rule as the in-core `http`
  transport.
- The bridge marks itself as a relay to a remote server. Its `initialize`
  result carries `"terva.sh/remote": true` in `_meta`, and the bridge replaces
  any value that the remote server put at that key. terva then classifies the
  bridge's read-only tools as `network-read`, the same as the read-only tools
  of an in-core `http` server (see [Behavior](#behavior)). Each call reaches
  the remote server, so these tools ask in `workspace` and `auto-edit`, and
  `plan` does not offer them.
- A third-party bridge such as `mcp-remote` does not set the mark. To terva it
  is an ordinary `stdio` server, so its read-only tools count as local reads
  and run without a prompt in `workspace`, although each call reaches the
  remote server. Add an ask rule such as
  `{ "tool": "mcp_linear_*", "decision": "ask" }` to make its tools ask too.

## Behavior

- Tools register as `mcp_<server>_<tool>` (non-alphanumerics become
  `_`), so origins stay visible in transcripts and permission rules
  can target `mcp_*`, `mcp_github_*`, or one tool exactly.
- Name collisions: built-in and extension tools win; among MCP
  servers, alphabetical server order registers first and later
  duplicates are shadowed with a note.
- A server that fails to start (or dies) is a stderr note and its
  tools are absent, never a fatal error. Its stderr streams to
  `$TERVA_HOME/logs/mcp-<name>.log`.
- Text and image result content map to the transcript natively;
  other content kinds degrade to their JSON so nothing is silently
  dropped.
- The text of one result is capped at 50 KiB, the limit the built-in
  `bash` and `read` tools use. A server can otherwise return up to the
  4 MiB frame limit in one call, which is enough to fill the context
  window. Past the cap, terva keeps the first 50 KiB and writes the full
  text to a private file (mode 0600) named `terva-mcp-<hex>.log` in the
  OS temp directory. It then appends a note that names the file and where
  to continue, so the model pages the rest with `read` instead of calling
  the tool again. The note gives a line as `offset` when the cut ends a
  line, and a byte as `byte_offset` when the cut falls inside one, which
  is the usual shape of a compact JSON result. Images do not count toward
  the cap and pass through unchanged. The file never goes under
  `$TERVA_HOME/sessions`, which the agent cannot read, and terva removes it
  when the session that made the call ends.
- A tool whose description is over 4 KiB, or whose input schema is over
  16 KiB, is withdrawn. The schema is measured without whitespace, as a
  provider sends it. A server writes this text, and terva places it in the
  cached prompt prefix, where it costs every request and speaks to the
  model with the weight of a tool definition. A withdrawn tool is absent
  from the tool list and from the server's tool count, and a call to it
  fails. Each withdrawal prints a startup note, and `/mcp` shows the
  first note for the server. The server's other tools are not affected.
  terva never cuts or replaces a schema, because a cut schema no longer
  says what the server accepts.
- Classification follows the tool's annotations and the server's
  transport ([permissions.md](permissions.md#authority-classes) has the
  table). A tool with `readOnlyHint: true` on a `stdio` server counts as a
  read: it runs without a prompt in `workspace` and `auto-edit`, and `plan`
  offers it. The same tool on an `http` server is `network-read`, because
  every call sends its arguments off the machine. It asks in `workspace`
  and `auto-edit`, and `plan` does not offer it. A `stdio` server that
  marks itself as a relay to a remote server gets the `http` treatment.
  The mark is `"terva.sh/remote": true` in the `_meta` of its `initialize`
  result, and `terva-mcp-bridge` sets it. Any other value, or no value, is
  no mark. A tool that also sets `destructiveHint: true`, or sets no
  `readOnlyHint`, has unknown side effects: it asks, and `plan` does not
  offer it.

## Enabling and disabling servers (`/mcp`)

Run `/mcp` in the interactive TUI to see every configured server, its
scope (`global` for a user-config server, `project` for a trusted
project's), whether it's connected, its tool count, and any startup
error. Two toggles, mirroring `/extensions`:

- `g`: enable/disable **globally** (user config). Disabling adds the
  server name to `disable_mcp` in `$TERVA_HOME/config.json`; enabling
  removes it. There is no per-server manifest, so "enabled" simply means
  the name is absent from `disable_mcp`. Only meaningful for a
  user-defined server (a project-defined one has nothing to toggle here;
  use `p`).
- `p`: enable/disable **for this project** (`.terva/config.json`
  `disable_mcp`). Restrict-only: it can stop a server from running in
  this directory, but can never start one the user disabled. Honored even
  in an untrusted workspace, because refusing to spawn is always safe.
- `l`: open a scrollable view of that server's log
  (`$TERVA_HOME/logs/mcp-<name>.log`) without leaving the TUI. A server
  that failed to start, or that had a tool withdrawn, also shows a
  one-line reason inline.

Toggles apply **live**: enabling spawns the server and its `mcp_*` tools
appear on the next turn; disabling stops the subprocess and removes its
tools, with no restart. `disable_mcp` is also honored headlessly and under
ACP (gating only; no dialog there).

```json
{
  "disable_mcp": ["github"]
}
```

## Permissions example

Auto-allow one read-only MCP tool, force prompts for the rest:

```json
{
  "permissions": [
    { "tool": "mcp_github_search_repositories", "decision": "allow" },
    { "tool": "mcp_*", "decision": "ask" }
  ]
}
```

The same shape trusts a read-only tool on a remote `http` server, or behind
`terva-mcp-bridge`, which otherwise asks on every call. Name the one tool
rather than `mcp_<server>_*`, so a mutating tool the server adds later still
asks. An allow rule does not
bring a remote tool into `plan`, because plan refuses the call before any
rule runs.
