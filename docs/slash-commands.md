# terva slash commands

Every command the TUI accepts, and what each one does. Type `/` in the TUI to
open the same list as an autocomplete popup; `/help` prints it.

This is a catalog to look things up in. For how the interface behaves around
them, the status bar, copying text out, inline images and the key bindings, see
[tui.md](tui.md).


Type `/` in the TUI to open the autocomplete popup. Available commands:

| Command | Description |
|---|---|
| `/help` | Show key bindings and commands. |
| `/login` | Log in via API key or subscription (opens a dialog). |
| `/logout [provider]` | Clear credentials for any logged-in provider, or all when omitted. `/logout openai-codex` clears ChatGPT/Codex subscription auth while preserving a public OpenAI API key; `/logout kimi` also disables fallback to the official Kimi Code CLI token until you log in to Kimi through terva again. |
| `/model` | Pick a model from a list (or `/model <id>` to set directly). Press `ctrl+e` on a highlighted model to edit its config; changes save to `$TERVA_HOME/models.json` and override the defaults. Press `ctrl+t` on a **provider** to view and edit its sub-agent tier ladder; see [Sub-agent tiers](#sub-agent-tiers-ctrlt). |
| `/new` | Start a fresh session in the current directory. The current session stays on disk (resume it later with `/sessions`); the transcript, context meter, and cost reset, while the provider/model stay put. |
| `/sessions` | Resume a previous session for this directory. |
| `/session` | Four ops on the current session: `export` to a portable `.tervasession` file, `import` one back in, `fork` from a past user message into a new branch, `tree` to switch between branches. Opens a picker without an argument; direct forms: `/session export [path]`, `/session import <path>`, `/session fork`, `/session tree`. Default export destination is `~/Downloads`. |
| `/jump` | Scroll the chat to a previous turn (or `/jump <text>` to filter). |
| `/copy` | Open the copy picker: choose a turn, then choose a part of it (a paragraph, a code block, a table) and copy that part **as the model wrote it** (markdown source, no left gutter, no wrap). `/copy last` copies the whole last reply outright, `/copy code` just its last fenced block, and `/copy <text>` opens the picker filtered. Also on `ctrl+y`. See [Copying text out](tui.md#copying-text-out). |
| `/btw` | Side chat with full context that doesn't add to the main thread. |
| `/nextstep` | Ask what to type next. The answer arrives as a dimmed offer in the composer, where `tab` or `→` accepts it, and nothing is sent until you send it. |
| `/swarm` | Spawn, monitor, and chat with background subagents. Each runs in parallel with your main session and shares its working directory. |
| `/worktree` | Managed git worktrees (the built-in `worktree_*` engine): the list view shows each worktree's claim state, base, and dirtiness. ↑/↓ select, ↵ `/cd`s into one, `c` switches to the merge-back **collect** overview (commits ahead of base, dirty/unpushed flags), `r` refreshes. Also fills the status bar's worktree glance. Unavailable outside a git repo. |
| `/ticket` | Open the repository's `.tickets/` work ledger in [git-ticket](https://github.com/terva-sh/git-ticket)'s own full-screen UI: browse, read, and write tickets, then come back. terva hands the terminal over for the visit and takes it back on exit, leaving the transcript and scrollback untouched: terva stays on the terminal's main screen while the ledger runs on the alternate one, so the two never contend for a buffer. Inside, `esc` steps back out of a detail view and `q` or `ctrl+c` closes the ledger. Listed only when a store governs the session cwd. `--no-ticket` and `tickets: false` leave it in place, because they drop the model's `ticket_*` tools and never the ledger itself. Refused, with the repair named, when the store's `actors:` roster is empty, because every write would be rejected, and finding that out from behind a full-screen app is worse than being told first. Same UI as `terva ticket ui`. |
| `/shared` | The files the agent handed you this session with `share_file`: name, kind, size, and how long the bytes have left. `↑`/`↓` select, `c` copies the file's path to the clipboard, `o` (or `↵`) opens it in the system viewer, `s` saves a copy into the working directory, `r` refetches, `esc` closes. `s` is the one that works from `terva attach`: the path in the listing names the **daemon's** disk, so on a remote carrier copy and open refuse and point you at save, which pulls the bytes over the control plane. A file whose deadline has passed stays listed, because the session did share it, but its actions are refused rather than dispatched into a filesystem error. Saving never overwrites: an existing name gets a `-2` suffix. |
| `/skill` | Prime your next request with a specific skill: `/skill <name> [request]` rewrites to "use the *name* skill for: request" so the model reaches for it. Autocompletes skill names after `/skill `. |
| `/skills` | List discovered skills (SKILL.md files) and preview their bodies. |
| `/reload-skills` | Re-scan the SKILL.md ladder so a skill written or edited this session is usable now, without a relaunch. Rebuilds the system prompt **only if the manifest changed**: a new, renamed, or re-described skill is announced to the model (and the next turn starts uncached); editing a body costs nothing. Reports what it found and whether it rebuilt. |
| `/context` | Token breakdown of the assembled context and what each extension injects. Under lazy tool visibility it reports both numbers honestly: the *advertised* tool set that is actually on the wire, and the *installed* total that would load if every group were activated (`[4 of 31 tools · 48 KB installed]`), plus the bytes the capability note itself costs. Read-only. |
| `/lore` | List this run's active lore (keyed-context) entries: name, trigger, and source. Read-only. It does **not** report which entries fired on the last turn: the default TUI reads lore over ctrlproto and the wire view carries no per-turn firing record. To see what actually fired, use `--dump-prompt=json` and read the `tail` sources. See [debugging-prompts.md](debugging-prompts.md). |
| `/tasks` | Show the agent's task list, the built-in task tracker the model writes to as it works. Read-only; `esc` closes. |
| `/memory` | The agent's durable memory: facts it carries into future sessions, in a project scope and a cross-project user scope, each split into an **active** tier (in the model's context on every request) and an **archived** tier (out of context until the conversation matches its keys). Archived rows are marked: `·` stored, `▸` matched last turn and injected, `✗` matched but cut to fit the tail budget. The selected one shows its triggers, so a memory keyed on words nobody would type is visible rather than silently unreachable. `d` deletes the selected entry from whichever tier owns it, `c` twice clears a scope's **active** entries (archived ones are kept, being the expensive half to rebuild, and go one at a time), `r` re-reads both tiers from disk (they are hand-editable markdown), `esc` closes. Each scope header shows how full the active tier is against the cap that refuses the next write, with the archived count alongside but outside that fraction. The status glance reads `🧠 active+archived`. Absent when `--no-memory` is set. |
| `/usage` | Show usage limits: subscription windows (5h/weekly), the provider's rate-limit windows, and any credit balance, with how much is used and when each resets. Read-only; `esc` closes. See [providers.md](providers.md#usage-limits-usage). |
| `/resets` | List the banked usage-reset credits a subscription has accrued (OpenAI Codex today) and redeem one to clear a spent window. Redeeming is irreversible and confirmed first; `esc` closes. |
| `/compact` | Summarize the transcript into one message to free up context. |
| `/continue` | Ask for the reply a turn died without producing. When a provider error kills a turn, your message stays on the transcript and nothing is running, so the session looks like it is simply waiting on you. This runs the loop again against what is already there: nothing is appended, nothing is discarded, and your prompt is not re-sent. It also finishes a reply the provider cut off partway, which needs a provider that can continue an unfinished message (Anthropic today). Refused when the session is not waiting on a reply. On resume, terva says so in the status line when a session is in this state. |
| `/study` | Run the canned prompt "Read and understand everything in the current directory." so the agent has full project context before you start asking targeted questions. Pass a path (typed, drag-dropped, or selected via `@`) to target a specific file or directory instead: `/study [dir:packages/]`, `/study cmd/terva/main.go`. |
| `/jail` | Confine tools to the current directory. (On by default in interactive sessions; `--no-jail` starts unjailed.) `/jail always` also forgets a saved unjail rule for this directory. |
| `/unjail` | Allow tools to touch paths outside again, this session only. `/unjail always` records the directory so it starts unjailed from now on (see [permissions.md](permissions.md#unjailing-a-directory-for-good)). |
| `/trust` | Trust the current directory so its project content (`.terva` extensions, skills, lore, context, permission rules) loads. `/trust parent` trusts the parent so every directory under it counts as trusted too. Project extensions become discoverable immediately (`/reload-ext` picks them up); project skills land with `/reload-skills`; the rest of the prompt-baked content lands on the next launch. |
| `/untrust` | Remove the current directory from the trust list; its project content stops loading on the next launch. |
| `/permissions` | Show the current approval mode and the active permission rules grouped by source (user/project/extension), and revoke this session's "always allow" grants: `↑`/`↓` select a grant, `r` or `del` takes it back, `R` clears them all, `esc` closes. Rules stay read-only (edit them in config). Alias: `/perms`. See [permissions.md](permissions.md). |
| `/reload-ext` | Hot-reload all extensions (re-read manifests, respawn subprocesses, rebuild tool registry). |
| `/extensions` | List installed extensions and their state; enable/disable each globally (`g`) or per-project (`p`). Alias `/ext`. |
| `/mcp` | List the configured MCP servers with their state and tool counts; enable/disable each globally or per-project. See [mcp.md](mcp.md). |
| `/connect` | Connect, disconnect, or show status of the chat bridge (takes `connect` / `disconnect` / `status` as an optional argument; opens a picker without one). When connected, DMs from the paired user become prompts in the running session and the assistant's replies are mirrored back to the chat. The picker lists every configured service: the built-in telegram and discord connectors plus any connector extensions (tagged "extension"); `/connect <name>` selects one directly. Aliases: `/telegram`, `/tg` (pin telegram). |
| `/status` | Read-only harness status, inline like `/help`: the running build (version/commit/date), process uptime, provider/model, auth, reasoning, cwd + trust state, session id and transcript file, live context usage, cumulative token/cost totals, and provider usage windows. The operator's view of what the model-facing `terva_status` tool reports, with no turn spent. |
| `/restart` | Re-exec terva into the currently-installed binary and resume this session (Tier-1 self-restart; needs `--allow-restart`). The terminal is restored before the exec, the new image reattaches to the same session, and the status line reports the build hop (`restarted, was vX, now vY`). Same semantics as web: an in-flight turn is cancelled first and given a brief, bounded window to unwind and persist before the image is replaced, and only already-persisted history is guaranteed across the hop. The agent-driven `terva_restart` tool is registered under the same flag, so the edit-own-code → reinstall → relaunch loop works from the TUI too. |
| `/settings` | Open the settings dialog: thinking level, auto-condense, lazy tool loading, lore, theme, status line, and the rest, with `enter`/`space` or the option picker. Saved to `$TERVA_HOME/config.json`, effective immediately. The **approval mode** picker is the exception: it switches the mode live for the **current session only** (like `/jail`), and is *not* persisted; the startup default comes only from an explicit `approval` key in config or the `--approval` flag, so the picker can never silently pin a mode into your config. |
| `/paste` | Paste an image from the system clipboard into the prompt as a `[clipboard image #N]` marker (the same thing `ctrl+v` does). Text paste needs neither, because it arrives as an ordinary bracketed paste. |
| `/migrate` | Move a pre-rename `zot` data directory to the terva location. <!-- rename:keep --> A one-time upgrade path; the default TUI reports it as unavailable, since it doesn't carry the interactive migrator. |
| `/clear` | Clear the chat transcript. |
| `/exit` | Exit terva. |

Extension-registered commands appear under a divider at the bottom of the popup, sorted by name.

## Sub-agent tiers (`ctrl+t`)

In the `/model` picker's **provider** list, press `ctrl+t` on a provider to see the ladder its sub-agents are spawned from: the model `swarm_spawn` gets for `tier: weak`, `medium`, `strong` or `cheap`, and the model a RAATI seat is filled with at rigor level 1.

The provider list carries a glyph per rung, in ladder order, so the state of every ladder is visible without opening any of them:

| | |
|---|---|
| `●` | Pinned by you, in `swarm_tiers`. |
| `○` | Filled by a built-in rule matched against the provider's catalog. |
| `-` | Nothing resolves, so a `tier:` spawn against this rung silently runs on the host model. |

The column is absent entirely when the ladders could not be read, rather than drawn as empty rungs: "unknown" and "unfilled" are different answers, and only one of them is a problem.

The screen shows what each rung **resolves to today**, not what your config holds, and that distinction is the reason it exists. An empty ladder in config is the ordinary case and tells you nothing about whether the ladder is right: google's medium and strong rungs once resolved to *image-generation* models, on a stock install with nothing configured, and every automated check passed. Each row names its model and where the pick came from: `built-in` (terva matched a family rule against the provider's catalog) or `override` (you pinned it).

A rung that resolves to nothing says so plainly: **falls back to the host model**. That is not "off": a sub-agent asking for a weak tier there quietly runs on your main model, at your main model's cost.

| key | |
|---|---|
| `enter` | Pick a model for the highlighted rung, from the same list `/model` uses. |
| `ctrl+t` | Cycle the rung's thinking level, over the rungs *that model* actually distinguishes. |
| `r` | Reset the rung to terva's built-in guess. |
| `esc` | Back to the provider list. |

Pinning only a thinking level is allowed and useful: it keeps the built-in model for that rung and just changes how hard it thinks. That is the cheapest way to build a ladder on a provider that ships one good model and no cheap sibling, because "K3 with thinking off" and "K3 at high" really are different amounts of compute for different money. Changes save to `swarm_tiers` in `$TERVA_HOME/config.json`; `terva models tiers` prints the same resolved view from the command line.

## Editing a model's config (`ctrl+e`)

In the `/model` picker, press `ctrl+e` on the highlighted model to open its config editor (a bare `e` is taken by the type-to-filter input). Each field is tri-state: leave it **inherit** to keep the catalog/live default, or set an explicit value to override:

- **base url**: point the model at a different endpoint (a local server, a gateway).
- **context window** / **max tokens**: correct sizes terva doesn't know for a custom model.
- **reasoning** / **image input**: capability flags (e.g. mark a local model text-only so images are dropped instead of bricking the request).

`↑`/`↓` move between fields, `enter` edits a value (or cycles a flag through inherit → on → off), `s` saves, `esc` cancels. A value being edited is a real one-line field: `←`/`→` move the cursor, `alt+←`/`alt+→` (or `ctrl+←`/`ctrl+→`) jump a word, `home`/`end` (or `ctrl+a`/`ctrl+e`) go to the ends, `delete` cuts forward, and `ctrl+u`/`ctrl+k`/`ctrl+w` kill to the start, to the end, and back a word. These are the composer's keys, and the two fields are held to the same map by a conformance test. Saving writes a *minimal* entry to `$TERVA_HOME/models.json`, only the fields you set so unset fields keep tracking the default, and applies immediately; models carrying an override are tagged `[edited]` in the picker. Press `r` to **reset**: after a `y`/`n` confirmation it removes the model's `models.json` entry, returning it to defaults. (The editor covers the operational fields above; per-model **prices** stay hand-editable in `models.json` and are preserved across edits.)

## Extensions (`/extensions`)

`/extensions` (alias `/ext`) lists every installed extension, global (`$TERVA_HOME/extensions`) and project (`.terva/extensions`), with its version, language, what it provides (commands/tools), and current state. Two independent on/off controls:

- **`g`** enables or disables **globally** by writing the extension's manifest `enabled` flag (the same field `terva ext enable/disable` uses).
- **`p`** enables or disables **for this project** by adding or removing it in `.terva/config.json`'s `disable_extensions`. This is *restrict-only*: it can switch off a globally-enabled extension here, but can't force-enable one that's disabled globally.

Toggling applies live and **surgically**: only that one extension is started or stopped (a stop is a graceful, silent shutdown), every other extension keeps running. A failed start shows as `off (not running)` (check `terva ext logs <name>`). `↑`/`↓` move between extensions, `esc` closes.

## Shell escape (`!command`)

Type `!` followed by a command to run it directly without going through the model. Everything after the `!` is passed to the same shell the `bash` tool uses (`/bin/sh -c` on Unix, `cmd /C` on Windows), runs in the session working directory, and honors the `/jail` sandbox. The output is appended below the transcript as a terminal-log block (command echo, output, exit code), styled by success or failure. It stays on screen until you send your next prompt (or run `/clear`), so it doesn't bleed into the model conversation. A running `!command` shares the busy state with the agent: `esc` cancels it, and you cannot start one while a turn (or another shell escape) is in flight.

## `/new`

Starts a fresh session in the current directory without leaving terva, the in-place equivalent of quitting and relaunching. The outgoing session is flushed to disk and left intact (resume it later via `/sessions`), then a new session file is opened and the agent's transcript, context-usage meter, and running cost reset to empty. Your provider, model, reasoning effort, and `/jail` state carry over unchanged. Unlike `/clear`, which only wipes the in-memory transcript of the *current* session, `/new` mints a genuinely new session with its own id and file. With `--no-session` there's no file to open, so `/new` simply clears the conversation.

## `/sessions`

Shows previous sessions for the current working directory, newest first, with timestamp, model, message count, cost, and the first user prompt. Pick one with `up`/`down`, `enter` to resume, `esc` to cancel. terva swaps the current session file for the selected one and replays the full transcript (including tool calls) into the agent. Sessions remember the model they ended on, so resuming picks up on that exact model even if your global default changed.

## `/session`

Four ops on the current session. `/session` alone opens a picker; each is also runnable directly.

- **`/session export [path]`**. Writes the running transcript to a portable `.tervasession` file. Default destination is `~/Downloads/<timestamp>-<session-id>-<prompt-slug>.tervasession`. Pass a path to override; a directory is fine (a dated name is built inside), a bare name gets `.tervasession` appended. The meta's cwd is stripped on the way out so the recipient doesn't see your filesystem layout.

  **What's included.** Only the main chat thread of the running session: messages, tool calls, tool results, compactions, and usage. **`/swarm` subagents are NOT included.** Their transcripts, unix-socket inboxes, and per-agent session files are all machine-local; a `.tervasession` is just a chat transcript and has no way to revive a unix socket on another box. If you want the conversation, copy it out of the dashboard manually.
- **`/session import <path>`**. Copies a `.tervasession` file into `$TERVA_HOME/sessions/<cwd-hash>/` with a fresh id and the current cwd, then switches the running agent onto it. Imported sessions are first-class: they show up in `/sessions`, `/jump`, and the tree. Drag-drop paths in the editor are accepted (terva strips the surrounding quotes automatically).
- **`/session fork`**. Opens a turn picker (same shape as `/jump`). Pick any past user message; terva copies every message up to and including that turn into a new session, records `parent` + `fork_point` in the new meta, and switches onto the branch. The parent session stays on disk. Use it to try a different question without polluting the original transcript, or to rewind after the agent went down the wrong path.
- **`/session tree`**. Shows every session in the current cwd arranged by parent/child relationships, depth-first with indent per level. The current session is tagged `[current]`. Pick any entry to switch into it. Parentless sessions are roots; branches created via `/session fork` nest under whichever session they were forked from. Orphaned children (whose parent file was deleted) still show as roots so they stay discoverable.

## `/jump`

Opens a turn picker for the current session, one row per user prompt, each showing the turn number, how many tools that turn invoked, and the first line of the prompt. `up`/`down` to pick, `enter` to jump, `esc` to cancel. Any printable rune while the picker is open extends a filter; backspace narrows it back. The filter is a one-line field with a cursor, so `←`/`→`, `home`/`end` and the kill chords work inside it, and a cursor move leaves the highlighted turn where it is. `/jump <text>` pre-applies the filter; if exactly one turn matches, terva jumps straight there without showing the picker.

Jumping is non-destructive. The transcript is untouched, the viewport just scrolls so the chosen turn is at the top. A muted line at the top of the chat reads `viewing turn N of M, pgdn to catch up`. Scroll back to the bottom with `pgdn` (or keep scrolling with the arrow keys) and the indicator goes away.

## `/btw`

Opens a side-chat overlay with the full main session as frozen context, so you can ask quick clarifying questions ("does asyncio.gather() catch exceptions?", "btw the bundle budget is 10MB", "what's the default fetch timeout?") without bloating the main thread.

Each question fires a one-off model call against `system + main transcript + side-chat history so far`. Responses render in the overlay and stay there. When you press `esc` to close, **nothing** has been added to the main session and subsequent main-thread turns don't re-read any of the side-chat exchanges, keeping the running context window lean.

```
/btw                              # open the overlay, type questions interactively
/btw does PUT replace the whole resource?
```

Inside the overlay: `enter` sends, `esc` cancels an in-flight call (or closes the overlay if idle), `ctrl+c` closes immediately. Side-chat exchanges never touch the transcript and aren't persisted to the session file.

## `/nextstep`

Asks the agent for the smallest next step and offers it as **ghost text** in the composer: a dimmed line you can accept with `tab` or `→`, edit, ignore, or type straight over. Nothing is sent until you send it, and the ask never enters the transcript, neither the question nor the answer, in memory or on disk. It costs one short model call, billed to the session like any other.

The same offer can arrive on its own, without the command, if you switch on **Suggest a next step automatically** in `/settings`: after a reply, if you go quiet at an empty composer for half a minute, terva asks once. That setting is off by default, because it spends money on terva's own initiative. It governs only the automatic offer; `/nextstep` works whether it is on or off, since a command you typed is not unbidden.

Two differences when you ask rather than wait:

- **It reports back.** A failure, or an answer of "nothing obvious to suggest", shows on the status line. The automatic offer stays silent about both: you didn't ask, so an error banner would cost you more than the feature saves.
- **It waits behind your writing.** If you start typing while the answer is in flight, the offer is held rather than thrown away, and appears if you clear the composer. It is discarded once you send something, because by then it was drafted against a conversation that has moved on.

Refused while a turn or a `!` shell command is still running: the reply in progress is the next step. Ghost text only ever draws on an empty composer, so an offer can never overwrite what you are writing.

## `/swarm`

Background subagents that run alongside your main session. Each one is a separate `terva` subprocess with its own model loop, its own persistent session file, and its own chat in the dashboard. By default they all run in **the same working directory as the host**, so they see and edit the same files you do. Spawn one for a side task (“draft the migration”, “investigate this stack trace”, “write the test harness for module X”), keep going in the main thread, check in on it whenever you want.

> **Agents edit the same files you do, unless you opt into worktree
> isolation.** By default they use the same `read` / `write` / `edit` /
> `bash` tools as the main agent against the host's working directory.
> Start terva with `--swarm-worktrees` (config: `swarm_worktrees`) to give
> each sub-agent its own git worktree and branch instead. Isolation is
> leased from the built-in worktree engine (no extension required); the
> cwd must be a git repository; outside one, spawns fail loudly rather
> than silently sharing the host tree. A finished worktree is kept for
> review/merge only when it holds work: uncommitted changes, or commits
> that exist nowhere else. One that holds nothing is reclaimed when its
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
/swarm new --reasoning low <task>  # pin how hard it thinks (the --thinking ladder)
/swarm logs <id>                  # jump straight into one agent's transcript
/swarm send <id> <text>           # send a follow-up without opening the dashboard
/swarm resume                     # pick a stopped agent to bring back
/swarm resume <id>                # bring a specific agent back
/swarm kill <id>                  # stop a running agent (its state stays)
/swarm remove <id>                # delete the agent's session and state
/swarm list                       # alias for opening the dashboard
```

**Dashboard (`/swarm` with no arg)** is a list of every agent for the current session, with status, age, and current activity. Keys:

| Key | Action |
|---|---|
| `↑` / `↓` | Move cursor between rows. |
| `enter` | Open the highlighted agent's transcript view. |
| `n` | Spawn a new agent (opens an inline task editor; inherits the host's current model). |
| `p` | One-off prompt editor for the selected row (without entering the transcript). |
| `R` | Resume a stopped agent in place. |
| `k` | Kill the selected running agent. Its session and state stay so you can resume it later. |
| `a` | Archive the selected agent: compress its record under `$TERVA_HOME/swarm/archive/` and drop it from the list. One-way, and terva cannot read it back. |
| `r` | Remove the selected agent entirely (session + meta gone). |
| `s` | Sweep now: archive every finished agent past the retention age (see below). |
| `esc` | Close the dashboard. |

**Retention.** Finished agents do not pile up forever. On startup, and whenever you press `s`, terva archives every sub-agent that reached a terminal state (`done`, `failed`, `killed`, or `detached`) longer than `swarm_retention_days` ago. The default is 7 days; set the key to `0` to switch the sweep off.

Three things bound it, and they are the whole safety story:

- **It archives, and never removes.** The record is gzipped under `$TERVA_HOME/swarm/archive/` and recoverable with `gunzip` alone. No automatic path in terva deletes a transcript.
- **It never touches a live agent.** A `running` or `pending` sub-agent is left alone however long it has been quiet. An idle agent is usually one waiting on its inbox for a follow-up turn, and elapsed silence cannot tell that apart from one waiting on a slow tool.
- **An 8-hour floor outranks the setting.** Whatever `swarm_retention_days` says, nothing that went quiet in the last 8 hours is swept. A small value cannot reach an agent that finished this morning.

Why it exists: `Reload` reads the tail of every agent's event log at every launch, so a directory that only grows makes every start slower.

**Inside an agent's transcript** is a chat overlay with an always-on inline composer at the bottom. The conversation flows above it; type and `enter` to send a follow-up. The view auto-follows streaming output and shows an inline spinner with the agent's current activity (`thinking`, `tool: edit_file`, etc.) while it's busy. `esc` returns to the dashboard.

**Switching the spawn model from inside the editor:** while composing a task in the `n`-prompt, type `/model` on its own line and `enter`. The standard `/model` picker pops up; pick a model, the picker closes, and the editor reopens with your typed task intact and the new model pinned for the spawn.

**Session scoping.** Each agent is stamped with the host session that spawned it and only shows up in that session's dashboard. Swap sessions with `/sessions` and the dashboard re-narrows accordingly. Agents from other sessions keep running in the background and reappear when you switch back.

**Persistence across terva restarts.** Every spawn writes a `meta.json` next to its event log and session file under `$TERVA_HOME/swarm/agents/<id>/`. On the next `terva` launch they show up in the dashboard as **detached**; press `R` (or `/swarm resume <id>`) to bring one back. Resumed agents reattach to the same session and inbox socket, so the conversation continues from where it left off.

**Where state lives.** Everything per-agent (session file, events log, inbox socket, meta) lives under `$TERVA_HOME/swarm/agents/<id>/`. The agent's actual code edits land directly in your repo; track them with normal `git status` / `git diff`.

**`/session export` does NOT bundle subagents.** A `.tervasession` is just the main chat transcript; per-agent state (session file, unix-socket inbox) is machine-local and doesn't round-trip through a JSONL file. To share what an agent said, copy it out of the transcript view manually.

**Auto-swarm.** With `/settings` -> auto-swarm on, the main agent gets a built-in `swarm_spawn` tool and a system-prompt nudge to use it. It can then fork sub-agents on its own when a request naturally splits into independent parallel work ("implement A and B", "investigate three files"). Each spawn returns the sub-agent id immediately and the main turn keeps going. The agent can pick a model strength per sub-agent with a `tier` of `weak`/`medium`/`strong`, never stronger than the host model so routine sub-tasks run cheap, or `cheap`, which is a cost tier rather than a strength and is not capped. A rung is not always a different model: on Anthropic and Codex the built-in ladders vary the *thinking level* on the same model, because low thinking on the largest model beats the middle model outright. Several providers ship a built-in mapping; for one that does not (gateways like opencode-go/OpenRouter/LiteLLM) `tier` is ignored until you configure one. Run `terva models tiers` to see what resolves and set per-provider tiers (see [models.md](models.md#swarm-sub-agent-tiers-weak--medium--strong--cheap)). When every sub-agent the agent spawned in that batch finishes its initial task, terva injects one `[auto-swarm update]` message back into the main chat recapping each agent's status, task, and transcript tail; the main agent then writes a short follow-up summary referencing the agents by id. Off by default; toggle from `/settings`.

**Structured deliverables.** A spawn can demand a machine-readable report
instead of trusting prose: `swarm_spawn`'s optional `deliverable_schema` (a
JSON Schema whose top level must be an object) makes the child's report data.
A native child gets a `deliver_result` tool whose argument schema *is* the
spawn schema, so it calls the tool once with its findings, and a mismatch comes
back as a retryable validation error rather than a silent acceptance. A
worker that can't carry the tool (an external harness) gets the same contract
appended to its briefing and reports by ending its final message with one
fenced ` ```json ` block. Either way the supervisor re-validates when the
task ends and records the result on the agent: the parsed deliverable, or
*absent* with the concrete reason (never delivered, invalid, fence didn't
parse). The `[auto-swarm update]` recap then marks each agent's contract
**met** or **not met**. The captured report also lands as `deliverable.json`
in the agent's state directory, next to its session file.

## `/settings`

Opens a dialog with every setting. `up`/`down` to navigate, `enter` or `space` to flip a checkbox or open an enum's option picker, `esc` to close. Persisted changes are written to `$TERVA_HOME/config.json`; no restart needed.

Most of the dialog is a **generic rendering of the daemon's settings surface**, the same single source the web panel's Settings pane renders (`packages/agent/workspace/workspace_settings.go`), so a setting added there shows up in both without a TUI change. Each row carries its own hint about when it bites: *applies live*, *applies to new sessions* (the tool set and system prompt are baked at session construction), or *per-session, not saved*.

From the daemon surface:

- **approval mode**: how tool calls are gated (plan / ask / auto-edit / workspace / yolo). The exception to everything else here: it switches the mode live for the **current session only** (like `/jail`) and is *not* persisted; the startup default comes only from an explicit `approval` key in config or the `--approval` flag, so the picker can never silently pin a mode into your config. `shift+tab` cycles the everyday three. See [permissions.md](permissions.md).
- **thinking**: reasoning effort for supported models: off (default; no reasoning), minimum (~1k tokens), low (~2k), medium (~8k), high (~16k), maximum (~32k), and `max` (the model's native maximum: the GPT-5.6 and GPT-6 ceiling, adaptive on Claude). Applies live and becomes the default for new sessions.
- **auto-title sessions**: name a session with a short model call instead of the first message line.
- **language**: the UI language. Switches live and is saved as the default. See [localization.md](localization.md).
- **background sub-agents** (auto-swarm): let the main agent spawn sub-agents in parallel via a built-in `swarm_spawn` tool. Off by default. When on, a nested **proactive delegation** toggle appears: on (the default) the system prompt gains a short addendum telling the model to delegate independent sub-tasks; off keeps the tool but lets the agent decide when to reach for it. terva watches every sub-agent spawned, and as the last one in a batch finishes an `[auto-swarm update]` message is injected back into the chat with each agent's status / task / transcript tail. See `/swarm` for the dashboard.
- **lazy tool loading**: advertise only the core coding tools at first and let the agent pull extension/MCP tool groups in on demand (`activate_tools`), trimming the tool schemas that fill context every turn.
- **auto-condense**: when to automatically compact the transcript as the window fills: `steps` (mid-turn), `turns` (only at turn boundaries), or `off`. Applies live to every session.
- **temperature**: sampling temperature; the default defers to the model/provider. An off-preset value hand-set in `config.json` round-trips.
- **inline images**: render images inline with the terminal's image protocol, or fall back to a text placeholder. Auto-detected from `TERM_PROGRAM`; the toggle overrides the detection.
- **recursive file search**: fuzzy-search the whole tree in the `@`-mention picker (the default, matching the web composer); turn off to browse one directory at a time instead.
- **respect .gitignore**: hide git-ignored files from the `@`-mention picker.
- **lore (keyed context)**: discover and inject keyword-triggered context entries. Off is the persistent form of `--no-lore`.
- **swarm worktrees**: give each background sub-agent its own git worktree so parallel work never collides in the tree (the persistent form of `--swarm-worktrees`).
- **offer the core tool pack**: offer to install the recommended extension pack on the first run in a new workspace.

Three rows are TUI-local widgets the generic surface can't drive: a terminal-only layout, and theme discovery the daemon's fixed enum can't see:

- **color theme**: choose the built-in auto/dark/light theme (including the color-vision-friendly `daltonized` variants) or any JSON theme discovered under `$TERVA_HOME/themes` or a loaded extension. Theme files can override any subset of UI colors, syntax colors, and spinner frames/messages. Changes apply immediately; if a selected theme file is deleted, terva resets to auto. See [docs/themes.md](themes.md).
- **status line**: pick a layout preset for the status bar: `default` (the built-in three-row layout), `compact` (one row), or `detailed` (everything, including session + clock). A hand-edited `status_line.rows` in config shows up as `custom` and is never clobbered unless you pick a different preset. See the Status bar section below.
- **status: git / edits / thinking / swarm / tasks / session / clock**: show or hide individual status-bar segments on top of the current layout. Toggling writes the resulting rows to `status_line.rows`, so the config file stays the single source of truth.

## `/skills`

Opens a picker listing every discovered SKILL.md file, built-ins hidden. Each row shows the skill name, source, and description. `enter` opens the body inline (scrollable with `up`/`down`/`pgup`/`pgdn`); `esc` goes back. Re-runs discovery each time it opens, so edits to a SKILL.md during a session are reflected immediately, and `r` re-scans without closing.

Opening the picker refreshes the *catalog* only, and never touches the system prompt, so browsing your skills can never cost you a prompt cache. That also means the model's manifest is unchanged: a skill added this way is loadable when you name it, but the model has not been told it exists. `/reload-skills` is the command that does both.

## `/reload-skills`

Re-runs the discovery ladder, swaps the session's live catalog, and rebuilds the system prompt **only when the manifest actually changed**.

That split is the point. Making a skill loadable is free; announcing it to the model means changing the pinned prompt prefix, which discards the provider's cached request prefix so the next turn re-reads the transcript uncached. So a new skill, or a renamed one, or a changed `description`, rebuilds and says it did, while editing a skill's **body**, the usual beat of writing one, costs nothing and still serves the model the rewritten text the next time it loads the skill.

It re-reads the trust verdict too, so `terva trust` followed by `/reload-skills` brings a new project skill live in the running session.

Over ACP the command exists but does half the job: the catalog is swapped, so the skill loads when you name it, but the manifest cannot be rebuilt mid-session and the model only learns of the skill on a new session. The confirmation says so.

## `/compact`

Sends the current transcript through the model with a structured summarization prompt. The returned summary replaces the transcript as one synthetic user message, with the last few exchanges kept verbatim for continuity. The status bar's context meter resets. Use it when the context meter creeps past ~80%.

terva also auto-compacts in the background: after any turn that leaves context usage at or above **85%** of the model's window, the agent kicks off a condense pass on its own. You'll see `condensing history, esc to cancel` above the status bar and an `(auto)` tag next to the context percentage; `esc` aborts it without touching the transcript.

## `/jail`

Enforces a sandbox rooted at the cwd shown in the status bar. `read`, `write`, and `edit` resolve their target path (including through symlinks) and refuse anything outside the sandbox. `bash` refuses obvious escape patterns: `sudo`, `rm -rf /`, leading `cd /`, `cd ..`, `cd ~`, `chmod -R`, `dd of=/`, and similar. The status bar shows `jailed, ~/your/cwd` while active.

This is a guardrail against accidents, not a hard security boundary. If you need real isolation, run terva under docker or a proper sandbox.

`/unjail` lifts it for the session. `/unjail always` records the directory in `$TERVA_HOME/unjailed.json` so it starts unjailed every time, which is useful for a dotfiles repo that writes into your home, and `/jail always` takes it back. terva says so on the status line at launch when a saved rule is what lowered the jail, because otherwise the only sign is the *absence* of the `jailed` badge.
