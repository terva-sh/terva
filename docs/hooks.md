# Hooks

Hooks run your own programs at tool-call boundaries. They are the
zero-protocol extension point: any executable that reads one JSON
object on stdin — a ten-line shell script qualifies — can observe,
veto, or rewrite tool calls without speaking the
[extension protocol](extensions.md).

**Trust boundary:** your **user config** (`$TERVA_HOME/config.json`)
always defines hooks. A project's `.terva/config.json` may define them
too, but only when the workspace is **trusted** — a hook is arbitrary
code execution at a tool-call boundary, so an untrusted (the default)
cloned repository's hooks are never loaded: the engine sees only yours.
Trust the directory (`terva trust`, or `--trust` for a single run) and
the project's hooks are **appended** to yours: both sets fire, user
hooks first within each phase. It is a union, never a replacement, so a
repo cannot displace a hook you rely on — and because the first
allow/deny stops the chain (see below), your hooks get the first say.
See `docs/plans/workspace-trust.md`.

## Configuration

```json
{
  "hooks": {
    "pre_tool_use": [
      { "command": "/home/me/hooks/guard.sh", "tools": "bash" },
      { "command": "terva-audit", "args": ["--log", "/tmp/audit"], "timeout_ms": 3000 }
    ],
    "post_tool_use": [
      { "command": "/home/me/hooks/after-edit.sh", "tools": "edit" }
    ]
  }
}
```

- `command` — the executable; bare names resolve via `PATH`.
- `args` — fixed arguments, placed before the JSON-on-stdin event.
- `tools` — exact tool name or prefix glob (`mcp_*`); empty = all.
- `timeout_ms` — per invocation; defaults 10s (pre) / 30s (post).

Hook subprocesses start from the same sanitized environment as
extensions (loader/interpreter injection vars stripped — see
[extensions.md](extensions.md)) and run in the session cwd.

## Pre-tool-use

Stdin:

```json
{"event":"pre_tool_use","tool":"bash","args":{"command":"rm -rf build"},"cwd":"/work/repo"}
```

Respond on stdout (or stay silent for "no opinion"):

```json
{"decision":"deny","reason":"use just clean instead"}
```

- `decision: "deny"` — refuse the call; `reason` is shown to the
  model. **Exit status 2 is the shell-ergonomic spelling of deny**:
  stderr becomes the reason. Every other failure (other non-zero
  exits, timeouts, invalid JSON) is logged to
  `$TERVA_HOME/logs/hooks.log` and treated as *no opinion* — a broken
  hook never blocks a session.
- `decision: "allow"` — final: the call runs, skipping the
  [confirm gate](permissions.md) entirely.
- `decision: "ask"` — defer to the confirm gate even if your hook ran
  in a mode that would auto-allow. In a gateless pure-yolo session
  this is a no-op; use deny for enforcement.
- `updated_args` — replaces the call's arguments for everything
  downstream: the gate evaluates the rewritten args, extensions and
  the tool see them. Rewrites accumulate across the chain.

Hooks run in config order; the first allow/deny stops the chain.

The full ladder for every tool call, in every mode:

```
pre-tool-use hooks  →  confirm gate (mode + rules)  →  extension intercept  →  execute
```

## Post-tool-use

Fire-and-forget after a tool finishes; stdout is ignored:

```json
{"event":"post_tool_use","tool":"edit","args":{"path":"main.go","edits":[...]},"is_error":false,"cwd":"/work/repo"}
```

Use it for audit trails, linting after edits, notifications. Post
hooks run concurrently with the session (they never delay the next
turn).

## Example: lint after every edit

```sh
#!/bin/sh
# ~/.hooks/lint-after-edit.sh — post_tool_use, tools: "edit"
payload=$(cat)
path=$(printf '%s' "$payload" | sed -n 's/.*"path":"\([^"]*\)".*/\1/p')
case "$path" in *.go) gofmt -w "$path" 2>>/tmp/lint.log ;; esac
```

## Example: refuse force-pushes

```sh
#!/bin/sh
# pre_tool_use, tools: "bash"
if grep -q 'push --force\|push -f' /dev/stdin; then
  echo "force-push needs a human" >&2
  exit 2
fi
```
