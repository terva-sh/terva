# The terva web panel: what each surface does

Every surface the browser control panel puts in front of you once it is open:
the panes, the model picker, usage, tool-call display, slash commands,
@-file mentions, attachments, shared files, and the queue. This is a reference
to look a control up in.

For standing the daemon up, the reverse proxy, auth, deployment, self-restart
and building the client, see [web.md](web.md). You read that page once, when
you deploy; you come back to this one whenever you wonder what something does.

## Session titles

A new session is named from the first line of your opening message as soon as
the first turn finishes, and the name is pushed to every open tab live (no page
refresh). To have terva write a short, specific title with a one-shot model call
instead, set it in `config.json` (off by default so no extra tokens are spent):

```json
{
  "auto_title": true,
  "auto_title_model": "anthropic/claude-haiku-4-5-20251001"
}
```

`auto_title_model` is optional; leave it out to title with the session's own
model. Write it as `provider/id`. A bare model ID uses the session's provider
when that provider lists it. See
[Naming a model in a setting](models.md#naming-a-model-in-a-setting). Either way you can always rename a session by hand; a manual name is
never overwritten by the automatic pass.

You can also generate a title **on demand**: the ✨ button next to rename in
the session drawer (or `g` in the TUI's `/sessions` picker) runs one bounded
model call over the conversation (the latest compaction summary when one
exists, plus the most recent exchanges) and works regardless of the
`auto_title` setting, including as backfill for old untitled sessions. An
explicit generate does replace whatever name is there: you asked for it.
Long sessions get better titles this way than from their opening line.

With `auto_title` on, titles also **refresh automatically after each
compaction**, the moment a session has provably outgrown the name its
opening earned. Only machine-generated titles refresh; a session you named
by hand keeps its name (title provenance is tracked in the transcript, so
this holds across restarts too).

## Panes

Auxiliary views live in a **pane host**, a collapsible right rail on desktop and
a full sheet on mobile, with a switcher across the top. Toggle it with the ⊞
button in the top bar; a tab lists each available pane (see
docs/proposals/web-surfaces.md). Today's panes:

- **Usage** (below): one pane holding the live usage picture (gauge, cost,
  subscription windows) *and* the context-size breakdown that explains where the
  usage goes. It refreshes in place as turns complete, no tab-switching needed.
- **Tasks**: the background-agent (swarm) dashboard: one row per agent with a
  status badge, live activity, an expandable transcript tail, and stop / resume /
  remove actions. It's workspace-global (shows every agent, including ones
  detached from a prior run) and appears when auto-swarm is on or any agents
  exist. Live-updated by a poller (the swarm has no push). The agent can spawn
  tasks via `swarm_spawn` when auto-swarm is enabled in `/settings`.
- **Raati**: the deliberation board (`/raati`,
  docs/raati.md): convene a three-unit panel on a
  question and watch it deliberate live: a blind round, cross-examination,
  then the verdict landing as kanji before settling into your language, with
  the tally and the minority report. Workspace-global; the convene form picks
  a convening profile (built-ins: `triage`, `counsel`, `code-review`,
  `ethics`), the decision class and the rigor level (0 *kaiku*: the workspace binding;
  1 *kuoro*: the provider's tier ladder; 2 *käräjät*: cross-provider seats
  from the user config's `raati.level2`); each block shows its seat's
  binding. The record persists under `$TERVA_HOME/raati/`. Pushed live (the
  coordinator has a real event feed, so no poller). With `raati.convene_tool`
  enabled, the **agent** can convene a panel too (`raati_convene`, always
  behind the approval gate); its deliberation renders on this same board.
- **Worktrees**: managed git worktrees (the built-in `worktree_*` engine): the
  same one-fetch view the TUI's `/worktree` panel renders. Each worktree shows
  its claim state (`claimed(self)` means claimed by *this* session), branch,
  base, and dirtiness; the **Merge-back** toggle switches to the collect
  overview: what each branch carries over its base (commits ahead,
  dirty/unpushed flags; merging back stays a manual act). Read-only, and the
  tab appears only when the session's cwd sits inside a git repo. No push
  event exists for worktree changes, so the pane fetches when opened and the ↻
  button re-fetches on demand.
- **Settings**: every setting the daemon exposes, rendered from one
  server-side surface (`workspace_settings.go`) that the TUI's `/settings`
  renders too, so the two never drift. Each row states when it bites: **approval
  mode** (per-session, live, not saved: a security posture); **thinking**
  effort, **auto-condense** (steps / turns / off) and **language** (all live, and
  saved as the default; language switches every open tab, see
  [Languages](web.md#languages)); **auto-title**, **temperature**, **theme**,
  **inline images**, **lazy tool loading** (advertise core tools first, let the agent pull tool groups in with
  `activate_tools`), **recursive file search**, **respect .gitignore**, **lore**
  (keyed context), **swarm worktrees**, and the first-run **core tool pack**
  offer; plus **auto-swarm** as two nested toggles: *background sub-agents* (the
  `swarm_spawn` tool) and, under it, the *proactive-delegation nudge* (default
  on; off keeps the tool but drops the system-prompt push). The ones marked
  *applies to new sessions* (the swarm toggles, lazy tools, lore, …) are baked at
  session construction. Config writes are concurrency-safe.
- **Commands**: every slash command an extension registered, as clickable
  buttons grouped by extension. The web has no command line, so a command is a
  button, not a `/name` you type (the TUI keeps the slash prompt). Running one
  applies its response the same way the TUI does: it can open a panel, submit a
  prompt to the model, or post a one-shot note back into the conversation
  (`display`/`error`; `insert` degrades to a note since there's no shared
  composer to fill). The pane appears whenever any loaded extension has
  commands. User-driven slash commands such as `/skill` are a separate composer
  autocomplete surface; this pane contains only the extension-provided set.
- **Extensions**: a management pane listing the session's installed + loaded
  extensions with a health rollup: a status badge (running / stopped / disabled /
  gated), version, scope, language, tool + command counts, and, for one that
  failed, the tail of its log as the reason. It mirrors the TUI's extensions
  dialog and appears when any extension is loaded. Each has an **enable/disable
  toggle**: it persists to the project config and applies **live**: the
  subprocess starts/stops and the agent's model-facing tool set is rebuilt on the
  spot (so a toggled-on extension's tools reach the model and a toggled-off one's
  stop lingering), no session restart. Gated (untrusted-project) extensions show
  no toggle; an extension disabled in its own manifest stays off until re-enabled
  there.
- **Lore**: the authored keyword-triggered context ([lore](localization.md))
  inspector + editor: each entry shows its name, trigger keywords (or an *always*
  badge for constant entries), source, and content. You can **create, edit, and
  delete user lore** from here; the form (name + comma-separated keywords, or
  "always active" + content + **scope**) writes a `<slug>.md` file to user
  (`$TERVA_HOME/lore`) or, in a **trusted** workspace, project (`.terva/lore`)
  scope (validated through the real parser; project scope is offered only when
  trusted, since project lore is trust-gated on load). Edits appear in the pane
  immediately. **Keyword-triggered** entries also take effect **live**: the
  running session re-wires its per-turn lore, so the next turn sees the edit.
  **Constant ("always active")** entries are baked into the system prompt at build, so those apply to **new
  sessions** (kept that way on purpose, so an edit doesn't reset the prompt
  cache). Only web-managed user entries are editable; entries from extension
  bundles, character cards, or other tiers are read-only.
- **Characters**: the content library the immersive
  [Stage](web.md#stage-the-immersive-chatplay-surface) surface plays from,
  surfaced in the panel so both manage **one** store. Two rosters: **character
  cards** (import a SillyTavern CCv2 card by drag or file, see
  its greeting/lorebook/PHI inventory, edit a card's own fields, delete) and
  **personas** (the embedded crew plus your own; create/edit is gated to a
  **trusted** workspace, because a persona charter shapes identity in the cached
  prefix, while a card is untrusted data whose edit never changes what it can
  *do*). Backed by the `cards.*` / `personas.*` control verbs; avatars serve
  over the auth-gated `/media/` route (never SW-precached, like the panel). It is
  workspace-global and always offered, because the embedded persona crew is
  never empty, and an import or edit refreshes it live.
- **Chat**: the chat-bridge manager (the TUI's `/connect`). Lists every
  registered chat service (the compiled-in telegram and discord connectors plus
  any connector extensions, tagged `extension`, and `dev` where applicable),
  each either **not configured** (with a pointer to `terva bot setup`) or a
  **connect** / **connect & pair** button. Once connected it shows the connector,
  the bot's `@username`, the paired user (or *awaiting `/start` from your phone*),
  and which session is being mirrored, with **disconnect** and **mirror this
  session** actions. The bridge is bound to one session and does *not* follow
  whichever session a tab happens to be showing, so rebinding is explicit.
  Workspace-global, like the bridge itself. A running `terva bot` daemon already
  polling the service blocks connecting (both consumers would race each update
  and one always loses); the pane says so and names the pid. The pane is offered
  whenever any chat service is registered, so it can explain "not configured"
  rather than silently going missing. See [connectors.md](connectors.md).
- **MCP**: the Model Context Protocol server manager. `terva web` now starts the
  configured MCP servers (user `config.json` + a trusted project's) once for the
  daemon and merges their tools into every session, so MCP tools are usable in
  the browser. The pane lists each server with a status badge (running / stopped
  / disabled / gated / failed), scope, tool count, and any startup error, and an
  enable/disable toggle. Because the servers are shared across sessions, toggling
  one restarts/stops it and rebuilds **every** open session's tool set live.
- **Permissions**: the approval inspector, headed by the **Workspace trust**
  state with a **Trust workspace** / **Untrust** control. Trust is
  workspace-global (keyed on the cwd) and gates whether project-scoped content
  loads at all (project extensions, project lore, project permission rules), so
  granting it here is what unlocks the *project* scope in the other panes.
  Trusting brings that content **live** across every open session (project extensions reload,
  tool sets rebuild, project lore becomes visible/editable); project skills and
  context baked into the system prompt take effect on the next session. Untrust
  tears it back down. Backed by the ctrlproto control verbs `control.trust` /
  `control.untrust`; the state rides on `SessionInfo.trusted`. Below trust: the
  session's approval mode (its setter lives in Settings), the compiled **rules**
  (tool → allow / deny / ask, with source), and the session's live
  **"always-allow" grants**, the decisions
  you accrued by answering "always allow" in approval prompts. Each grant has a
  **Revoke** button (and *Revoke all* clears a blanket allow-all). You can also
  **add and remove rules**: an add form (tool name or `mcp_*` glob + allow/deny/
  ask + optional args regex + **scope**) writes to your **user** `config.json` or
  the **project** `.terva/config.json`, and user/project rules get a **×** to
  delete. Edits persist and apply **live**: the rule takes effect on the next
  tool call across every open session (a deny rule blocks immediately). Project
  rules are restrict-only: they can't grant **allow** (the self-approval ban), so
  the form drops that option for project scope. Extension/builtin rules aren't
  config and stay read-only.
- **Extension panels**: an extension that opens a panel appears as a live pane
  and disappears when it closes; a **Status** pane aggregates extension status
  segments. A panel can be plain text lines *or* a rich **widget tree**: an
  extension sends a semantic vocabulary (heading, text, meter, keyvalue, table,
  list, group, note, action, divider) via the SDK's `OpenPanelWidgets` /
  `RenderPanelWidgets`, and the panel renders it natively: meters as bars,
  actions as buttons that call back to the extension, with no per-extension
  client code. The extension also sends text `Lines` as the TUI fallback. A
  panel can be opened spontaneously (on a session event) or by running the owning
  extension's command from the **Commands** pane.

## The model picker and sub-agent tiers

The model button opens a searchable picker: favorites first, then a group per
provider, each row switching the session and carrying its own ★ favorite and ◉
set-as-default toggles.

Each provider group header carries a **⇅** that opens that provider's sub-agent
tier ladder: the model `swarm_spawn` gets for `tier: weak`, `medium`, `strong`
or `cheap`, and the model a RAATI seat is filled with at rigor level 1. Tiers
belong to a provider, so the affordance sits on the group header rather than on
a row, and never on the ★ group, which spans every provider.

Beside it, a dot per rung in ladder order, so the state of every ladder is
visible without opening any of them:

| | |
|---|---|
| filled | Pinned by you, in `swarm_tiers`. |
| hollow | Filled by a built-in rule matched against the provider's catalog. |
| dash | Nothing resolves. A `tier:` spawn against this rung silently runs on the host model, at host-model price. |

A provider whose ladder could not be read shows **no dots at all** rather than
dashes: "not known" and "nothing resolves" are different answers, and only the
second is a problem.

The ladder itself shows what each rung **resolves to today**, not what config
holds. An empty `swarm_tiers` is the ordinary case and says nothing about
whether the ladder is right. Each rung's model picker carries the *pin* while
the line above it carries the resolved model, so an untouched rung does not look
pinned, and saving one cannot freeze a rung that had been tracking a family
rule. Pinning only a thinking level is allowed and useful: it keeps the built-in
model and just changes how hard it thinks.

## Usage

The top bar shows a live **context meter**: the last turn's real input+cache
tokens against the model's window, as a small bar + percent that turns amber
past 70% and red past 85% (the TUI status bar's `ctx` gauge). Click it (or the
cost number, or the **Context breakdown** button in the ⓘ popover) to open the
single **Usage** pane. Everything below lives on that one pane. The size
breakdown is really just a clarification of where the context usage goes, so the
once-separate usage and context views are merged. The pane refreshes in place as
turns complete:

- **Provider / model**: the active provider and model backing the session.
- **Context gauge**: real last-turn tokens vs the window (falls back to a byte
  estimate before the first turn).
- **Cumulative usage**: session input/output/cache tokens and cost, with a
  `(sub)` marker for OAuth/subscription credentials.
- **Subscription windows**: for providers that report them (e.g. Codex's 5h +
  weekly), each as a meter with a reset countdown (the status bar's usage
  meters; populated once the provider returns usage, i.e. after a turn).
- **Next-request size breakdown**: system prompt (with the extension-guidance
  share broken out), tool definitions, ephemeral extension context, and the
  transcript per message (largest flagged). terva has no tokenizer, so these
  are bytes with ~bytes/4 token estimates, enough to finger a bloat source.

It mirrors the TUI's `/context` plus the status-bar usage meters.

## Tool calls

The tool-display button in the top bar cycles four levels (the choice is
remembered): **box** (full name/args/result), **grouped** (a run of tool calls
between replies collapses to a single "N tool calls" line, with a summary of
which tools and any failures, that expands to the full boxes on click),
**minimal** (one greyed line per call), and **hidden**. Grouped is the answer to
a dozen tools burying a reply; the TUI has the same four levels on its `ctrl+t`
cycle, and both sides summarize a run identically (one shared format, pinned by
golden fixtures).

## Slash commands

Type `/` in the composer for an autocomplete menu of **user-driven** commands:
operator chrome, distinct from the extension **Commands** pane (which is
buttons). Arrow keys navigate, Tab/Enter selects (a command with no argument runs
immediately; one that takes an argument primes `/name ` and keeps focus), Esc
dismisses. A message that merely starts with `/` but isn't a known command still
sends as a normal prompt.

| Command | Does |
|---|---|
| `/compact` | Summarize + replace the transcript to reclaim context (the TUI's `/compact`; the daemon otherwise auto-compacts near the window limit). Refused mid-turn; a already-minimal transcript reports a benign note. |
| `/clear` | Wipe the transcript with **no** summary and start over in the same session. Unlike compact it keeps nothing; the durable session file gets an empty checkpoint (old rows stay for audit). Refused mid-turn. |
| `/continue` | Ask for the reply a turn died without producing (the TUI's `/continue`, the verb `turn.resume`). Nothing is appended and nothing is discarded. The panel also offers this as a button above the composer whenever the daemon reports the session is waiting on a reply, so the command is for anyone who already knows it. Refused when the session is not stuck. |
| `/skill <name> [task]` | Prime the model to load a skill. It rewrites to *Use the "name" skill for: task* and sends it, so the model calls the `skill` tool. After `/skill ` the menu autocompletes skill names. |
| `/model [id]` | Switch to a model by id, or open the model picker. |
| `/context` | Open the Usage pane. |
| `/raati` | Open the deliberation board. |
| `/new` | Start a new session. |
| `/help` | List the commands. |

Only the daily-driver subset is exposed; TUI-only chrome (`/jail`, `/paste`, …)
and things with dedicated web UI (settings, sessions) are not. `/compact` and
`/clear` are each backed by a ctrlproto conversation method (`compact` / `clear`);
the rest are existing methods or client-side transforms.

## @-file mentions

Type `@` (at the start of the message or after a space) for workspace-file
completion; the daemon lists its own tree over ctrlproto (`files.list`,
gitignore-filtered, advertised as the `files-list` hello feature; on an older
daemon the stage simply doesn't appear). Matching is substring-then-subsequence
over the whole relative path; selecting a file inserts the path, selecting a
directory keeps the token live and narrows into it. The listing
is fetched lazily on the first `@` and re-fetched on a 30-second TTL, so
keystrokes never ride the wire.

**Tab** shell-completes the token in place: segment-wise prefix completion
to the unique candidate (a directory gains `/` and stays live) or the
longest common prefix, bash dot-name rules included. Tab never commits;
Enter applies the highlighted row. The TUI's `@`-picker Tab runs the same
semantics, and both implementations are pinned to one shared golden-fixture
file, so they cannot drift.

## File attachments

Drag any file onto the composer (or paste one) and it rides the next message.
Two destinations, chosen by what the file is:

- **An image** goes inline, base64 in the prompt frame, because that is the only
  form a vision model can see. Capped at 10 MB: the frame is 32 MiB and base64
  inflates by ~4/3, so this leaves room for the JSON envelope and your text.
- **Everything else**, meaning an email-filters export, a CSV, a log, a PDF, or
  an image too large to inline, is uploaded to the daemon (`POST /upload`,
  auth-gated and same-origin checked) and staged under
  `$TERVA_HOME/attachments/<session>/`. The message then carries only the
  upload's **id**; the daemon resolves the path, name, type, and size itself, so
  nothing a client claims about a file is taken on trust.

The turn tells the model what was attached and where, and it reads them with its
ordinary tools. That instruction is a block of its own on the message, not part
of what you typed: the panel shows the files as inert labels above your words
and never renders the staging path, which is the model's copy and wraps to nine
lines on a phone. The labels are deliberately not clickable: an uploaded
attachment is not retrievable from them, because the bytes are swept on a TTL
and an affordance that failed most of the time would be worse than none.
`$TERVA_HOME/attachments` is a sandbox **read-only** root, so a jailed agent
can read a staged file and copy what it wants into the workspace, but never
write back into the staging area.

Staged files are **not permanent, and messages do not pretend otherwise.** A
sweeper (daemon start, then hourly) removes anything past a 24-hour TTL and, if
the whole area exceeds 2 GiB, evicts oldest-first, never touching a file staged
in the last hour, so a message being composed cannot lose its own attachments. A
message keeps the record of what was attached; if you re-open it much later, the
paths it names may be gone. Ask the agent to copy anything worth keeping into
the workspace during the turn.

A file can also lapse between the drop and the send; a composer left open
overnight is enough. The send still goes: the model is told how many
attachments it is not getting, and the message says "*N attachments had
expired*" beside whatever survived, so an answer that ignores a file is never
left unexplained.

The composer only offers this where the daemon advertises the `attachments`
hello feature, which the web carrier sets because it is the carrier that mounts
the upload route. A client connected some other way (`terva attach` over a unix
socket) has nowhere to POST bytes and says so rather than dropping the file
silently.

Per-file limit: 100 MB, advertised as `Hello.MaxAttachmentBytes`. Over it, the
upload is rejected whole rather than truncated. Half an export is silent
corruption. See [resource-limits.md](resource-limits.md).

## Shared files (the agent hands you one)

The other direction. When the agent has produced something you should *have*
(an export, a chart, a log slice, a PDF it fetched), it calls **`share_file`**,
and the file appears in the transcript as a card: an image you can see, an audio
or video clip you can play, or a chip you can click to download. Before this it
could only tell you a path, which is no use from a browser on another machine.

Mechanically it copies the file into `$TERVA_HOME/shared/<session>/` and the turn
records an id. The panel builds `GET /shared/<session>/<id>` from it, which is
**auth-gated exactly like `/media/`**: the `terva_token` cookie authenticates a
plain `<img src>` or `<a download>`, so no token ever rides a file URL. Pasting
that URL into another browser gets a 401: a shared file is yours, not a link you
can forward.

It **copies**, deliberately. A hardlink would be free, but then an agent editing
the file afterwards would retroactively change what it already handed you, and a
download that quietly serves different bytes than the transcript described is
worse than no download.

Three things about how the bytes come back, because the route serves content the
*model* chose from the same origin your session cookie lives on:

- **Everything downloads by default** (`Content-Disposition: attachment`). Only
  the panel asks for inline rendering, and only a closed allowlist of media types
  gets it: PNG/JPEG/GIF/WebP/AVIF, MP3/OGG/WAV/FLAC, MP4/WebM.
- **SVG is not on that list, despite being an image.** An inline SVG executes
  script in this origin, and so does an HTML file; both come back as downloads no
  matter what is asked for. An agent cannot turn a hostile file into script
  execution by choosing an extension.
- The content type is **derived from what landed on disk**, never from a name the
  agent picked, and the response carries `X-Content-Type-Options: nosniff` plus
  its own `Content-Security-Policy: sandbox`.

Shared files outlive uploads but are **not permanent either**: a 7-day TTL, and
oldest-first eviction above 2 GiB. Seven days rather than the inbound 24 hours
because the two ends are not symmetric. An uploaded file has done its job once
the agent has read it, while a shared one is the deliverable and the obvious way
to want it is to reopen the session next week. Past that, the card still says
what was shared and says it is no longer available; ask for it again.

The card knows that without asking. Each record carries an `expires_at`, the
file's own modification time plus the store's TTL, which is the same arithmetic
the sweeper performs, so a card goes inert on the deadline for **every** kind
of file. It used to infer the answer from an `<img>` failing to load, which
works only where there is an image: a document, a clip, and a track all kept
offering a download long after the bytes were gone, and the user found out by
clicking. The image's load failure is still watched, because `expires_at` is an
upper bound and eviction above the size cap can take a file sooner.

A daemon too old to send the field sends nothing, and a client reads that as
*unknown* rather than as expired. The opposite reading would silently withdraw
every download on a mixed-version deployment.

The card renders where the daemon advertises the `shared-files` hello feature:
the web carrier, because the web carrier is what mounts the route. The record
itself reaches every client either way; the feature is only whether there is a
**link** behind it.

A link is not the only way to get at the bytes. The TUI has no HTTP route to
fetch from, which is why it showed a bare tool call and nothing else for as long
as this feature existed there. It now renders its own card under the tool box
(name, kind, size, expiry, and an inline preview for an image it can pull), and
`/shared` lists the session's whole set with copy-path, open, and save-here. Both
resolve handles over two read-only ctrlproto verbs, `shared.list` and
`shared.fetch` (see [controllers.md](controllers.md)), which any carrier can
serve because they need no route at all. See [slash-commands.md](slash-commands.md).

`$TERVA_HOME/shared` is deliberately **not** a sandbox root, unlike the inbound
`attachments/`. The agent publishes through the tool, never by writing there, so
a grant would buy it nothing and would let a jailed session read every other
session's deliverables.

## Queued messages

Sending while a turn is running queues the message (shown as a dashed bubble).
Before the agent consumes it you can **edit** it in place (✎) or **remove** it
(×); the change is pushed to the queue and broadcast to every open tab.

Stop stays available through tool execution, approval waits, and provider
retry delays. Enter submits a text follow-up to the queue in both the panel
and Stage. Attachments must wait until the current turn finishes.

The composer clears submitted input after the daemon accepts it. New text
typed while acceptance is pending stays in the composer. A refused send keeps
its text and attachment references. If the connection drops before acceptance
arrives, check the transcript after reconnecting before sending again. The
browser keeps the draft and does not resend it automatically.

Unsent input stays with its session while you navigate in the same tab.
The panel persists draft text separately; attachment references and Stage
drafts stay in memory and do not survive a reload. Composition confirmation
keys belong to the input method and do not submit or select autocomplete items.

## Scope

v1 is the daily-driver: chat, session switching + nicknames, model switching
(searchable picker with per-model favorites), a context breakdown, usage, and
the PWA. Extensions load per session (like ACP), so lore, hooks, tool
approvals, and extension tools all work. Management surfaces have since landed
on top of that: lore editing and extension enable/disable both ship (see the
Lore and Extensions panes above), as do MCP, permissions, and the chat bridge.
Still deferred (further `ctrlproto` control group): prompt overrides and
templates.
