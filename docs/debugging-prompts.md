# Debugging a prompt

When a run behaves oddly — a persona/card isn't taking, lore won't fire, an
extension's context is missing, the model ignores an instruction — the first
question is almost always **"what did we actually send?"** This guide walks
through answering that.

## The mental model

Every terva request is assembled from four regions:

| region | what's in it | cached? |
|---|---|---|
| **system** | identity/intro (or a card's `system_prompt`), persona/card charter, harness conventions, `constant` lore, skills manifest, context files, AGENTS.md, and — **coding mode only** — a terva-docs hint + a date/cwd footer | **yes** — the cached prefix, set once per session |
| **messages** | the conversation transcript (a card's seeded greeting is `messages[0]`) | yes (grows) |
| **tail** | keyword-**triggered** lore, a card's `post_history_instructions`, extension context cards | **no** — a per-turn block after the cache breakpoint |
| **tools** | the tool schemas sent alongside | yes |

The load-bearing rule: **static → cached prefix; dynamic → uncached tail.**
Anything that changes turn-to-turn (triggered lore, PHI) must live in the tail,
or it would bust the prompt cache. If something dynamic shows up in `system`,
that's a bug.

## First move: `--dump-prompt`

`--dump-prompt` assembles the prompt for the pending turn, prints it, and
**exits before any model call** — no API key, no tokens:

```
terva --card examples/cards/aava-v2.json --dump-prompt -p "tell me about the fog-bell"
```

Three formats:

- **`--dump-prompt`** (text, default) — annotated, each segment tagged with its
  `[source]`. The *"where did this come from"* view.
- **`--dump-prompt=json`** — the structured manifest, for assertions/tooling.
- **`--dump-prompt=raw`** — segment text concatenated, unlabeled: the logical
  prompt. (Not the literal wire payload — no `cache_control` markers or JSON
  escaping; a wire-level dump is a planned follow-up.)

The manifest is the **source of truth**: the flat system-prompt string is
*derived* from the same labeled segments the dump shows, so the dump can't
disagree with what the model receives.

### The fastest read: section → sources

```
terva --card examples/cards/aava-v2.json --dump-prompt=json -p "the fog-bell" \
  | jq -c '.sections[] | {section: .name, sources: [.segments[].source]}'
```

```
{"section":"system","sources":["card:system_prompt","charter","conventions","restricted-workspace","card:character_book"]}
{"section":"messages","sources":["card:greeting","user"]}
{"section":"tail","sources":["lore:triggered [card]","card:post_history"]}
{"section":"tools","sources":[]}
```

That one line answers most questions: *is the card's `system_prompt` in `system`?
did the greeting seed? did the fog-bell lore fire into `tail`? are tools present?*

The chat/play system section is deliberately leaner than coding mode: the
**date/cwd footer** and the **terva-docs hint** are dropped (a character
shouldn't be told today's date or pointed at a `read` tool it doesn't have), so
their absence here is expected, not a bug.

## What the source labels mean

| source | region | meaning |
|---|---|---|
| `identity-intro` | system | terva's generated identity intro (branded), used when nothing overrides it |
| `card:system_prompt` / `card:framing` | system | a card owns the intro: its `system_prompt` (with `{{original}}` → a short brand-free framing), or that framing alone when the card has no `system_prompt` |
| `persona:introduction` | system | a native persona's `agent_introduction` field replacing the branded intro (conventions still kept) |
| `charter` | system | the persona/card descriptive body (description/personality/scenario/examples) |
| `conventions` | system | terva's output invariants (always last so nothing erodes them) |
| `lore:constant` / `card:character_book` | system | always-on lore folded into the cached prefix |
| `skills`, `context-files`, `agents-md` | system | skill manifest, `--context-file`/config context, repo AGENTS.md |
| `restricted-workspace` | system | note that project content was withheld (untrusted cwd) |
| `card:greeting` | messages | the seeded `first_mes` (or `--greeting N`) |
| `lore:triggered [files]` | tail | keyword-triggered lore that fired this turn, labeled by source file |
| `card:post_history` | tail | a card's `post_history_instructions` |
| `extension-context` | system/tail | an extension's static/`register_context` block |

## In-session

- **`/lore`** — lists the run's active lore (name · trigger · source) and shows
  which entries **fired last turn**. The in-session version of the tail check.
- **`/context`** — token breakdown of the assembled context and what extensions
  inject.

## Troubleshooting

| symptom | check | likely cause → fix |
|---|---|---|
| A lore entry never fires | `--dump-prompt=json` tail sources, or `/lore` "fired last turn" | keyword not in the scan window → widen `scan_depth`, or the term isn't in the recent messages; whole-word/case mismatch; `--no-lore` set; `.terva/lore` untrusted → `terva trust`. Validate with `terva lore validate`. |
| A card's `system_prompt` is ignored | is `card:system_prompt` in `system`? | `--system-prompt`/`SYSTEM.md` (a raw `custom` replace) wins over a card; or the card has none. |
| The greeting is missing | is `messages[0]` = `card:greeting`? | it only seeds on a **fresh** session (not `--continue`/`--resume`), or `first_mes` is empty. Try `--greeting N`. |
| The character calls me the wrong name (or "User") | read the intro/greeting text where the card uses `{{user}}` | no name set → pass `--as NAME`, or set `user_name` (an interactive card session asks once and remembers it **globally**, so it persists across projects even under project-scoping). Precedence: `--as` > a trusted project's `user_name` > global `user_name` > `"User"`. |
| Constant lore isn't in the prefix | `system` sources include `lore:constant`? | the entry isn't `constant: true`, `--no-lore`, or untrusted project dir. |
| Wrong tools present/absent | `tools` section | mode: `--chat` (none), `--play`/`--no-workspace-tools` (ext/MCP only), `--no-tools` (none). |
| Prompt cache keeps missing | anything dynamic in `system`? | dynamic content belongs in `tail`. If a per-turn value landed in the prefix, that's the bug. |
| PHI/lore "not taking effect" | is it in `tail`? | the tail is sent after history (for Anthropic, a trailing user turn) — confirm it's present and not empty. |

## Offline assertions

Because `--dump-prompt=json` is structured and needs no model, you can assert
assembly in a test or script:

```
terva --card mycard.png --dump-prompt=json -p "vault" \
  | jq -e '.sections[] | select(.name=="tail") | .segments | map(.source) | any(startswith("lore:triggered"))'
```

The `packages/agent/card` and `packages/agent` tests use exactly this idea to
verify the full assembly (card `system_prompt`, constant-lore→prefix, greeting,
triggered-lore→tail, PHI→tail) without ever calling a model.
