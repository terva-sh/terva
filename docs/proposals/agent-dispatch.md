# Proposal — agent dispatch: one engine, composable front-ends (coding swarm + immersive cast)

- **Status:** Implemented through Stage 3 on `feat/character-cards` (PR #6):
  Stage 0 skin gate (`b34b278`), Stage 1 boot spec (`0fa464b`), Stage 2 play
  skin + director coordination (`f162412`, `b316ebd`), Stage 3 warm actors
  (`975a0e1`). Stage 4 (Model X) remains future; the hard per-turn
  interaction budget is still deferred (soft pacing guidance shipped).
- **Date:** 2026-07-01
- **Scope:** `packages/agent/swarm/` (engine), `packages/agent/tools/swarm_spawn.go`
  (coding skin), `build.go` (skin gate + boot spec), `args.go`, `systemprompt.go`,
  `persona_dispatch.go`, plus a new play skin + coordination front-end. Ties into
  the character-cards / persona arc (PR #6) — a dispatched actor can be seeded
  from a persona **or** a CCv2 card.
- **Origin:** The card work surfaced auto-swarm's coding skin leaking into
  immersive sessions (the `swarm_spawn` tool + a coding-framed addendum injected
  into `--chat`/`--play`). That exposed a deeper truth: **"auto-swarm" is one
  front-end (a coding skin) over a general dispatch engine, and `--play` wants a
  different front-end over the same engine.** This specs the shared architecture.

## TL;DR

A swarm sub-agent and a roleplay NPC are the same primitive: **a parent agent
instantiates role-bound child agents against a shared substrate, coordinates them
over a channel, and observes their lifecycles.** The engine that does this already
exists (process management, an inbox transport, an event-log bus, persistence /
resume, tier/trust selection, and a `Runner` swap seam) and is mode-agnostic. The
"coding-ness" lives in four composable axes — **boot spec, substrate,
coordination, skin** — of which the boot spec and skin have clean additive seams
and the substrate is the one real design problem. Plan: **gate the coding skin out
of immersive modes now**, then generalize the boot spec (experience + substrate
binding + card identity), add a play skin (an **`actor_spawn`** tool, sibling to
`swarm_spawn`) with a **director-style** coordination front-end (**Model Y**: the GM
owns the world, actors are performers), and keep the
substrate abstraction general enough that a **shared authoritative state service**
(**Model X** — useful for coding swarms too: shared task boards, findings, dedup)
stays reachable without redoing Y. Keep process-per-agent behind the existing
`Runner` seam so an in-process or multiplexed backend is a swap, not a rewrite.

## The primitive

> A parent agent **instantiates** role-bound **child agents** against a **shared
> substrate**, **coordinates** them over a **channel**, and **observes** their
> lifecycles.

That sentence describes a coding coordinator fanning out sub-tasks and a game
master summoning NPCs equally well. The engine bundles six concerns; only two are
inherently mode-specific.

| Axis | What it is | Coding today | Play needs | Status |
|---|---|---|---|---|
| **Boot spec** | what the child *is* | `{task, model/provider/tier, persona}` | + experience, + substrate binding, + card identity | **Stage 1 ✅** — `SpawnRequest` carries experience/substrate/card + persona, threaded through persist/reload/resume to child argv |
| **Substrate** | what agents *share* | the cwd (or a leased worktree) | a shared world / state surface | filesystem shared today; world extension exists but **single-agent-per-process**; **no cross-agent shared state** (`world.json` keyed by ext name → writers clobber) |
| **Channel** | parent↔child | inbox (P→C control) + event log (C→P async) | same | general, content-agnostic ✓ |
| **Coordination** | when children run & report | fire-and-forget → batch recap | director-pull / reactive | **Stage 2 ✅** — director-pull shipped (`actor_spawn` awaits the actor's turn); recap stays front-end-only (`interactive_swarm.go`) |
| **Skin** | tool + addendum | `swarm_spawn` + coding prose | `cast`/`summon` + in-fiction prose | **Stages 0+2 ✅** — coding skin gated out of immersive modes; `actor_spawn` + cast addendum shipped |
| **Selection/trust** | model + safety | tier (user-config only), persona name-only, sanitized env | identical | general, mode-independent ✓ |

## What is already general (keep, do not redesign)

The engine in `packages/agent/swarm/` is a reusable dispatch mechanism:

- **Process / lifecycle management** — `Swarm`, `Spawn`/`SpawnReq`, `run`, the
  `pending→running→done|failed|killed|detached` state machine, idempotent
  terminal cleanup (`Agent.finish`).
- **Inbox transport** — `Inbox`/`Listener`/`InboxMsg` is content-agnostic; `Text`
  can carry a coding task or a scene beat.
- **Event-log bus** — `Event`/`EventLog`/`EventFollower`, a generic append-only
  JSONL stream a parent can tail (`FollowEventLog`).
- **Persistence / resume** — meta + event-log replay + `Resume`.
- **Tier/trust selection** — `resolveSpawnRoute` + the tier map; user-config-only
  tiers, name-only personas, sanitized env. Correct and mode-independent.
- **The `Runner` swap seam** — `Config.NewRunner` / `execRunner.Command` decouples
  *dispatch* from *execution*. Today a child is a `terva` subprocess; tomorrow it
  can be a goroutine or a multiplexed session **without touching the supervisor.**

**Verdict:** the process-per-agent model is a strength (isolation, crash boundary,
model routing, resume, full-stack reuse). Keep it. The design work is confined to
the boot spec, the substrate, the coordination front-end, and the skin.

## Decision 1 — the world model: build Y, keep X reachable

**Model Y — Director & Performers (the GM owns the world).** The GM agent holds
the single authoritative world (its own extension instance, as today). NPCs are
dispatched as persona/card sub-agents that do **not** run the world — each receives
the current scene as its turn context and returns an in-character line/intent; the
GM, as referee, applies it and weaves the result back into the scene.

- *Reuse:* near-total. The engine + persona-dispatch already spawn a persona
  child; we add experience passthrough + scene-as-task + a front-end that pulls the
  reply and weaves it. **The world stays single-writer (the GM) → the
  shared-mutable-state problem never arises.**
- The world extension already assumes this shape — `interact(talk)` returns
  *"GROUND TRUTH: X is a person… improvise their reply in character."* Today the GM
  voices NPCs itself; Model Y externalizes that voicing to a dedicated sub-agent
  with its own identity and context. It **degrades gracefully** to today's behavior.
- *Cost:* NPCs aren't autonomous and don't perceive the world directly (the GM
  feeds them the scene).

**Model X — Shared substrate service (the substrate owns truth; agents are
clients).** The world (or, for coding, a shared task/results board) becomes one
authoritative service; the GM and NPC agents — or coordinator and coding workers —
all attach as clients, perceiving via context cards and acting via its tools.

- *Needs new infra:* a multi-client substrate (today it's one instance per
  process with disk-only sharing), turn/consistency arbitration, and a
  substrate→clients event fan-out. This is the distributed-state problem.
- *Payoff is not play-only:* a shared authoritative surface is exactly what a
  large coding swarm wants — a shared task list, a findings ledger, cross-agent
  dedup — so X is a general capability, not a roleplay indulgence.

**Recommendation: build Y now; make the substrate binding in the boot spec
abstract enough that X is reachable later without redoing Y.** Y makes immersive
play a low-risk composition over machinery that already mostly works; X front-loads
the hardest problem (shared mutable state across agents) and should be earned once
we know what multi-agent play/coding wants to feel like.

## Decision 2 — actor lifecycle: ephemeral first, warm is cheap later

The engine already runs children as **long-lived, multi-turn daemons** that keep
their own session and loop on the inbox — so "warm/persistent" is structurally
present. The real gap is a **synchronous request/response ergonomic**: parent→child
is control-only (inbox); the child's reply returns *asynchronously* via the event
log. So a clean *"send a turn, await this turn's completion, capture the line"*
primitive is what's missing — a coordination-layer addition (built on
`SendUserTurn` + tailing the event log to the next task-level `task_end`), **not an
engine rework.**

**Plan:** define one **conversation-handle** abstraction that covers both lifecycles
and **ship ephemeral first** (spawn → line → discard; the GM curates re-injected
context). **Warm** then becomes "keep the handle alive and reuse it" for character
continuity/memory — deferred, but cheap, because persistence is already there.
Warm's real cost is a live process per active actor, which is what motivates the
multiplexer in Decision 3.

## Decision 3 — execution backend: process-per-agent, behind the `Runner` seam

Start with process-per-agent (today's model). The two "later" options are
different bets, not one:

- **In-process goroutines** ≈ the **Model-X substrate bet**: a shared *in-memory*
  world. It requires a genuinely new "many agents in one process" abstraction that
  breaks terva's one-agent-per-process assumptions (extension manager, tool
  registry, session writer, confirm gate are process-singletons today) and forfeits
  crash isolation. Its payoff is fast turn interleaving + trivially shared in-memory
  state — i.e. it *is* Model X's substrate, in-process.
- **A sister multiplexer** (a bounded pool of terva processes, each hosting several
  agent sessions) ≈ the **warm-NPC-scaling bet**: bound the process count when a
  scene has many persistent actors, keeping partial isolation.

**Design rule:** keep the child an **opaque agent handle**; the coordination layer
must never assume subprocess semantics. The `Runner` seam already gives us this, so
both backends stay swappable and we decide by the pressure we actually hit
(shared-in-memory-state → in-process; many-warm-actors → multiplexer).

## Decision 4 — the cast: scoped, declared, and disjoint from the coding roster

**An *actor* is any role-bound entity with a charter for how it responds** — an
NPC, but equally a spaceship, a volcano with eruption rules, a haunted door, the
weather. That is the world-extension's referee shape (ground truth + rules →
response) lifted onto a dispatchable sub-agent, so one primitive covers characters,
hazards, and forces. A *cast* is a set of such actors; each one's charter is a
**persona or a card**. The boot spec's identity axis accepts a persona name **or** a
card (path/name): the card→immersive-persona pipeline already exists, so
`actor_spawn` boots a child through the same `Resolve → ResolvePersona /
loadCardIdentity → BuildSystemPrompt` path.

**The cast is scoped and declared — never the global roster.** This is the load-
bearing rule:

- **The play cast and the coding dispatch roster are disjoint namespaces.** The
  code-review specialists are in the coding roster via their `good_for` tags
  (`dispatchablePersonas`); they must never surface as fantasy-cast members, and
  actors must never surface in a coding dispatch roster. Two pools, two tools.
- **The cast is sourced only from explicit declarations**, unioned and trust-gated:
  the active **world/experience extension's** bundled personas/cards (namespaced
  `ext:<world>:<name>` — a world *ships its cast*); a **scene/project-declared**
  cast list (honored only in a trusted project); and **explicit user-provided**
  cards/personas for this run. **No implicit global fallback** — a `--play` /
  `--project` roleplay can run *closed*, only its built-in cast.
- **The `actor_spawn` schema shows only the scoped cast, by name.** The model
  literally cannot reference an out-of-scope identity, so an irrelevant or hostile
  charter can neither be dispatched nor poison the scene.

**Guardrail — card-is-data still holds for actors:** a card seeds an actor's
*identity/charter*, never capability. Under Model Y an actor is a **tool-less
voice** (see Resolutions), so a card actor has zero effectors by construction;
under Model X an actor's tools come from its **world binding + experience**, never
the card's `extensions`. The sub-agent's model route stays user-config-only (a card
can't redirect it).

Multi-actor ensembles (several actors interacting in one scene) are a **non-goal for
now** but the structure supports them once the **interaction controls** below exist.

## Coordination & control (ordering, limiting, turn budgeting)

Free-running actor-to-actor chatter is the failure mode to design against from day one.
The near-term coordination model is **director-pull**: the GM decides when to
invoke an actor, hands it the scene, awaits its line, and weaves it in. On top of
that, first-class controls:

- **Interaction budget** — a per-scene / per-beat cap on how many actor turns (and
  actor→actor exchanges) may fire before control returns to the GM/user.
- **GM-mediated turn-taking** — actors never address each other directly; the GM
  is the single scheduler and the single writer of world state (Model Y).
- **Reactive coordination** (actors subscribe to world events and act on their own)
  is a **later** option that only makes sense under Model X, and only behind the
  interaction budget.

## The substrate abstraction (the one piece that must be right for Y *and* X)

Define a substrate as **a shared authoritative state surface an agent binds to**:

- **Coding, implicit:** the filesystem (already shared; worktrees give isolation).
- **Coding, explicit (X):** a shared task board / findings ledger the coordinator
  and workers read and append to.
- **Play (Y):** the GM's world instance — the GM is the surface (single writer);
  actors receive projections (scene context) and return intents.
- **Play (X):** the world as a multi-client service agents perceive and act on.

The **boot spec carries an abstract binding reference**, not a concrete
mechanism. Under Y that binding can be minimal (the GM feeds context; the actor
needs no direct handle). Under X the same field resolves to a service endpoint the
child attaches to. Getting this field's *shape* right now (an opaque, resolvable
substrate reference) is what keeps X reachable without reworking Y.

## Staged plan

- **Stage 0 — gate the coding skin. ✅ Implemented (`b34b278`).** The `swarm_spawn`
  tool and its addendum are injected only in non-immersive sessions; `--chat`,
  `--no-tools`/`--no-workspace-tools`, and (until it has its own skin) `--play` get
  none. Gate both sites (`build.go` addendum + `injectSwarmSpawn`) on the same
  condition the base tool registry uses. Fixes the leak; engine untouched.
- **Stage 1 — generalize the boot spec. ✅ Implemented (`0fa464b`).** `SpawnRequest` → add `Experience`, an
  abstract `Substrate` binding, and card identity; thread through
  `Agent`/`agentMeta`/`swarmAgentArgs` (emit `--chat`/`--play`, `--card`, binding)
  + the arg parser. Low blast radius — the flag builder is designed for this.
- **Stage 2 — play skin + director coordination (Model Y). ✅ Implemented
  (`f162412` actor_spawn, `b316ebd` Kertoja + cast advertisement); the hard
  interaction budget stays deferred (soft pacing guidance shipped).** An `actor_spawn` tool
  (sibling to `swarm_spawn`, fork of `SwarmSpawnTool`) whose schema exposes only the
  scoped cast; a coordination front-end that dispatches an actor with scene context,
  awaits its line (`awaitActorLine` → event-log tail), and weaves it in; the
  interaction budget + GM-mediated turn-taking.
- **Stage 3 — warm actors. ✅ Implemented (`975a0e1`, `a93e60d`).** The
  conversation-handle + the await-reply ergonomic; warm-actor resource caps /
  eviction. Shipped eviction retires the actor (stop + remove state);
  R3's non-destructive evict + `Resume` revival remains deferred.
- **Stage 4 — Model X shared substrate service (future).** Multi-client world +
  arbitration + event fan-out; the same surface reused as a coding task/results
  board.
- **Cross-cutting:** keep tier/trust as-is; keep the `Runner` seam clean; the
  coordination layer never assumes subprocess semantics.

## Non-goals (for now)

Autonomous free-running actors; multi-actor ensembles without interaction
controls; the in-process / multiplexed backend; a reply channel on the inbox
socket (Stage 2 tails the event log instead).

## Resolutions

The four open questions are resolved (2026-07-01):

- **Keystone — under Model Y, actors are tool-less voices.** An actor is dispatched
  as its identity fed the current scene and simply *returns a line/intent*; it never
  touches the world — the GM (the sole `--play` agent) applies any effect. So a
  Y-actor is effectively `terva --chat --persona|--card <actor>` fed a scene prompt.
  This is what makes Q1 and Q4 fall away for the near term: a pure voice has no
  substrate to bind and no capability to leak.

- **R1 — Substrate binding shape:** the boot spec carries **one opaque,
  scheme-qualified substrate reference** (`--substrate <scheme>:<ref>`) resolved by
  the child through a **substrate-resolver registry** (the same pattern as
  `--persona`/`ResolvePersona`, `--card`). *Empty ⇒ projected by the parent* (Model
  Y — nothing to implement). Model X registers resolvers for `world:` / `board:`
  schemes; the boot-spec schema and argv never change. Reserve the field + the
  resolver seam now; implement nothing for Y.

- **R2 — Reply re-entry:** use the **event-log tail** (`FollowEventLog` → the
  actor's next task-level `task_end`), wrapped in one `awaitActorLine(agent)` helper.
  Model-generation latency dominates transport, so a socket reply buys nothing at
  turn granularity; the inbox stays control-only. A reply channel on the socket is
  reserved solely for future *streaming* of an actor's line into the scene.

- **R3 — Warm actors** are a **bounded live-process cache over durable, resumable
  sessions**: scene-scoped, small cap (a fixed default of 5 for now —
  `tools.DefaultWarmActorCap`; a config knob when demanded). Eviction is
  non-destructive — evict = stop the process (free the slot); revive = `Resume`
  (session/memory intact via the event log). The GM may explicitly dismiss. Hitting
  the cap is the signal that motivates the sister-multiplexer (Decision 3). Policy
  fixed now; built in Stage 3. Stage-1 implication is only that actor sessions are
  per-scene identifiable + resumable (already true).

- **R4 — Cast/actor trust:** (a) Y-actors are tool-less voices ⇒ card-is-data holds
  by construction; under X, actor tools come from the world binding + experience,
  never the card's `extensions`. (b) `actor_spawn` is scoped to the scene's
  **declared cast** (Decision 4), by name — not arbitrary card paths and not the
  global library — so a cloned repo or injected content can't redirect the cast. (c)
  Sub-agent model route stays user-config-only.

- **Naming (settled):** the tool is **`actor_spawn`** (sibling to `swarm_spawn`,
  making "two skins, one engine" self-documenting); *cast* = the declared set;
  *actor* = any role-bound entity (person, place, force, object). The dispatch verb
  is a **fixed built-in name** — extensions may not define/override it (consistent
  with the built-ins-never-shadowed merge policy; multi-extension conflict has no
  principled resolver). If theming ever matters, project-level config is the venue —
  parked until demanded.

## Deferred questions

- Whether Stage 3 warm-actor streaming justifies the inbox reply channel (R2).
- Model X's concurrency/consistency design (arbitration, event fan-out) — deferred
  to Stage 4, when we know what multi-actor play/coding wants to feel like.
