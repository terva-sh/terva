# Localizing and customizing terva

terva ships in English, but every word it shows you — and every canned
instruction it sends the model *on your behalf* — lives in an overlay catalog
you can edit. Two use cases, one mechanism:

- **Translate** terva's interface into your language (Finnish, Portuguese, …).
- **Customize** terva's wording or its model-facing prompts to taste — which
  works in English too, so you can reword a label or give terva a different
  personality without touching the binary.

It's the same composable idiom as [themes](themes.md) and
[personas](personas.md): terva ships a default, you override the subset you
care about in `$TERVA_HOME`, and everything you don't touch inherits the
built-in. Nothing is all-or-nothing — override one string, one prompt, or a
whole language.

## The catalogs

terva splits its text into catalogs — separate files per **surface**, so you can
translate the part you actually use first (the interactive TUI alone is about
two-thirds of all UI text):

| catalog | what it is | keyed by | lives in |
|---|---|---|---|
| **UI — core** | short CLI / engine / connector text: errors, status lines, prompts you answer outside the terminal UI | the **English text itself** (`"start a fresh session"`) | `locales/<lang>.json` |
| **UI — tui** | the interactive terminal UI: dialogs, the status bar, the slash menu, transcript chrome | the **English text itself** | `locales/tui/<lang>.json` |
| **UI — web** | the browser control panel (`terva web`) | the **English text itself** | `locales/web/<lang>.json` |
| **UI — stage** | the Stage play surface (the installable `/stage/` PWA) | the **English text itself** | `locales/stage/<lang>.json` |
| **Prompts** | canned English terva sends *to the model*: the `/study` task, the auto-swarm result summary, the base system-prompt segments, the compaction instructions | a stable **dotted id** (`study.file`, `system.identity.default`) | `locales/prompts/<lang>.json` |
| **Help** | the large `terva <cmd> --help` screens (`terva bot`, `terva models`, …) | a stable **dotted id** (`help.bot`, `help.models`) | `locales/help/<lang>.json` |
| **Tools** | each tool's description — the text the model reads to decide whether to call it | a stable **dotted id** (`tool.read.description`, `tool.bash.description`) | `locales/tools/<lang>.json` |

Short UI text uses English-as-key so a translator sees the sentence, not an
invented id, and the UI catalog is split by surface (core / tui / web / stage)
purely so each can be finished on its own — at runtime every UI string resolves
against one merged lookup, so nothing changes about how the code calls `i18n.T`.
A source directory routes its UI strings to a non-core catalog with a
`//i18n:catalog` directive (e.g. `//i18n:catalog tui` on `packages/agent/modes`
and `packages/tui`); the web and stage catalogs are authored in the web client
(its extractor routes `src/apps/stage/` to stage, the rest to web) and served
to the browser as one merged document — Stage is split out because it is the
surface most likely to be handed to someone who isn't the operator. Prompts and Help are **large, few, and stable**, so
English-as-key would mean paragraph-length JSON keys — they use short dotted keys
instead, kept apart because a prompt author and a UI translator are usually
different people. Each dotted entry is one translatable template, never English
glue around fragments, so a translation reads as coherent `<lang>`.

## The overlay model

For any language `<lang>`, terva reads two layers and merges them per key:

```text
embedded default            (shipped in the binary)
  └─ $TERVA_HOME/locales/<lang>.json          ← your overlay wins, per key
```

Each non-core catalog overlays the same way under its own subdirectory —
`$TERVA_HOME/locales/tui/<lang>.json`, `.../web/<lang>.json`,
`.../stage/<lang>.json`, `.../prompts/<lang>.json`, `.../help/<lang>.json`,
`.../tools/<lang>.json` — so you can drop in just the surface you want to
translate.

- **Per-key, not whole-file.** Override `"quit"` alone; every other string
  still resolves.
- **Blank or missing falls back to English.** A partial file is always safe to
  run — unfilled entries render in English.
- **The default build is byte-identical.** With no overlay and no language set,
  terva returns every string verbatim, so nothing changes until you opt in.

## Selecting a language

Two ways, highest priority first:

```bash
TERVA_LANG=fi terva            # environment, per-run
```

```json
// $TERVA_HOME/config.json — persistent
{ "language": "fi" }
```

The tag is [BCP-47](https://www.rfc-editor.org/rfc/rfc5646): `fi`, `pt-BR`,
`de`. `en` (or unset, or any `en-*`) is the built-in source. An unknown tag
logs a warning and stays in English rather than aborting.

---

## Translating terva into your language

The loop is scaffold → edit → check → (optionally) capture-as-you-go →
contribute.

### 1. Scaffold

```bash
terva locale init fi
```

scaffolds **every** catalog for that language in one pass — the core UI
(`$TERVA_HOME/locales/fi.json`), the UI sub-catalogs (`locales/tui/fi.json`,
`locales/web/fi.json`, `locales/stage/fi.json`), and the dotted-key ones
(`locales/prompts/fi.json` for
the model-facing prompts — see [below](#customizing-tervas-prompts) — and
`locales/help/fi.json` for the big `--help` screens). Existing translations are
pre-filled; the rest are blank. It's idempotent — re-run it after a terva update
to pick up new strings without disturbing your work.

### 2. Edit the JSON

Replace each empty value with your translation. **Never edit a key.**

```json
{
  "show this help": "näytä tämä ohje",
  "saved %d files": "",
  "%d agent|%d agents": { "one": "%d agentti", "other": "%d agenttia" }
}
```

Keep the machinery intact:

- **Format verbs** — every `%s` / `%d` / `%.1f`, the same ones, though you may
  reorder them for grammar.
- **Placeholders and names** — `{{user}}`/`{{char}}` macros, flag names
  (`--persona`), command names (`terva locale`), and paths (`$TERVA_HOME/…`).
- **Plurals** — a key like `"%d agent|%d agents"` maps to an object of CLDR
  categories. Your language's set may be richer than English's `one`/`other`
  (Polish has `one`/`few`/`many`/`other`); `terva locale init` scaffolds the
  right categories for the tag. Fill each; `other` is required.

### 3. Check

```bash
terva locale validate $TERVA_HOME/locales/fi.json   # JSON + %-arg parity (alias: check)
terva locale diff fi                                # what's missing or now-orphaned (alias: status)
terva locale list                                   # coverage %, per language (alias: ls)
```

`list` reports the core-UI percentage, then one bracket per side catalog —
`[prompts …]`, `[help …]`, `[tui …]`, `[web …]`, `[stage …]` — each showing
translated over total for that catalog (the prompt catalog is the largest of
the dotted-key ones, and grows with every new model-facing surface).

### 4. Translate-as-you-use (optional)

To fill gaps while actually driving terva:

```bash
TERVA_CAPTURE_LOCALE=1 TERVA_LANG=fi terva
```

Every untranslated string you hit is recorded to
`$TERVA_HOME/locales/fi.todo.json` (and prompt misses to
`locales/prompts/fi.todo.json`). Fill in the entries you want, then:

```bash
terva locale merge fi     # folds filled entries into fi.json; blanks stay pending
```

---

## Customizing terva's prompts

The prompt catalog is where terva keeps the English it sends *to the model*:
how it introduces itself, what `/study` actually asks for, how it summarizes a
finished swarm, the compaction instructions. Overriding these lets you reshape
terva's behavior and voice — **and this works in English**, so you can keep the
UI in English while giving terva a different personality or house style.

Some of the keys (`system.identity.*`, `system.conventions.*`) overlap with
what [personas and `--system-prompt`](personas.md) already let you change; the
prompt overlay is the finer-grained, key-by-key seam for the pieces those don't
reach.

### Customizing in English (no language switch)

`en` is the built-in source, so terva normally reads no files for it — the
fast, byte-identical path. But the moment you create an English overlay, terva
honors it. Scaffold one:

```bash
terva locale init en
```

This writes `$TERVA_HOME/locales/en.json` (UI) and `locales/prompts/en.json`
(prompts), **seeded with the current English so you edit in place**. Change
only the values you want; delete the rest (anything you don't override falls
back to the built-in English, so a two-line file is perfectly valid).

For example, to give terva a different identity and a punchier `/study`, edit
`$TERVA_HOME/locales/prompts/en.json` down to just:

```json
{
  "system.identity.default": "You are Aava, a tidewatcher who happens to be an excellent coding assistant.",
  "study.dir.current": "Survey this whole directory like a harbor-master doing rounds."
}
```

Next launch, the assembled system prompt leads with your identity and `/study`
carries your wording. Confirm what actually got sent with
[`--dump-prompt`](debugging-prompts.md):

```bash
terva --dump-prompt -p "hi"
```

### The prompt keys

Run `terva locale init en` (or read `locales/prompts/en.json` in a checkout) to
see the full set. The families:

| prefix | what it drives |
|---|---|
| `system.identity.*` | terva's opening "You are …" for coding / chat / play, default and custom-persona-named |
| `system.conventions.*` | the output/tooling discipline paragraph (coding vs chat/play) |
| `system.immersive.*`, `system.*_hint`, `system.footer` | immersive framing, the docs/status-tool hints, the date+cwd footer |
| `compact.system`, `compact.instruction` | the summarizer's system prompt and the "summarize this" instruction |
| `study.*`, `skill.directive` | the `/study` task variants and the "use the X skill for:" preamble |
| `swarm.*` | the auto-swarm addendum, the persona roster, and every line of the sub-agent result summary |
| `raati.*` | the [deliberation](raati.md) panel's whole script — the clerk's system + prompt, the evidence framing, each round's header/question, the cross-examination, and the summarizer |
| `classifier.*` | the [approval classifier](permissions.md#the-approval-classifier) that screens a tool call before it prompts. `classifier.system` is its whole instruction, `classifier.policy` frames the site policy appended to it, and `classifier.call.*` are the four labels that render the call being judged. ⚠ `classifier.system` must keep the five tokens `Classify` parses: the JSON field names `decision` and `reason`, and the verdict words `allow`, `deny`, and `ask`. Translate those and every verdict becomes unparseable, an unparseable verdict abstains, and an abstention looks exactly like a screener that is working. |
| `context.pressure*` | the warning the agent is handed when the context window is filling, assembled from four parts: `context.pressure.guard` (the do-not-reply preamble), `context.pressure.gauge` (how full), one `context.pressure.advice.*` line per pressure band, and a closing policy sentence — `context.pressure` or `context.pressure.autocompact_now` normally, the `no_autocompact` pair when auto-condense is off |
| `play.*`, `chat.*` | the `--play` cast/actor framing and the `--chat` intro / idle-nudge / attachment preamble |
| `stage.*` | the Stage side-channel generators — the meta-narrator router and voiced line (`stage.route.*`, `stage.voice.*`), the Suggest drafter (`stage.suggest.*`), the card doctor/editor contracts (`stage.doctor.*`, `stage.editor.*`), the advance-turn cue, and the shared section framing (`stage.frame.*`). The largest family. ⚠ `stage.route.narrator_name` and the words the router/doctor parse from model output (`Narrator`, the JSON field names and `warn`/`info`/`suggestion` severities inside the doctor tasks) must survive translation intact — the narrator name is round-tripped (a translated name is matched too), the JSON skeleton is not. |
| `title.*` | the session title generator — its system prompt, the "Title this chat." instruction, and the seed's section labels |
| `persona.user.*` | how a prompt introduces the human in the scene: the "About X (the user…)" frame and the gender/pronoun clauses (including the anti-inference steers) |

Keep any `%s`/`%d` in a template — they're filled at runtime (a path, an agent
id, the persona name). `terva locale validate
$TERVA_HOME/locales/prompts/en.json` checks that parity.

---

## Retuning a tool description

The **tools** catalog is the same seam pointed at a different surface: the
description each tool advertises to the model. One key per tool, named for the
tool itself — `tool.read.description`, `tool.bash.description`,
`tool.raati_convene.description`.

This is a separate catalog from **prompts** because the two are edited for
different reasons. A prompt changes what terva *says*; a tool description
changes what the model *does*, and the effect shows up directly in which tools
get called and how. It is the highest-leverage model-facing text terva has: a
recorded session had the model pin [raati](raati.md) to level 1 for its entire
run because the description never offered the alternative.

It overlays in English exactly like prompts do:

```json
// $TERVA_HOME/locales/tools/en.json
{
  "tool.bash.description": "Run a shell command with %s. Commands run in %s. Prefer read/write/edit and grep/glob over shell equivalents."
}
```

Two things to keep:

- **The `%s` verbs.** `tool.bash.description` is handed the resolved shell name
  and the directory commands run in, and `tool.world_note.description` the
  reserved scene-state entry name. Drop a verb and the model loses the one fact
  a relative path depends on. `terva locale validate` checks the parity.
- **Whatever the tool's own schema depends on.** A description that stops
  mentioning an argument is an argument the model stops passing.

Because it is per-key, an overlay naming one tool leaves every other
description at its shipped English — so trying one rewording is a one-key file,
and comparing two is a file swap rather than two builds.

> **Careful with structural tokens.** A few model-facing strings are left in
> English on purpose because the model (or terva) parses them positionally —
> XML-ish wrappers, the `## Context Summary` marker, transcript role markers.
> Those aren't in the prompt catalog; don't try to translate them.

---

## Where everything lives

```text
$TERVA_HOME/locales/
  <lang>.json               core UI translations / overrides  (English-as-key)
  <lang>.todo.json          capture gaps to fill in
  tui/
    <lang>.json             interactive-TUI strings           (English-as-key)
  web/
    <lang>.json             web control-panel strings         (English-as-key)
  stage/
    <lang>.json             Stage play-surface strings        (English-as-key)
  prompts/
    <lang>.json             prompt overrides                  (dotted keys)
    <lang>.todo.json        captured prompt gaps
  help/
    <lang>.json             help-screen overrides             (dotted keys)
    <lang>.todo.json        captured help gaps
```

`en` uses the same layout for in-place customization.

## Contributing a language upstream

A partial language is a welcome PR — English fills the rest.

```bash
terva locale export fi     # → $TERVA_HOME/locales/fi.export.json (clean, sorted, non-empty only)
```

Then in a terva checkout:

1. Copy it to `packages/i18n/locales/fi.json` (and, for prompt translations,
   `packages/i18n/locales/prompts/fi.json`).
2. `go run ./cmd/terva-i18n-lint -check` (references current) and
   `terva locale validate` the file.
3. `just test-pkg ./packages/i18n`, then open a PR.

The [`write-terva-locale`](../packages/agent/skills/builtin/write-terva-locale/SKILL.md)
skill has the same guide in a form terva itself can follow if you ask it to
translate.

---

## For developers: making new text translatable

When you add operator-facing text in Go:

- **At a render site** (a function that runs after startup): wrap the literal.
  ```go
  status = i18n.T("not logged in. type /login first.")
  msg    = i18n.T("saved %d files", n)          // args → fmt.Sprintf
  label  = i18n.TN(n, "%d agent", "%d agents")  // plural via CLDR
  err    = i18n.Errorf("no bot token configured")  // translated error (vet-safe)
  ```
- **In a table built at `init()`** (slash descriptions, menu labels): the
  language isn't known yet, so **mark** the literal with `i18n.M` and translate
  it with `i18n.T` at the render site. Never call `i18n.T` in a `var`
  initializer or `init()` — it freezes English before `Configure` runs.
- **For large stable text keyed by a dotted id**, use `i18n.P` (a model-facing
  prompt → `locales/prompts/`) or `i18n.H` (an operator-facing `--help` screen
  → `locales/help/`): `i18n.P("study.file", english, args…)`,
  `i18n.H("help.bot", english, args…)`. The english default may be a string
  literal or a documented `const` (the extractor resolves either, including a
  `+`-concatenated const). Write the whole thing as **one** parameterised
  template — never `` `…` + runtimeValue() + `…` `` (the extractor can't
  resolve that and fails the build; use a `%s` and pass the value). Emit prompts
  with `sb.WriteString(i18n.P(...))`, never `fmt.Fprintf(&sb, i18n.P(...))` (a
  non-constant format trips `go vet`).
- Leave command names, flags, model/provider ids, URLs, and paths unwrapped —
  they're not prose.
- **Most of the model-facing surface stays English, with two carve-outs.** A
  tool's *schema* and the *result text a tool produces* are model-API
  machinery: the operator meets them raw in a tool-view pane, like code, so
  leave them alone. Round 2 of the i18n work (`docs/proposals/i18n-round-2.md`,
  D3) drew that line around the whole tool surface, and two pieces have since
  moved out from behind it. A tool *description* goes through `i18n.D` into
  `locales/tools/`, so an operator can retune it in place (see *Retuning a tool
  description* above). A **gate refusal reason** goes through `i18n.T`: the
  sentences in `packages/core/confirm.go` that say why a call was refused are
  read by a person as often as by the model. `i18n.P` remains for canned prose
  that shapes what the user ultimately reads: identity, narration cues,
  summaries, section framing.
- **Wire errors translate their prose, never their code.** A ctrlproto error
  the panel or Stage shows a user gets its message translated at the
  construction site — `ctrlproto.Errorf(code, "%s", i18n.T("what went wrong",
  args…))` (the outer `"%s"` keeps vet's constant-format rule) — while the
  machine `Code` stays English forever; clients classify by code. Internal
  operation wraps (`"open session: %v"`) and log lines stay English.

After wrapping, regenerate the reference catalogs and commit them:

```bash
go run ./cmd/terva-i18n-lint          # rewrites locales/en.json + locales/prompts/en.json
go run ./cmd/terva-i18n-lint -unwrapped packages/agent/modes   # advisory: prose not yet wrapped
```

`just lint` (hence `just ci`) runs `-check` and fails if either reference is
stale. Default-English output must stay byte-identical after wrapping
(`i18n.T(x) == x` when the language is English), so existing golden and
`strings.Contains` tests never churn.

In the **web client**, the same rules wear TypeScript clothes: wrap render-site
literals in `t('…')` / `tn(n, '…', '…')` (English-as-key, `%s`/`%d` args), mark
module-level table strings with `m('…')` and translate them where rendered with
`tr(v)` (the `i18n.M`/`T` pair), and never pass a non-literal key to `t()`. The
extractor (`npm run i18n-extract`) routes `src/apps/stage/` to the stage
catalog and everything else to web, and its `i18n-check` (in `just web-check`)
also **fails on unwrapped user-visible strings** — JSX text, `placeholder` /
`title` / `aria-label` / `alt`, `confirm()`/`prompt()` prose. A string that is
deliberately English (a wordmark, a key name) takes an `i18n-exempt` comment on
its line or the line above, with a reason.
