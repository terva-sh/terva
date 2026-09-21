# The terva TUI

Everything interactive mode does beyond typing prompts: attaching to a
daemon, the status bar, copying text out, sessions, inline images, message
queueing, and key bindings.

The slash-command catalog has its own page, [slash-commands.md](slash-commands.md).
Flags and run modes live in [cli.md](cli.md).

## Slash commands

The command catalog moved to its own page. See
[slash-commands.md](slash-commands.md) for every command, and `/help` or the
`/` popup for the same list inside a running session.

## Attaching to a running daemon (`terva attach`)

`terva attach [URL]` runs this same TUI as a **client** of a running `terva
web` daemon instead of hosting the workspace in-process. **With no URL it finds
the daemon serving this `$TERVA_HOME`**: `terva web` publishes its bound endpoint
to `$TERVA_HOME/listen.json` when it starts and removes it when it stops, so a
bare `terva attach` reaches a daemon on a filesystem socket without being told
where. A stale record (the daemon crashed, and the file is heartbeated) is ignored,
and the old `ws://127.0.0.1:8730/ws` default still applies when nothing is
serving. (Explicit URL forms: `--token` matches the daemon's `--web-token`, or
`--token-file PATH` / `TERVA_WEB_TOKEN` to keep the secret off the command line,
the same three sources the daemon reads under its own `--web-token*` spellings
(see the persistent-attach note below); bare `host:port` and `http(s)://` forms normalize, and
`unix:/path/to.sock` targets a daemon serving a filesystem socket, where no token
is needed because the socket file's permissions gate access). Sessions, credentials, extensions,
and tools all live daemon-side. The TUI renders and controls, and the
browser panel can watch the same session simultaneously.

The status line tells the daemon's truth, not the local process's: the daemon
advertises its working directory and sandbox lock in the hello, and the wire
carries the session's context window, transcript name, and subscription flag,
so the cwd/git segments, ctx gauge, `(sub)` cost tag, jailed badge, and
thinking level all describe the workspace you're attached to, from whatever
directory you launched. `/swarm` and the status bar's swarm glance ride the
daemon's tasks surface.

The connection is self-healing: if the daemon restarts (its `/restart`, the
`terva_restart` tool, or a crash-and-relaunch), the TUI shows "connection lost,
reconnecting…", re-subscribes, resyncs from the snapshot, and announces
`daemon restarted: vX → vY` when the build changed. The inverse is the
quality-of-life win for iterating on terva itself: quit the TUI mid-turn,
reinstall it, re-attach, and the agent never noticed. Note that `/restart` from
an attached TUI restarts the **daemon**, over the wire, not this client.

The client can restart **itself**, too. Started with `--allow-restart`,
`terva attach` re-execs into the freshly-installed binary and reconnects on
**SIGHUP**, on the same Tier-1 mechanics the daemon uses (terminal restored first,
same session rebound, a brief outage while the new client boots). That makes a
persistent attach (one supervised in a systemd unit or a long-lived
tmux/screen pane) pick up a new build via `systemctl --user reload …` /
`kill -HUP`, the client-side counterpart to reloading the daemon. It is off
unless `--allow-restart` is passed: an attach client owns a terminal, where
SIGHUP is *also* the hangup signal, so without the flag SIGHUP keeps its default
(exit on hangup). Reserve it for those persistent, non-throwaway attaches.
`examples/deploy/systemd/` ships a ready-made socket-activated daemon + `dtach`
attach that does exactly this; see docs/web.md §"A persistent terminal".

A persistent attach lives as long as the daemon, so a `--token` on its command
line sits in the unit file and in `ps` / `/proc/<pid>/cmdline` for every local
user to read, the same argv leak the daemon has. When you attach across a
network to an authenticated daemon (rather than the local `unix:` socket, whose
file permissions are the auth), reach for `--token-file PATH` (the client's
spelling of the daemon's `--web-token-file`), so the token is read from disk and
never touches the command line, exactly what systemd's `LoadCredential=`
provides. An unreadable or empty file is fatal, not a silent token-less dial.
`TERVA_WEB_TOKEN` (systemd `EnvironmentFile=`) is the middle
ground; since an attach client runs no agent shell, the environment is a safer
place for it than it is on the daemon.

The `@`-file picker lists the daemon's tree **over the wire** (the
`files.list` verb, advertised as the `files-list` hello feature), correct
from any host, with the same gitignore filtering and caps as the local
picker; against an older daemon it falls back to reading local disk at the
daemon's advertised cwd (same-host correct).

v1 boundaries: the git probe and model-catalog reads are local, pointed at
the daemon's advertised cwd, so they're truthful from any directory on the
daemon's host and degrade to absent cross-host; session file operations
(`/session` export/import/fork/tree) and `/login` are daemon-side concerns
and degrade with a clear message; `/jail` and extension pickers likewise.
See docs/proposals/orchestration-frontend.md for the trajectory (the
sessions board over N subscriptions).

## Copying text out

Selecting with the mouse copies what is **on the screen**, and the screen is not
the source. Every prose row carries terva's two-column left gutter, and any line
longer than the pane carries a newline terva put there to make it fit. For prose
that is cosmetic. For a block of Python it is the difference between code that
runs and code that raises `IndentationError` on its first line, and for a URL it
is the difference between a link and two halves of one.

Two things address that.

**`ctrl+y`** (or `/copy`) opens the copy picker, which hands back source instead
of render. It has two stages. The first lists turns, the way `/jump` does. `enter`
descends into one, and the second lists that turn's parts: every paragraph, code
fence, list, quote and table separately, alongside your own prompt and any
recorded thinking. `enter` copies the highlighted part.

The second stage is the point of the whole thing: what you usually want is a
piece out of the *body* of a reply, and copying the whole answer to get one
paragraph is the same problem as dragging it out of the terminal. A preview pane
under the list shows the highlighted part in full, so two similar paragraphs are
told apart before the clipboard changes.

Type to filter either list, with the same cursor keys the other pickers take.
The block kind is part of the match, so `fence` finds the code in a turn without
knowing a word inside it. `esc` backs out one stage before it closes, and
`ctrl+y` copies the whole reply of the turn under the cursor from either stage,
the escape hatch for when you did want all of it. Tool calls and their results
are never offered: they are most of a transcript and none of what people copy.

**`/copy last`** keeps the old one-shot behaviour, the whole last reply with no
picker, and `/copy code` narrows that to the last fenced code block in it,
including one the model never finished, since a cancelled or truncated reply is
when you are most likely to want it. The picker keeps a fence's ``` markers so it
pastes as code; `/copy code` strips them, as it always has.

Locally this runs `pbcopy` / `wl-copy` / `xclip`. Over ssh those would address the
wrong machine, so terva instead asks **your** terminal to set **its** clipboard
(OSC 52). That route has no reply to read, so the status line says which one ran:
a terminal configured to refuse clipboard writes leaves you with a success
message and an unchanged clipboard. Set `TERVA_CLIPBOARD=local` or `terminal` to
pick the route yourself.

**Hyperlinks.** On a terminal that supports OSC 8, URLs in replies, in recorded
thinking, and the login URL in `/login` are emitted as real hyperlinks: the target travels out of band, so
cmd/ctrl+click opens the whole URL even when the visible text is split across
rows. The dialog also binds `c` to copy the login URL, which is the answer for a
terminal without hyperlink support, or a login you are finishing on your phone.

Support is detected from the environment (iTerm2, Ghostty, kitty, WezTerm, VS
Code, Windows Terminal, Rio, recent VTE). Detection is off inside `tmux` and
`screen`, where a multiplexer that does not pass the sequence through would smear
the URL across the transcript as literal text. Override with
`TERVA_HYPERLINKS=on` or `off`. Terminals without the extension ignore the
sequence and render exactly what they rendered before, so the visible layout is
byte-for-byte identical either way.

## Status bar

The block above the editor is built from named **segments** laid out in rows. Each default row has a left group and a right group around a flexible gap: row 1 is place (cwd, git state, the agent's edits) with the two glance segments, model and cost, pinned in the top-right corner; row 2 is the meters left with thinking effort and token counters right; row 3 is ambient state left, glances right. Rows with nothing to show vanish (an idle session with no tags is two rows):

```text
  ~/W/g/t/terva · ⎇ main* +499 -109 · Δ +120 -45      (openai-codex) gpt-5.5 · $0.529 ~$0.71/hr (sub)
  ctx 202k/272k ▓▓▓▓▓▓░░ 74% · 5h ▓░░░░░ 15% ↻4h33m      thinking: high · ↑94k ↓1.8k
  ask mode · jailed · ⛭ 2 agents                        telegram connected
```

While a turn runs, the busy line (spinner, quip, elapsed time) renders as its own row directly above the bar and disappears when the turn ends. The segments themselves never shift. If that one-row vertical step bothers you, `status_line.reserve_busy_row: true` keeps the row present and blank while idle, trading a line of chat height for zero motion.

Rows lay out inside a content width of `min(columns, status_line.max_width)`, with a two-cell inset held clear at each edge. That inset is the same margin a tool box uses, so the bar's left and right edges land on the columns where the box corners above them sit. The default is uncapped, so on a wide terminal the bar spans the chat column. A cap costs no information: the atoms are the same either way, and only the flex gap between spacer groups shrinks. Set `"max_width": 140` when you would rather read the bar as one block than track a group at each edge of a wide terminal.

| segment | shows | notes |
|---|---|---|
| `replay` | the session-player scrubber | leads row 1; absent outside `terva replay` |
| `cwd` | the working directory, abbreviated (`~/W/g/t/terva`) | `cwd:full` skips the abbreviation |
| `git` | branch, dirty `*`, `+added -removed` vs HEAD | fed by a background prober (10s + refresh at turn end and `/cd`); absent outside a repo |
| `edits` | `Δ +N -M`, lines the agent's own edit/write tools changed this session | resets on `/new` and session load |
| `model` | `(provider) model` | |
| `persona` | the persona's emoji + name, tinted with its accent color | leads the rows in `--chat`/`--play` instead of `model` |
| `thinking` | reasoning level | |
| `tokens` | `↑in ↓out R…cache-read W…cache-write` | `tokens:io` keeps only ↑in ↓out |
| `cost` | session cost, `~$/hr` burn rate (after 10 min), `(sub)` on subscription | burn counts only spend since this run started, so resumed history doesn't inflate it |
| `context` | context-window meter with percentage | `(auto)` while auto-compacting; `context:bar=N` pins the meter width |
| `usage` | one meter per subscription window with `↻` reset countdown | provider-defined windows (e.g. 5h + weekly); `usage:bar=N` pins the meter width |
| `swarm` | `⛭ N agents` while background agents run | |
| `session` | the session file's short name | config-only (not in the defaults); `session:short` keeps only the trailing hash |
| `clock` | 24h wall clock | config-only |
| `tags` | approval mode, `jailed` | |
| `tasks` | the built-in task board's current task and done/total count (`▸ Wiring the panel (2/5)`) | absent when the board is empty |
| `bridge` | connected chat bridge | |
| `ext` | extension `status_segment` frames | |
| `spacer` | a flexible gap | pseudo-segment: absorbs the slack between the segments before and after it, so a row can pin a group against the right edge; several spacers split the slack evenly |

Meters change color in stages as they fill (70% / 90%); the stage colors and per-segment colors are theme-controlled; see [themes.md](themes.md), including the color-vision-friendly `daltonized` built-ins. Meter bars also scale with the bar's effective width, which is the terminal width until you set a cap: 5 context / 4 usage cells below 100 columns, 8/6 from 100, 12/8 from 140. A cap below 140 therefore also caps the meters. A meter that is in use but rounds to zero filled cells marks its first cell `▒`, so a 2% window and an untouched one read apart. `bar=N` pins a width and bypasses the tiers.

Rearrange, drop, or re-row segments in `$TERVA_HOME/config.json` (unknown IDs are ignored; rows are open-ended):

```json
"status_line": {
  "max_width": 140,
  "rows": [
    ["cwd", "git", "edits", "spacer", "model", "cost"],
    ["context:bar=10", "usage", "spacer", "tokens:io"],
    ["session:short", "clock"]
  ]
}
```

An entry is a segment id plus optional suffix options: `id:flag` or `id:key=value`, comma-separated (`"tokens:io"`, `"context:bar=10"`). Unknown options are skipped with the same silence as unknown ids, so a config survives renames and older binaries. The full option set:

| option | effect |
|---|---|
| `tokens:io` | `↑134k ↓229k` only; the cache totals stay in `/usage` |
| `context:bar=N`, `usage:bar=N` | pin the meter width, bypass the width tiers |
| `session:short` | the trailing hash only (`sess 83c2dcf8`) |
| `cwd:full` | no path abbreviation |

A row wider than the effective width collapses its spacers to the ordinary `·` separator and wraps at segment boundaries rather than truncating; segments never migrate between rows on resize. In `--chat`/`--play` the workspace segments (`cwd`, `git`, `edits`, `swarm`, `tags`) stay hidden even if a config names them.

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

Each script runs through the platform shell (`sh -c` / `cmd /C`) with a JSON session snapshot on **stdin**; its first stdout line renders wherever `rows` names it (with no `rows` config, scripts append to the last default row). SGR colors in the output pass through; tabs, extra lines, and cursor-moving escapes are stripped. Name collisions with built-in segments lose to the built-in. A script whose name contains `:` still matches its `rows` entry exactly, ahead of the options-suffix parse, so such a name keeps working.

Scripts re-run on a coalesced trigger (turn end, `/cd`, and once a minute) and never a free-running poll, with one child process at a time and a hard per-run timeout (`timeout_ms`, default 2000, clamped 100–10000). A timed-out run keeps the previous output; a failing script goes blank and notes `status script <name> failed` once per failure streak. Empty output hides the segment.

The stdin payload (`"schema": 1`, additive, so fields get added and never renamed): `cwd`, `provider`, `model`, `reasoning`, `experience`, `session_path`/`session_name`, `persona_name`, `subscription`, `cost_usd`, `run_cost_usd` (spend since this run started), `tokens` (`input`/`output`/`cache_read`/`cache_write`), `context_used`/`context_max`, `usage_windows` (`label`/`used_percent`/`resets_at` RFC 3339), `git` (`branch`/`dirty`/`added`/`removed`, absent outside a repo), `swarm_agents`, `edits_added`/`edits_removed`, `cols`, `version`.

**Trust:** scripts are code execution from config, so they follow the same rule as hooks: only the user-layer `config.json` defines them (a project's `.terva/config.json` cannot), and a project-scoped home only activates after you trust the workspace.

## Tool display (`ctrl+t`)

Tool calls render as bordered boxes by default. `ctrl+t` cycles the transcript through four densities: **boxes** → **minimal** (one muted line per call, `· bash go test ./... — 42 lines`) → **grouped** (a run of consecutive calls between replies collapses to one muted line, `▸ 5 tool calls  bash ×3, read, edit · 1 failed`) → **hidden** (nothing at all). Failed calls stay visible as a `×` line even when hidden, and `ctrl+o` always force-expands everything back to full boxes, so nothing is more than a keystroke from recoverable. `--chat`/`--play` default to minimal.

## Sessions

Every interactive or print/json run (unless `--no-session`) writes a JSONL transcript under `$TERVA_HOME/sessions/<cwd-hash>/`. Resume any of them with `--continue`, `--resume`, `--session <path>`, or interactively via `/sessions` inside the TUI. `terva --resume` (and `terva attach --resume`) boots straight into that same picker, showing titles, age, model, message count, cost; `r` renames, `g` generates a title with a one-shot model call (works on old untitled sessions too, and unlike a rename it overwrites the current name, because you asked for it), Esc falls through to the session the boot bound (a fresh one for `terva`, the daemon's current one attached). `--resume <id>` skips the picker and resumes the id directly. Start a fresh session without leaving the TUI via `/new`. Empty sessions (the user exited without prompting) are deleted on close so the list stays tidy.

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

While a turn is streaming, terva repaints as text arrives. Each repaint costs CPU here, CPU in your terminal emulator (often more), and, over SSH, bytes on the wire. Model output reveals at reading speed, so terva caps **streaming** repaints at **30fps** by default: visually identical to painting every frame, but roughly half the frequency-bound cost. Keystroke echo at an idle prompt is unaffected (it uses a tighter interval); the cap applies only while a turn is busy.

Override with `TERVA_REDRAW_FPS`:

| Value | Effect |
|---|---|
| unset | 30fps cap (default). |
| `60`, `120`, … | Higher cap: smoother, more CPU/bandwidth. Around 60+ is effectively uncapped (the streaming pacer tops out near there). |
| `0` | Uncapped, painting every frame. |

When the variable is set, terva prints a one-line `note:` at startup so the value shows up in a bug report. Profiling builds (`-tags terva_pprof`; see [profiling.md](profiling.md)) default to **uncapped** so a CPU profile shows every redundant draw; set `TERVA_REDRAW_FPS` there to study the capped behaviour.

## Busy signal (`TERVA_PROGRESS`)

While a turn is in flight terva can tell the terminal it is working, using the **OSC 9;4** progress sequence (a ConEmu extension, since adopted by Windows Terminal and WezTerm). Terminals that implement it show an indeterminate progress indicator on the tab or taskbar, so a terva working in a background window says so without being looked at.

Exactly two sequences are emitted per turn: one when it goes busy, one when it goes idle. The indicator is also cleared on exit, so a terva killed mid-turn cannot leave a stuck progress bar on the tab behind it.

| Value | Effect |
|---|---|
| unset (default) | Auto-detect: on for ConEmu, Windows Terminal, and WezTerm. Off everywhere else. |
| `on` | Always emit. |
| `off` | Never emit. |

The auto-detect allowlist is deliberately short, because an unrecognised terminal is not a safe bet here the way it is for the OSC 8 hyperlinks above. A bare `OSC 9`, without the `;4`, is iTerm2's "post a desktop notification" extension, so a terminal implementing *that* but not the progress sub-parameter would pop a toast reading `4;3;0` on every single turn instead of harmlessly ignoring the sequence. iTerm2 and Ghostty are therefore left out of the sniff; set `TERVA_PROGRESS=on` if yours handles it. Multiplexers (tmux, screen) are excluded too, since they do not forward the sequence to the outer terminal anyway.

Because the sequence is fixed and carries no theme data, it is also the reliable way to find "when was the agent actually working?" in a terminal recording; see [recording.md](recording.md).

## Queued messages

You can keep typing while the agent is working. Pressing `enter` during a turn queues the message instead of interrupting: it shows up above the status bar as `sliding in: <text>` and is delivered as the next user turn the moment the current one finishes. Queue as many as you want; they run in order. `esc` cancels the active turn and drops the queue so a runaway turn doesn't flood you with stale follow-ups; `ctrl+c` while busy arms the exit hint instead of interrupting, a second `ctrl+c` within two seconds exits terva.

To recover the most recently queued message back into the editor (to tweak it before it runs), press `Option+↑`. In VS Code's integrated terminal that chord doesn't survive xterm.js's macOS key handling, so use `Option+Shift+↑` there. terva's hint line under the sliding-in queue adapts automatically based on `$TERM_PROGRAM`.

## Setting a draft aside (`ctrl+s`)

Queuing covers messages you've already submitted; `ctrl+s` covers the one you haven't. The situation it exists for: you start composing a reply while the agent is still responding, and the response turns out to end in a question you need to answer before your draft makes sense to send. Press `ctrl+s` to set the draft aside. It parks above the status bar as `set aside: <text>` (cursor position, collapsed pastes, and pending clipboard images all preserved) and the editor clears so you can type the answer. The moment you send it, the parked draft drops back into the editor and you continue where you left off.

`ctrl+s` again brings the draft back early; pressed with a draft on both sides it swaps them. A muted hint appears once you've typed a few characters of a draft while a turn is running, the situation where you're most likely to need it, and stays through the turn's end, which is when the question you have to answer actually lands.

Slash commands also work while the agent is busy. Read-only ones (`/help`, `/jump`, `/copy`, `/btw`, `/sessions`, `/skills`, `/context`, `/lore`, `/memory`, `/tasks`, `/status`, `/usage`, `/resets`, `/settings`, `/permissions`, `/jail`, `/unjail`, `/exit`) take effect immediately. Destructive ones (`/new`, `/clear`, `/compact`, `/login`, `/logout`, `/model`, `/reload-ext`, `/reload-skills`, `/restart`, `/trust`, `/untrust`, `/migrate`, `/cd`) cancel the active turn first and then run.

`/continue` is in neither group. It refuses while a turn is running rather than cancelling one, because a turn in flight is itself proof the session is not stuck, and cancelling to "resume" would destroy the reply it was asked to recover.


## Keys (interactive mode)

### Input

| Key | Action |
|---|---|
| `enter` | Submit (queued if the agent is busy). |
| `alt+enter`, `shift+enter` | Newline. |
| `tab` | Complete the selected slash command. |
| `shift+tab` | Cycle the approval mode: plan → workspace → auto-edit, wrapping. This session only, never persisted (`ask` and `yolo` are deliberately off the wheel; reach them from `/settings` or `--approval`). See [permissions.md](permissions.md). |
| `esc` | Cancel the current turn (while busy); clear input (while idle). |
| `ctrl+c` | Clear the input and queue (while idle) or arm the exit hint (while busy). Press again within 2s to exit. Use `esc` to cancel a running turn. |
| `ctrl+d` | Exit on empty input. |
| `ctrl+l` | Redraw the screen. |
| `ctrl+o` | Expand or collapse long tool results (read, write, edit, bash outputs over ~12 lines) and the compaction summary. Also overrides `ctrl+t`'s minimal/grouped/hidden modes with full boxes. Recorded thinking has its own key; see `ctrl+r`. |
| `ctrl+r` | Expand or collapse recorded thinking (`▸ thinking`, present only with **Record thinking** on), and lift the height cap off the block streaming during a turn. The newest turn's thinking is already open without this; `ctrl+r` reaches the older ones. Separate from `ctrl+o` so reading the model's reasoning does not mean unfolding every bash dump and diff in the transcript. |
| `ctrl+s` | Set the current draft aside to answer the agent first (press again to bring it back; it also returns on its own after your next message goes out). See [Setting a draft aside](#setting-a-draft-aside-ctrls). |
| `ctrl+t` | Cycle tool display: boxes → minimal one-liners → grouped → hidden. Errors stay visible; `ctrl+o` recovers everything. |
| `ctrl+v` | Paste an image from the system clipboard into the prompt (same as `/paste`). Text paste needs no key, because it arrives as a bracketed paste. |
| `ctrl+y` | Open the copy picker: turn, then part, then clipboard. Inside it, `ctrl+y` copies the whole reply of the highlighted turn. Same as `/copy`. See [Copying text out](#copying-text-out). |
| `@` | Open the file picker. Browse files and directories in the working directory. |

### File picker (`@`)

| Key | Action |
|---|---|
| `@` | Open the file picker (type after a space or at the start of input). |
| `up`, `down` | Navigate the file list. |
| `right` | Open the selected directory. |
| `left` | Go back to the parent directory. |
| `tab` | Shell-complete the token in place: extend to the unique candidate (a directory gains `/` in recursive mode and the next tab descends) or the longest common prefix, bash dot-name rules included. Never commits, because that's enter's job. |
| `enter` | Select the file or directory and insert it as a chip (`[file:name]` or `[dir:name/]`). |
| `esc` | Close the file picker. |

Type `@` followed by a filter string to narrow the list (e.g. `@read` shows only entries containing "read"). By default the picker fuzzy-searches the **whole tree** (nested paths match too, so `@foobar` finds `src/foo/bar.go`), matching the web composer; the `→`/`←` browse keys apply when **recursive file search** is turned off in `/settings`, which lists one directory at a time instead. Selected files are inserted as compact chips that expand to the full path on submit. Dragged-and-dropped files and directories also collapse to chips automatically.

### Editor line navigation

| Key | Action |
|---|---|
| `ctrl+a`, `ctrl+e` | Jump to start or end of line. |
| `alt+left`, `alt+right` | Jump one word back or forward. |
| `ctrl+left`, `ctrl+right` | The same word jump, for terminals that send ctrl rather than alt. |
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
