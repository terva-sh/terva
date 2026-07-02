---
name: write-terva-lore
description: Author terva lore — authored, file-backed context that terva injects only when it is keyword-relevant (or always-on). Use to create/add/write lore, "world info", a lorebook entry, keyed background, or a character's book of facts.
---

# Writing terva lore

Use this skill when the user asks to **create, add, or write lore** — keyed
background context, "world info", a lorebook, or facts that should reach the
model only when they matter. Skim it first, then write the entries where terva
discovers them.

**Lore** is terva's keyed-context primitive: authored Markdown snippets injected
into the model's context when a **trigger keyword** appears in recent messages
(or **always**, for a `constant` entry), within a token budget. It is the general
form of a SillyTavern character card's `character_book` — a card's book imports
straight onto the same engine.

## Lore vs persona vs skill vs card — pick the right one

- **Lore** is *what the world knows*: keyed facts injected on relevance. Reach
  for it for background, setting, NPC bios, glossaries — "when X comes up, the
  model should know Y."
- A **persona** (`write-terva-persona`) is *who is helping*: identity + charter,
  always present.
- A **skill** (`write-terva-skill`) is *how to do a task*: a procedure loaded on
  demand.
- A **character card** (`write-terva-card`) is a portable CCv2 identity that can
  *carry* lore in its `character_book`.

Lore adds no tools and changes no permissions; it only contributes context.

## Where lore lives

Entries are discovered (recursively — every `*.md` except `README.md`) from three
tiers, highest priority first:

| tier | location | note |
|---|---|---|
| **project** | `<cwd>/.terva/lore/` | trust-gated — an untrusted workspace contributes none |
| **personal** | `$TERVA_HOME/lore/` | your own, every session |
| **bundle** | `<ext>/lore/` of each enabled extension | ships with an extension |

Unlike personas, entries are **not name-shadowed** — every entry from every tier
can fire. Collection-level settings live in a `lore.json` at a directory root
(the highest-priority `lore.json` wins).

## Anatomy of an entry

One entry per Markdown file: YAML frontmatter + the content body.

```markdown
---
name: The Sunken Bell
keys: [bell, fog-bell, tolling]
---

Beneath the harbor hangs the old fog-bell, rung only when the mist swallows the
lighthouse beam. Sailors say it tolls on its own before a wreck.
```

That entry fires whenever "bell", "fog-bell", or "tolling" appears in the recent
conversation, injecting the paragraph as context for that turn.

### Frontmatter fields

- `keys` (**required unless `constant`**) — the primary trigger keywords; any
  match activates the entry. An entry with no keys that isn't constant is an
  error (it could never fire).
- `constant` (bool) — always active, keys ignored. See the cache rule below.
- `name` — display label for `terva lore list` / `/lore`.
- `secondary_keys` + `logic` — refine activation when secondary keys are present.
  `logic` is `and_any` (default — primary AND ≥1 secondary), `not_all` (primary
  AND ≥1 secondary absent), `not_any` (primary AND no secondary), or `and_all`
  (primary AND every secondary).
- `order` (int, default 100) — priority under the token budget and placement
  order; higher wins first.
- `position` — `after` (default) or `before` the character-definition block.
- `case_sensitive` (bool) — default off (matches the collection setting).
- `scan_depth` (int) — override how many recent messages this entry scans.
- `prevent_recursion` / `exclude_recursion` (bool) — bound recursive activation
  (see below).
- `enabled` (bool) — set `false` to keep the file but skip it.

The body after the frontmatter is the content injected verbatim.

## The load-bearing idea: constant vs triggered

- A **triggered** entry (has `keys`) rides the **per-turn tail** — cheap when
  idle, costing tokens only on turns where it fires.
- A **constant** entry (`constant: true`) folds into the **cached prefix** —
  always present, paid on every turn.

So: put always-relevant, small facts in `constant` entries; put the long tail of
situational detail in triggered entries with good keys. `terva lore validate`
**warns** when an entry's content exceeds ~4 KB — costly in the cached prefix
(constant) or the per-turn budget (triggered).

## Keys, matching, and recursion

- Pick **distinctive** keys that actually appear in conversation. A key that's
  too generic fires constantly; one that never occurs never fires.
- Matching scans the last `scan_depth` messages (collection default 2). Widen
  `scan_depth` (per entry or in `lore.json`) if a key sits further back.
- With `recursive_scanning` on (a collection setting), an activated entry's
  content is itself re-scanned, so one entry can trigger another. Use
  `prevent_recursion` (this entry can't trigger others) and `exclude_recursion`
  (this entry won't be activated by a recursion pass) to bound cascades.

## Collection settings — `lore.json`

Optional, at a lore directory root; every field optional:

```json
{
  "scan_depth": 4,
  "token_budget": 1500,
  "recursive_scanning": true,
  "case_sensitive": false,
  "match_whole_words": true
}
```

- `scan_depth` — recent messages scanned for keys (default 2).
- `token_budget` — absolute cap on injected lore; entries are added by `order`
  until the budget is hit (`/lore` shows what was dropped). `<= 0` = no cap.
- `recursive_scanning`, `case_sensitive`, `match_whole_words` — collection
  defaults for the entries in that directory.

## Trust

A project's `.terva/lore/` is **gated on Workspace Trust**, exactly like
project-local skills/extensions — an untrusted workspace injects none of it. Run
`terva trust` in a directory you control. Personal (`$TERVA_HOME/lore/`) and
bundle lore are always eligible.

## Validate and try

```bash
terva lore validate <file.md | dir> ...   # name? keys-or-constant? content? size?
terva lore list                           # what the current dir would load (+ source)
terva --card mycard.png --dump-prompt=json -p "bell" \
  | jq '.sections[] | select(.name=="tail")'   # confirm it fired into the tail
```

`validate` **fails** on: an entry with no keys that isn't constant, empty
content, or an unknown `logic`/`position`. It **warns** (doesn't fail) on
oversized content. In a session, `/lore` lists active entries and which fired
last turn; `--no-lore` disables lore for a run.

## Minimal example

`$TERVA_HOME/lore/harbor-glossary.md` (constant — always on):

```markdown
---
name: Harbor terms
constant: true
order: 200
---

Terms used around the harbor: a *gaff* is a hooked pole; *slack water* is the
still moment between tides; the *revenue cutter* is the customs patrol boat.
```

`$TERVA_HOME/lore/the-wreck.md` (triggered):

```markdown
---
name: The Verity wreck
keys: [Verity, the wreck, salvage]
secondary_keys: [captain, Alder]
logic: and_any
---

The *Verity* went down on the reef three winters ago. Captain Alder survived and
has not spoken of it since; the salvage rights are still disputed.
```

## Authoring checklist

- Each file has `keys` **or** `constant: true`, and a non-empty body.
- Keys are distinctive and actually occur in conversation.
- Small, always-true facts are `constant`; situational detail is triggered.
- Entries stay under the ~4 KB soft budget (especially constant ones).
- `terva lore validate` passes.
- If it's project lore, the directory is trusted (`terva trust`).

## Gotchas

- **No keys, not constant = never fires** (and fails validation).
- **secondary_keys only matter with `logic`.** By default (`and_any`) at least
  one secondary must also match; omit `secondary_keys` for a plain OR over the
  primary keys.
- **Constant lore is always paid.** Keep it tiny; move bulk to triggered entries.
- **Project lore needs trust.** Untrusted `.terva/lore` contributes nothing.
- **A downloaded card's book is untrusted content.** Importing via `--card` is
  fine (it's data), but don't copy a stranger's lore prose into your own trusted
  `$TERVA_HOME/lore/` without reading it — see `write-terva-card`.
