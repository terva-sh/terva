# Proposal — status line v2: segment engine, per-mode presets, user customization

- **Status:** Stages 1–4 IMPLEMENTED on sothr-main, 2026-07-02 (segment
  engine + two-row default layout, clock/cost-epoch feed, async git
  prober, `status_line.rows` config). Stage 5 (script segment) and the
  `/settings` rows editor remain proposed. Implementation notes: the
  segment renderer signature became `func(StatusBarParams) []string`
  (multi-atom segments: usage/tags/ext), and the per-window ids below
  (`usage-5h`/`usage-wk`) shipped as a single `usage` segment emitting
  one atom per provider window — providers report arbitrary window
  sets, so fixed ids couldn't work.
- **Date:** 2026-07-01
- **Scope:** `packages/tui/view.go` (`StatusBar`, `StatusBarParams`),
  `packages/agent/modes/interactive_render.go` (param assembly), config +
  `/settings` plumbing for the segment list, a new async git-status prober.
- **Origin:** TUI QoL session 2026-07-01. The current bar is a single
  hardcoded layout with fixed two-space pads; every new datum (usage hint,
  ext status, chat bridge) has been bolted onto the same string. The user's
  target styling is a Claude-Code-style two-row segment bar (abbreviated
  path · git branch + diff stats · model · cost + burn rate on row one;
  context/5h/weekly meters with bars and reset countdowns on row two).

## What the field does (survey, 2026-07)

Seven harnesses surveyed (Claude Code, Codex CLI, Gemini CLI, aider,
opencode, Crush, goose). The load-bearing findings:

- **Fixed hide/show toggles don't survive contact with users.** Gemini
  started with `hideCWD`-style booleans and *migrated* to an ordered
  segment-ID list (`ui.footer.items` + `/footer` picker); Codex went
  straight to one (`[tui] status_line = ["model-with-reasoning",
  "current-dir", ...]` + `/statusline` picker with ~26 segment ids).
  Toggles can't express order or selection, so every want becomes a
  feature request.
- **Claude Code is the only script hook** (`statusLine.command`, JSON
  session snapshot on stdin, output rendered verbatim, 300ms debounce,
  in-flight cancellation). Maximal power, real ecosystem — but it is
  config-as-code: a project-supplied command is arbitrary code execution.
  terva just closed exactly that trust-laundering class in project-scoped
  mode; we must not reopen it.
- Codex users are already asking for command-backed segments on top of the
  segment list (openai/codex#20244) — the two mechanisms are complements,
  not rivals.

## Design

### 1. Segment engine (the mechanism)

Break `StatusBar` into named segment renderers, each
`func(StatusBarParams) (text string, ok bool)`:

| id | content | notes |
|---|---|---|
| `cwd` | `~/W/g/t/terva`-style abbreviated path | abbreviation: keep first letter of each ancestor |
| `git` | `⎇ sothr-main* +499 -109` | async prober, cached; omitted outside a repo |
| `model` | `(openai-codex) gpt-5.5` or persona-flavored | existing |
| `thinking` | `thinking: high` | existing |
| `tokens` | `↑94k ↓1.8k R8.2k` | existing |
| `cost` | `$0.529 (sub)` (+ optional `$X.XX/hr` burn rate) | burn = cost / wall-clock |
| `context` | `ctx 202k/1M ▓░ 20%` | existing % + new bar glyphs |
| `usage` | `5h ▓░ 15% ↻4h33m` (one atom per window) | from existing `UsageWindows`; window set is provider-defined |
| `bridge` | telegram connection tag | existing |
| `ext` | extension status segments | existing |
| `tags` | approval mode / jailed | existing |
| `edits` | `Δ +120 -45` — lines the agent's edit/write tools changed | shipped in the segment batch; session-epoch reset |
| `swarm` | `⛭ 2 agents` — live background agents | shipped; absent at zero |
| `persona` | `🌲 Mieli`, accent-tinted | shipped; leads the immersive default rows |
| `session` | `sess <name>` | shipped; config-only |
| `clock` | `15:04` | shipped; config-only |

**Colors (shipped with the segment batch):** the theme layer owns
presentation — `status_colors` (per-segment 256-color overrides keyed
by segment ID) and a staged meter ramp (`meter_low`/`meter_mid`/
`meter_high`, stages at 70/90%; stages rather than per-cell gradients
so the hue jump survives color-impaired vision). A built-in
`dark-daltonized` theme re-bases every red/green semantic slot on a
blue/orange axis with a cyan→amber→magenta ramp; candidate to become
the default dark theme after some live time.

Rows are lists of segment ids joined with a ` · ` separator (the reference
styling); segments that report `ok == false` (no git repo, no
subscription, zero tokens) drop out silently, matching Codex's graceful
omission. Width overflow keeps the current `appendWrappedStatusLines`
behavior: split at segment boundaries, never mid-segment.

### 2. Config surface

```json
"status_line": {
  "rows": [
    ["cwd", "git", "edits", "model", "cost"],
    ["context", "usage", "swarm"],
    ["session", "clock"]
  ]
}
```

Rows are open-ended. The default is three semantic rows — identity,
meters, ambient tags — but empty rows vanish, so an idle session
without tags/bridge/ext renders two. Overflow within a row wraps at
segment boundaries; membership never moves with width (segments
teleporting between rows on resize reads as flicker).

- Layer it like themes: built-in default → user config → **never**
  project/card/extension layers (card-is-data invariant).
- Per-mode presets: normal agent mode shows the full default; `--chat` /
  `--play` default to `[["model", "context"]]`-class minimalism (today's
  `HideWorkspace` becomes "the immersive preset omits `cwd`/`tags`").
  Personas/themes may *suggest* a preset the same way they suggest accent
  colors — suggestion, not code.
- A `/statusline`-style picker in `/settings` can come later; the config
  list alone is v1.

### 3. Script hook (later, explicitly gated)

Claude-style escape hatch as a *segment*: `"script:<path>"` runs a user
command with a JSON snapshot on stdin and splices its stdout in as one
segment (or whole-row when it's the only entry). Constraints, all
load-bearing:

- Honored **only from user-level trusted config** (`$TERVA_HOME`), never
  from project dirs, cards, or extension manifests — same rule as the
  clone-to-RCE fix (see persona-platform pre-release review).
- Debounce ~300ms, cancel in-flight on redraw, blank-on-failure with a
  muted `(statusline script failed)` note rather than a frozen stale row.
- ANSI passthrough allowed; output is still `truncateToWidth`-clipped.

### 4. Data gaps to fill

- **git segment**: needs an async prober (branch, dirty flag, +N/-M diff
  stats) with a short TTL cache; never block the render loop on `git`.
- **burn rate**: derivable from existing cumulative cost + session start.
- **context/usage bars**: pure rendering over data `StatusBar` already
  receives (`ContextUsed/ContextMax`, `UsageWindows`).

## Staging

1. **Stage 1 — segment engine** — SHIPPED (with stage 2 in one commit:
   the plan review chose making the new layout the default directly,
   so the byte-for-byte intermediate was dropped).
2. **Stage 2 — reference styling:** ` · ` separators, abbreviated cwd,
   context/usage meter bars, burn rate — SHIPPED.
3. **Stage 3 — config:** `status_line.rows` + per-mode presets — SHIPPED.
4. **Stage 4 — git segment** (async prober) — SHIPPED.
5. **Stage 5 — script segment** (user-level only, debounced) — SHIPPED
   (named `status_line.scripts` entries rather than `script:<path>`
   ids; triggers are turn end / `/cd` / minute boundary, sequential
   with a hard timeout — no free-running poll; trust rides the Hooks
   rule via the project-scope trust gate).

The `/settings` layer also shipped: a layout preset picker
(default/compact/detailed/custom) plus curated per-segment toggles,
both persisting through `status_line.rows`. A full reorder dialog with
live preview stays demand-driven.

## Adjacent conventions adopted this session (context)

- Tool display modes (`ctrl+t`: boxes → minimal → hidden; immersive modes
  default to minimal; errors always surface) — shipped.
- Slash menu: grouped catalog with divider headers (Codex's "frequently
  used first, never alpha-sort" + Gemini's section titles) — shipped.
- Still worth stealing later: Esc-Esc backtrack-and-fork (Claude/Codex/
  Gemini all converged), Tab-to-queue vs Enter-to-steer (Codex),
  unfocused-only desktop notifications, keymap remapping file.
