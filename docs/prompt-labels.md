# Prompt source labels and portability classes

Reference for the two vocabularies `--dump-prompt` prints: the **source label**
that names where a segment came from, and the **portability class** that says
whether it could travel to an agent that is not terva.

For the task of reading a dump and working out what went wrong, see
[debugging-prompts.md](debugging-prompts.md). This page defines the terms that
guide uses.

## What the source labels mean

| source | region | meaning |
|---|---|---|
| `identity-intro` | system | who the agent is: the name, and nothing else. Brand-free, so it can travel |
| `vessel` | system | what carries it: terva, the harness, the pine-tar image, the pronunciations. Emitted beside the identity, and withheld when a card or persona brings its own |
| `card:system_prompt` / `card:framing` | system | a card owns the intro: its `system_prompt` (with `{{original}}` → a short brand-free framing), or that framing alone when the card has no `system_prompt` |
| `persona:introduction` | system | a native persona's `agent_introduction` field replacing the branded intro (conventions still kept) |
| `charter` | system | the persona/card descriptive body (description/personality/scenario/examples) |
| `conventions` | system | terva's output invariants, written against the run's **surface** (see [the surface rule](debugging-prompts.md#the-prompt-is-written-for-the-surface-it-lands-on)) plus the edit/write discipline when those tools exist. Always last, so nothing erodes them |
| `lore:constant` / `card:character_book` | system | always-on lore folded into the cached prefix |
| `skills`, `context-files`, `agents-md` | system | skill manifest, `--context-file`/config context, repo AGENTS.md |
| `ticket` | system | prefer the `ticket_*` tools over the `git ticket` command line. Emitted only where those tools registered, and placed after `agents-md` so it outranks a repository's older command-line instructions. Absent under `--no-ticket` |
| `restricted-workspace` | system | note that project content was withheld (untrusted cwd) |
| `card:greeting` | messages | the seeded `first_mes` (or `--greeting N`) |
| `lore:triggered [files]` | tail | keyword-triggered lore that fired this turn, labeled by source file |
| `card:post_history` | tail | a card's `post_history_instructions` |
| `extension-context` | system/tail | an extension's static/`register_context` block |

## What the portability class means

`system` and `tail` are the regions terva *assembles* from labeled sources.
Every segment in them carries a **portability class** as well, printed beside
the source:

```
---- [identity-intro · portable] ----
---- [vessel · harness-local] ----
---- [agents-md · discovery-owned] ----
```

It answers one question: **would this segment reach an agent that is not terva?**

| class | meaning |
|---|---|
| `portable` | travels verbatim. Authored by you or by a persona, and about identity or intent rather than terva's machinery |
| `harness-local` | never crosses. It describes terva's tools, terva's surfaces, or terva's policy; abroad it is false at best, and at worst it invites an agent to call a tool it does not have |
| `discovery-owned` | content a foreign agent finds for itself (AGENTS.md, skills, context files). A renderer passes the *path*, not the payload, because pasting it would duplicate or contradict that agent's own discovery |
| `no-analog` | no foreign delivery mechanism exists. terva injects lore, card books, and extension context into a per-turn tail region that other agents simply do not expose |

The class is **derived from the source at render time**, by the same function the
composer consults. It is never stored on a segment, so the dump cannot disagree
with what would actually be sent. An unrecognised source classifies `harness-local`:
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
composed from a labeled source, and a briefing carries the task on purpose.
Classifying it would print an answer to a question nobody asked.

The generated segments themselves (`identity-intro`, `conventions`, the
docs/status hints, the footer) have their wording overridable per key without a
persona or `--system-prompt`, via the prompt overlay
(`$TERVA_HOME/locales/prompts/en.json`, works in English). See
[localization](localization.md#customizing-tervas-prompts).
