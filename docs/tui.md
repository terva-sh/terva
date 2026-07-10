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
| `/swarm` | Spawn, monitor, and chat with background subagents. Each runs in parallel with your main session and shares its working directory. |
| `/skills` | List discovered skills (SKILL.md files) and preview their bodies. |
| `/context` | Token breakdown of the assembled context and what each extension injects. Read-only. |
| `/lore` | List this run's active lore (keyed-context) entries — name, trigger, and source — and which fired last turn. Read-only. See [debugging-prompts.md](debugging-prompts.md). |
| `/usage` | Show subscription usage limits — the 5h/weekly windows, how much is used, and when they reset — for providers that report them (OpenAI Codex today). Read-only; `esc` closes. See [providers.md](providers.md#usage-limits-usage). |
| `/compact` | Summarize the transcript into one message to free up context. |
| `/study` | Run the canned prompt "Read and understand everything in the current directory." so the agent has full project context before you start asking targeted questions. Pass a path — typed, drag-dropped, or selected via `@` — to target a specific file or directory instead: `/study [dir:packages/]`, `/study cmd/terva/main.go`. |
| `/jail` | Confine tools to the current directory. (On by default in interactive sessions; `--no-jail` starts unjailed.) |
| `/unjail` | Allow tools to touch paths outside again. |
| `/permissions` | Show the current approval mode and the active permission rules grouped by source (user/project/extension), and revoke this session's "always allow" grants: `↑`/`↓` select a grant, `r` or `del` takes it back, `R` clears them all, `esc` closes. Rules stay read-only (edit them in config). Alias: `/perms`. See [permissions.md](permissions.md). |
| `/reload-ext` | Hot-reload all extensions (re-read manifests, respawn subprocesses, rebuild tool registry). |
| `/extensions` | List installed extensions and their state; enable/disable each globally (`g`) or per-project (`p`). Alias `/ext`. |
| `/connect` | Connect, disconnect, or show status of the chat bridge (takes `connect` / `disconnect` / `status` as an optional argument; opens a picker without one). When connected, DMs from the paired user become prompts in the running session and the assistant's replies are mirrored back to the chat. The picker lists every configured service — the built-in telegram and discord connectors plus any connector extensions (tagged "extension") — and `/connect <name>` selects one directly. Aliases: `/telegram`, `/tg` (pin telegram). |
| `/settings` | Toggle persistent settings (inline images, auto-swarm, reasoning, theme) with `enter`/`space` or the option picker — saved to `$TERVA_HOME/config.json`, effective immediately. The **approval mode** picker is the exception: it switches the mode live for the **current session only** (like `/jail`), and is *not* persisted — the startup default comes only from an explicit `approval` key in config or the `--approval` flag, so the picker can never silently pin a mode into your config. |
| `/clear` | Clear the chat transcript. |
| `/exit` | Exit terva. |

Extension-registered commands appear under a divider at the bottom of the popup, sorted by name.

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

### `/swarm`

Background subagents that run alongside your main session. Each one is a separate `terva` subprocess with its own model loop, its own persistent session file, and its own chat in the dashboard — by default they all run in **the same working directory as the host**, so they see and edit the same files you do. Spawn one for a side task (“draft the migration”, “investigate this stack trace”, “write the test harness for module X”), keep going in the main thread, check in on it whenever you want.

> **Agents edit the same files you do — unless you opt into worktree
> isolation.** By default they use the same `read` / `write` / `edit` /
> `bash` tools as the main agent against the host's working directory.
> Start terva with `--swarm-worktrees` (config: `swarm_worktrees`) to give
> each sub-agent its own git worktree and branch instead. Isolation is
> leased through the `terva-git-worktree` extension (`worktree_create` /
> `worktree_release`), so that extension must be installed — if it isn't,
> spawns fail loudly rather than silently sharing the host tree. Finished
> worktrees and branches are kept for review/merge via the extension's
> `/worktree collect`.

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

### `/settings`

Opens a dialog with every persistent setting. `up`/`down` to navigate, `enter` or `space` to change the selected row, `esc` to close. Changes are written to `$TERVA_HOME/config.json` and take effect on the next turn (no restart needed). Current settings:

- **render images when supported** — draw screenshots / `read`-returned images inline using the terminal's image protocol, or fall back to a text placeholder. Auto-detected from `TERM_PROGRAM`; the toggle overrides the detection. The row is greyed out and forced off on terminals that don't speak any image protocol.
- **auto-swarm** — let the main agent spawn background sub-agents in parallel via a built-in `swarm_spawn` tool. Off by default. When on, the tool is registered with the running agent, the system prompt gains a short addendum telling the model to delegate independent sub-tasks proactively, and terva watches every sub-agent the main agent spawns. As soon as the last sub-agent in a batch finishes its initial task, an `[auto-swarm update]` message is injected back into the chat with each agent's status / task / transcript tail, so the main agent can summarise the collective outcome. Flipping off mid-session removes the tool from the live agent and strips the addendum on the next turn — the model stops trying to delegate. See `/swarm` for the dashboard that lets you monitor, message, kill, or remove the spawned agents.
- **thinking level** — choose reasoning for supported models: off (default; no reasoning), minimum (~1k tokens), low (~2k), medium (~8k), high (~16k), maximum (~32k). The change is persisted to `config.json` and applied to the running agent's next model call.
- **color theme** — choose the built-in auto/dark/light theme (including the color-vision-friendly `daltonized` variants) or any JSON theme discovered under `$TERVA_HOME/themes` or a loaded extension. Theme files can override any subset of UI colors, syntax colors, and spinner frames/messages. Changes apply immediately; if a selected theme file is deleted, terva resets to auto. See [docs/themes.md](themes.md).
- **status line** — pick a layout preset for the status bar: `default` (the built-in three-row layout), `compact` (one row), or `detailed` (everything, including session + clock). A hand-edited `status_line.rows` in config shows up as `custom` and is never clobbered unless you pick a different preset. See the Status bar section below.
- **status: git / edits / thinking / swarm / session / clock** — show/hide individual status-bar segments on top of the current layout. Toggling writes the resulting rows to `status_line.rows`, so the config file stays the single source of truth.

### `/skills`

Opens a picker listing every discovered SKILL.md file, built-ins hidden. Each row shows the skill name, source, and description. `enter` opens the body inline (scrollable with `up`/`down`/`pgup`/`pgdn`); `esc` goes back. Re-runs discovery each time it opens, so edits to a SKILL.md during a session are reflected immediately.

### `/compact`

Sends the current transcript through the model with a structured summarization prompt. The returned summary replaces the transcript as one synthetic user message, with the last few exchanges kept verbatim for continuity. The status bar's context meter resets. Use it when the context meter creeps past ~80%.

terva also auto-compacts in the background: after any turn that leaves context usage at or above **85%** of the model's window, the agent kicks off a condense pass on its own. You'll see `condensing history, esc to cancel` above the status bar and an `(auto)` tag next to the context percentage; `esc` aborts it without touching the transcript.

### `/jail`

Enforces a sandbox rooted at the cwd shown in the status bar. `read`, `write`, and `edit` resolve their target path (including through symlinks) and refuse anything outside the sandbox. `bash` refuses obvious escape patterns: `sudo`, `rm -rf /`, leading `cd /`, `cd ..`, `cd ~`, `chmod -R`, `dd of=/`, and similar. The status bar shows `jailed, ~/your/cwd` while active.

This is a guardrail against accidents, not a hard security boundary. If you need real isolation, run terva under docker or a proper sandbox.

## Status bar

The block above the editor is built from named **segments** laid out in rows. The default is three semantic rows — identity + spend, meters, ambient state — and rows with nothing to show vanish (an idle session with no tags is two rows):

```text
  ~/W/g/t/terva · ⎇ main* +499 -109 · Δ +120 -45 · (openai-codex) gpt-5.5 · thinking: high · ↑94k ↓1.8k · $0.529 ~$0.71/hr (sub)
  ctx 202k/272k ▓▓▓▓░ 74% · 5h ▓░░░ 15% ↻4h33m · wk ▓░░░ 8% ↻3d17h · ⛭ 2 agents
  ask mode · jailed · telegram connected
```

| segment | shows | notes |
|---|---|---|
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

Tool calls render as bordered boxes by default. `ctrl+t` cycles the transcript through three densities: **boxes** → **minimal** (one muted line per call, `· bash go test ./... — 42 lines`) → **hidden** (nothing at all). Failed calls stay visible as a `×` line even when hidden, and `ctrl+o` always force-expands everything back to full boxes, so nothing is more than a keystroke from recoverable. `--chat`/`--play` default to minimal.

## Sessions

Every interactive or print/json run (unless `--no-session`) writes a JSONL transcript under `$TERVA_HOME/sessions/<cwd-hash>/`. Resume any of them with `--continue`, `--resume`, `--session <path>`, or interactively via `/sessions` inside the TUI. Start a fresh one without leaving the TUI via `/new`. Empty sessions (the user exited without prompting) are deleted on close so the list stays tidy.

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

Slash commands also work while the agent is busy. Read-only ones (`/help`, `/jump`, `/btw`, `/sessions`, `/skills`, `/context`, `/lore`, `/usage`, `/settings`, `/jail`, `/unjail`, `/exit`) take effect immediately. Destructive ones (`/clear`, `/compact`, `/login`, `/logout`, `/model`, `/reload-ext`) cancel the active turn first and then run.


## Keys (interactive mode)

### Input

| Key | Action |
|---|---|
| `enter` | Submit (queued if the agent is busy). |
| `alt+enter` | Newline. |
| `tab` | Complete the selected slash command. |
| `esc` | Cancel the current turn (while busy); clear input (while idle). |
| `ctrl+c` | Clear the input and queue (while idle) or arm the exit hint (while busy). Press again within 2s to exit. Use `esc` to cancel a running turn. |
| `ctrl+d` | Exit on empty input. |
| `ctrl+l` | Redraw the screen. |
| `ctrl+o` | Expand or collapse long tool results (read, write, edit, bash outputs over ~12 lines). Also overrides `ctrl+t`'s minimal/hidden modes with full boxes. |
| `ctrl+t` | Cycle tool display: boxes → minimal one-liners → hidden. Errors stay visible; `ctrl+o` recovers everything. |
| `@` | Open the file picker. Browse files and directories in the working directory. |

### File picker (`@`)

| Key | Action |
|---|---|
| `@` | Open the file picker (type after a space or at the start of input). |
| `up`, `down` | Navigate the file list. |
| `right` | Open the selected directory. |
| `left` | Go back to the parent directory. |
| `enter` | Select the file or directory and insert it as a chip (`[file:name]` or `[dir:name/]`). |
| `esc` | Close the file picker. |

Type `@` followed by a filter string to narrow the list (e.g. `@read` shows only entries containing "read"). Selected files are inserted as compact chips that expand to the full path on submit. Dragged-and-dropped files and directories also collapse to chips automatically.

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
