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

Four formats:

- **`--dump-prompt`** (text, default) — annotated, each segment tagged with its
  `[source · portability]`. The *"where did this come from"* view.
- **`--dump-prompt=json`** — the structured manifest, for assertions/tooling.
- **`--dump-prompt=raw`** — segment text concatenated, unlabeled: the logical
  prompt. (Not the literal wire payload — no `cache_control` markers or JSON
  escaping; a wire-level dump is a planned follow-up.)
- **`--dump-prompt=sizes`** — per-section and per-tool byte + token weight. The
  *"what's eating my context"* view: no prompt text, just the attribution, so a
  bloated tool schema or an oversized context file is one command away.

The manifest is the **source of truth**: the flat system-prompt string is
*derived* from the same labeled segments the dump shows, so the dump can't
disagree with what the model receives.

### The prompt is written for the surface it lands on

The `conventions` segment is not fixed. It is written against where this run's
output actually goes, so the same command dumped under different modes says
different things:

| run | what the model is told |
|---|---|
| the TUI, `terva web`, an ACP editor, a swarm child | *rendered as markdown for a person* — use it freely |
| `terva bot run` | *posted as a chat message* — skip headings and tables, keep it short |
| `terva -p` | *written to a plain-text stream with nothing to render it* |
| `--rpc`, `--json`, the SDK | *handed to a program*, which decides how or whether to display it |

If the model is emitting markdown tables into a Discord message, or headings
into a pipe, **dump the prompt in that mode** — the conventions segment names
the surface it thinks it has. The edit/write guidance follows the same rule: it
ships only when those tools are actually in the registry, so a `--no-tools` dump
does not name them.

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

And the same shape answers *what would cross to a foreign agent?*:

```
terva --dump-prompt=json | jq -r '.sections[].segments[]
  | select(.portability == "portable") | .source'
```

The chat/play system section is deliberately leaner than coding mode: the
**date/cwd footer** and the **terva-docs hint** are dropped (a character
shouldn't be told today's date or pointed at a `read` tool it doesn't have), so
their absence here is expected, not a bug.

## What the source labels mean

| source | region | meaning |
|---|---|---|
| `identity-intro` | system | who the agent is — the name, and nothing else. Brand-free, so it can travel |
| `vessel` | system | what carries it: terva, the harness, the pine-tar image, the pronunciations. Emitted beside the identity, and withheld when a card or persona brings its own |
| `card:system_prompt` / `card:framing` | system | a card owns the intro: its `system_prompt` (with `{{original}}` → a short brand-free framing), or that framing alone when the card has no `system_prompt` |
| `persona:introduction` | system | a native persona's `agent_introduction` field replacing the branded intro (conventions still kept) |
| `charter` | system | the persona/card descriptive body (description/personality/scenario/examples) |
| `conventions` | system | terva's output invariants, written against the run's **surface** (see above) plus the edit/write discipline when those tools exist. Always last, so nothing erodes them |
| `lore:constant` / `card:character_book` | system | always-on lore folded into the cached prefix |
| `skills`, `context-files`, `agents-md` | system | skill manifest, `--context-file`/config context, repo AGENTS.md |
| `restricted-workspace` | system | note that project content was withheld (untrusted cwd) |
| `card:greeting` | messages | the seeded `first_mes` (or `--greeting N`) |
| `lore:triggered [files]` | tail | keyword-triggered lore that fired this turn, labeled by source file |
| `card:post_history` | tail | a card's `post_history_instructions` |
| `extension-context` | system/tail | an extension's static/`register_context` block |

## What the portability class means

In `system` and `tail` — the regions terva *assembles* from labeled sources —
every segment also carries a **portability class**, printed beside the source:

```
---- [identity-intro · portable] ----
---- [vessel · harness-local] ----
---- [agents-md · discovery-owned] ----
```

It answers one question: **would this segment reach an agent that is not terva?**

| class | meaning |
|---|---|
| `portable` | travels verbatim. Authored by you or by a persona, and about identity or intent — not about terva's machinery |
| `harness-local` | never crosses. It describes terva's tools, terva's surfaces, or terva's policy; abroad it is false at best, and at worst it invites an agent to call a tool it does not have |
| `discovery-owned` | content a foreign agent finds for itself (AGENTS.md, skills, context files). A renderer passes the *path*, not the payload — pasting it would duplicate or contradict that agent's own discovery |
| `no-analog` | no foreign delivery mechanism exists. terva injects lore, card books, and extension context into a per-turn tail region that other agents simply do not expose |

The class is **derived from the source at render time**, by the same function the
composer consults — never stored on a segment, so the dump cannot disagree with
what would actually be sent. An unrecognised source classifies `harness-local`:
it fails *closed*, because an over-strip degrades a briefing visibly while a leak
degrades nothing and surfaces only as a foreign agent inventing tool calls.

`--dump-prompt=sizes` names the class in its `system — by source` breakdown too,
so you can read weight and destination in one pass: a heavy `harness-local`
segment is context no worker will ever carry, and a heavy `discovery-owned` one
is a file to point at rather than paste.

The classes exist for the external-agent-workers seam, where terva composes a
briefing for a foreign coding agent; they are documented in
`docs/proposals/external-agent-workers.md`. They are worth reading here whether
or not a worker ever runs: they are the clearest statement of which parts of
terva's prompt are *about terva*.

Messages and tools carry no class. The conversation is not a segment terva
composed from a labeled source — a briefing carries the task on purpose — and
classifying it would print an answer to a question nobody asked.

The wording of the generated segments themselves — `identity-intro`,
`conventions`, the docs/status hints, the footer — is overridable per key
without a persona or `--system-prompt`, via the prompt overlay
(`$TERVA_HOME/locales/prompts/en.json`, works in English). See
[localization](localization.md#customizing-tervas-prompts).

## In-session

- **`/lore`** — lists the run's active lore (name · trigger · source): what's
  *loaded and eligible*. It does **not** report what fired on the last turn — the
  TUI reads lore over ctrlproto and the wire view carries no per-turn firing
  record — so for *did it actually fire*, the `tail` sources in
  `--dump-prompt=json` remain the check.
- **`/context`** — token breakdown of the assembled context and what extensions
  inject.

## Troubleshooting

| symptom | check | likely cause → fix |
|---|---|---|
| A lore entry never fires | `--dump-prompt=json` tail sources (`/lore` only confirms the entry is *loaded*, not that it fired) | keyword not in the scan window → widen `scan_depth`, or the term isn't in the recent messages; whole-word/case mismatch; `--no-lore` set; `.terva/lore` untrusted → `terva trust`. Validate with `terva lore validate`. |
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
