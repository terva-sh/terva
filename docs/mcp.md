# MCP servers

terva can attach [Model Context Protocol](https://modelcontextprotocol.io)
servers as tool providers. A server is reached one of two ways — a **stdio**
subprocess terva spawns (newline-delimited JSON-RPC), or a remote **http**
endpoint terva POSTs to (Streamable HTTP) — and either way its tools join the
registry under namespaced names, and everything downstream — the
[permission ladder](permissions.md), [hooks](hooks.md), plan mode's read-only
promise — applies to them like any other tool.

Scope, deliberately: **tools only.** Resources, prompts, sampling, and
server-initiated messages can arrive later behind the same seam. On SSE: the
*deprecated* transport is the 2024-era standalone HTTP+SSE (two endpoints),
which terva never implements; its replacement, **Streamable HTTP** (single
endpoint, a POST response that may upgrade to an SSE stream), is the live
remote-MCP wire and is exactly what the `http` transport speaks. The http
transport is build-tagged (default on); a `terva-min` / `terva_no_mcp_http`
build drops it and an `http` server then fails cleanly with "not compiled in"
(stdio servers are unaffected).

**Trust boundary:** the gate is **Workspace Trust**, not the config
layer. The user config (`$TERVA_HOME/config.json`) is trusted, so its
servers always start. A project's `.terva/config.json` may declare
`mcp.servers` too, but they are spawned **only in a trusted workspace**
(`terva trust`, or `--trust` for one run) — an untrusted cloned repo's
servers never start, so a clone alone still can't execute arbitrary
commands at session start. When the workspace is trusted the two sets
merge and the **user wins on a name collision**: a project may only *add*
servers, never shadow one you defined. (Hooks stay stricter — user config
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
- `command` / `args` — the server process; bare names resolve via `PATH`.
- `env` — extra variables for the child. The base environment is the
  sanitized one every terva subprocess gets (loader/interpreter
  injection vars stripped — see [extensions.md](extensions.md)), and
  config `env` cannot re-introduce those keys or override `PATH`.

**http** (remote, Streamable HTTP):
- `transport: "http"` and `url` — the remote endpoint (both required; an http
  server must not also set `command`).
- `headers` — sent on every request. Values may reference `${ENV}`; a
  referenced-but-unset variable fails the server's startup rather than sending a
  broken request.
- `auth.bearer_env` — names an env var whose value rides as
  `Authorization: Bearer <value>`. **Tokens are never inlined in config** — they
  live in the environment, the same posture as provider keys — so a shared or
  project config can't ship a secret (and can't point your token at a URL of its
  choosing unless that env is already set).

Common to both:
- `timeout_ms` — per `tools/call` (default 60s).

`--no-mcp` skips all servers for one run. `--mcp git,jira` is the
narrowing form: only the listed servers start (restrict-only — config
disables still subtract, and the `/mcp` dialog cannot live-enable an
excluded server for the run). Scope headless agents to what they need:
a chat bot exposed to a group room shouldn't carry every server your
TUI sessions use.

## OAuth servers — `terva-mcp-bridge`

The in-core `http` transport handles **static auth** (a bearer token or fixed
headers). Hosted SaaS MCP servers (Linear, Notion, GitHub remote, …) instead
require **OAuth 2.1** — a browser sign-in, a localhost redirect capture, and token
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
bridge owns the HTTP upstream, this works **even in a `terva_no_mcp_http` build** —
core needs no HTTP transport to reach a remote server through the bridge.

**One-time setup** — authorize in a browser, which stores a refresh token:

```
terva-mcp-bridge login https://mcp.linear.app/mcp
```

Discovery (RFC 9728 / RFC 8414), dynamic client registration (RFC 7591), and the
authorization-code + PKCE flow are automatic. After that, the terva-spawned relay
refreshes tokens transparently — no further prompts. Install it with
`go install ./cmd/terva-mcp-bridge` (an opt-in auxiliary binary, like the chat
connectors; it is **not** a chat connector — its wire to terva is plain MCP stdio).

- Tokens persist under `$TERVA_HOME/mcp-bridge/<host>/tokens.json` at `0600`; the
  bridge owns them. `--client-id ID` uses a pre-provisioned client (skips dynamic
  registration).
- `--bearer-env VAR` makes the bridge a static-auth relay too (no OAuth), so one
  mechanism can cover every remote server if you prefer; in-core `http` stays the
  lighter path for static auth.

## Behavior

- Tools register as `mcp_<server>_<tool>` (non-alphanumerics become
  `_`), so origins stay visible in transcripts and permission rules
  can target `mcp_*`, `mcp_github_*`, or one tool exactly.
- Name collisions: built-in and extension tools win; among MCP
  servers, alphabetical server order registers first and later
  duplicates are shadowed with a note.
- A server that fails to start (or dies) is a stderr note and its
  tools are absent — never a fatal error. Its stderr streams to
  `$TERVA_HOME/logs/mcp-<name>.log`.
- Text and image result content map to the transcript natively;
  other content kinds degrade to their JSON so nothing is silently
  dropped.
- In `plan` approval mode MCP tools (unknown side effects) are not
  offered at all.

## Enabling and disabling servers (`/mcp`)

Run `/mcp` in the interactive TUI to see every configured server, its
scope (`global` for a user-config server, `project` for a trusted
project's), whether it's connected, its tool count, and any startup
error. Two toggles, mirroring `/extensions`:

- `g` — enable/disable **globally** (user config). Disabling adds the
  server name to `disable_mcp` in `$TERVA_HOME/config.json`; enabling
  removes it. There is no per-server manifest, so "enabled" simply means
  the name is absent from `disable_mcp`. Only meaningful for a
  user-defined server (a project-defined one has nothing to toggle here —
  use `p`).
- `p` — enable/disable **for this project** (`.terva/config.json`
  `disable_mcp`). Restrict-only: it can stop a server from running in
  this directory, but can never start one the user disabled. Honored even
  in an untrusted workspace — refusing to spawn is always safe.
- `l` — open a scrollable view of that server's log
  (`$TERVA_HOME/logs/mcp-<name>.log`) without leaving the TUI. A server
  that failed to start also shows a one-line reason inline.

Toggles apply **live**: enabling spawns the server and its `mcp_*` tools
appear on the next turn; disabling stops the subprocess and removes its
tools — no restart. `disable_mcp` is also honored headlessly and under
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
