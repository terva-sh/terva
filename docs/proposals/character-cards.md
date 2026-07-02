# Proposal — CCv2 character cards for chat/play, on a native lore primitive

- **Status:** Implemented on `feat/character-cards` (PR #6) — Stage 1
  (`31dad87`..`c98901c`), Stage 2 (`049ca38`, `661a345`, `12cd13a`, `4899a60`),
  and the Stage 3 tooling (`a487ec5`/`46f40c4` /lore, `612518e` --dump-prompt).
  Grounded in a read of SillyTavern's actual source (`release`, HEAD `51ad27f`)
  and the shipped terva persona platform (v0.112.0).
- **Scope:** a new **lore** primitive (`packages/agent` — entry format, loader,
  scan/select/budget engine, a per-turn injection seam, and a `--no-lore`
  toggle) plus **CCv2 card
  import** for `--chat`/`--play` (a `--card` flag, a JSON+PNG parser, macro
  substitution, immersive-identity assembly, a seeded greeting, and a
  `character_book`→lore adapter). No protocol changes.
- **Origin:** revisiting SillyTavern Character Card V2 support for the roleplay
  meta-modes. The earlier review (`docs/proposals/persona-format.md`) rejected
  CCv2 as the *native persona format* and parked "untrusted CCv2 import → bounded
  `summary` only" as phase 2. This proposal is that phase — narrowed to chat/play
  (where the mode is the containment boundary) and widened by a reframing of the
  lorebook as a general-purpose primitive.
- **Name:** *lore* — the store of authored, *evolving* reference knowledge, picked
  for the sense that the knowledge accretes and is revised over time (matching the
  curation loop), where *almanac* would have implied settled, accepted facts.

## TL;DR

Support SillyTavern **Character Card V2** cards as the identity for `--chat` and
`--play` — **not** regular mode, because a card is roleplay staging that can't be
losslessly lifted into a terva working charter, and regular mode has a real tool
surface. In chat/play there is no filesystem/shell/skills surface (`--chat` also
blocks the ext/MCP merge), so **the mode is the sandbox** and a card can safely
*be* the immersive identity there.

The card's one dynamic feature — the `character_book` lorebook — is not built as a
SillyTavern-compatibility wart. It is one *serialization* of a general
**lore**: authored, file/dir-backed context that is injected when it's relevant,
within a budget. We build lore as a first-class terva primitive (useful far
beyond roleplay — e.g. `.terva/lore/` for project knowledge) and make
`character_book` a thin adapter onto it.

## Two features, one foundation

1. **The lore** — a native keyed-context primitive. The foundation.
2. **CCv2 card import** — a consumer of the lore, plus identity/greeting
   assembly, gated to chat/play.

Cards motivate the work; the lore is the durable core. `post_history_instructions`
and the lorebook both reduce to the lore's per-turn injection seam, so building
the seam once serves both.

## Why chat/play only (for cards)

`--chat`/`--play` already strip the coding frame: all built-in workspace tools are
dropped (`build.go:1062`), skills and AGENTS.md are skipped, and `--chat`
additionally blocks the extension/MCP tool merge (`build.go:210`). The identity is
already "you are a presence in a conversation / a world" (`chatIdentityIntro` /
`playIdentityIntro`, `systemprompt.go:168-172`). A CCv2 card is a *specification of
exactly that character*, and there is no code-execution surface for its prose to
weaponize. That inverts the old "untrusted card → summary only, never touch
identity" rule **specifically here**: the containment is structural, so the card
can be the identity. In regular mode neither condition holds (real tools; no
lossless charter mapping), so `--card` is a hard error there.

---

# Part A — The lore primitive

## What it is

**Lore** is a collection of authored **entries**. Each entry is
`{ content, triggers, placement, budget-cost }`. At prompt-assembly time the lore
scans a window of recent conversation, selects the entries whose triggers fire
(plus any always-on entries), orders them, includes them until a token budget is
exhausted, and injects them at a configured position. This is the generic form of
SillyTavern's World Info; the CCv2 `character_book` is one way to serialize it.

It fills a real gap in terva's context toolkit:

| primitive | when it enters context | authored by |
|---|---|---|
| `--context-file` / `context_files` | **always** (static prefix) | human |
| skill | when **name-invoked** | human |
| memory (`memory-evolution-and-dreaming`) | when **semantically** relevant | the agent |
| **lore** | when **keyword**-relevant | human |

The coding use is concrete: a repo ships `.terva/lore/` with entries keyed on
subsystem / API / error names, so when the conversation turns to "the auth flow,"
the auth-flow note surfaces **without a tool call** and **without permanently
eating context**.

## Entry format

One entry **per file** — Markdown + YAML frontmatter, mirroring skills/personas.
One-entry-per-file is deliberate: it gives clean per-entry diffs, which the
curation loop (below) depends on.

```markdown
---
name: Auth flow                 # human label (CCv2 name/comment)
keys: [auth, login, session]    # trigger keywords (CCv2 keys)
secondary_keys: []              # CCv2 secondary_keys (used when set)
logic: and_any                  # and_any | not_all | not_any | and_all (CCv2 selectiveLogic)
constant: false                 # always-on regardless of keys (CCv2 constant)
order: 100                      # priority + placement (CCv2 insertion_order)
position: after                 # before | after char definitions (CCv2 before_char/after_char)
case_sensitive: false           # CCv2 case_sensitive
recursion: normal               # normal | prevent | exclude (CCv2 prevent_/exclude_recursion)
enabled: true
---
The auth flow issues a short-lived JWT on login and refreshes it via the
session cookie. Token validation lives in packages/auth/verify.go.
```

Lore-level settings (`scan_depth`, `token_budget`, `recursive_scanning`) live in a
`lore.json` at the directory root (or terva config), mapping the CCv2 book-level
fields. A card's `character_book` deserializes straight into this shape in memory.

## Tiering & resolution

Same tier model as skills/personas (trust-by-provenance):

```
$TERVA_HOME/lore/**        personal, cross-project        (trusted)
.terva/lore/**             project-local                  (trust-gated, like project exts)
<ext-bundle>/lore/**       shipped by an extension        (static scan, like ext skills/personas)
card-imported               character_book, this session   (ephemeral, in-memory, never written to disk)
```

Project (`.terva/lore/`) is gated on Workspace Trust exactly as project
extensions are — untrusted repo content must not inject prompt context. The
card-imported lore is ephemeral and lives only for the chat/play session.

## The engine (grounded in SillyTavern)

Modeled on ST's `checkWorldInfo` (`world-info.js:4597`) but reduced to the CCv2
subset. The grounded finding: ST's `world-info.js` is 6,289 lines, but the
irreducible CCv2 engine is **≈250–300 lines**, with a model token-counter (terva
already has one) as the only hard dependency. The other ~60% of even the core scan
function is ST-only (timed effects, inclusion groups, scoring, probability,
filters, outlets, min-activations, vectorization) — all out of scope.

The load-bearing pieces:

1. **Scan buffer** — the last *N* messages (`scan_depth`), most-recent-first, joined
   with a `\x01` boundary marker so whole-word matches can't bleed across messages
   (ST `WorldInfoBuffer.get`, `world-info.js:279`). Includes the user's just-sent
   turn.
2. **Match** — `matchKeys` (`world-info.js:337`): plain substring by default;
   `case_sensitive` lowercases both sides; optional whole-word via `\W` boundaries.
   (Regex keys are an ST extension — skip for v1.)
3. **Activation** — per entry: `constant` → always; else at least one primary key
   must match; if `secondary_keys` present, apply `logic` (and_any / not_all /
   not_any / and_all).
4. **Budget** — sort by **descending `order`**, accumulate token cost, and drop the
   overflowing entry and everything lower-priority (ST `world-info.js:4900`). Log
   what was dropped (no silent truncation).
5. **Recursion** — if enabled, matched entries' content is re-scanned to trigger
   more, bounded by an already-activated set (`recursion: prevent` excludes an
   entry's content from re-scan; `exclude` bars an entry from being triggered *by*
   recursion).
6. **Placement** — two buckets only: `before` / `after` character definitions.

Reconciliation note: ST's budget is a **percentage of context**; CCv2
`token_budget` is **absolute**. We use absolute (the card's own number) and treat a
lore-level percentage as an optional override.

## The injection seam & cache contract

The lore splits cleanly along terva's cache boundary — this is the crux and it's
what makes `post_history_instructions` fall out for free:

- **`constant` / always-on entries → cached prefix.** They don't change per turn, so
  they join the static system context (alongside description/scenario/etc.), set
  once per session, cache-stable.
- **Keyword-triggered entries → uncached per-turn tail.** The active set changes as
  the conversation moves, so they *cannot* live in the prefix. They are assembled
  each turn and injected via the per-turn context-card seam (the core analog of an
  extension's `register_context` — a block that sits after history, uncached).

This is the same seam CCv2 `post_history_instructions` needs (Part B), and the same
place a future semantic-memory provider would plug in. Rule: **static → cached
prefix; triggered → uncached tail.**

## CLI

- `terva lore list` — entries across the active tiers (name · keys · constant · order).
- `terva lore validate <dir|file>` — frontmatter present/typed; no macro leftovers
  where inappropriate; budget sane.
- (follow-on) `terva lore add` / the curation loop below.

## Enabling & disabling (`--no-lore`)

Lore is **on by default in every mode** — it is a general context primitive, not a
roleplay feature, so a project's `.terva/lore/` is meant to work in ordinary coding
sessions, not just chat/play. `--no-lore` is the per-run kill switch, a sibling of
`--no-skill` / `--no-ext` / `--no-mcp`: it disables discovery across **every** tier
and suppresses **all** injection — no `constant` entries in the prefix, no triggered
entries in the tail, and a loaded card's `character_book` is ignored (logged, not
injected). The prompt is then assembled exactly as if no lore existed — no empty
markers, no cost. The `terva lore` management CLI is unaffected (it edits files; it
does not inject). A `lore` config key (default `true`) sets the per-user default, so
flag + config mirror the existing `--no-skill` / `WithSkills` pair. Lore is
orthogonal to the tool flags: `--no-tools` and `--no-lore` are independent (a
tool-less bot can still benefit from lore context).

---

# Part B — CCv2 card import

## The flag

`--card <path>` loads a CCv2 card as the immersive identity. It is **gated to
chat/play** (hard error otherwise) and is a **separate flag from `--persona`** —
the two trust models stay legible (native charter = trusted; card = chat/play-gated
roleplay content), and macro substitution lives only on the card path (native
personas still *reject* `{{char}}`/`{{user}}`, `personacmd.go:19`). If neither
`--chat` nor `--play` is given, `--card` implies `--chat`.

Internally the card is parsed into an **immersive `Persona`** (`persona.go:33`,
`Immersive: true`) whose charter is assembled from the card fields, then fed the
exact seam `--persona` already uses (`immersiveCustom` → `build.go:745` →
`BuildSystemPrompt`). Almost everything downstream is reuse. `--card` is **core, not
an extension** — identity is the first thing `BuildSystemPrompt` emits, and an
extension is spawned *after* identity exists, so it can only append to identity,
never *be* it (the same argument that put personas in core).

## Sources

- **JSON** — a `.json` card (V1 or V2). V1 (flat six fields) auto-upgrades to the V2
  `data` shape.
- **PNG** — the common case: JSON base64'd into a `chara`/`Chara` text chunk. Go's
  `image/png` doesn't expose text chunks, so a small hand-rolled chunk walker reads
  the signature and iterates `length·type·data·crc`, decoding `tEXt` / `zTXt` /
  `iTXt` (the Python `png_card.py` in the character-cards workspace is a working
  reference to port). Bounded, safe reads.

## Field mapping

| CCv2 `data` field | terva treatment |
|---|---|
| `name` | identity name (banner, `{{char}}`) |
| `description` + `personality` + `scenario` | the immersive charter body |
| `system_prompt` (+ `{{original}}`) | replaces the chat/play **intro block**; `{{original}}` = a short brand-free immersive framing (deliberately NOT terva's branded intro — branding must never leak onto a character) |
| `first_mes` | seeded opening **assistant** message (`chat[0]`) |
| `mes_example` | example dialogue — static block (Stage 1) → parsed example messages (Stage 2) |
| `alternate_greetings` | greeting choices (Stage 2) |
| `post_history_instructions` (+ `{{original}}`) | per-turn **tail card** (Stage 2) |
| `character_book` | imported into an ephemeral **lore** (Part A) |
| `creator_notes` | **never** sent to the model; shown by `terva card info` |
| `tags`, `creator`, `character_version` | metadata / provenance display |
| `extensions` (incl. `terva.sh/harness`) | **ignored for capability** — a card is data, never code |

## Macros

`{{char}}` → card name, `{{user}}` → a configurable user name (--as flag >
trusted-project/global `user_name` > "User"), `{{original}}` → the brand-free
default it replaces (the immersive framing for system_prompt; empty for PHI —
terva ships no default PHI; system_prompt/PHI only).
Case-insensitive, plus legacy `<BOT>`/`<USER>`/`<CHAR>` (ST `macros.js:610`,
`baseChatReplace` `script.js:3282`). Substituted across
description/personality/scenario/first_mes/mes_example, with card-field recursion
disabled.

## Identity assembly

The grounded finding dissolves the fork we thought we had. In ST, `system_prompt`
overrides **only** the `main` (lead-instruction) slot (`openai.js:1487`) —
description/personality/scenario/examples are *separate slots that always render*.
So even full CCv2 fidelity never lets `system_prompt` own the whole prompt. terva's
analog of `main` is the identity intro, so the faithful mapping is:

```
[intro  OR  card system_prompt with {{original}}=intro]   ← lead block
+ card charter (description / personality / scenario)
+ example dialogue
+ terva output conventions                                 ← always last (can't be eroded)
```

This is exactly the "terva brackets the card" reading — which turns out to be both
the safe choice *and* the ST-faithful one. Conventions stay last on purpose
(`systemprompt.go` additive-persona order), so a card can't erode terva's harness
invariants via recency.

## Greeting ("agent speaks first")

`first_mes` is not display-only. In ST it is a real `assistant` message stored at
`chat[0]` (`script.js:7628-7683`), so every later turn sees it as prior context.
terva does the same: seed the card's greeting as the opening assistant message
(macros substituted); `alternate_greetings` become the choices (Stage 2).

Provider caveat: a leading assistant turn is legal for Anthropic — ST only injects
a placeholder user message when the array is otherwise empty
(`prompt-converters.js:222-229`). We verify terva's provider layer tolerates a
leading assistant turn; if not, we prepend a minimal placeholder user turn, exactly
as ST does. This is the one place the feature touches the session/message layer
rather than reusing the persona seam.

## Post-history instructions → a per-turn tail card

Grounded: PHI is ST's `jailbreak` slot, positioned **after** the entire chat
history and rebuilt every generation (`openai.js:1232` / `1246`); for Claude the
converter rewrites the non-leading `system` message to `user`, so PHI arrives as a
**trailing user message appended after the conversation**
(`prompt-converters.js:253-268`). That is precisely terva's per-turn context-card
seam. So PHI is not special machinery — it's a tail card carrying the card's PHI
text (with `{{original}}` = terva's default, empty unless set). Same seam as the
lore's triggered entries; same cache treatment (uncached tail).

## Security — a card is data, never code

- The `extensions` object (including any `terva.sh/harness` block) is **never**
  interpreted as capabilities — it cannot grant tools, spawn MCP, run hooks, or
  raise authority. At most it feeds display hints (emoji/accent). This is the load-
  bearing line.
- `creator_notes` never reaches the model.
- PNG parsing is bounded/defensive (untrusted input).
- The card shapes identity *only* inside chat/play, where the tool surface is
  already clamped. Loading its prose is no riskier than `--system-prompt`.

---

## Trust & authority

- **Native lore** — trust by provenance, like personas: `$TERVA_HOME/lore/`, an
  explicit `--card`, and shipped-extension lore are trusted; `.terva/lore/` is
  gated on Workspace Trust.
- **CCv2 card** — chat/play-gated roleplay content. Its prose is the identity in
  those modes; its `extensions` metadata is inert for capability. It never runs in
  regular mode.

## Staging

**Stage 1 — lore + play the card. ✅ Implemented (`31dad87`..`c98901c`), full `just ci` green.**
- Lore primitive: entry format, tiered loader, the ≈250–300-line engine, the
  injection seam (constant→prefix, triggered→tail), the `--no-lore` kill switch,
  and `terva lore list/validate`.
- Card import: `--card` flag (chat/play-gated), JSON+PNG parser, V1 upgrade, macro
  substitution, immersive-identity assembly (description/personality/scenario +
  `mes_example` as static text), `first_mes` seeded as `chat[0]`.
- `character_book` → ephemeral lore adapter (cards get lorebooks via the lore).
- `terva card info` (metadata, never-to-model fields surfaced).

**Stage 2 — full CCv2 fidelity. ✅ Implemented (`049ca38`, `661a345`, `12cd13a`, `4899a60`).**
- `system_prompt` override (+`{{original}}`, terva brackets).
- `alternate_greetings` selection (`--greeting N` / picker).
- `mes_example` parsed into named example messages, budget-pruned (with a
  pin-examples equivalent).
- `post_history_instructions` → per-turn tail card.

**Stage 3 — tooling & observability. ✅ Largely implemented (`a487ec5`/`46f40c4` /lore + fired/dropped indicators; `612518e`/`0a3d4f7` --dump-prompt, dump-and-exit). The browse/toggle panel remains future.**
- **Lore TUI surface** — lore is silent in-session today (triggered entries fire
  invisibly). Add a `/lore` slash command (active entries: name · trigger ·
  source — the in-session `terva lore list`), a "lore fired this turn" indicator
  (surface which triggered entries matched, closing the observability gap), and
  eventually a browse/toggle panel like the extensions panel. terva already has a
  slash-command registry (`modes/slash_registry.go`) and panel machinery.
- **Prompt-dump debug flag** — dump the fully-assembled prompt before a turn so
  the construction is inspectable: the cached system prefix (identity + charter +
  constant lore + skills/context), the messages, and the per-turn ephemeral tail
  (triggered lore + PHI + extension cards). Open design: a provider-agnostic
  readable dump at the core `Request` level vs the actual wire JSON at the
  provider (`buildRequest`) level; and dump-and-continue vs dump-and-exit
  (`--dump-prompt` / `--dump-prompt-only`), to stderr or a file. Especially
  useful for debugging lore injection + card identity assembly.

**Follow-ons.**
- **End-of-session lore curation** (below).
- A semantic-memory provider on the same injection seam.
- CCv3 / CHARX, APNG.

## End-of-session lore curation

The lore is authored *files*, which makes agent-assisted maintenance clean and
safe. At the end of a long session the user asks the agent whether anything should
be added / updated / removed in the lore; the agent proposes **diffs** (one entry
per file → surgical), the human approves. This keeps the lore human-owned and
trusted (the agent proposes, never silently mutates) — deliberately distinct from
the automatic memory/dreaming thread, which is the agent writing its *own* decaying
store. Designed-for from day one via the one-entry-per-file format; implemented as a
follow-on skill/command, not Stage-1 scope.

## Open questions

1. **Name — settled:** `lore` (over `almanac`, for the evolving-knowledge
   connotation).
2. **`{{user}}` — resolved:** `--as NAME` > a trusted project's / the global
   `user_name` config (`resolveCardUserName`) > the literal "User"; the
   interactive path asks once and remembers.
3. **Budget units.** Absolute `token_budget` (card's own) as primary, lore-level
   percentage as override — confirm.
4. **Greeting continuity — resolved: placeholder-user-turn** — a request-scoped
   guard (`EnsureLeadingUserTurn`, a neutral `[Begin.]` stage cue) applied in
   the anthropic, bedrock, gemini, and OpenAI-family/Codex `buildRequest`s,
   unit-tested per wire.

## Testing sketch

- Lore: scan-depth windowing; constant vs keyed vs selective (all four `logic`
  values); budget overflow drops lowest-priority + logs; recursion terminates;
  before/after placement; trust-gating of `.terva/lore/`; `--no-lore` suppresses
  all injection (prefix + tail + card `character_book`), prompt byte-identical to
  no-lore-present.
- Card parse: V1→V2 upgrade; PNG `tEXt`/`zTXt`/`iTXt` extraction; macro
  substitution (case-insensitive, legacy forms); `extensions` never grants
  capability; `creator_notes` never in prompt.
- Identity: `--card` errors in regular mode; chat/play assembles intro + charter +
  conventions with conventions last; `first_mes` seeded as `chat[0]`.
- character_book → lore round-trips (keys, secondary_keys gated on
  `selective`, constant, insertion_order, position). Per-entry recursion
  controls live in CCv2 `entry.extensions`, which terva never interprets
  (card-is-data) — intentionally out of card-import scope; native lore
  entries keep `recursion: normal|prevent|exclude`.
- Stage 2: `system_prompt` replaces only the intro block; PHI emitted as a trailing
  tail card; examples pruned under budget.
