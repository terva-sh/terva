# terva CLI reference

Flags, tools, run modes, and the data directory. The interactive
TUI's own surface (slash commands, keys) lives in [tui.md](tui.md).

## Ways to run terva

terva is one binary and one agent core. What changes between these shapes is
**where the session lives** and **how you reach it** — every front end drives
the same core over the [control plane](controllers.md) (same sessions, same
permissions, same event stream), so you can even mix them against one session.

**1. The terminal UI — `terva`** *(the default)*
Everything runs in one process bound to your terminal: the agent loop, the
tools, the session. Start it, work, exit. Sessions persist to disk (reopen with
`--continue` / `--resume`), but the *process* is your terminal — close the
terminal and the agent stops. It is the simplest, fastest way to work on one
machine, and what you get by just running `terva`. Guide: [tui.md](tui.md).

**2. The web daemon — `terva web`**
A long-lived server *holds* the workspace — the agent, sessions, credentials,
and tools all live in the daemon; a browser is a thin client that renders and
controls over the wire. The daemon keeps running when no one is watching, so you
can drive it from a browser on any device, disconnect and reconnect without
losing the live turn, and put several clients on one session at once. Run it as
a service for always-on, phone, or remote access. Guide: [web.md](web.md).

**3. The web daemon + an attached terminal — `terva web` + `terva attach`**
The same persistent daemon as (2), reached with the **terminal UI** instead of
(or alongside) a browser. `terva attach <url>` connects the full TUI to a
running daemon as a client; the state stays in the daemon, so the terminal is
disposable and reattachable — the conversation survives a dropped connection,
and the same session is reachable from a terminal and a browser at once. Paired
with a detach tool and systemd it becomes a **persistent terminal** that is
always your live session ([web.md](web.md#a-persistent-terminal)).

The dividing line is where state lives: in (1) it lives in the terminal process
(ephemeral process, on-disk session); in (2) and (3) it lives in a **daemon**,
and the client — browser or terminal — is disposable.

| | `terva` | `terva web` | `terva web` + `attach` |
|---|---|---|---|
| Front end | terminal | browser | terminal |
| Session lives in | terminal process | a daemon | a daemon |
| Survives client disconnect | resumable from disk | yes, live | yes, live |
| Reach from other devices | — | yes | same-host / tunnel |
| Best for | single-machine work | always-on, mobile, remote | a persistent, reattachable TUI |

Beyond these interactive shapes, terva runs headless too — **print**, **json**,
**rpc**, **acp** (editor integration), **replay**, and chat **bot** connectors,
all in [Modes](#modes) below.

## Flags

| Flag | Description |
|---|---|
| `--provider <id>` | Pick the provider. Around thirty are built in — `anthropic`, `openai`, `openai-codex`, `google`, `kimi`, `deepseek`, `groq`, `mistral`, `xai`, `github-copilot`, `openrouter`, `amazon-bedrock`, `ollama`, `openai-compatible`, and more. [providers.md](providers.md) has the full list with login methods; `terva --list-models` prints what your credentials can actually reach. |
| `--model <id>` | Pick the model (see `--list-models`; `--list-models=available` shows only what your credentials can use right now, `--list-models=live+` hides unconfigured catalog noise — see [models.md](models.md)). |
| `--list-models[=<filter>]` | Print the known models and exit. `<filter>` is a comma list of sources (`user`, `live`, `catalog`, `speculative`), a tier threshold like `live+` (that tier and above), or `available` (only providers whose credentials resolve right now). Terms AND together. |
| `--api-key <key>` | Override the API key. |
| `--base-url <url>` | Override the provider base URL (tests, self-hosted). |
| `--insecure` | Skip TLS verification for the inference `--base-url`. Gated to `openai-compatible`/`ollama` with an explicit `--base-url` — auth, discovery, and every other provider keep normal verification. For a self-signed local endpoint, nothing else. |
| `--temperature <n>` | Sampling temperature, `0`–`2`. Omit for the provider default. |
| `--system-prompt <text>` | Replace the default system prompt for this run (also overrides `$TERVA_HOME/SYSTEM.md`). |
| `--append-system-prompt <text>` | Append text to the system prompt (repeatable). |
| `--persona <name\|path>` | Load a persona — a built-in/on-disk name, or a path to a `.md` file — as the agent's identity. Omitted, it resolves via `Persona.md`, the `default_persona` config key, then the built-in default. See [personas.md](personas.md). |
| `--context-file <path>` | Inject a file's contents into the system prompt (repeatable). |
| `--reasoning off\|minimal\|minimum\|low\|medium\|high\|maximum\|max` | Set thinking level on supported models (default: off). `minimal` and `max` are accepted aliases at the ends of the ladder. |
| `-c`, `--continue` | Resume the latest session for this cwd. |
| `-r`, `--resume [id]` | Open the session picker at startup — titles, age, model, size, cost; Esc keeps the fresh session. With an `id` (the transcript's filename stem), resume that session directly, no picker. Also works attached: `terva attach -r`. |
| `--session <path>` | Resume a specific session file. |
| `--no-session` | Don't read or write session files. |
| `--replay <path>` | Play a recorded transcript back through the TUI instead of starting a live session (see Modes). |
| `--cwd <path>` | Use `<path>` as the working directory. |
| `--trust` | Trust the cwd for **this run only** — project-local extensions, skills, hooks, MCP, and context files load as if the directory were in the trust store, but nothing is persisted. The headless analog of answering the TUI's trust prompt; `terva trust` is the persistent form. |
| `--project` / `--no-project` | Force project-scoped mode on / off for this run, overriding `project_scoped` in `.terva/config.json`. Scoped: data lives in a project-local home and only the project's own extensions load; login and trust stay global. |
| `-e`, `--ext <path>` | Load an extension from `<path>` for this run (repeatable; wins against installed extensions of the same name). |
| `--no-workspace-tools` | Turn off the whole built-in base tool set — read/write/edit/bash/grep/glob plus `terva_status`, `session_inspect`, `ask_user_question`, the `task_*` board, `activate_tools` (lazy activation), and (if configured) `generate_image`; extensions, MCP, and skills stay — an agent with its integrations but no host filesystem/shell. |
| `--no-ext` | Turn off extension discovery for this run. `--ext` still works on top, so `--no-ext --ext ./x` runs only `x`. |
| `--no-mcp` | Turn off MCP servers for this run. |
| `--extensions <csv>` | Only load the listed installed extensions, by manifest name (repeatable). Restrict-only: `--ext` paths bypass it, config disables still subtract. Narrows which extensions are **loaded and spawned** — a group-room Discord bot gets `--extensions calendar`, so your mail extension's process never starts. It does **not** narrow bundle contributions: the skills, personas, and lore scanners honor only `enabled`/`disable_extensions`, so an excluded extension's bundled content still reaches the prompt. To exclude a bundle too, disable the extension. See [extensions.md](extensions.md#bundle-contributions). |
| `--mcp <csv>` | Only start the listed MCP servers, by name (repeatable). Same restrict-only semantics; the `/mcp` dialog can't live-enable an excluded server for the run. |
| `--no-tools` | All three building blocks above together (plus the `skill` tool) — no tools at all. |
| `--no-skill` | Disable all skills, including built-ins. No `skill` tool is registered and the system prompt has no skill manifest. |
| `--tools <csv>` | Only enable the listed (built-in) tools. |
| `--chat` | Conversational meta-mode: all tools off + a talk-naturally, non-coding identity, for fronting a conversation with a persona or card. Mutually exclusive with `--play`. See [personas.md](personas.md#chat-and-play-modes). |
| `--play` | Roleplay/simulation meta-mode: extensions + MCP only (like `--no-workspace-tools`) + an embodied identity, for acting in a [world extension](extensions.md). Mutually exclusive with `--chat`. |
| `--card <path>` | Load a SillyTavern Character Card V2 (`.json` or `.png`) as the immersive chat/play identity. Implies `--chat` when no mode is set; not valid in regular coding mode. See [personas.md](personas.md#character-cards) and [debugging-prompts.md](debugging-prompts.md). |
| `--greeting <n>` | With `--card`: which opening line to seed — `0` = `first_mes`, `1..N` = alternate greetings. |
| `--as <name>` | What a card's `{{user}}` macro resolves to (defaults to the saved name, else `"User"`). |
| `--cast NAME=REF` | Declare an actor a `--play` director can voice via the `actor_spawn` tool (`REF` = a persona name or a card path); repeatable. Implies `--play`; rejected with `--chat`. A trusted project's `.terva/cast.json` can declare a cast too. See [personas.md](personas.md#cast-and-actor-dispatch). |
| `--no-lore` | Disable the lore keyed-context primitive for this run — no discovery, no injection. See [debugging-prompts.md](debugging-prompts.md). |
| `--max-steps <n>` | Cap agent loop iterations to a positive integer. Omit the flag for the default (unlimited); `0` is rejected. |
| `--approval MODE` | Approval mode: `plan` (read-only only), `ask` (confirm everything), `auto-edit` (read-only + file editors run freely, the rest asks), `workspace` (built-in tools + read-only tools run, foreign side-effecting tools ask — the interactive default), `yolo` (run freely — the headless default). Combines with permission rules in config — see [permissions.md](permissions.md). In print / json / rpc modes anything that would need a prompt is **refused** with a model-readable message; allow rules and the mode's auto-allows still run. |
| `--jail` / `--no-jail` | Force the sandbox on / off at startup. Default: on for an interactive session (so the trusted built-in tools stay confined to the cwd), off for headless modes. `/jail` and `/unjail` toggle it at runtime in the TUI. |
| `--no-yolo` | Alias for `--approval ask`. In the interactive TUI a dialog shows the tool name and a one-line preview of its args with five choices (yes, always-this-tool, always-this-tool-saved, always-this-session, no). In print / json / rpc modes there is no prompt to confirm at, so every not-pre-allowed tool call is **refused** rather than run unconfirmed — use permission rules, an approval carrier (below), or omit the flag for unattended automation. |
| `--rpc-approvals` | In `rpc` mode, answer confirmation prompts over the JSON-RPC wire — the driver receives an approval request and replies — instead of refusing them. Opt-in: a driver that never answers keeps the safe refuse-by-default. See [rpc.md](rpc.md). |
| `--approval-socket <path>` | Carry `rpc`-mode approvals through a local MCP approval bridge at a Unix socket (terva's own MCP client). The transport-opaque sibling of `--rpc-approvals`; a backend sets one or the other, never both. Fail-closed if the bridge won't start. |
| `--approval-http <addr>` | Carry `rpc`-mode approvals through a Streamable-HTTP MCP permission endpoint (a remote orchestrator). The networked sibling of `--approval-socket`, used only when no local socket carrier claimed the gate. Fail-closed. |
| `--connector-manifest <path>` | Load one external chat connector from a `connector.json` for this invocation only (repeatable). Nothing is discovered, nothing persists — the `--ext` precedent, for connector development. See [connectors.md](connectors.md). |
| `--allow-restart` | Enable Tier-1 self-restart: the agent (via the `terva_restart` tool) or the control plane can re-exec terva into the currently-installed binary. The TUI restores the terminal and resumes its session (`/restart`); web clients reconnect. Off by default. `--web-allow-restart` is an accepted alias — the capability is not web-only. Needs a platform with `exec(2)`: on Windows the flag warns and does nothing, and no restart control is offered rather than one that could only ever fail. |
| `--swarm-worktrees` | Give each swarm sub-agent its own git worktree instead of sharing the host tree, leased from the built-in worktree engine (the cwd must be a git repo). Overrides the config's `swarm_worktrees` for this run. |
| `--swarm-agent <socket>` | Internal: marks this process a swarm-spawned agent and points it at its supervisor inbox socket. Set by the swarm engine, not by hand. |
| `--substrate <scheme>:<ref>` | Reserved: binds a dispatched actor to a shared authoritative state surface (a world instance, a task board). Threaded through the swarm boot spec; nothing resolves it yet — empty means the parent projects the substrate. |
| `--dump-prompt[=text\|json\|raw\|sizes]` | Assemble the prompt for the pending turn, print it, and exit before any model call. `sizes` reports per-section and per-tool byte/token weight. Needs no credential — a debugging and assertion tool. See [debugging-prompts.md](debugging-prompts.md). |
| `-h`, `--help` | Show the help screen (modes, commands, flags). |
| `-v`, `--version` | Print the version, commit, and build date. |

A few flags are still accepted but do nothing, kept so old scripts don't
break: `--with-skills` / `--with-skill` (user skills load by default now),
`--experimental-oauth` (subscription login is always available), and
`--tui-legacy` / `--tui-ctrlproto` (the legacy TUI driver was removed; the
ctrlproto carrier is the only backend).

### Web-mode flags

These apply to `terva web` / `--web` (see Modes). The server binds
loopback with no auth by default; most flags below exist to widen that
safely (`--web-stage` is the exception — it mounts an extra surface, not auth).
Details in [web.md](web.md).

| Flag | Description |
|---|---|
| `--web-addr <host:port>` | Listen address (default `127.0.0.1:8730`). |
| `--web-token <token>` | Require this bearer token on requests. The simplest way to expose the panel beyond loopback — but `ps` shows it to every local user, so for anything long-lived use one of the next two. |
| `--web-token-file <path>` | Read the bearer token from a file. Never enters the environment; pairs with systemd `LoadCredential=`. Missing or empty is a startup error, never a silent fall back to no auth. |
| `TERVA_WEB_TOKEN` (env) | The bearer token, for systemd `EnvironmentFile=`. Scrubbed from `os.Environ()` once read, so a child that inherits it (the agent's shell) can't see it with `env` — but the value **remains in `/proc/<pid>/environ`**, where any same-UID process can still read it back. Use `--web-token-file` to keep the token out of process memory entirely. Also supplies `terva attach --token`. |
| `--web-token-require-file` | Hardening opt-in (off by default): accept the token only from `--web-token-file`, and refuse `--web-token` and `TERVA_WEB_TOKEN` as a startup error. For a host that has committed to the file route and wants to prevent a silent regression to a leaky source. |
| `--web-auth-header <name>` | Trust a forward-auth header (e.g. `X-Forwarded-User`) as the authenticated user — for running behind an authenticating reverse proxy. |
| `--web-trusted-proxy <ip\|cidr>` | IPs/CIDRs, besides loopback, allowed to assert that forward-auth header (comma-separated, repeatable). Without it a spoofed header from anywhere else is ignored. |
| `--web-insecure-cidr <ip\|cidr>` | Grant no-auth access to specific IPs/CIDRs besides loopback — e.g. a tailnet range. The scoped, safer form of `--web-insecure`. |
| `--web-insecure` | Allow binding a non-loopback address with no auth mode at all. Dangerous: anyone who can reach the port drives your agent. |
| `--web-stage` | Mount **Stage**, the immersive chat/play surface, at `/stage/` (off by default), alongside the control panel at `/`. A distinct web app opted into per deployment, gated by the same auth as the panel — not itself an auth flag. The `web_stage` config knob (or the "Stage surface" Settings toggle) is the persistent twin. See [web.md](web.md#stage-the-immersive-chatplay-surface). |

`--allow-restart` (below) is not web-specific, but web mode refuses to enable it
on an unauthenticated non-loopback listener.

## Tools

- `read`: read text files, or inline images (PNG, JPEG, GIF, WebP).
- `write`: create or overwrite files, making parent directories as needed.
  Pass `mode` (octal, e.g. `0755`) to set the permission bits in the same
  step — for an executable script — instead of a follow-up `chmod`; omit it
  to keep the secure default (new files honor the umask, existing files keep
  their mode).
- `edit`: one or more exact-match replacements in an existing file, with
  an optional `replaceAll` per edit and a whitespace-tolerant fallback
  when an exact match fails (see
  [context-construction.md](context-construction.md)).
- `bash`: run a shell command in the session cwd, with merged stdout/stderr and a timeout.
- `grep`: search file contents for an RE2 regular expression. Returns `path:line:text` in deterministic order, honors `.gitignore` (and always skips `.git`), skips binary files, and pages via `offset`/`max_results`. Read-only. Prefer it over `bash grep`/`rg`.
- `glob`: list files whose path matches a glob pattern (`**` recurses, e.g. `**/*.go`). Returns paths relative to cwd in lexical order, honors `.gitignore`, and pages via `offset`/`max_results`. Read-only. Prefer it over `bash find`/`ls`.
- `ask_user_question`: ask the user a structured clarifying question (with optional multiple-choice options and/or a free-text answer) and wait for the reply, instead of guessing when requirements are ambiguous. Permitted in every approval mode, plan included — asking has no side effect. Interactive (TUI) only: in print/json/rpc/ACP modes and swarm subagents there is no question channel — ACP has no native question primitive, only tool-permission requests — so it returns a "no channel — proceed on your best judgment" result rather than blocking.
- `terva_status`: report the agent's own runtime state — model, provider, the running binary's version/commit/build timestamp, the loaded extensions and their versions, working directory, session id and transcript file, reasoning effort, and how full the context window is. Takes no arguments. The extension line matters for a long-lived project agent: beyond files and shell its whole tool surface comes from extensions, so a review or plan it writes should name the versions it ran against — that is what makes a carried-forward finding checkable later instead of merely plausible.
- `task_create` / `task_update` / `task_list` / `task_archive`: the built-in task board — plan multi-step work as checkable tasks (one active at a time, evidence to close, archives per phase). The current board rides each turn as a context card, persists per session, and `task_list format:"markdown"` exports a checkbox worklog. Ships exactly when the coding tools above do.
- `session_inspect`: a bounded, filterable view over a session transcript — this one, another session in this project (by id, as `terva_status` prints), or a swarm sub-agent's (by the id `swarm_spawn` and the auto-swarm recap print). Filter by event kind, tool, or failures; page with `cursor`/`limit`; `expand` returns one event's full text in pages. Secrets are redacted and both the input scan and the output are size-capped. A sub-agent streams its transcript as it works, so a running one can be inspected mid-task; in the brief window before it has written anything, the result says so rather than pretending the filters missed.
- `activate_tools`: present only when `lazy_tools` is enabled — activates a hidden capability group; by default the model is automatically continued with the tools live when it finishes the activating reply (activation continuation), otherwise they join the next turn. Visibility only; every revealed tool keeps its normal permission gate. See [standard-tools.md](standard-tools.md#lazy-tool-visibility-lazy_tools).
- `skill`: load a skill's full instructions on demand. Registered unless `--no-skill` (or `--no-tools`); the system prompt carries only the skill manifest until the model asks for one. See [skills.md](skills.md).

The rest of the model-visible surface is **conditional** — the host injects
each one only when a session opts in. [standard-tools.md](standard-tools.md)
carries the full classification:

- `swarm_spawn`: fire-and-forget parallel sub-agents, in coding sessions with auto-swarm on. A `tier` picks a cheaper model, never a stronger one than the host. An optional `deliverable_schema` demands a schema-validated structured report from the sub-agent (delivered via a `deliver_result` tool, or a fenced JSON block for workers that can't carry tools). See [tui.md](tui.md) and [standard-tools.md](standard-tools.md#host-injected-skins-conditional).
- `actor_spawn`: the `--play` director voices a declared cast member and waits for its line. Needs `--play` plus a `--cast` (or a trusted project's `.terva/cast.json`). See [personas.md](personas.md#cast-and-actor-dispatch).
- `raati_convene`: convene a deliberation panel on a decisive question, from inside a turn. Opt-in via the `raati.convene_tool` config key; base workspace sessions only. Every call passes the approval gate — a convening spends real sub-agent turns. See [raati.md](raati.md).
- `generate_image`: turn a prompt into an image, returned inline and optionally written into the workspace through the sandbox. Present only when an `image` config block resolves a backend. See [image-generation.md](image-generation.md).
- `code_execution`: run a short JavaScript program that calls `read`/`grep`/`glob` as functions and returns only what it `print`s — multi-step read-only lookups for one tool result's context cost. Present only in binaries built with `-tags terva_scripting` (release builds are). Read-only; every host call a script makes passes the normal permission gate. See [scripting.md](scripting.md).
- `worktree_list` / `worktree_create` / `worktree_claim` / `worktree_release` / `worktree_remove`: managed git worktrees with an available/claimed reuse model — create one per task, hand idle ones between agents, remove with dirty/unmerged safety rails. Present only when the session cwd is (or has an immediate child that is) a git repository. `worktree_list` is read-only; the rest mutate git state. See [standard-tools.md](standard-tools.md#current-standard-bundle).
- `chat_send_image` / `chat_send_file`: send an image or file into the paired chat, while a chat bridge is connected and bound to this session — and only for the capabilities the connector advertises. See [connectors.md](connectors.md).
- `terva_restart`: re-exec the running binary in place, so a session picks up a newly installed build without being torn down. Present only when self-restart is on (`--allow-restart`), in the TUI as well as web — web mode additionally refuses to enable it on an unauthenticated non-loopback listener. Deliberately left unclassified in the permission table so it prompts in every *gating* mode (`ask`, `auto-edit`, `workspace`). **`--approval yolo` bypasses the gate for every tool, this one included** — running yolo with `--allow-restart` means the agent can re-exec the daemon without asking.

When the sandbox is on (see `/jail` in [tui.md](tui.md)), the file, command, and search tools (`read`, `write`, `edit`, `bash`, `grep`, `glob`) refuse paths outside the session cwd. `grep`/`glob` also skip symlinks so a walk can't follow a link out of the tree. `terva_status` touches no paths.

### terva_status

`terva_status` lets the model introspect its own session. None of this is otherwise visible to it: the system prompt carries only the date and cwd, and context usage is computed by the harness after each turn and never surfaced. With the tool, the model can check how full its context is — and decide to summarize or wrap up — or report which model and provider it's actually running as.

A call returns the provider, model, auth method, working directory, session identity, reasoning effort, the context window and how much of it the last turn used (as a percentage), and the cumulative session token/cost totals. Context usage reflects the **most recent completed turn**, so it approximates the current size rather than giving an exact mid-turn count.

It also reports the running binary's build identity — semantic version, commit hash, and build timestamp (the same triple `terva --version` prints, but read from the process actually serving the session rather than from whichever binary a shell would resolve). The text line abbreviates the commit; the structured `Details` (`version`, `commit`, `build_date`) carries the full hash. This is what to ask for in a bug report, when checking extension/documentation compatibility, or to confirm a `terva_restart` loaded the build you expected. A dev build with no VCS stamp omits the commit/date; an SDK embedder that never recorded a build omits the line entirely.

The session identity is the transcript file the conversation persists to: the id is the file basename `--resume` accepts, plus the absolute path. This is the ground truth for debugging headless runs (bot daemons, print/json) where nothing else surfaces it — ask the agent for its session id and you can `terva --resume <id>` from that cwd or read the `.jsonl` directly. Conversations that don't persist (`--no-session`, bot-mode group chats) say so explicitly rather than inventing an id.

If a turn fails (the provider errors, a stream drops), the failure is recorded in an error sidecar next to the transcript — `<session-id>.errors.jsonl`, one JSON line per error with a timestamp and the provider/model in play. The transcript itself stays clean (its record vocabulary is a contract for replay/resume/compaction); the sidecar is where a red-X-with-no-detail becomes recoverable after the fact. It is created only if an error actually occurs, so a clean session leaves none.

The model is nudged toward the tool by a one-line hint in the default system prompt; the hint (and the tool) are omitted when `--no-tools`, or a `--tools` allowlist that excludes `terva_status`, is in effect.

## Modes

- **Interactive** (default): chat TUI with streaming output, spinner, cost meter, slash commands.
- **Print**: `terva -p "prompt"` runs the agent to completion and writes only the final assistant text to stdout.
- **JSON**: `terva --json "prompt"` emits one JSON object per agent event to stdout, newline-delimited. The schema is documented in [docs/rpc.md](rpc.md).
- **RPC**: `terva rpc` runs as a long-lived child process; commands in on stdin, events and responses out on stdout, both as NDJSON. Designed for embedding terva in third-party apps written in any language. See [docs/rpc.md](rpc.md) for the wire schema and `examples/rpc/{python,node,shell,go}` for working clients.
- **ACP**: `terva acp` (or `--acp`) speaks the Agent Client Protocol — editor↔agent JSON-RPC 2.0 over stdio, for Zed and other ACP clients. An opt-in build (`-tags terva_acp`); a binary built without the tag routes here and exits saying so.
- **Web**: `terva web` (or `--web`) serves the browser control panel — a local HTTP server speaking ctrlproto over a WebSocket to a self-hosted workspace (chat, sessions, models, raati board) — plus, behind `--web-stage`, the opt-in **Stage** immersive chat/play surface at `/stage/`. Loopback and no-auth by default; see the web-mode flags above and [web.md](web.md). Also an opt-in build (`-tags terva_web`).
- **Attach**: `terva attach [url]` runs the interactive TUI as a **client** of a running `terva web` daemon instead of hosting the workspace in-process — same sessions and live stream as the browser panel, reattachable, resyncing from the daemon's snapshot on reconnect. With no URL it finds the daemon serving this `$TERVA_HOME` (published to `$TERVA_HOME/listen.json` by `terva web`), falling back to `ws://127.0.0.1:8730/ws`; a `unix:/path/to.sock` or a remote URL also work, and `--token` matches the daemon's `--web-token`. This is variant 3 in [Ways to run terva](#ways-to-run-terva). See [tui.md](tui.md#attaching-to-a-running-daemon-terva-attach).
- **Replay**: `terva replay <file>` (or `--replay <file>`) plays a recorded transcript back through the TUI as a deterministic scene. It backs the TUI with a read-only replay carrier instead of a live workspace, so it needs no credential and refuses prompts.
- **Raati**: `terva raati "question"` convenes a three-unit deliberation panel (YATA-1 truth / KUSANAGI-2 decisiveness / MAGATAMA-3 benevolence) over the swarm engine: a blind round, a cross-examination round, then a tallied verdict with the minority report — a 2–1 split is information, not failure. Flags: `--class advisory|gate|veto` (gate = unanimity, fails closed), `--veto-holder UNIT` (which seat may block under `--class veto`; defaults to MAGATAMA-3), `--evidence PATH` (repeatable), `--round-timeout DUR` (a late unit abstains), `--single-round` (blind ballots are final), and `--level 0|1|2` — the rigor ladder: 0 *kaiku* seats every unit on the invocation's provider/model (cheapest; correlated judgment — pair with `--provider ollama --model <tag>` for a free local panel), 1 *kuoro* seats the provider's weak/medium/strong tier ladder (needs a full ladder: `terva models tiers`), 2 *käräjät* seats three providers from the user config's `raati.level2` (real error decorrelation, gate-grade). How the level's model pool maps onto seats is `--seat-order` (or user config `raati.seat_order`): `convene` (default) shuffles once per convening, `fixed` uses pool order (remappable via `raati.seat_map`, an index permutation), and `turn` reshuffles per voting round — the seat persists while the weights behind it rotate, at the cost of respawning every seat cold for the final round (no cross-round cache reuse, evidence re-read per seat) in exchange for the least fixed model-to-prior bias. Agents can convene a panel themselves via the opt-in `raati_convene` tool (user config `raati.convene_tool`; every call passes the approval gate, and the run renders live on the web board). `--profile NAME` convenes under a named bundle — built-ins `triage`, `counsel`, `code-review`, `ethics`, overridden or extended via the user config's `raati.profiles`; the profile fills whatever flags you leave unsaid (explicit flags win). The full record persists under `$TERVA_HOME/raati/`; see `docs/raati.md` for the full guide.

Orthogonal to the output modes above are the **experience meta-modes** (`--chat`, `--play`) that reframe the harness away from coding — identity, tools, and TUI chrome — so a persona or [character card](personas.md#character-cards) can front a conversation or a roleplay. Any output mode can pair with either. See [personas.md](personas.md#chat-and-play-modes).

## Subcommands

Everything terva does besides run a session is a subcommand. Each has its
own detailed screen — `terva <command> --help`.

| Command | What it does |
|---|---|
| `terva ext ...` | Install, list, update, and remove extensions (`--ext PATH` loads one for a single run). See [extensions.md](extensions.md). |
| `terva models ...` | Scaffold and edit custom model definitions in `$TERVA_HOME/models.json`; `terva models tiers` shows each provider's weak/medium/strong ladder. See [models.md](models.md) and [providers.md](providers.md). |
| `terva locale ...` | Manage UI languages and translations (or set `TERVA_LANG=<tag>` to run in one). See [localization.md](localization.md). |
| `terva persona ...` | Manage personas — the agent identities `--persona` selects. See [personas.md](personas.md). |
| `terva lore ...` | Manage lore entries, the keyed-context store that fires on keywords in the conversation. Unaffected by `--no-lore`, which only disables injection for a run. See [debugging-prompts.md](debugging-prompts.md). |
| `terva card ...` | Inspect character cards (`.json`/`.png`) before running one with `--card`. See [personas.md](personas.md#character-cards). |
| `terva bot ...` | Run a chat-bridge bot (Telegram, Discord, and other connectors) — `terva bot run` daemonizes an agent onto a chat platform. See [connectors.md](connectors.md). |
| `terva raati "question"` | Convene the three-unit deliberation panel; prints the verdict and the dissent. Flags under Modes above; full guide in [raati.md](raati.md). |
| `terva workflow run <script.js>` | Run a workflow — a JavaScript script that orchestrates sub-agents deterministically, with a per-run journal so `--resume` replays completed agents instead of re-spending. Result JSON to stdout, narration to stderr. Needs a `terva_workflows` build (releases are). Full guide in [workflows.md](workflows.md). |
| `terva web` | Serve the browser control panel (see Modes and [web.md](web.md)). |
| `terva attach [url]` | Attach the interactive TUI to a running `terva web` daemon as a client (no url = the daemon serving this `$TERVA_HOME`, else `ws://127.0.0.1:8730/ws`; a `unix:` socket or remote URL also work). See Modes, [tui.md](tui.md#attaching-to-a-running-daemon-terva-attach), and [web.md](web.md). |
| `terva trust` / `terva untrust` | Manage which directories may load project-local extensions, skills, hooks, MCP servers, and context files. `--trust` does it for one run without persisting. See `docs/plans/workspace-trust.md`. |
| `terva unjail` / `terva jail` | Record which directories run without the filesystem sandbox, so tools there may read and write outside the working directory. `--parent` covers descendants; `--list` shows the list. `--no-jail` does it for one run without persisting. Not the same as trust — see [permissions.md](permissions.md#unjailing-a-directory-for-good). |
| `terva project ...` | Project-scoped agents: data and extensions pinned to a directory (login and trust stay global). See [extensions.md](extensions.md#project-scoped-agents). |
| `terva migrate` | Migrate a legacy install's data into the terva data directory. |
| `terva update` | Download and install the latest release. |

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
├── personas/           # optional: user personas (terva persona)
├── cards/              # optional: character-card library (Stage; cards.import)
├── backgrounds/        # optional: scene backdrops (Stage; backgrounds.import)
├── lore/               # optional: user lore entries (terva lore)
├── locales/            # optional: user translations (terva locale)
├── extensions/         # installed extensions, one dir per extension
├── ext-data/           # per-extension private data, one dir per extension name
├── tasks/              # the built-in task board, per session
├── raati/              # deliberation records, one per convening
└── logs/               # app log files
```

**`auth.json` is the exception to "all data lives under `$TERVA_HOME`."**
Credentials and trust verdicts resolve against the *global* home even when
`--project` pins everything else to a project-local one. That is what lets a
project-scoped agent inherit your login instead of re-authenticating per
directory — and it keeps the trust store out of the repo, so a project can
never trust itself.

Drop a `SYSTEM.md` in `$TERVA_HOME` to replace the built-in identity and guidelines for every run. `--system-prompt` still wins per-invocation. Delete the file to revert to the default.
