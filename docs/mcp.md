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

**Trust boundary:** MCP servers configure from the **user config
only** (`$TERVA_HOME/config.json`). A project's `.terva/config.json`
cannot add servers — that would let a cloned repo execute arbitrary
commands at session start. Same posture as hooks.

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

`--no-mcp` skips all servers for one run.

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
