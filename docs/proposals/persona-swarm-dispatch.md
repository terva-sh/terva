# Proposal — persona dispatch (phase 2): coordinators spawn specialist sub-agents

- **Status:** **Phase 2a implemented** (2026-06-30) — the persona passthrough,
  name-only trust, tier-1 roster, and per-persona result labeling shipped on
  `feat/persona-swarm-dispatch` (commit `9520d56`). Phase 2b (the `persona_search`
  tool, persona-named worktrees, persona→tier hint) is not yet implemented.
  Phase 2 of the persona work (`docs/proposals/persona-format.md`, shipped in
  v-merge `ba16277`).
- **Date:** 2026-06-30
- **Scope:** `packages/agent/tools/swarm_spawn.go`, `packages/agent/swarm/`
  (`swarm.go`, `runner.go`), `packages/agent/modes/swarm_slash.go` +
  `interactive_swarm.go`, and the auto-swarm system addendum. No protocol
  changes; no new prompt path.
- **Origin:** The persona-format proposal deferred swarm dispatch to phase 2.
  This specs it, plus a researched answer to the one open design question
  (how a coordinator discovers/selects specialists at scale).

## TL;DR

Let a coordinator agent dispatch a swarm sub-agent that boots **as a specific
persona** (a trusted identity + behavioral charter). The core is a one-field
**argv passthrough**: the spawned `terva` subprocess already builds its system
prompt through the same `Resolve()` → `ResolvePersona()` → `BuildSystemPrompt`
path as every other mode (`swarm_agent.go:42`), so adding `--persona <name>` to
the child's argv makes the persona apply with **no new prompt logic**. Around
that, four small layers: name-only trust for the model-facing tool,
per-persona result labeling, a **discoverability** design (inject a compact
roster now; reserve a search tool for the long tail), and orthogonality to
model/tier.

## Why this is mostly plumbing

A swarm sub-agent is a real `terva` subprocess. Spawn config crosses to it **as
argv flags** (not over the control socket): `runner.go`'s `swarmAgentArgs`
(~`runner.go:104`) emits `--swarm-agent`, `--session`, `--cwd`, `--model`,
`--provider`, and the positional task. The child's daemon entry
`runSwarmAgentMode` (`swarm_agent.go:36`) calls `Resolve(args, true)`, which is
the exact persona path from phase 1:

```go
persona, err := ResolvePersona(args.Persona)        // build.go
sys := BuildSystemPrompt(SystemPromptOpts{ …, PersonaName: persona.Name, Charter: persona.Charter })
```

Today `swarmAgentArgs` never emits `--persona`, so the child falls through to
`persona.md` / `default_persona` / Mieli. **Threading a per-spawn persona name
into the argv is the entire identity hook.** The control-socket inbox
(`swarm/inbox.go`) only carries follow-up prompts + cancel/shutdown — persona is
set once at boot and does not belong there.

### The passthrough chain (5 edits)

1. `swarmSpawnArgs` + `swarmSpawnSchema` — add a `persona` param
   (`tools/swarm_spawn.go:56-85`).
2. `swarm.SpawnRequest` — add `Persona string` (`swarm/swarm.go:221-225`); the
   spawn call site (`swarm_spawn.go:114-118`) passes it through.
3. `Agent` + `SpawnReq` stamping — store `a.Persona` (`swarm.go:~299-316`).
4. `swarmAgentArgsOpts` + `swarmAgentArgs` + `defaultChildArgs` — emit
   `--persona <name|path>` (`runner.go:61-123`).
5. `/swarm new` `parseSpawnFlags` + `spawnAdapter` — add `--persona` for
   CLI/dialog parity (`modes/swarm_slash.go:53,220-250`).

No change to the child's prompt build.

## What dispatch looks like

- **Auto-swarm (model-driven):** with auto-swarm on, you ask the default
  coordinator (Mieli) to "review this PR for security and test gaps." It calls
  `swarm_spawn(persona="vartija", task=…)` and `swarm_spawn(persona="koestaja",
  task=…)`. Each boots as that specialist; the charter shapes the review; they
  run in parallel; results return **labeled by persona** so the coordinator
  synthesizes across lenses.
- **Human:** `/swarm new --persona vartija "audit packages/agent/auth"`.
- **Dev loop:** `/swarm new --persona ./draft.md "…"` exercises an *unsaved*
  persona file while authoring it.

Structural point: the **dispatch capability lives in the auto-swarm addendum**
(`AutoSwarmSystemAddendum`), not in any persona's charter. Personas stay pure
identity; *any* persona + auto-swarm + the roster can coordinate. No
"coordinator role" flag is needed.

## Decision 1 — discoverability / selection (researched)

How does the coordinator know which specialists exist and pick the right one?
We researched the prior art (Anthropic Skills/subagents, MCP "tool overload,"
the tool-retrieval literature: RAG-MCP, Toolshed, the skill-capacity
phase-transition study). The decision-shaping findings:

- **It's Anthropic's own pattern.** Skills inject only name+description at
  startup and lazy-load the body ("progressive disclosure"); subagents inject
  the free-text `description` and let the model match — **no retrieval step**.
- **Confusability, not count, drives breakdown.** Injected-roster selection
  holds **>90% accuracy at ≤20 options**, degrades past ~30, collapses toward
  ~20% at 200 — but at a *fixed* N=20, making descriptors overlap costs 18–30%
  while *distinct* options hold ~95–100%.
- **Cost is the weaker axis and caching blunts it.** Token bloat bites at the
  thousands-of-tools scale; a static cached manifest is cheap. Caching fixes
  cost but **not** the accuracy/distractor axis (they break at different N).
- **Retrieval is the proven fix at large N, not small** (RAG-MCP 13.6%→43%;
  98.7% token cuts) — with a per-dispatch round-trip not worth paying below
  ~30–100.

Our personas are **few, curated, and tagged with structured non-overlapping
`good_for`/`avoid_for`/`specialty`** — the low-confusability case with the
highest effective capacity. So:

**Inject the roster now; shape it as the first tier of progressive disclosure;
add retrieval only at a tripwire.**

1. **Tier 1 (this phase):** inject a compact roster — `name · specialty ·
   good_for` — into the **auto-swarm addendum** (cached prefix), gated on
   auto-swarm **and** non-empty `good_for` (only dispatchable specialists
   appear, which keeps the set small and distinct → preserves low
   confusability). Charters are *not* injected; they lazy-resolve in the child.
2. **Tier 2 (deferred, not built now):** a `persona_search` tool (query by
   tag/specialty → matching names) wrapping the existing `AllPersonas()` data
   layer and `terva persona list`. Shape Tier 1 so adding this is non-breaking.
3. **Tripwire:** when the *dispatchable* roster crosses ~20–30 (or the injected
   block exceeds a token budget), flip on Tier 2; emit a one-line log when the
   threshold is passed so it surfaces.

Caveat: the precise numbers (~30 onset, ~83–92 capacity) come from
preprints/stress-tests — treat as magnitudes. The robust takeaway is
confusability-over-count, which is what governs the design.

## Other decisions (settled)

- **Trust — name-only for the model.** `swarm_spawn`'s `persona` resolves **by
  name** against the trusted library (embedded ∪ `$TERVA_HOME/personas`),
  **never a path** — the model can't point a sub-agent at an arbitrary file.
  `/swarm new --persona` (human-provided ⇒ trusted) may use a path. Keeps the
  phase-1 invariant: untrusted prose can't become identity.
- **Result labeling.** Add `Persona` to `AgentSnapshot` (`swarm.go:562`) so the
  dashboard shows it and `flushSwarmSummary` (`interactive_swarm.go:113`) tags
  each result with its persona — that's what lets the coordinator synthesize
  across lenses meaningfully.
- **Orthogonal to compute.** Persona = identity; model/tier stay the
  coordinator's separate choice (`swarm_spawn` keeps `tier`, *adds* `persona`).
  A persona does not pin a model. (Possible future: a persona *hints* a
  preferred tier — out of scope; it would re-touch the format.)
- **Worktrees compose.** A dispatched specialist that mutates files is isolated
  under `--swarm-worktrees` exactly as today; optionally pass `Persona` into
  `WorktreeReq` (`swarm.go:102`) to name the worktree by persona.

## Scope

**Phase 2a (this proposal):**
- the argv passthrough (5 edits) + `/swarm new --persona`.
- name-only trust on the `swarm_spawn` `persona` param.
- per-persona result labeling (`AgentSnapshot` + summary).
- Tier-1 roster injection into the auto-swarm addendum (gated on auto-swarm +
  non-empty `good_for`) + the tripwire log.

**Phase 2b (deferred to the tripwire):**
- the `persona_search` retrieval tool (Tier 2).
- optional persona-named worktrees.
- optional persona→tier hinting.

## Implementation sketch

1. **Passthrough:** the 5 edits above. `Persona` flows
   `swarm_spawn`/`/swarm new` → `SpawnRequest` → `Agent` → `swarmAgentArgs`
   `--persona` → child `Resolve()`.
2. **Trust gate:** in `swarm_spawn.Execute`, resolve `persona` via a
   name-only lookup (reject `/` or `.md`); error clearly on miss (fail-fast,
   like a bad `--context-file`). Reuse `loadPersonaByName` from `persona.go`.
3. **Roster addendum:** when `AutoSwarmEnabled()`, build a compact roster from
   `AllPersonas()` filtered to non-empty `good_for`, append it to the
   auto-swarm system addendum (cached prefix, set once). Add the tripwire log.
4. **Labeling:** thread `Persona` onto `Agent`/`AgentSnapshot`; include it in
   the `flushSwarmSummary` synthetic turn ("Vartija (security): …").
5. **Tests:** persona crosses to the child argv; name-only rejects a path;
   roster lists only `good_for` personas and is gated on auto-swarm; snapshot
   carries persona; legacy spawns (no persona) unchanged.

## Open questions

1. Roster format for **structured tags** vs free text — once Tier 2 lands, is
   the always-on index a tag-faceted list or an embedding index? (Research
   flagged this as unresolved for structured-tag catalogs.)
2. Exact tripwire threshold + budget — ~20–30 is the evidence-backed band;
   pick a concrete number (and token cap) at implementation.
3. Should the coordinator be able to dispatch the **same** persona N-way
   (e.g., shard a large review across three `vartija` sub-agents on different
   paths)? Falls out naturally; worth a test.
