# Web surfaces: core + extension panes for the control panel

Status: draft / design. Depends on `terva web` (docs/proposals/terva-web.md) and
the control plane (docs/proposals/control-plane-protocol.md). Extends the
`ctrlproto` wire and the Preact client under `packages/agent/web/`.

## Motivation

The web UI already has one auxiliary pane: the **context breakdown** (`context.get`
→ `ctrlproto.ContextBreakdown`, rendered as a modal). It works because it is a
*session-scoped, structured, on-demand panel* — fetch a typed payload, render it
natively. The user likes it and wants more of them.

The TUI already has a whole family of these panes — `/usage`, `/swarm` (the tasks
dashboard), extension-owned panels (`/memory`, …), status segments, `/lore`. On
the web today none of them exist: extension panels and status segments are
**dropped** (the web session builds its extension manager with
`nonInteractiveExtHooks`, whose panel/status hooks are no-ops — see
`workspace_session.go` → `setupWebExtensions`), and there is no `Swarm` on the web
`Workspace` at all.

This proposal generalizes the one-off context modal into a **surfaces** system: a
small registry of panes that core and extensions expose, a uniform way to fetch
and live-update them, and a client "pane host" that switches between them.

## The surface kinds we already have

From the TUI/extension inventory, the distinct pane shapes are:

1. **Structured inspector** — `/context`. Numbers + meters + a flagged table.
   Static on open, per-session. *(Already wired on the web.)*
2. **Budget/quota meter** — `/usage`. Provider windows + credits. Semi-live,
   account-global. *(Data already reachable: `agent.Usage()`.)*
3. **Entity dashboard w/ row actions** — `/swarm` tasks. Live list of
   `swarm.AgentSnapshot` + spawn/stop/resume/send + a drill-in transcript.
   Workspace-global. *(No `Swarm` on the web Workspace yet.)*
4. **Extension-owned interactive panel** — `extproto.PanelSpec{id,title,lines,footer}`
   with host→ext key forwarding. Live, per-session, ext-owned. *(No web host-hook
   yet; single-instance in the TUI, the web wants N keyed by `(ext,id)`.)*
5. **Ambient status segments** — `(ext,id)→text` short strings. Live, per-session.
   *(No web host-hook yet.)*
6. **Provenance / "what's loaded"** — `/lore`, the `/context` Extensions tab
   (`extMgr.ContextSnapshot()`). Static lists, per-run.

Management surfaces (extensions/MCP manager, permissions inspector, sessions
browser, settings) are *also* panes, but they are configuration UIs with their own
richer interaction model; they are out of scope for v1 and get their own control
group later.

## The model

A **surface** is an addressable pane a client can list, fetch, render, live-update,
and act on. Two ctrlproto concepts:

### Registry — what panes exist

`surfaces.list` (session group) → `[]SurfaceMeta`:

```go
type SurfaceMeta struct {
    ID      string // "context", "usage", "tasks", "ext:memory:main"
    Title   string // "Context", "Usage", "Tasks", "Memory"
    Icon    string // emoji/glyph hint for the switcher
    Kind    string // context | usage | tasks | settings | panel | widgets | commands
    Scope   string // session | workspace
    Live    bool   // pushes surface_updated events
    Actions bool   // accepts surfaces.action
    Badge   string // optional switcher badge, e.g. "3" running tasks (live)
}
```

The registry is dynamic: extension panels appear/disappear as extensions open/close
them, so `surfaces.list` changes over a session and the client is told via a
lightweight `surfaces_changed` event (re-fetch the list).

### Content — a pane's data

`surface.get {id}` (session group) → `Surface`. The payload is discriminated by
`Kind`. **Two candidate content models** (this is the main design fork — see
"Decisions"):

**(A) Hybrid: typed core panes + generic widgets for extensions.** Keep bespoke,
polished renderers for the panes we already know (context/usage/tasks) and give
extensions a generic widget tree. Lowest risk; preserves the context pane's exact
look; each *new core* kind is a client change.

```go
type Surface struct {
    ID, Title, Kind string
    Context *ContextBreakdown  // kind=context   (already exists)
    Usage   *UsageView         // kind=usage
    Tasks   *TaskList          // kind=tasks
    Panel   *PanelView         // kind=panel     (ext flat lines, today's PanelSpec)
    Widgets []Widget           // kind=widgets   (generic, for rich ext panes)
}
```

**(B) Pure generic: every surface is a widget tree.** Core builds widget trees for
context/usage/tasks in Go; extensions send widget trees. One renderer, fully
extensible, no client change per new pane — but we re-express (and re-style) the
context pane as widgets to prove parity.

Both share a **widget vocabulary** (used by extensions in A, by everything in B).
The vocabulary is semantic, not layout primitives, so the client renders each
widget natively (a `meter` is a colored progress bar, not a styled div):

```
Widget =
  | heading   { text, level }
  | text      { text, tone? }                       // tone: default|muted|danger|ok
  | meter     { label, value, max, unit?, tone? }   // the context/usage bars
  | keyvalue  { rows: [{ key, value, note?, mono? }] }   // info-popover style
  | table     { columns:[…], rows:[[…]], highlight?: rowIndex }  // context messages
  | list      { items: [{ text, note?, tone?, action? }] }
  | group     { label?, collapsible?, collapsed?, children: Widget[] }  // tool-group
  | note      { text, tone }                         // toasts/status lines
  | action    { label, action_id, tone? }            // buttons → surfaces.action
  | divider   {}
```

This vocabulary already covers everything the context pane renders (meter +
keyvalue + table-with-highlight + group), which is the test of whether it is rich
enough.

### Live updates

Reuse the existing per-session event hub. A new event:

```
EventSurfaceUpdated  { surface_id, surface? }   // full payload for cheap panes; signal-only for expensive ones (client re-fetches)
EventSurfacesChanged {}                          // the registry changed; re-list
```

Static panes (context) never push. Live panes (tasks, ext panels, status) push
`surface_updated` as their source changes — the ext-panel `panel_render` frames and
`swarm` snapshots map straight onto this.

### Actions

`surfaces.action {id, action_id, params?}` (session group) → ok / updated surface.
Maps to:
- ext panel: `extMgr.SendPanelKey(ext,id,key,text)` / `SendPanelClose`.
- tasks: `swarm.Stop/Remove/Resume/SpawnReq/SendUserTurn`.
- generic `action` widgets: the ext/core owner's handler.

## The missing web plumbing

Two gaps the Explore surfaced, both required for extension + task panes:

1. **Web extension host-hooks.** Replace the panel/status no-ops in
   `nonInteractiveExtHooks` (for the web path) with a `webExtHooks` that, on
   `OpenPanel/UpdatePanel/ClosePanel` and `RefreshStatus`, updates a per-session
   surface table on the `wsSession` and broadcasts `surface_updated` /
   `surfaces_changed`. This is the single link that makes `PanelSpec` and status
   segments visible on the web. Panels start as `kind=panel` (flat styled lines,
   the existing shape); a later ext-SDK bump can let extensions send widget trees.

2. **Swarm on the web Workspace.** The tasks pane needs a `Swarm` wired into
   `Workspace` (today only the TUI has one via `i.cfg.Swarm`). This is the larger
   lift and can be a second slice; the surface model does not depend on it.

Everything else the surfaces need is already on `wsSession`: `s.agent` (context,
usage) and `s.extMgr` (embeds the extdriver `Driver`, so `ContextSnapshot`,
`StatusSegments`, `SendPanelKey`, `ListExtensions` are already callable).

## Client: the pane host

Replace the single context modal with a **pane host**:

- A **surface switcher** — a compact row of chips/tabs (icon + title + optional live
  badge) built from `surfaces.list`, refreshed on `surfaces_changed`.
- A **pane region** whose placement adapts (the layout fork — see "Decisions"):
  a collapsible right rail beside the chat on desktop, a full sheet on mobile.
- A **renderer** dispatched by `Kind`: bespoke for context/usage/tasks (model A) or
  a single widget renderer (model B); ext panels via the widget/lines renderer.
- Live panes update in place from `surface_updated`; the switcher badge (e.g. a
  running-task count, an unread ext-panel dot) updates from `surfaces_changed`.

The existing context modal is the seed: it becomes the first registered surface,
rendered inside the pane host instead of its own modal.

## First implementation slice

1. `ctrlproto`: `surfaces.list` + `surface.get` + `EventSurfaceUpdated` /
   `EventSurfacesChanged`; `surfaces.action` (stub for panel keys). `SurfaceMeta`
   + `Surface` types.
2. Workspace: a `surfaceRegistry` per `wsSession`; register `context` (reuse
   `contextBreakdown()`) and `usage` (from `agent.Usage()` + cumulative).
3. Client: the pane host (switcher + adaptive pane region) and renderers for
   `context` + `usage`; migrate the context modal into it.
4. Ext bridge: `webExtHooks` feeding `panel` + status surfaces + broadcasts;
   render `panel` as flat styled lines.

Deferred to later slices: tasks (needs `Swarm` in `Workspace`), the full generic
widget vocabulary (start panels as flat lines, grow widget types as needed),
richer `surfaces.action`, management surfaces.

**Update (shipped):** slice 1 (context + usage + ext-panel bridge) and the
**tasks** pane both landed. Tasks wired a workspace-global `swarm.Swarm` into
`Workspace` (RepoRoot = cwd, `Reload` on start), registered `swarm_spawn` in the
web session tool ladder (gated by auto-swarm; also closes the prompt/tool gap),
and — since the swarm has no observer — a poller diffs `SnapshotAll()` and
broadcasts `surface_updated("tasks")`. The pane is workspace-scoped (all agents,
no per-session filter) with stop/resume/remove/send actions. Still deferred:
generic widget rendering, interactive ext-panel key forwarding in the client,
management surfaces.

**Update (merged context + usage):** the two `session`-scoped panes were
collapsed into one. The context pane already rendered the entire usage picture
(gauge, cumulative cost, subscription windows) above its size breakdown, so the
separate `usage` pane was a strict subset — it only added a provider/model
header line. That header moved onto `ContextBreakdown` (new `provider`/`model`
fields), the `usage` surface/`kind=usage` was removed, and the combined pane is
titled "Usage" (the breakdown is just a clarification of where that usage goes;
id stays `context`). It's marked `live` and refreshes in place on each
`usage` wire event (the client reloads it when it's the open pane, since nothing
broadcasts `surface_updated("context")`).

**Update (generic widgets shipped):** the deferred widget vocabulary now
renders. An extension sends a widget tree — the semantic vocabulary above
(heading/text/meter/keyvalue/table/list/group/note/action/divider) — via the SDK
`OpenPanelWidgets` / `RenderPanelWidgets` (extproto `PanelSpec.Widgets`), plus
text `Lines` as the TUI fallback. The web bridge stores it on `webPanel`, emits
`kind=widgets` (mapping `extproto.Widget` → `ctrlproto.Widget`), and the client's
`WidgetBody` renders each node natively; `action`/`list` buttons fire
`surface.action{action:"action", id:<action_id>}`, delivered to the extension as
a panel key (reusing the existing channel). Content model (A) is thus complete:
bespoke typed core panes + a generic widget tree for extensions.

**Update (extension commands shipped):** the web now dispatches extension slash
commands — as a **pane of buttons**, not a command line, since the browser has no
`/`-prompt (the TUI keeps typed slash commands; user-driven ones like `/skill`
are a separate future concern). A `commands` surface (`CommandsView` — every
`Driver.Commands()` entry, grouped by extension) appears whenever a loaded
extension has commands; running one is `surface.action{action:"run",
args:{name}}`, which calls `Driver.Invoke` and applies the `Response` the way the
TUI's `invokeExtensionCommand` does: `open_panel` → `paneOpen`, `prompt` →
`s.prompt`, and `display`/`insert`/`error` become one-shot **notices**
(`EventNotice` → an ephemeral in-stream `notice` item; `insert` degrades to a
note, matching ACP, since the web has no shared composer to fill). This is what
lets a command-opened panel (todo's `/todo`, memory's `/memory`) surface from the
browser. Deferred still: a host widget→text flattener (so an extension can send
*only* widgets and the TUI auto-renders text), and user-driven web slash commands
(`/skill`, etc.).

## Decisions (confirmed 2026-07-03)

1. **Content model — Hybrid (A).** Typed payloads + bespoke renderers for the
   core panes (context/usage/tasks), a generic widget tree for extensions. Keeps
   the context pane's exact look; extensions get rich panes without client
   changes. The generic `Widget` vocabulary is defined but deferred — the first
   ext panes render as `kind=panel` flat styled lines (today's `PanelSpec`);
   widget trees land when an extension needs them.
2. **Pane host placement — collapsible right rail (desktop) + full sheet
   (mobile).** So a live pane (context meter, tasks) can sit beside the chat.
3. **First slice — context + usage + the extension-panel bridge.** Prove the
   foundation with two typed core panes AND the `webExtHooks` link so a real
   extension panel (and aggregated status segments) shows up as a live pane.
   Tasks (needs `Swarm` in `Workspace`) and the generic widget vocabulary are
   later slices.

## Non-goals (v1)

Management/config surfaces (extensions/MCP/permissions/settings) — richer
interaction, own control group later. Multi-pane tiling (one active pane at a
time). Cross-session/global surfaces beyond tasks.
