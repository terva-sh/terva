# MCP servers

terva can attach [Model Context Protocol](https://modelcontextprotocol.io)
servers as tool providers: each configured server is spawned as a
subprocess speaking newline-delimited JSON-RPC over stdio, its tools
join the registry under namespaced names, and everything downstream —
the [permission ladder](permissions.md), [hooks](hooks.md), plan
mode's read-only promise — applies to them like any other tool.

Scope, deliberately: **stdio transport, tools only.** Resources,
prompts, sampling, and HTTP transports can arrive later behind the
same seam; SSE is skipped for good (the ecosystem deprecated it).

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
      }
    }
  }
}
```

- `command` / `args` — the server process; bare names resolve via `PATH`.
- `env` — extra variables for the child. The base environment is the
  sanitized one every terva subprocess gets (loader/interpreter
  injection vars stripped — see [extensions.md](extensions.md)), and
  config `env` cannot re-introduce those keys or override `PATH`.
- `timeout_ms` — per `tools/call` (default 60s).

`--no-mcp` skips all servers for one run. `--mcp git,jira` is the
narrowing form: only the listed servers start (restrict-only — config
disables still subtract, and the `/mcp` dialog cannot live-enable an
excluded server for the run). Scope headless agents to what they need:
a chat bot exposed to a group room shouldn't carry every server your
TUI sessions use.

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
