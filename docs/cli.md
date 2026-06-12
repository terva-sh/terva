# terva CLI reference

Flags, tools, run modes, and the data directory. The interactive
TUI's own surface (slash commands, keys) lives in [tui.md](tui.md).

## Flags

| Flag | Description |
|---|---|
| `--provider <id>` | Pick the provider (for example `anthropic`, `openai`, `openai-codex`, `kimi`, `google`, `github-copilot`, `groq`, `openrouter`, `amazon-bedrock`, `ollama`, `openai-compatible`; see [models.md](models.md)). |
| `--model <id>` | Pick the model (see `--list-models`). |
| `--api-key <key>` | Override the API key. |
| `--base-url <url>` | Override the provider base URL (tests, self-hosted). |
| `--system-prompt <text>` | Replace the default system prompt for this run (also overrides `$TERVA_HOME/SYSTEM.md`). |
| `--append-system-prompt <text>` | Append text to the system prompt (repeatable). |
| `--context-file <path>` | Inject a file's contents into the system prompt (repeatable). |
| `--reasoning off\|minimum\|low\|medium\|high\|maximum` | Set thinking level on supported models (default: off). |
| `-c`, `--continue` | Resume the latest session for this cwd. |
| `-r`, `--resume` | Pick a session to resume. |
| `--session <path>` | Resume a specific session file. |
| `--no-session` | Don't read or write session files. |
| `--cwd <path>` | Use `<path>` as the working directory. |
| `--no-tools` | Disable all tools. |
| `--tools <csv>` | Only enable the listed tools. |
| `--max-steps <n>` | Cap agent loop iterations (default: unlimited; pass `0` for unlimited). |
| `-e`, `--ext <path>` | Load an extension from `<path>` for this run (repeatable; wins against installed extensions of the same name). |
| `--no-ext` | Skip extension discovery for this run. `--ext` still works on top, so `--no-ext --ext ./x` runs only `x`. |
| `--no-skill` | Disable all skills, including built-ins. No `skill` tool is registered and the system prompt has no skill manifest. |
| `--no-yolo` | Confirm every tool call before it runs. In the interactive TUI a dialog shows the tool name and a one-line preview of its args with four choices: yes, yes-always-this-tool-this-session, yes-always-this-session, no. In print / json / rpc modes there is no prompt to confirm at, so `--no-yolo` **refuses** every tool call with a model-readable message rather than running it unconfirmed — omit the flag for unattended automation that needs tools to run. |

## Tools

- `read`: read text files, or inline images (PNG, JPEG, GIF, WebP).
- `write`: create or overwrite files, making parent directories as needed.
- `edit`: one or more exact-match replacements in an existing file.
- `bash`: run a shell command in the session cwd, with merged stdout/stderr and a timeout.
- `terva_status`: report the agent's own runtime state — model, provider, working directory, reasoning effort, and how full the context window is. Takes no arguments.

When the sandbox is on (see `/jail` in [tui.md](tui.md)), the four file/command tools (`read`, `write`, `edit`, `bash`) refuse paths outside the session cwd. `terva_status` touches no paths.

### terva_status

`terva_status` lets the model introspect its own session. None of this is otherwise visible to it: the system prompt carries only the date and cwd, and context usage is computed by the harness after each turn and never surfaced. With the tool, the model can check how full its context is — and decide to summarize or wrap up — or report which model and provider it's actually running as.

A call returns the provider, model, auth method, working directory, reasoning effort, the context window and how much of it the last turn used (as a percentage), and the cumulative session token/cost totals. Context usage reflects the **most recent completed turn**, so it approximates the current size rather than giving an exact mid-turn count.

The model is nudged toward the tool by a one-line hint in the default system prompt; the hint (and the tool) are omitted when `--no-tools`, or a `--tools` allowlist that excludes `terva_status`, is in effect.

## Modes

- **Interactive** (default): chat TUI with streaming output, spinner, cost meter, slash commands.
- **Print**: `terva -p "prompt"` runs the agent to completion and writes only the final assistant text to stdout.
- **JSON**: `terva --json "prompt"` emits one JSON object per agent event to stdout, newline-delimited. The schema is documented in [docs/rpc.md](rpc.md).
- **RPC**: `terva rpc` runs as a long-lived child process; commands in on stdin, events and responses out on stdout, both as NDJSON. Designed for embedding terva in third-party apps written in any language. See [docs/rpc.md](rpc.md) for the wire schema and `examples/rpc/{python,node,shell,go}` for working clients.

## Embedding

Two ways to drive terva from another program:

- **Go in-process**: import `terva.sh/terva/packages/agent/sdk`. One `Runtime` per project; `Prompt(ctx, text, images)` returns a channel of `Event`. Small example in `examples/sdk/`.
- **Any language, out-of-process**: spawn `terva rpc` as a subprocess and exchange newline-delimited JSON over its stdin/stdout. Wire format and event schema in [docs/rpc.md](rpc.md). Reference clients live under `examples/rpc/`.

Both interfaces share the same event schema, so transcripts captured by one can be replayed through the other.
## Data directory

All data lives under `$TERVA_HOME`:

```
$TERVA_HOME/
├── config.json         # last-used provider/model/theme, saved automatically
├── auth.json           # api keys and oauth tokens (mode 0600)
├── sessions/           # jsonl transcripts, one dir per cwd
├── models-cache.json   # live /v1/models discovery cache (6h ttl)
├── SYSTEM.md           # optional: replaces the default system prompt
├── skills/             # optional: user SKILL.md files
├── themes/             # optional: user theme JSON files
├── extensions/         # installed extensions, one dir per extension
└── logs/               # app log files
```

Drop a `SYSTEM.md` in `$TERVA_HOME` to replace the built-in identity and guidelines for every run. `--system-prompt` still wins per-invocation. Delete the file to revert to the default.
