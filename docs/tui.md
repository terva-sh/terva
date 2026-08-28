# The terva TUI

Everything interactive mode does beyond typing prompts: slash
commands, sessions, inline images, message queueing, and key
bindings. Flags and run modes live in [cli.md](cli.md).

## Slash commands

Type `/` in the TUI to open the autocomplete popup. Available commands:

| Command | Description |
|---|---|
| `/help` | Show key bindings and commands. |
| `/login` | Log in via API key or subscription (opens a dialog). |
| `/logout [provider]` | Clear credentials for any logged-in provider, or all when omitted. `/logout openai-codex` clears ChatGPT/Codex subscription auth while preserving a public OpenAI API key; `/logout kimi` also disables fallback to the official Kimi Code CLI token until you log in to Kimi through terva again. |
| `/model` | Pick a model from a list (or `/model <id>` to set directly). Press `ctrl+e` on a highlighted model to edit its config — changes save to `$TERVA_HOME/models.json` and override the defaults. |
| `/new` | Start a fresh session in the current directory. The current session stays on disk (resume it later with `/sessions`); the transcript, context meter, and cost reset, while the provider/model stay put. |
| `/sessions` | Resume a previous session for this directory. |
| `/session` | Four ops on the current session: `export` to a portable `.tervasession` file, `import` one back in, `fork` from a past user message into a new branch, `tree` to switch between branches. Opens a picker without an argument; direct forms: `/session export [path]`, `/session import <path>`, `/session fork`, `/session tree`. Default export destination is `~/Downloads`. |
| `/jump` | Scroll the chat to a previous turn (or `/jump <text>` to filter). |
| `/btw` | Side chat with full context that doesn't add to the main thread. |
| `/nextstep` | Ask what to type next. The answer arrives as a dimmed offer in the composer — `tab` or `→` accepts it, and nothing is sent until you send it. |
| `/swarm` | Spawn, monitor, and chat with background subagents. Each runs in parallel with your main session and shares its working directory. |
| `/worktree` | Managed git worktrees (the built-in `worktree_*` engine): the list view shows each worktree's claim state, base, and dirtiness — ↑/↓ select, ↵ `/cd`s into one, `c` switches to the merge-back **collect** overview (commits ahead of base, dirty/unpushed flags), `r` refreshes. Also fills the status bar's worktree glance. Unavailable outside a git repo. |
| `/shared` | The files the agent handed you this session with `share_file` — name, kind, size, and how long the bytes have left. `↑`/`↓` select, `c` copies the file's path to the clipboard, `o` (or `↵`) opens it in the system viewer, `s` saves a copy into the working directory, `r` refetches, `esc` closes. `s` is the one that works from `terva attach`: the path in the listing names the **daemon's** disk, so on a remote carrier copy and open refuse and point you at save, which pulls the bytes over the control plane. A file whose deadline has passed stays listed — the session did share it — but its actions are refused rather than dispatched into a filesystem error. Saving never overwrites: an existing name gets a `-2` suffix. |
| `/skill` | Prime your next request with a specific skill: `/skill <name> [request]` rewrites to "use the *name* skill for: request" so the model reaches for it. Autocompletes skill names after `/skill `. |
| `/skills` | List discovered skills (SKILL.md files) and preview their bodies. |
| `/context` | Token breakdown of the assembled context and what each extension injects. Under lazy tool visibility it reports both numbers honestly — the *advertised* tool set that is actually on the wire, and the *installed* total that would load if every group were activated (`[4 of 31 tools · 48 KB installed]`), plus the bytes the capability note itself costs. Read-only. |
| `/lore` | List this run's active lore (keyed-context) entries — name, trigger, and source. Read-only. It does **not** report which entries fired on the last turn: the default TUI reads lore over ctrlproto and the wire view carries no per-turn firing record. To see what actually fired, use `--dump-prompt=json` and read the `tail` sources. See [debugging-prompts.md](debugging-prompts.md). |
| `/tasks` | Show the agent's task list — the built-in task tracker the model writes to as it works. Read-only; `esc` closes. |
| `/memory` | The agent's durable memory: facts it carries into future sessions, in a project scope and a cross-project user scope, each split into an **active** tier (in the model's context on every request) and an **archived** tier (out of context until the conversation matches its keys). Archived rows are marked — `·` stored, `▸` matched last turn and injected, `✗` matched but cut to fit the tail budget — and the selected one shows its triggers, so a memory keyed on words nobody would type is visible rather than silently unreachable. `d` deletes the selected entry from whichever tier owns it, `c` twice clears a scope's **active** entries (archived ones are kept — they are the expensive half to rebuild, and go one at a time), `r` re-reads both tiers from disk (they are hand-editable markdown), `esc` closes. Each scope header shows how full the active tier is against the cap that refuses the next write, with the archived count alongside but outside that fraction. The status glance reads `🧠 active+archived`. Absent when `--no-memory` is set. |
| `/usage` | Show usage limits — subscription windows (5h/weekly), the provider's rate-limit windows, and any credit balance — with how much is used and when each resets. Read-only; `esc` closes. See [providers.md](providers.md#usage-limits-usage). |
| `/resets` | List the banked usage-reset credits a subscription has accrued (OpenAI Codex today) and redeem one to clear a spent window. Redeeming is irreversible and confirmed first; `esc` closes. |
| `/compact` | Summarize the transcript into one message to free up context. |
| `/study` | Run the canned prompt "Read and understand everything in the current directory." so the agent has full project context before you start asking targeted questions. Pass a path — typed, drag-dropped, or selected via `@` — to target a specific file or directory instead: `/study [dir:packages/]`, `/study cmd/terva/main.go`. |
| `/jail` | Confine tools to the current directory. (On by default in interactive sessions; `--no-jail` starts unjailed.) `/jail always` also forgets a saved unjail rule for this directory. |
| `/unjail` | Allow tools to touch paths outside again — this session only. `/unjail always` records the directory so it starts unjailed from now on (see [permissions.md](permissions.md#unjailing-a-directory-for-good)). |
| `/trust` | Trust the current directory so its project content — `.terva` extensions, skills, lore, context, permission rules — loads. `/trust parent` trusts the parent so every directory under it counts as trusted too. Project extensions become discoverable immediately (`/reload-ext` picks them up); prompt-baked content lands on the next launch. |
| `/untrust` | Remove the current directory from the trust list; its project content stops loading on the next launch. |
| `/permissions` | Show the current approval mode and the active permission rules grouped by source (user/project/extension), and revoke this session's "always allow" grants: `↑`/`↓` select a grant, `r` or `del` takes it back, `R` clears them all, `esc` closes. Rules stay read-only (edit them in config). Alias: `/perms`. See [permissions.md](permissions.md). |
| `/reload-ext` | Hot-reload all extensions (re-read manifests, respawn subprocesses, rebuild tool registry). |
| `/extensions` | List installed extensions and their state; enable/disable each globally (`g`) or per-project (`p`). Alias `/ext`. |
| `/mcp` | List the configured MCP servers with their state and tool counts; enable/disable each globally or per-project. See [mcp.md](mcp.md). |
| `/connect` | Connect, disconnect, or show status of the chat bridge (takes `connect` / `disconnect` / `status` as an optional argument; opens a picker without one). When connected, DMs from the paired user become prompts in the running session and the assistant's replies are mirrored back to the chat. The picker lists every configured service — the built-in telegram and discord connectors plus any connector extensions (tagged "extension") — and `/connect <name>` selects one directly. Aliases: `/telegram`, `/tg` (pin telegram). |
| `/status` | Read-only harness status, inline like `/help`: the running build (version/commit/date), process uptime, provider/model, auth, reasoning, cwd + trust state, session id and transcript file, live context usage, cumulative token/cost totals, and provider usage windows. The operator's view of what the model-facing `terva_status` tool reports — no turn spent. |
| `/restart` | Re-exec terva into the currently-installed binary and resume this session (Tier-1 self-restart; needs `--allow-restart`). The terminal is restored before the exec, the new image reattaches to the same session, and the status line reports the build hop (`restarted — was vX, now vY`). Same semantics as web: an in-flight turn is cancelled first and given a brief, bounded window to unwind and persist before the image is replaced — only already-persisted history is guaranteed across the hop. The agent-driven `terva_restart` tool is registered under the same flag — the edit-own-code → reinstall → relaunch loop works from the TUI too. |
| `/settings` | Open the settings dialog — thinking level, auto-condense, lazy tool loading, lore, theme, status line, and the rest — with `enter`/`space` or the option picker. Saved to `$TERVA_HOME/config.json`, effective immediately. The **approval mode** picker is the exception: it switches the mode live for the **current session only** (like `/jail`), and is *not* persisted — the startup default comes only from an explicit `approval` key in config or the `--approval` flag, so the picker can never silently pin a mode into your config. |
| `/paste` | Paste an image from the system clipboard into the prompt as a `[clipboard image #N]` marker (the same thing `ctrl+v` does). Text paste needs neither — it arrives as an ordinary bracketed paste. |
| `/migrate` | Move a pre-rename `zot` data directory to the terva location. <!-- rename:keep --> A one-time upgrade path; the default TUI reports it as unavailable, since it doesn't carry the interactive migrator. |
| `/clear` | Clear the chat transcript. |
| `/exit` | Exit terva. |

Extension-registered commands appear under a divider at the bottom of the popup, sorted by name.

### Attaching to a running daemon (`terva attach`)

`terva attach [URL]` runs this same TUI as a **client** of a running `terva
web` daemon instead of hosting the workspace in-process. **With no URL it finds
the daemon serving this `$TERVA_HOME`**: `terva web` publishes its bound endpoint
to `$TERVA_HOME/listen.json` when it starts and removes it when it stops, so a
bare `terva attach` reaches a daemon on a filesystem socket without being told
where. A stale record (the daemon crashed — the file is heartbeated) is ignored,
and the old `ws://127.0.0.1:8730/ws` default still applies when nothing is
serving. (Explicit URL forms: `--token` matches the daemon's `--web-token` — or
`--token-file PATH` / `TERVA_WEB_TOKEN` to keep the secret off the command line,
the same three sources the daemon reads under its own `--web-token*` spellings
(see the persistent-attach note below); bare `host:port` and `http(s)://` forms normalize, and
`unix:/path/to.sock` targets a daemon serving a filesystem socket — no token
needed there, the socket file's permissions gate access). Sessions, credentials, extensions,
and tools all live daemon-side — the TUI renders and controls, and the
browser panel can watch the same session simultaneously.

The status line tells the daemon's truth, not the local process's: the daemon
advertises its working directory and sandbox lock in the hello, and the wire
carries the session's context window, transcript name, and subscription flag —
so the cwd/git segments, ctx gauge, `(sub)` cost tag, jailed badge, and
thinking level all describe the workspace you're attached to, from whatever
directory you launched. `/swarm` and the status bar's swarm glance ride the
daemon's tasks surface.

The connection is self-healing: if the daemon restarts (its `/restart`, the
`terva_restart` tool, or a crash-and-relaunch), the TUI shows "connection lost
— reconnecting…", re-subscribes, resyncs from the snapshot, and announces
`daemon restarted: vX → vY` when the build changed. The inverse is the
quality-of-life win for iterating on terva itself: quit the TUI mid-turn,
reinstall it, re-attach — the agent never noticed. Note that `/restart` from an
attached TUI restarts the **daemon**, over the wire — not this client.

The client can restart **itself**, too. Started with `--allow-restart`,
`terva attach` re-execs into the freshly-installed binary and reconnects on
**SIGHUP** — the same Tier-1 mechanics the daemon uses (terminal restored first,
same session rebound, a brief outage while the new client boots). That makes a
persistent attach — one supervised in a systemd unit or a long-lived
tmux/screen pane — pick up a new build via `systemctl --user reload …` /
`kill -HUP`, the client-side counterpart to reloading the daemon. It is off
unless `--allow-restart` is passed: an attach client owns a terminal, where
SIGHUP is *also* the hangup signal, so without the flag SIGHUP keeps its default
(exit on hangup). Reserve it for those persistent, non-throwaway attaches.
`examples/deploy/systemd/` ships a ready-made socket-activated daemon + `dtach`
attach that does exactly this — see docs/web.md §"A persistent terminal".

A persistent attach lives as long as the daemon, so a `--token` on its command
line sits in the unit file and in `ps` / `/proc/<pid>/cmdline` for every local
user to read — the same argv leak the daemon has. When you attach across a
network to an authenticated daemon (rather than the local `unix:` socket, whose
file permissions are the auth), reach for `--token-file PATH` (the client's
spelling of the daemon's `--web-token-file`) — the token is read from disk and
never touches the command line, exactly what systemd's `LoadCredential=`
provides. An unreadable or empty file is fatal, not a silent token-less dial.
`TERVA_WEB_TOKEN` (systemd `EnvironmentFile=`) is the middle
ground; since an attach client runs no agent shell, the environment is a safer
place for it than it is on the daemon.

The `@`-file picker lists the daemon's tree **over the wire** (the
`files.list` verb, advertised as the `files-list` hello feature) — correct
from any host, with the same gitignore filtering and caps as the local
picker; against an older daemon it falls back to reading local disk at the
daemon's advertised cwd (same-host correct).

v1 boundaries: the git probe and model-catalog reads are local — pointed at
the daemon's advertised cwd, so they're truthful from any directory on the
daemon's host and degrade to absent cross-host; session file operations
(`/session` export/import/fork/tree) and `/login` are daemon-side concerns
and degrade with a clear message; `/jail` and extension pickers likewise.
See docs/proposals/orchestration-frontend.md for the trajectory (the
sessions board over N subscriptions).

### Editing a model's config (`ctrl+e`)

In the `/model` picker, press `ctrl+e` on the highlighted model to open its config editor (a bare `e` is taken by the type-to-filter input). Each field is tri-state: leave it **inherit** to keep the catalog/live default, or set an explicit value to override:

- **base url** — point the model at a different endpoint (a local server, a gateway).
- **context window** / **max tokens** — correct sizes terva doesn't know for a custom model.
- **reasoning** / **image input** — capability flags (e.g. mark a local model text-only so images are dropped instead of bricking the request).

`↑`/`↓` move between fields, `enter` edits a value (or cycles a flag through inherit → on → off), `s` saves, `esc` cancels. Saving writes a *minimal* entry to `$TERVA_HOME/models.json` — only the fields you set, so unset fields keep tracking the default — and applies immediately; models carrying an override are tagged `[edited]` in the picker. Press `r` to **reset**: after a `y`/`n` confirmation it removes the model's `models.json` entry, returning it to defaults. (The editor covers the operational fields above; per-model **prices** stay hand-editable in `models.json` and are preserved across edits.)

### Extensions (`/extensions`)

`/extensions` (alias `/ext`) lists every installed extension — global (`$TERVA_HOME/extensions`) and project (`.terva/extensions`) — with its version, language, what it provides (commands/tools), and current state. Two independent on/off controls:

- **`g`** — enable/disable **globally** by writing the extension's manifest `enabled` flag (the same field `terva ext enable/disable` uses).
- **`p`** — enable/disable **for this project** by adding/removing it in `.terva/config.json`'s `disable_extensions`. This is *restrict-only*: it can switch off a globally-enabled extension here, but can't force-enable one that's disabled globally.

Toggling applies live and **surgically** — only that one extension is started or stopped (a stop is a graceful, silent shutdown), every other extension keeps running. A failed start shows as `off (not running)` (check `terva ext logs <name>`). `↑`/`↓` move between extensions, `esc` closes.

### Shell escape (`!command`)

Type `!` followed by a command to run it directly without going through the model. Everything after the `!` is passed to the same shell the `bash` tool uses (`/bin/sh -c` on Unix, `cmd /C` on Windows), runs in the session working directory, and honors the `/jail` sandbox. The output is appended below the transcript as a terminal-log block (command echo, output, exit code), styled by success or failure. It stays on screen until you send your next prompt (or run `/clear`), so it doesn't bleed into the model conversation. A running `!command` shares the busy state with the agent: `esc` cancels it, and you cannot start one while a turn (or another shell escape) is in flight.

### `/new`

Starts a fresh session in the current directory without leaving terva — the in-place equivalent of quitting and relaunching. The outgoing session is flushed to disk and left intact (resume it later via `/sessions`), then a new session file is opened and the agent's transcript, context-usage meter, and running cost reset to empty. Your provider, model, reasoning effort, and `/jail` state carry over unchanged. Unlike `/clear` — which only wipes the in-memory transcript of the *current* session — `/new` mints a genuinely new session with its own id and file. With `--no-session` there's no file to open, so `/new` simply clears the conversation.

### `/sessions`

Shows previous sessions for the current working directory, newest first, with timestamp, model, message count, cost, and the first user prompt. Pick one with `up`/`down`, `enter` to resume, `esc` to cancel. terva swaps the current session file for the selected one and replays the full transcript (including tool calls) into the agent. Sessions remember the model they ended on, so resuming picks up on that exact model even if your global default changed.

### `/session`

Four ops on the current session. `/session` alone opens a picker; each is also runnable directly.

- **`/session export [path]`**. Writes the running transcript to a portable `.tervasession` file. Default destination is `~/Downloads/<timestamp>-<session-id>-<prompt-slug>.tervasession`. Pass a path to override; a directory is fine (a dated name is built inside), a bare name gets `.tervasession` appended. The meta's cwd is stripped on the way out so the recipient doesn't see your filesystem layout.

  **What's included.** Only the main chat thread of the running session — messages, tool calls, tool results, compactions, and usage. **`/swarm` subagents are NOT included.** Their transcripts, unix-socket inboxes, and per-agent session files are all machine-local; a `.tervasession` is just a chat transcript and has no way to revive a unix socket on another box. If you want the conversation, copy it out of the dashboard manually.
- **`/session import <path>`**. Copies a `.tervasession` file into `$TERVA_HOME/sessions/<cwd-hash>/` with a fresh id and the current cwd, then switches the running agent onto it. Imported sessions are first-class: they show up in `/sessions`, `/jump`, and the tree. Drag-drop paths in the editor are accepted (terva strips the surrounding quotes automatically).
- **`/session fork`**. Opens a turn picker (same shape as `/jump`). Pick any past user message; terva copies every message up to and including that turn into a new session, records `parent` + `fork_point` in the new meta, and switches onto the branch. The parent session stays on disk. Use it to try a different question without polluting the original transcript, or to rewind after the agent went down the wrong path.
- **`/session tree`**. Shows every session in the current cwd arranged by parent/child relationships, depth-first with indent per level. The current session is tagged `[current]`. Pick any entry to switch into it. Parentless sessions are roots; branches created via `/session fork` nest under whichever session they were forked from. Orphaned children (whose parent file was deleted) still show as roots so they stay discoverable.

### `/jump`

Opens a turn picker for the current session, one row per user prompt, each showing the turn number, how many tools that turn invoked, and the first line of the prompt. `up`/`down` to pick, `enter` to jump, `esc` to cancel. Any printable rune while the picker is open extends a filter; backspace narrows it back. `/jump <text>` pre-applies the filter; if exactly one turn matches, terva jumps straight there without showing the picker.

Jumping is non-destructive. The transcript is untouched, the viewport just scrolls so the chosen turn is at the top. A muted line at the top of the chat reads `viewing turn N of M, pgdn to catch up`. Scroll back to the bottom with `pgdn` (or keep scrolling with the arrow keys) and the indicator goes away.

### `/btw`

Opens a side-chat overlay with the full main session as frozen context, so you can ask quick clarifying questions ("does asyncio.gather() catch exceptions?", "btw the bundle budget is 10MB", "what's the default fetch timeout?") without bloating the main thread.

Each question fires a one-off model call against `system + main transcript + side-chat history so far`. Responses render in the overlay and stay there. When you press `esc` to close, **nothing** has been added to the main session and subsequent main-thread turns don't re-read any of the side-chat exchanges, keeping the running context window lean.

```
/btw                              # open the overlay, type questions interactively
/btw does PUT replace the whole resource?
```

Inside the overlay: `enter` sends, `esc` cancels an in-flight call (or closes the overlay if idle), `ctrl+c` closes immediately. Side-chat exchanges never touch the transcript and aren't persisted to the session file.

### `/nextstep`

Asks the agent for the smallest next step and offers it as **ghost text** in the composer: a dimmed line you can accept with `tab` or `→`, edit, ignore, or type straight over. Nothing is sent until you send it, and the ask never enters the transcript — neither the question nor the answer, in memory or on disk. It costs one short model call, billed to the session like any other.

The same offer can arrive on its own, without the command, if you switch on **Suggest a next step automatically** in `/settings`: after a reply, if you go quiet at an empty composer for half a minute, terva asks once. That setting is off by default, because it spends money on terva's own initiative. It governs only the automatic offer — `/nextstep` works whether it is on or off, since a command you typed is not unbidden.

Two differences when you ask rather than wait:

- **It reports back.** A failure, or an answer of "nothing obvious to suggest", shows on the status line. The automatic offer stays silent about both: you didn't ask, so an error banner would cost you more than the feature saves.
- **It waits behind your writing.** If you start typing while the answer is in flight, the offer is held rather than thrown away, and appears if you clear the composer. It is discarded once you send something — by then it was drafted against a conversation that has moved on.

Refused while a turn or a `!` shell command is still running: the reply in progress is the next step. Ghost text only ever draws on an empty composer, so an offer can never overwrite what you are writing.

### `/swarm`

Background subagents that run alongside your main session. Each one is a separate `terva` subprocess with its own model loop, its own persistent session file, and its own chat in the dashboard — by default they all run in **the same working directory as the host**, so they see and edit the same files you do. Spawn one for a side task (“draft the migration”, “investigate this stack trace”, “write the test harness for module X”), keep going in the main thread, check in on it whenever you want.

> **Agents edit the same files you do — unless you opt into worktree
> isolation.** By default they use the same `read` / `write` / `edit` /
> `bash` tools as the main agent against the host's working directory.
> Start terva with `--swarm-worktrees` (config: `swarm_worktrees`) to give
> each sub-agent its own git worktree and branch instead. Isolation is
> leased from the built-in worktree engine (no extension required); the
> cwd must be a git repository — outside one, spawns fail loudly rather
> than silently sharing the host tree. A finished worktree is kept for
> review/merge only when it holds work — uncommitted changes, or commits
> that exist nowhere else; one that holds nothing is reclaimed when its
> sub-agent exits, taking its branch with it when that branch never
> carried a commit. `/worktree collect` view shows what each surviving
> branch carries (each lives under `$TERVA_HOME/worktrees/`).
>
> With isolation on, the main agent is told so in its system prompt, and
> each sub-agent's worktree path rides its line in the `[auto-swarm
> update]` recap. Without both, a coordinator reads a reported file path
> in its *own* tree, finds its untouched copy, and concludes the
> sub-agent did nothing. The prompt also states that leftovers in a
> finished sub-agent's worktree may be deleted once their content has
> been applied to (or verified in) the host tree.

```
/swarm                            # open the dashboard
/swarm new <task>                 # spawn an agent
/swarm new --model gpt-5 <task>    # pin the new agent to a specific model
/swarm logs <id>                  # jump straight into one agent's transcript
/swarm send <id> <text>           # send a follow-up without opening the dashboard
/swarm resume                     # pick a stopped agent to bring back
/swarm resume <id>                # bring a specific agent back
/swarm kill <id>                  # stop a running agent (its state stays)
/swarm remove <id>                # delete the agent's session and state
/swarm list                       # alias for opening the dashboard
```

**Dashboard (`/swarm` with no arg)** — a list of every agent for the current session, with status, age, and current activity. Keys:

| Key | Action |
|---|---|
| `↑` / `↓` | Move cursor between rows. |
| `enter` | Open the highlighted agent's transcript view. |
| `n` | Spawn a new agent (opens an inline task editor; inherits the host's current model). |
| `p` | One-off prompt editor for the selected row (without entering the transcript). |
| `R` | Resume a stopped agent in place. |
| `k` | Kill the selected running agent. Its session and state stay so you can resume it later. |
| `r` | Remove the selected agent entirely (session + meta gone). |
| `esc` | Close the dashboard. |

**Inside an agent's transcript** — a chat overlay with an always-on inline composer at the bottom. The conversation flows above it; type and `enter` to send a follow-up. The view auto-follows streaming output and shows an inline spinner with the agent's current activity (`thinking`, `tool: edit_file`, etc.) while it's busy. `esc` returns to the dashboard.

**Switching the spawn model from inside the editor** — while composing a task in the `n`-prompt, type `/model` on its own line and `enter`. The standard `/model` picker pops up; pick a model, the picker closes, and the editor reopens with your typed task intact and the new model pinned for the spawn.

**Session scoping** — each agent is stamped with the host session that spawned it and only shows up in that session's dashboard. Swap sessions with `/sessions` and the dashboard re-narrows accordingly. Agents from other sessions keep running in the background and reappear when you switch back.

**Persistence across terva restarts** — every spawn writes a `meta.json` next to its event log and session file under `$TERVA_HOME/swarm/agents/<id>/`. On the next `terva` launch they show up in the dashboard as **detached**; press `R` (or `/swarm resume <id>`) to bring one back. Resumed agents reattach to the same session and inbox socket, so the conversation continues from where it left off.

**Where state lives** — everything per-agent (session file, events log, inbox socket, meta) lives under `$TERVA_HOME/swarm/agents/<id>/`. The agent's actual code edits land directly in your repo; track them with normal `git status` / `git diff`.

**`/session export` does NOT bundle subagents.** A `.tervasession` is just the main chat transcript; per-agent state (session file, unix-socket inbox) is machine-local and doesn't round-trip through a JSONL file. To share what an agent said, copy it out of the transcript view manually.

**Auto-swarm.** With `/settings` -> auto-swarm on, the main agent gets a built-in `swarm_spawn` tool and a system-prompt nudge to use it. It can then fork sub-agents on its own when a request naturally splits into independent parallel work ("implement A and B", "investigate three files"). Each spawn returns the sub-agent id immediately and the main turn keeps going. The agent can pick a model strength per sub-agent with a `tier` of `weak`/`medium`/`strong` (e.g. Haiku/Sonnet/Opus on Anthropic) — never stronger than the host model, so routine sub-tasks run cheap. Only Anthropic has a built-in mapping; for any other provider (gateways like opencode-go/OpenRouter/LiteLLM) `tier` is ignored until you configure one — run `terva models tiers` to see what resolves and set per-provider tiers (see [models.md](models.md#swarm-sub-agent-tiers-weak--medium--strong)). When every sub-agent the agent spawned in that batch finishes its initial task, terva injects one `[auto-swarm update]` message back into the main chat recapping each agent's status, task, and transcript tail; the main agent then writes a short follow-up summary referencing the agents by id. Off by default; toggle from `/settings`.

**Structured deliverables.** A spawn can demand a machine-readable report
instead of trusting prose: `swarm_spawn`'s optional `deliverable_schema` (a
JSON Schema whose top level must be an object) makes the child's report data.
A native child gets a `deliver_result` tool whose argument schema *is* the
spawn schema — it calls the tool once with its findings, and a mismatch comes
back as a retryable validation error rather than a silent acceptance. A
worker that can't carry the tool (an external harness) gets the same contract
appended to its briefing and reports by ending its final message with one
fenced ` ```json ` block. Either way the supervisor re-validates when the
task ends and records the result on the agent — the parsed deliverable, or
*absent* with the concrete reason (never delivered, invalid, fence didn't
parse) — and the `[auto-swarm update]` recap marks each agent's contract
**met** or **not met**. The captured report also lands as `deliverable.json`
in the agent's state directory, next to its session file.

### `/settings`

Opens a dialog with every setting. `up`/`down` to navigate, `enter` or `space` to flip a checkbox or open an enum's option picker, `esc` to close. Persisted changes are written to `$TERVA_HOME/config.json`; no restart needed.

Most of the dialog is a **generic rendering of the daemon's settings surface** — the same single source the web panel's Settings pane renders (`packages/agent/workspace/workspace_settings.go`), so a setting added there shows up in both without a TUI change. Each row carries its own hint about when it bites: *applies live*, *applies to new sessions* (the tool set and system prompt are baked at session construction), or *per-session, not saved*.

From the daemon surface:

- **approval mode** — how tool calls are gated (plan / ask / auto-edit / workspace / yolo). The exception to everything else here: it switches the mode live for the **current session only** (like `/jail`) and is *not* persisted — the startup default comes only from an explicit `approval` key in config or the `--approval` flag, so the picker can never silently pin a mode into your config. `shift+tab` cycles the everyday three. See [permissions.md](permissions.md).
- **thinking** — reasoning effort for supported models: off (default; no reasoning), minimum (~1k tokens), low (~2k), medium (~8k), high (~16k), maximum (~32k), and `max` (the model's native maximum — GPT-5.6's ceiling, adaptive on Claude). Applies live and becomes the default for new sessions.
- **auto-title sessions** — name a session with a short model call instead of the first message line.
- **language** — the UI language. Switches live and is saved as the default. See [localization.md](localization.md).
- **background sub-agents** (auto-swarm) — let the main agent spawn sub-agents in parallel via a built-in `swarm_spawn` tool. Off by default. When on, a nested **proactive delegation** toggle appears: on (the default) the system prompt gains a short addendum telling the model to delegate independent sub-tasks; off keeps the tool but lets the agent decide when to reach for it. terva watches every sub-agent spawned, and as the last one in a batch finishes an `[auto-swarm update]` message is injected back into the chat with each agent's status / task / transcript tail. See `/swarm` for the dashboard.
- **lazy tool loading** — advertise only the core coding tools at first and let the agent pull extension/MCP tool groups in on demand (`activate_tools`), trimming the tool schemas that fill context every turn.
- **auto-condense** — when to automatically compact the transcript as the window fills: `steps` (mid-turn), `turns` (only at turn boundaries), or `off`. Applies live to every session.
- **temperature** — sampling temperature; the default defers to the model/provider. An off-preset value hand-set in `config.json` round-trips.
- **inline images** — render images inline with the terminal's image protocol, or fall back to a text placeholder. Auto-detected from `TERM_PROGRAM`; the toggle overrides the detection.
- **recursive file search** — fuzzy-search the whole tree in the `@`-mention picker (the default, matching the web composer); turn off to browse one directory at a time instead.
- **respect .gitignore** — hide git-ignored files from the `@`-mention picker.
- **lore (keyed context)** — discover and inject keyword-triggered context entries. Off is the persistent form of `--no-lore`.
- **swarm worktrees** — give each background sub-agent its own git worktree so parallel work never collides in the tree (the persistent form of `--swarm-worktrees`).
- **offer the core tool pack** — offer to install the recommended extension pack on the first run in a new workspace.

Three rows are TUI-local widgets the generic surface can't drive — a terminal-only layout, and theme discovery the daemon's fixed enum can't see:

- **color theme** — choose the built-in auto/dark/light theme (including the color-vision-friendly `daltonized` variants) or any JSON theme discovered under `$TERVA_HOME/themes` or a loaded extension. Theme files can override any subset of UI colors, syntax colors, and spinner frames/messages. Changes apply immediately; if a selected theme file is deleted, terva resets to auto. See [docs/themes.md](themes.md).
- **status line** — pick a layout preset for the status bar: `default` (the built-in three-row layout), `compact` (one row), or `detailed` (everything, including session + clock). A hand-edited `status_line.rows` in config shows up as `custom` and is never clobbered unless you pick a different preset. See the Status bar section below.
- **status: git / edits / thinking / swarm / tasks / session / clock** — show/hide individual status-bar segments on top of the current layout. Toggling writes the resulting rows to `status_line.rows`, so the config file stays the single source of truth.

### `/skills`

Opens a picker listing every discovered SKILL.md file, built-ins hidden. Each row shows the skill name, source, and description. `enter` opens the body inline (scrollable with `up`/`down`/`pgup`/`pgdn`); `esc` goes back. Re-runs discovery each time it opens, so edits to a SKILL.md during a session are reflected immediately.

### `/compact`

Sends the current transcript through the model with a structured summarization prompt. The returned summary replaces the transcript as one synthetic user message, with the last few exchanges kept verbatim for continuity. The status bar's context meter resets. Use it when the context meter creeps past ~80%.

terva also auto-compacts in the background: after any turn that leaves context usage at or above **85%** of the model's window, the agent kicks off a condense pass on its own. You'll see `condensing history, esc to cancel` above the status bar and an `(auto)` tag next to the context percentage; `esc` aborts it without touching the transcript.

### `/jail`

Enforces a sandbox rooted at the cwd shown in the status bar. `read`, `write`, and `edit` resolve their target path (including through symlinks) and refuse anything outside the sandbox. `bash` refuses obvious escape patterns: `sudo`, `rm -rf /`, leading `cd /`, `cd ..`, `cd ~`, `chmod -R`, `dd of=/`, and similar. The status bar shows `jailed, ~/your/cwd` while active.

This is a guardrail against accidents, not a hard security boundary. If you need real isolation, run terva under docker or a proper sandbox.

`/unjail` lifts it for the session. `/unjail always` records the directory in `$TERVA_HOME/unjailed.json` so it starts unjailed every time — useful for a dotfiles repo that writes into your home — and `/jail always` takes it back. terva says so on the status line at launch when a saved rule is what lowered the jail, because otherwise the only sign is the *absence* of the `jailed` badge.

## Status bar

The block above the editor is built from named **segments** laid out in rows. The default is three semantic rows — identity + spend, meters, ambient state — and rows with nothing to show vanish (an idle session with no tags is two rows):

```text
  ~/W/g/t/terva · ⎇ main* +499 -109 · Δ +120 -45 · (openai-codex) gpt-5.5 · thinking: high · ↑94k ↓1.8k · $0.529 ~$0.71/hr (sub)
  ctx 202k/272k ▓▓▓▓░ 74% · 5h ▓░░░ 15% ↻4h33m · wk ▓░░░ 8% ↻3d17h · ⛭ 2 agents
  ask mode · jailed · telegram connected
```

| segment | shows | notes |
|---|---|---|
| `replay` | the session-player scrubber | leads row 1; absent outside `terva replay` |
| `cwd` | the working directory, abbreviated (`~/W/g/t/terva`) | |
| `git` | branch, dirty `*`, `+added -removed` vs HEAD | fed by a background prober (10s + refresh at turn end and `/cd`); absent outside a repo |
| `edits` | `Δ +N -M` — lines the agent's own edit/write tools changed this session | resets on `/new` and session load |
| `model` | `(provider) model` | |
| `persona` | the persona's emoji + name, tinted with its accent color | leads the rows in `--chat`/`--play` instead of `model` |
| `thinking` | reasoning level | |
| `tokens` | `↑in ↓out R…cache-read W…cache-write` | |
| `cost` | session cost, `~$/hr` burn rate (after 10 min), `(sub)` on subscription | burn counts only spend since this run started — resumed history doesn't inflate it |
| `context` | context-window meter with percentage | `(auto)` while auto-compacting |
| `usage` | one meter per subscription window with `↻` reset countdown | provider-defined windows (e.g. 5h + weekly) |
| `swarm` | `⛭ N agents` while background agents run | |
| `session` | the session file's short name | config-only (not in the defaults) |
| `clock` | 24h wall clock | config-only |
| `tags` | approval mode, `jailed` | |
| `tasks` | the built-in task board's current task and done/total count (`▸ Wiring the panel (2/5)`) | absent when the board is empty |
| `bridge` | connected chat bridge | |
| `ext` | extension `status_segment` frames | |

Meters change color in stages as they fill (70% / 90%); the stage colors and per-segment colors are theme-controlled — see [themes.md](themes.md), including the color-vision-friendly `daltonized` built-ins.

Rearrange, drop, or re-row segments in `$TERVA_HOME/config.json` (unknown IDs are ignored; rows are open-ended):

```json
"status_line": {
  "rows": [
    ["cwd", "git", "edits", "model", "cost"],
    ["context", "usage", "swarm"],
    ["session", "clock"]
  ]
}
```

A row wider than the terminal wraps at segment boundaries rather than truncating; segments never migrate between rows on resize. In `--chat`/`--play` the workspace segments (`cwd`, `git`, `edits`, `swarm`, `tags`) stay hidden even if a config names them.

### Script segments

Define your own segments as shell commands:

```json
"status_line": {
  "rows": [["cwd", "git", "weather", "cost"], ["context", "usage"]],
  "scripts": {
    "weather": { "command": "~/bin/weather-segment.sh", "timeout_ms": 2000 }
  }
}
```

Each script runs through the platform shell (`sh -c` / `cmd /C`) with a JSON session snapshot on **stdin**; its first stdout line renders wherever `rows` names it (with no `rows` config, scripts append to the last default row). SGR colors in the output pass through; tabs, extra lines, and cursor-moving escapes are stripped. Name collisions with built-in segments lose to the built-in.

Scripts re-run on a coalesced trigger — turn end, `/cd`, and once a minute — never a free-running poll, with one child process at a time and a hard per-run timeout (`timeout_ms`, default 2000, clamped 100–10000). A timed-out run keeps the previous output; a failing script goes blank and notes `status script <name> failed` once per failure streak. Empty output hides the segment.

The stdin payload (`"schema": 1`, additive — fields get added, never renamed): `cwd`, `provider`, `model`, `reasoning`, `experience`, `session_path`/`session_name`, `persona_name`, `subscription`, `cost_usd`, `run_cost_usd` (spend since this run started), `tokens` (`input`/`output`/`cache_read`/`cache_write`), `context_used`/`context_max`, `usage_windows` (`label`/`used_percent`/`resets_at` RFC 3339), `git` (`branch`/`dirty`/`added`/`removed`, absent outside a repo), `swarm_agents`, `edits_added`/`edits_removed`, `cols`, `version`.

**Trust:** scripts are code execution from config, so they follow the same rule as hooks — only the user-layer `config.json` defines them (a project's `.terva/config.json` cannot), and a project-scoped home only activates after you trust the workspace.

## Tool display (`ctrl+t`)

Tool calls render as bordered boxes by default. `ctrl+t` cycles the transcript through four densities: **boxes** → **minimal** (one muted line per call, `· bash go test ./... — 42 lines`) → **grouped** (a run of consecutive calls between replies collapses to one muted line, `▸ 5 tool calls  bash ×3, read, edit · 1 failed`) → **hidden** (nothing at all). Failed calls stay visible as a `×` line even when hidden, and `ctrl+o` always force-expands everything back to full boxes, so nothing is more than a keystroke from recoverable. `--chat`/`--play` default to minimal.

## Sessions

Every interactive or print/json run (unless `--no-session`) writes a JSONL transcript under `$TERVA_HOME/sessions/<cwd-hash>/`. Resume any of them with `--continue`, `--resume`, `--session <path>`, or interactively via `/sessions` inside the TUI. `terva --resume` (and `terva attach --resume`) boots straight into that same picker — titles, age, model, message count, cost; `r` renames, `g` generates a title with a one-shot model call (works on old untitled sessions too, and unlike a rename it overwrites the current name — you asked for it), Esc falls through to the session the boot bound (a fresh one for `terva`, the daemon's current one attached). `--resume <id>` skips the picker and resumes the id directly. Start a fresh session without leaving the TUI via `/new`. Empty sessions (the user exited without prompting) are deleted on close so the list stays tidy.

## Inline images

When a tool returns an image (for example `read` on a PNG), terva renders it inline on terminals that support it: **Ghostty**, **Kitty**, **iTerm2**, **WezTerm**. On other terminals you see a text placeholder with MIME type, pixel dimensions, and byte size. Control with the `TERVA_INLINE_IMAGES` env var:

| Value | Effect |
|---|---|
| unset (default) | Auto-detect based on `TERM_PROGRAM`. |
| `iterm`, `iterm2` | Force the iTerm2 OSC 1337 protocol. |
| `kitty` | Force the Kitty graphics protocol. |
| `off`, `none` | Always use the text placeholder. |

Frames containing images are full-repainted (no differential diff) to prevent stale image pixels from lingering through scroll. That costs one terminal flash per image-containing frame; set `TERVA_INLINE_IMAGES=off` if that bothers you.

## Redraw rate

While a turn is streaming, terva repaints as text arrives. Each repaint costs CPU here, CPU in your terminal emulator (often more), and — over SSH — bytes on the wire. Model output reveals at reading speed, so terva caps **streaming** repaints at **30fps** by default: visually identical to painting every frame, but roughly half the frequency-bound cost. Keystroke echo at an idle prompt is unaffected (it uses a tighter interval); the cap applies only while a turn is busy.

Override with `TERVA_REDRAW_FPS`:

| Value | Effect |
|---|---|
| unset | 30fps cap (default). |
| `60`, `120`, … | Higher cap — smoother, more CPU/bandwidth. Around 60+ is effectively uncapped (the streaming pacer tops out near there). |
| `0` | Uncapped — paint every frame. |

When the variable is set, terva prints a one-line `note:` at startup so the value shows up in a bug report. Profiling builds (`-tags terva_pprof`; see [profiling.md](profiling.md)) default to **uncapped** so a CPU profile shows every redundant draw — set `TERVA_REDRAW_FPS` there to study the capped behaviour.

## Queued messages

You can keep typing while the agent is working. Pressing `enter` during a turn queues the message instead of interrupting: it shows up above the status bar as `sliding in: <text>` and is delivered as the next user turn the moment the current one finishes. Queue as many as you want; they run in order. `esc` cancels the active turn and drops the queue so a runaway turn doesn't flood you with stale follow-ups; `ctrl+c` while busy arms the exit hint instead of interrupting, a second `ctrl+c` within two seconds exits terva.

To recover the most recently queued message back into the editor (to tweak it before it runs), press `Option+↑`. In VS Code's integrated terminal that chord doesn't survive xterm.js's macOS key handling — use `Option+Shift+↑` there. terva's hint line under the sliding-in queue adapts automatically based on `$TERM_PROGRAM`.

## Setting a draft aside (`ctrl+s`)

Queuing covers messages you've already submitted; `ctrl+s` covers the one you haven't. The situation it exists for: you start composing a reply while the agent is still responding, and the response turns out to end in a question you need to answer before your draft makes sense to send. Press `ctrl+s` to set the draft aside — it parks above the status bar as `set aside: <text>` (cursor position, collapsed pastes, and pending clipboard images all preserved) and the editor clears so you can type the answer. The moment you send it, the parked draft drops back into the editor and you continue where you left off.

`ctrl+s` again brings the draft back early; pressed with a draft on both sides it swaps them. A muted hint appears once you've typed a few characters of a draft while a turn is running — the situation where you're most likely to need it — and stays through the turn's end, which is when the question you have to answer actually lands.

Slash commands also work while the agent is busy. Read-only ones (`/help`, `/jump`, `/btw`, `/sessions`, `/skills`, `/context`, `/lore`, `/memory`, `/tasks`, `/status`, `/usage`, `/resets`, `/settings`, `/permissions`, `/jail`, `/unjail`, `/exit`) take effect immediately. Destructive ones (`/new`, `/clear`, `/compact`, `/login`, `/logout`, `/model`, `/reload-ext`, `/restart`, `/trust`, `/untrust`, `/migrate`, `/cd`) cancel the active turn first and then run.


## Keys (interactive mode)

### Input

| Key | Action |
|---|---|
| `enter` | Submit (queued if the agent is busy). |
| `alt+enter`, `shift+enter` | Newline. |
| `tab` | Complete the selected slash command. |
| `shift+tab` | Cycle the approval mode: plan → workspace → auto-edit, wrapping. This session only — never persisted (`ask` and `yolo` are deliberately off the wheel; reach them from `/settings` or `--approval`). See [permissions.md](permissions.md). |
| `esc` | Cancel the current turn (while busy); clear input (while idle). |
| `ctrl+c` | Clear the input and queue (while idle) or arm the exit hint (while busy). Press again within 2s to exit. Use `esc` to cancel a running turn. |
| `ctrl+d` | Exit on empty input. |
| `ctrl+l` | Redraw the screen. |
| `ctrl+o` | Expand or collapse long tool results (read, write, edit, bash outputs over ~12 lines). Also overrides `ctrl+t`'s minimal/grouped/hidden modes with full boxes. |
| `ctrl+s` | Set the current draft aside to answer the agent first (press again to bring it back; it also returns on its own after your next message goes out). See [Setting a draft aside](#setting-a-draft-aside-ctrls). |
| `ctrl+t` | Cycle tool display: boxes → minimal one-liners → grouped → hidden. Errors stay visible; `ctrl+o` recovers everything. |
| `ctrl+v` | Paste an image from the system clipboard into the prompt (same as `/paste`). Text paste needs no key — it arrives as a bracketed paste. |
| `@` | Open the file picker. Browse files and directories in the working directory. |

### File picker (`@`)

| Key | Action |
|---|---|
| `@` | Open the file picker (type after a space or at the start of input). |
| `up`, `down` | Navigate the file list. |
| `right` | Open the selected directory. |
| `left` | Go back to the parent directory. |
| `tab` | Shell-complete the token in place: extend to the unique candidate (a directory gains `/` in recursive mode and the next tab descends) or the longest common prefix, bash dot-name rules included. Never commits — that's enter's job. |
| `enter` | Select the file or directory and insert it as a chip (`[file:name]` or `[dir:name/]`). |
| `esc` | Close the file picker. |

Type `@` followed by a filter string to narrow the list (e.g. `@read` shows only entries containing "read"). By default the picker fuzzy-searches the **whole tree** (nested paths match too — `@foobar` finds `src/foo/bar.go`), matching the web composer; the `→`/`←` browse keys apply when **recursive file search** is turned off in `/settings`, which lists one directory at a time instead. Selected files are inserted as compact chips that expand to the full path on submit. Dragged-and-dropped files and directories also collapse to chips automatically.

### Editor line navigation

| Key | Action |
|---|---|
| `ctrl+a`, `ctrl+e` | Jump to start or end of line. |
| `alt+left`, `alt+right` | Jump one word back or forward. |
| `ctrl+u`, `ctrl+k` | Delete to start or end of line. |
| `ctrl+w`, `alt+backspace` | Delete the previous word. |
| `up`, `down` (editor non-empty) | Cycle through prompt history. |

### Chat scroll

| Key | Action |
|---|---|
| `pgup`, `pgdn` | Scroll one page up or down. |
| `up`, `down` (editor empty) | Scroll three lines up or down. This is how the mouse wheel reaches the scroll logic on most terminals. |

## Changelog on update


The first time you launch a newer terva binary, the TUI shows the GitHub release notes once in a dismissible overlay. Press any key to close. The version is recorded in `config.json`'s `last_changelog_shown` so the same release notes never reappear. Fresh installs don't see a changelog (no upgrade has happened yet). The fetch is best-effort: a network failure or a missing release page silently skips, with another attempt on the next launch.
