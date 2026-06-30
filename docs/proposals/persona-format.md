# Proposal — first-class persona format (Markdown + frontmatter charter)

- **Status:** Proposed (2026-06-29, rev. 2) — converged direction with the four
  open questions resolved (see Resolved decisions); not yet implemented. v1 is
  deliberately small and trusted-only.
- **Date:** 2026-06-29
- **Scope:** `packages/agent/config.go` (persona resolution + a `Persona`
  loader), `packages/agent/systemprompt.go` (`personaIdentity` /
  `SystemPromptOpts`), `packages/agent/args.go` (a `--persona` flag), a
  `terva persona` CLI subcommand, an **embedded** persona crew (opt-in
  materialize to `$TERVA_HOME/personas/` via `terva persona init`), and a
  `persona-author` skill. No protocol changes.
- **Origin:** Review of an external proposal to drive terva's "personality"
  from SillyTavern Character Card V2 files
  (`/Users/drewshort/Workspace/testing/character_cards/docs/proposal-terva-character-cards.md`).
  CCv2-as-format was rejected after review; this is the terva-native
  distillation, plus a decision to adopt the proposal's specialist crew as
  shipped personas.

## TL;DR

terva already has a persona primitive — `PersonaName()` (`config.go:329`,
default `"Mieli"`) — but it only swaps a **name** into the identity line. Make
persona a structured, trusted, **core-loaded** artifact: a Markdown file whose
**frontmatter** carries identity/display/selection metadata and whose **body is
a behavioral charter** layered additively on top of terva's invariant harness
identity. Resolve it exactly like `SYSTEM.md`:

```
active persona =
  --persona <name|file>       ← explicit (user-provided ⇒ trusted), wins
  $TERVA_HOME/persona.md       ← hand-authored inline root persona (mirrors SYSTEM.md)
  config: default_persona      ← name pointer into personas/**; empty ⇒ Mieli
  embedded Mieli               ← built-in primary default
```

Ship terva's own agent (Mieli) defined in this format as the canonical example,
plus a crew of specialist personas (security, test, architecture, reliability,
…) **embedded** in the binary and materialized on demand. The charter sits in
the **cached prefix**, set once per session — no per-turn churn, no cache thrash. Downloaded/untrusted
cards are explicitly out of scope for v1 and, when added, never shape identity.

## Why core, not an extension

An extension is the wrong layer for a persona, and not by preference — by
construction. Extensions are discovered and spawned **after** the agent already
exists (`docs/extensions.md`, discovery → spawn → handshake → `ready`). But a
persona is the agent's **base identity** — the first thing `BuildSystemPrompt`
emits (`systemprompt.go:57`), ahead of everything else. An extension can only
contribute to the *cached system addendum* (a `register_context` static block)
or the per-turn tail; it can **append to** identity but never **be** identity.

So "define terva's own agent in the persona format as the reference example" is
*impossible* as an extension — it is circular: the extension would need the
agent's identity already resolved in order to supply the agent's identity. The
default persona has to be a core-loaded artifact for the same reason
`defaultIdentityIntro` and `SYSTEM.md` are. This also avoids inventing
persona-specific protocol frames to reach a layer the protocol structurally
can't touch.

## Why not CCv2 directly

CCv2 is a roleplay-chat format. For terva it is mostly ballast: `first_mes`,
`alternate_greetings`, and `mes_example` (with `<START>` separators and
`{{char}}`/`{{user}}` macros) are theatrical staging you actively do **not**
want leaking into a coding agent's context — token-expensive and tonally wrong.
Reviewing the proposal's own cards, the part that actually encodes a
specialty is the CCv2 `system_prompt`: a tight behavioral charter. Everything
terva needs is that charter plus a handful of structured fields; the CCv2
envelope around it is the bulk of the bytes (≈85% for the worked Vartija port —
see The crew) and none of the value. CCv2 import can remain a future convenience
(see Authority & trust), but it is not the format.

## The format

A persona is a single Markdown file: **YAML frontmatter** for structured
metadata, **body** for the behavioral charter.

```markdown
---
name: Vartija
pronunciation: VAR-tee-yah        # optional; shown in the identity line
specialty: security review        # short human/selection label
summary: Evidence-first application-security engineer for source-code review.
emoji: 🛡️                          # optional, display only
accent_color: "#f7768e"           # optional, display only
recommended_skills: []            # names only; surfaced, never embedded
good_for: [secure-code-review, threat-modeling, vulnerability-triage]
avoid_for: [pure-style-review]
---

Review source code as a security engineer. Inspect before judging.
Prioritize exploitable issues over checklist noise. For each finding give
evidence, the affected path, attacker capability, impact, severity rationale,
and a concrete remediation. Label uncertainty; separate confirmed
vulnerabilities from hypotheses and hardening notes. Never call a vulnerability
confirmed unless reachable context supports it.
```

The default persona, expressed in the same format — the reference people copy:

```markdown
---
name: Mieli
pronunciation: MYEH-lee
specialty: coding collaborator / coordinator
summary: Concise, practical collaborator who turns fuzzy goals into tested, documented, versioned work.
emoji: 🧠
accent_color: "#7aa2f7"
---

Work as a calm, practical coding collaborator. Inspect the current state before
changing it. Turn fuzzy goals into the smallest testable step, prefer reversible
moves, and leave durable notes, tests, and clean commits behind. Explain
outcomes in plain language.
```

**Field notes.**

- `name`, `pronunciation` — feed the identity line (today's job of
  `PersonaName()`/`personaPhonetic()`).
- **body / charter** — the behavioral specialization. Keep it *lean*: terva
  deliberately omits generic operating guidelines because frontier models
  internalize them (`systemprompt.go:48-52`). A charter is voice + specialty,
  **not** a place to re-add "don't run sudo" / "prefer edit over write." The
  default Mieli charter is intentionally short because it *is* the baseline;
  specialists carry more because they specialize.
- `summary` — one line, model-facing-safe; the only field an *untrusted* card
  would ever be allowed to surface (see Authority & trust).
- `emoji`, `accent_color` — display only (status line, `/persona` UI later).
- `recommended_skills` — skill **names** only, surfaced in `persona list` /
  UI; never embed skill bodies (skills stay "how," persona stays "who").
- `good_for` / `avoid_for` — **optional and inert in v1.** They document intent
  and are the seam phase-2 selection hangs off (which specialist handles which
  slice). Nothing enforces them; in particular `avoid_for` is not a guardrail.

## Resolution & precedence

`--persona` accepts a **bare name or a path**: a value with no `/` and no `.md`
resolves against `$TERVA_HOME/personas/**` (first frontmatter `name`/basename
match wins); anything else is a file path. So `terva --persona vartija` and
`terva --persona ./team/vartija.md` both work — same ergonomics as referencing
a model or skill by name.

The active-persona chain mirrors the existing identity chain
(`--system-prompt` → `$TERVA_HOME/SYSTEM.md` → built-in `defaultIdentity`,
`systemprompt.go:54-56`), with one addition — a config pointer so a user can
promote a library persona to their default without copying a file:

1. `--persona <name|file>` — explicit; user-provided, therefore trusted.
2. `$TERVA_HOME/persona.md` — a hand-authored inline root persona (mirrors
   `SYSTEM.md`).
3. `default_persona` in `config.json` — a name pointer selecting one persona from
   `personas/**`. Empty/unset ⇒ implicit Mieli. (A name, not content — so it does
   not reintroduce the JSON-prose bloat we rejected; it is the richer sibling of
   the existing `persona_name`.)
4. embedded **Mieli** — the built-in primary default.

When **both** `persona.md` and `default_persona` are set, the hand-authored file
wins (it is the more specific signal) and terva warns that both are present.
`.terva/persona.md` (project scope) is **reserved/deferred**; when added it slots
above `$TERVA_HOME/persona.md`.

**Relationship to `--system-prompt` / `SYSTEM.md`.** These remain the *raw
replace* escape hatch (`SystemPromptOpts.Custom`, which already "replaces the
default identity entirely" and ignores `PersonaName` — `systemprompt.go:24,34`).
`--persona` is the *structured default-identity* path. They are alternatives: if
both are present, the raw `Custom` replace wins and the persona is ignored (with
a warning), consistent with today's "Custom ignores PersonaName."

## How it lands in the prompt — additive, cached, set once

A persona does **not** replace terva's harness identity or output conventions.
The invariant terva framing (`defaultIdentityIntro` / `customIdentityIntro` +
`identityConventions`, `systemprompt.go:110-119`) stays in core, parameterized
by the persona's name/pronunciation. The **charter is layered additively**
inside the resolved identity block:

```
identity intro (terva framing, name + pronunciation)
+ persona charter            ← new: additive specialization
+ identity conventions (terva output/edit invariants)
```

Conventions come **last on purpose**: they are terva-owned harness invariants, so
keeping them as the final framing means a persona charter can't erode them — even
via recency. The invariants bracket the specialization.

This is the key correction to the source proposal, whose precedence list ranked
card hints *above* the user's live request. Here the charter is a **default the
user overrides**, not an authority over the user: it sits in the system prefix
below the hard-authority layers (system/dev, project config, permissions, tool
safety) and the user's live turn outranks it.

Because the charter lives in the cached prefix, it follows terva's cache
contract: **decide once per session; the prefix is a snapshot you replace at a
boundary, not a per-turn signal** (`docs/extensions.md` cache rules). v1 resolves
the persona at startup; it never mutates mid-turn. Runtime `/persona load`
(a deliberate, rare, cache-busting swap) is deferred to phase 2.

## Storage, teams & discovery

```
$TERVA_HOME/personas/
  mieli.md                 🧠 coordinator (default, materialized here to fork)
  review-crew/
    README.md              ← optional narrative: what this crew is for, who to pick
    arkkitehti.md          🏗️ architecture
    koestaja.md            🧪 test / QA
    vartija.md             🛡️ security
    luotsi.md              🧭 reliability / release
    luotain.md             🔍 research / investigation
    kirjuri.md             📝 docs
    huoltaja.md            🔧 maintenance
```

Teams are just subdirectories; a file directly under `personas/` is "ungrouped."
The README is optional human/LLM prose. **The authoritative roster is derived
from frontmatter, not the README** — there is no separate manifest to drift.
`terva persona list` walks `personas/**`, reads each frontmatter, and prints
`name · specialty · summary · good_for`.

The shipped crew is **embedded in the binary** and always available; terva never
auto-writes it (that would drift against the binary on upgrade). `terva persona
init` materializes the embedded set into `personas/` for editing, and an on-disk
persona then **shadows the embedded one of the same name** — a user's fork wins
without anyone inheriting a stale copy. `persona list` shows the merged view
(embedded ∪ on-disk), marking which entries are user-edited.

Discovery is **lazy, like skills**: only the *active* persona shapes the prompt.
The roster is not auto-injected into context (that would cost tokens and fight
the cache for no v1 benefit); it surfaces on demand via `persona list` / a future
`/personas`, and in phase 2 it is what a coordinator reads before dispatching.

Finnish naming is kept for brand cohesion (Mieli/terva). Discoverability rides on
`specialty` + `emoji` + `summary`, so nobody needs to know that *vartija* means
"sentinel" to find the security reviewer.

## Authority & trust (provenance, not a self-declared field)

Trust is decided by **where the file came from**, never by a field inside it (a
file cannot be trusted to declare its own trust level):

- **Trusted** — `--persona`, `$TERVA_HOME/persona.md`, the shipped crew, and a
  future `.terva/persona.md`. The charter shapes identity in the cached prefix.
- **Untrusted** — a future imported CCv2 card or a persona loaded at runtime from
  outside the repo. It may surface **only** the bounded `summary`, rendered
  through terva's existing escaped, size-bounded `<extension-context>` envelope.
  Its charter is **never** applied to identity.

This is why the source proposal's policy of *excluding* `system_prompt` is right
for untrusted input but exactly inverted for trusted personas, where the charter
is the whole point. v1 implements only the trusted path; the untrusted import
path is phase 2 and reuses the existing context-card bounding/escaping rather
than inventing anything.

## CLI & authoring

Make authoring trivial:

- `terva persona list` — roster from frontmatter across `personas/**`.
- `terva persona validate <file>` — required frontmatter present; charter body
  present and within the prefix budget; no leftover `{{char}}`/`{{user}}`/
  `<START>` macros; `accent_color` well-formed. Lives in the binary since persona
  is now a core concept.
- `terva persona init` — **opt-in**: materialize the embedded default + crew into
  `$TERVA_HOME/personas/` for editing. Embedded stays the source of truth; an
  on-disk persona shadows the embedded one of the same name. Deliberately *not*
  auto-run on install, to avoid stale on-disk copies after an upgrade.
- a **`persona-author` skill** — the MD+frontmatter structure, the
  trusted-vs-untrusted distinction, "charter not flavor," and a pointer to
  `persona validate`. (Replaces the external `sillytavern-card-author` skill.)

## The crew (the port)

Adopt the proposal's eight personas as **embedded** personas. Conversion from
each CCv2 card is mechanical:

| persona | specialty | charter ← | frontmatter ← |
|---|---|---|---|
| mieli | coordinator / default | `system_prompt` | `persona.*`, `display.*` |
| arkkitehti | architecture review | `system_prompt` | + `good_for`/`avoid_for` |
| koestaja | test / QA strategy | `system_prompt` | + `recommended_skills` |
| vartija | security review | `system_prompt` | |
| luotsi | reliability / release | `system_prompt` | |
| luotain | research / investigation | `system_prompt` | |
| kirjuri | docs | `system_prompt` | |
| huoltaja | maintenance | `system_prompt` | |

Rule: **body ← CCv2 `system_prompt`** (the charter, cleaned of `{{char}}`/
`{{user}}`); **frontmatter ← `persona.*` + `display.*` + `swarm.good_for/avoid_for`
+ `skills.recommended`**; the roleplay fields (`first_mes`,
`alternate_greetings`, `mes_example`) are dropped. `post_history_instructions`
is folded into the charter for v1 (see phase 2 for the per-turn split).

A worked example of this conversion — the actual Vartija port — lives at
`packages/agent/personas/builtin/review-crew/vartija.md` (the embedded-crew home,
mirroring `packages/agent/skills/builtin/`). It drops the CCv2 `tools`, `memory`,
`tags`, and `kind` blocks per this spec; the only authored addition is a
pronunciation, which CCv2 did not carry. The charter is the CCv2 `system_prompt`
plus `post_history_instructions`, cleaned of `{{char}}`/`{{user}}`. The result is
1.1 KB vs the 7.5 KB source card (~85% smaller) and immediately readable.

The full crew is ported alongside it: `mieli.md` (the default, top-level) plus
seven review specialists under `review-crew/` with a team README. Charters run
262 B (Mieli, the lean baseline) to 720 B (Vartija) — Mieli is shortest by design
because it *is* the baseline.

## Scope: v1 vs phase 2

**v1 (trusted-only):**
- `--persona <name|file>` flag + name/path resolution.
- `$TERVA_HOME/persona.md` root override; embedded Mieli default.
- charter as an additive, cached, set-once block in the resolved identity.
- `default_persona` config pointer (empty ⇒ Mieli).
- embedded crew; opt-in `terva persona init` to fork onto disk.
- `terva persona list/validate/init` + the `persona-author` skill.

**Phase 2:**
- swarm/subagent `--persona` passthrough (`swarm_agent.go` spawn currently takes
  only task/model/tier) so a coordinator dispatches specialists — also the dev
  loop of spawning a subagent to test a persona while authoring it.
- optional per-turn **re-assert** via a context card (the uncached tail). This
  maps cleanly onto CCv2 `post_history_instructions`, whose SillyTavern position
  (after chat history) is structurally terva's per-turn card.
- untrusted **CCv2 import** → bounded `summary` only, via the escaped
  `<extension-context>` envelope.
- runtime `/persona load` / `/persona clear` (deliberate cache-busting swap) and
  a `/persona` status surface.

## Implementation sketch

1. **`config.go`** — generalize the name-only path into a `Persona` value and a
   loader:

   ```go
   type Persona struct {
       Name, Pronunciation, Specialty, Summary string
       Emoji, AccentColor                       string
       RecommendedSkills, GoodFor, AvoidFor     []string
       Charter                                  string // the body
       Source                                   string // resolved path, "" for embedded
   }

   // ResolvePersona applies: --persona override → $TERVA_HOME/persona.md →
   // config default_persona → embedded Mieli. Bare names and default_persona
   // resolve against on-disk personas/** first, then the embedded crew.
   func ResolvePersona(override string) (Persona, error)
   ```

   The existing `PersonaName()`/`personaPhonetic()` become thin accessors over
   the resolved persona (or are subsumed), preserving `TERVA_PERSONA_NAME` /
   `persona_name` as a name-only override that still layers on top.

2. **`systemprompt.go`** — `SystemPromptOpts` carries the resolved persona;
   `personaIdentity` emits `intro(name,pronunciation) + charter + conventions`.
   `Custom` (`--system-prompt`/`SYSTEM.md`) still short-circuits to a full
   replace.

3. **`args.go`** — a `--persona <name|file>` flag next to `--system-prompt`;
   mutually-exclusive-with-`Custom` warning.

4. **`terva persona` CLI** + an **embedded** crew (`embed.FS`) + opt-in
   `persona init` + the **`persona-author` skill**.

## Testing

- Resolution order: `--persona` beats `persona.md` beats `default_persona` config
  beats embedded Mieli; bare name / `default_persona` resolve against on-disk
  `personas/**` then the embedded crew; path form loads a file; both `persona.md`
  and `default_persona` set → file wins + warning.
- Charter is additive: identity = intro + charter + conventions; terva framing
  and conventions are never dropped.
- `--system-prompt`/`SYSTEM.md` present → full replace wins, persona ignored
  (with warning).
- Cache: persona resolved once at startup; identical re-resolution is a no-op;
  no mid-turn mutation.
- `persona validate`: rejects missing `name`, empty charter, leftover
  `{{char}}`/`{{user}}`/`<START>`, oversized charter, malformed `accent_color`.
- `persona list`: roster derived from frontmatter matches files on disk; a team
  subdir groups correctly; an ungrouped file appears at top level.
- Embedded crew resolves with an empty `$TERVA_HOME`; after `persona init`, an
  edited on-disk persona shadows the embedded one of the same name and `persona
  list` marks it user-edited.
- Back-compat: `TERVA_PERSONA_NAME` / `persona_name` still override the name with
  no persona file present.

## Resolved decisions (2026-06-29)

1. **Crew distribution** — embedded in the binary, never auto-written; opt-in
   `terva persona init` materializes editable copies; an on-disk persona shadows
   the embedded one of the same name. Chosen over installing-like-docs because
   that drifts against the binary on upgrade.
2. **Charter placement** — `intro → charter → conventions`. Conventions stay last
   so terva's harness invariants remain the final framing and a charter cannot
   erode them, even via recency.
3. **Default-persona selection** — a single `$TERVA_HOME/persona.md` file (no
   directory form; that role is `personas/`), plus a `default_persona` name
   pointer in `config.json` (empty ⇒ implicit Mieli). When both are set the file
   wins, with a warning. The pointer is a name, not content, so it does not
   reintroduce JSON-prose bloat.
4. **Composition** — `--persona` composes with `--append-system-prompt`
   (orthogonal; append lands after the persona-shaped identity). A test asserts
   the order.

Nothing material remains open for v1; deferred items are tracked under
Scope → Phase 2. Note (open micro-decision): the file-wins-over-`default_persona`
tiebreak in §3 is reversible if the config pointer should instead win.
