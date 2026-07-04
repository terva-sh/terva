---
name: write-terva-locale
description: Translate terva's UI into another language, complete or fix a locale, or wrap new strings for translation. Use when the user wants to localize terva, add/finish a language (fi, pt-BR, …), edit translated text, or contribute a translation upstream.
---

# Translating terva

Use this skill when the user wants to **localize terva** — start a new UI
language, finish or fix an existing one, or make a newly added string
translatable. terva ships English and loads translations from a per-key
catalog that an operator can edit and contribute back.

## How terva's i18n works (read this first)

- **English is the key.** The message id *is* the English sentence. A locale
  file maps that English to your language:
  `"start a fresh session": "aloita uusi istunto"`. There is no separate key to
  invent or look up.
- **Embedded default + operator overlay.** Shipped translations live in
  `packages/i18n/locales/<lang>.json` (embedded in the binary). Your edits live
  in `$TERVA_HOME/locales/<lang>.json` and **shadow the embedded file per key** —
  override one string without copying the rest.
- **Graceful fallback.** Any key you leave blank (`""`) or don't include falls
  back to English, so a partial translation is always safe to ship and run.
- **Selecting a language.** `TERVA_LANG=<tag> terva`, or set `"language": "<tag>"`
  in `config.json`. The tag is BCP-47: `fi`, `pt-BR`, `de`. `en`/`en-*` is the
  built-in source — no file read UNLESS the operator opts into an English
  overlay (`$TERVA_HOME/locales/en.json` or `prompts/en.json`) to reword or
  override strings in place. See `docs/localization.md` in the repo.

## Translating a language — the workflow

1. **Scaffold the file.** `terva locale init <lang>` writes
   `$TERVA_HOME/locales/<lang>.json` listing every source string. Existing
   translations are pre-filled (so you tweak in place); the rest are blank.
2. **Edit the JSON.** For each entry, replace the empty value with your
   translation. The key stays exactly as-is — never edit a key.
   ```json
   {
     "show this help": "näytä tämä ohje",
     "saved %d files": "",              // still to translate
     "%d agent|%d agents": { "one": "%d agentti", "other": "%d agenttia" }
   }
   ```
3. **Preserve the machinery.** Keep every `%s` / `%d` / `%0.1f` format verb — same
   ones, any order. Keep `{{user}}`/`{{char}}` macros, flag names (`--persona`),
   command names (`terva locale`), and file paths (`$TERVA_HOME/...`,
   `docs/rpc.md`) verbatim. Translate the prose around them.
4. **Plurals.** A plural entry's key is `"english one|english other"`; its value
   is an object of CLDR categories. Your language's set may be more than
   `one`/`other` (Polish has `one`/`few`/`many`/`other`) — `terva locale init`
   scaffolds the right categories for the tag. Fill each; `other` is required.
5. **Check it.** `terva locale validate` (JSON + that your `%`-args match the
   source), `terva locale diff <lang>` (what's missing or now-orphaned),
   `terva locale list` (coverage %).

## Translate-as-you-use (capture)

To fill gaps while actually using terva:
```
TERVA_CAPTURE_LOCALE=1 TERVA_LANG=<lang> terva
```
Every untranslated string you encounter is recorded to
`$TERVA_HOME/locales/<lang>.todo.json`. Fill in the entries you want, then
`terva locale merge <lang>` folds the filled ones into `<lang>.json` (blank ones
stay pending). Repeat as you explore more of the UI.

## Translating the dotted-key catalogs (prompts + help)

Besides the UI strings, terva has two catalogs of **large stable text keyed by
a dotted id** rather than English-as-key:

- **prompts** (`locales/prompts/<lang>.json`) — canned English sent *to the
  model*, not shown to you: the `/study` task, the auto-swarm result summary,
  the base system-prompt segments (identity, conventions, framing), the
  compaction instructions. These matter for coherence: if your model answers in
  Finnish, you want the *whole* exchange in Finnish, not English instructions
  under a Finnish reply.
- **help** (`locales/help/<lang>.json`) — the big `terva <cmd> --help` screens.

Both work identically (below says "prompts", but `help/` is the same). `terva
locale init <lang>` scaffolds both alongside the UI file, and `terva locale
list` shows `[prompts N/M] [help N/M]`.

- **Dotted keys, not English-as-key.** Prompts are large and stable, so each is
  keyed by a stable id — `study.file`, `swarm.summary.instruction`,
  `system.identity.default` — and the file is a flat `{ "key": "template" }`.
- **Separate file.** Embedded default `packages/i18n/locales/prompts/<lang>.json`;
  operator overlay `$TERVA_HOME/locales/prompts/<lang>.json`. `terva locale init
  <lang>` scaffolds it alongside the UI file, **seeded with the English template
  to rewrite** (not blank — you edit the source prompt into your language).
- **Leaving one English keeps the default.** A prompt whose value still equals
  the English simply falls through, so a partial prompt translation is safe.
- **Keep the placeholders.** A template's `%s`/`%d` are filled at runtime (a path,
  an agent id, the persona name) — keep them, same count. `terva locale validate
  $TERVA_HOME/locales/prompts/<lang>.json` checks arg parity; `terva locale list`
  shows `[prompts N/34]` coverage. Capture writes gaps to
  `locales/prompts/<lang>.todo.json`.
- Contribute the same way: copy to `packages/i18n/locales/prompts/<lang>.json`.

## Contributing a language upstream

1. `terva locale export <lang>` writes a clean, sorted
   `$TERVA_HOME/locales/<lang>.export.json` of your non-empty translations.
2. In a terva checkout, copy it to `packages/i18n/locales/<lang>.json`.
3. `go run ./cmd/terva-i18n-lint -check` (reference current) and
   `terva locale validate packages/i18n/locales/<lang>.json`.
4. `just test-pkg ./packages/i18n` and open a PR.

A partial language is a welcome PR — English fills the rest.

## Making a NEW string translatable (for developers)

When you add user-facing text in Go:

- **At a render site** (a function that runs after startup — a `View()`, a
  `PrintHelp`, an error shown to the user): wrap the literal directly.
  ```go
  status = i18n.T("not logged in. type /login first.")
  msg    = i18n.T("saved %d files", n)          // args → fmt.Sprintf
  label  = i18n.TN(n, "%d agent", "%d agents")  // plural via CLDR
  ```
- **In a data table built at `init()`** (slash-command descriptions, menu
  labels, spinner lines): the language isn't known yet, so **mark** the literal
  with `i18n.M` (identity — returns the English) and translate at the display
  site with `i18n.T`.
  ```go
  var group = i18n.M("session")                     // declared at init
  ...
  out = append(out, header{Name: i18n.T(spec.group)}) // translated at render
  ```
  **Never call `i18n.T` at package-init or in a `var` initializer** — it would
  freeze whatever language was active before `Configure` ran (English).
- Use `i18n.TC("context", "Save")` only when the same English needs different
  translations in different places.
- **For large stable dotted-key text** use `i18n.P` (a prompt SENT TO THE MODEL —
  a `/study`-style task, a coordination addendum, a system-prompt segment) or
  `i18n.H` (a `terva <cmd> --help` screen). The dotted key is stable; the english
  is the default (a literal or a documented `const` — the extractor resolves
  either). Build the *whole* thing as ONE parameterised template — never
  concatenate with English glue or a runtime value
  (`i18n.H("help.trust", "…lives in %s…", path)`, NOT `` `…`+path()+`…` `` — the
  extractor can't resolve that and fails the build). `P`/`H` do the `Sprintf`
  themselves, so write `sb.WriteString(i18n.P(key, tmpl, arg))`, never
  `fmt.Fprintf(&sb, i18n.P(...))` (a non-constant format trips `go vet`). Leave
  structural/wire tokens the model parses positionally (`<conversation>`,
  `## Context Summary`, role markers) in English.
- Leave command names, flags, provider/model ids, URLs, and file paths
  unwrapped — they are not prose.
- After wrapping, regenerate the references: `go run ./cmd/terva-i18n-lint`
  (updates `packages/i18n/locales/en.json` for `T`/`TC`/`TN`/`M` and
  `locales/prompts/en.json` for `P`). `-check` fails CI if either is stale.
  `-unwrapped <dir>` lists prose literals not yet wrapped, as a migration
  checklist.

Default-English output must stay byte-identical after wrapping (`i18n.T(x) == x`
when the language is English), so existing tests never change.
