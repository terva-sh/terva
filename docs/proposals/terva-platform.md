# Proposal — terva as a hosted agent platform (the multi-user horizon)

- **Status:** VISION / HORIZON. Not committed work. Depends on `terva web`
  (docs/proposals/terva-web.md) and `ctrlproto`
  (docs/proposals/control-plane-protocol.md) shipping first. This doc exists so
  the near-term single-user work does not foreclose the destination — it is a map,
  not a plan.
- **Date:** 2026-07-03
- **Scope:** the future architecture for running terva as a **multi-user hosted
  service** — per-user chat/roleplay (a self-hosted SillyTavern-class product),
  and beyond it a tabletop **Dungeon Master assistant** that can drive a **Virtual
  Tabletop**. Enumerates the deltas from single-user `terva web` to multi-tenant,
  each grounded in the 2026-07-03 feasibility review.
- **Origin:** the original ask — *"could I use terva to power my own version of
  SillyTavern?"* — and its natural next step — *"a tabletop DM assistant with a
  custom subset of extensions, driving a VTT, as a rich web UI."* The feasibility
  review answered "yes, with specific deltas." `ctrlproto` and `terva web` are the
  first two rungs; this keeps the top of the ladder in view.
- **Prereq reading:** docs/proposals/terva-web.md, docs/proposals/control-plane-protocol.md,
  docs/proposals/agent-dispatch.md (the dispatch engine the fleet reuses),
  docs/proposals/character-cards.md, docs/proposals/path-triggered-lore.md,
  docs/proposals/persona-format.md.

## TL;DR

The feasibility review's headline: **the core engine is already well-isolated
(graded A−)** — three independent frontends drive it through one small seam, it
holds no terminal coupling, and it already runs many independent agents in one
process. The roleplay feature set (cards, lore, personas, cast, chat/play modes)
is **~80% frontend-agnostic** — all resolved into config and baked into a
`core.Agent` before any terminal exists. So a self-hosted SillyTavern is a
**frontend + a multi-tenant shell** over an unmodified engine, and the DM/VTT
evolution is an **extension** on top of that.

The one genuinely hard problem — OS-level sandboxing of untrusted code — **only
bites the code-development use-case.** Chat, roleplay, and DM/VTT never hand the
user a shell, so the scariest requirement evaporates for exactly the products
most wanted. The rest of the multi-tenant delta is host-layer plumbing (auth,
per-user creds, per-user persistence, per-user concurrency, a per-user config
seam) — and `ctrlproto`'s interface-first design is deliberately the substrate
for it.

## The ladder

Each rung reuses the one below; nothing here is a rewrite.

1. **`terva web` (single user, single workspace).** The control panel. Ships the
   OpenClaw replacement. — *docs/proposals/terva-web.md*
2. **Multi-tenant `terva web` (N users, one deployment).** The same workspace
   service, instantiated per user, behind real auth. This is where the deltas
   below live.
3. **SillyTavern-class product.** Multi-tenant + the roleplay stack surfaced in
   the web UI (character cards, lorebooks, personas, per-character sessions).
4. **DM assistant.** The roleplay product + a cast of NPCs from one session +
   **lore as the campaign/rules bible** + a tabletop extension (dice, initiative,
   stat lookups, narrative state).
5. **VTT driver.** The DM assistant + an extension that holds live campaign state
   and pushes map/token/state deltas to a browser in real time.

## What's already there (from the feasibility review)

Keep, do not redesign:

- **A frontend-agnostic engine.** `packages/core` imports no TUI/terminal code and
  holds all conversation state per-`Agent`. The driving contract is a small trio —
  `sink func(AgentEvent)` for output + `core.Confirmer` + `core.Asker` for
  decisions — plus optional `On*` persistence hooks, driven via
  `PromptWithPolicy` and constructed via `Resolved.NewAgent`. Three independent
  implementations already exist (TUI, chat/connproto, ACP).
- **In-process multi-agent concurrency.** Bot mode already mints one `core.Agent`
  per chat over a shared tool registry; `agentEpochSeq` and per-call context
  carriage exist specifically so many agents coexist in one process.
- **A ~80%-reusable roleplay stack.** Cards (CCv2 parse → greeting → intro
  override → lorebook), lore (a per-turn `ContextProvider` hook — SillyTavern
  lorebook parity, natively), personas, cast/actor-dispatch (warm,
  scene-remembering NPCs via the swarm engine), and `--chat`/`--play` modes are
  all resolved into `Resolved` and baked in before any terminal exists.
- **A powerful extension platform.** Extensions are unrestricted subprocesses that
  can register tools with effect-class semantics, hold per-session/per-project
  state, inject per-turn context via **context cards** (the "referee owns ground
  truth → model narrates" seam), call back into host tools, bundle personas, and
  act as connectors. The `examples/extensions/world/` and
  `experiments/holodeck/starship-sim/` examples are near drop-in templates for a
  "world" — i.e. a game/VTT backend.

## The multi-tenant deltas (rung 1 → 2)

Grounded in the review. Note that **only #5 is genuinely hard**, and it is scoped
to the code-development use-case:

1. **Auth & accounts.** Today's model is single-owner + chats, not N equal
   logged-in users. Multi-tenant needs real accounts. The single-user answer
   (front it with Authentik/OIDC) generalizes: the proxy asserts identity; terva
   maps it to a per-user workspace. This is where the single-owner pairing model
   is replaced.
2. **Per-user credentials.** API keys/OAuth resolve today from process env + one
   global `auth.json`. Route each user's key through the `explicit` credential
   parameter and disable the global paths server-side, so each user brings their
   own keys/model config.
3. **Per-user durable persistence.** The engine supports it (persistence is behind
   `On*` callbacks — a web backend can store transcripts in its own DB and never
   touch disk-JSONL). The *bot* layer only persists the owner's session; a
   multi-user product needs a real per-user conversation store.
4. **Per-user concurrency.** The bot serializes turns on one global queue. A
   multi-user backend drives at the agent-event level (one agent per user session,
   each single-flight, many in parallel) — which `ctrlproto`'s addressing model is
   built for.
5. **Filesystem isolation — the one hard piece, and only for code work.** There is
   no OS-level jail; the in-process sandbox is a guardrail, not a boundary, and
   bash inherits the environment. **Untrusted multi-tenant *coding* needs real
   per-session containment (container/VM/namespace).** But **chat, roleplay, and
   DM/VTT hand the user no shell** — the model calls narrative/dice/VTT tools, not
   bash in a shared workdir — so this requirement does not apply to the products
   at the top of the ladder. Ship those first; they are both the most-wanted and
   the safest.
6. **Global model catalog.** `provider/models.go` layers and the
   `registerEndpointsOnce` registry are process-wide; one tenant's discovery
   mutates everyone's view. Freeze to a shared read-only baseline server-side, or
   make the active catalog a per-session value. (The `ctrlproto` config-threading
   down-payment — workspace service holds a `Config` — is the seam for this.)

## `ctrlproto` as the fleet substrate

The multi-user backend is not a new engine — it is **the workspace service,
instantiated per user, addressed by the `ctrlproto` envelope's session/agent id.**
Because addressing and capability negotiation are in the protocol from day one
(see the protocol proposal), the path from "one user, one agent" to "a control
plane driving a fleet of per-user agents" is *adding carriers and instances*, not
reframing the protocol. This is why the interface-first decision matters beyond
the TUI: it is the same interface at every scale.

This also connects to existing work: the swarm / agent-dispatch engine
(docs/proposals/agent-dispatch.md) already spawns terva agents as subprocesses and
observes their lifecycles. A fleet control plane over `ctrlproto` is the clean way
to *manage and observe* such agents — the same primitive that drives a coding
swarm or an immersive cast drives a hosted fleet of user sessions.

## Rung 3 — SillyTavern-class product

The review's coupling scorecard: **cards REUSABLE · lore REUSABLE · personas
REUSABLE · modes REUSABLE · cast REUSABLE-engine / COUPLED-assembly.** The
integration contract is the same one `terva web` uses:

```
build Args → Resolve(args) → Resolved.NewAgent() → drive PromptWithPolicy(sink)
```

- Character cards import to a session (greeting seeded as `messages[0]`,
  `system_prompt` → intro override, `character_book` → lore) via terminal-free
  code in `cardsetup.go`.
- Lore injection is a `core.Agent.ContextProvider` hook — any frontend gets
  keyword-triggered context for free. This is the lorebook.
- The only extraction needed is the **cast/actor-dispatch assembly** (~70 lines
  currently inlined in the interactive loop); every underlying piece
  (`swarm.Swarm`, `ActorSpawnTool`, `WarmActors`, `buildActorCast`) is already in
  reusable, TUI-free packages.

So rung 3 = rung 2 (multi-tenant) + surfacing this stack in the web UI (card
upload/manage, lorebook editor, persona picker, per-character sessions). No engine
work beyond the cast extraction.

## Rung 4 — DM assistant

The DM assistant is rung 3 plus a **tabletop extension**, and it maps cleanly onto
existing primitives:

- **Lore = the campaign / rules / setting bible.** Keyword-triggered injection is
  exactly "when the party enters Waterdeep, inject the Waterdeep entry" — native
  lorebook parity. (See docs/proposals/path-triggered-lore.md for the lore
  direction.)
- **Cast = the NPC ensemble.** The director/performers model already spawns warm,
  scene-remembering actors from one session.
- **A tabletop extension** adds dice / initiative / stat-block tools with proper
  effect-class semantics, holds live campaign state across the session, and pushes
  authoritative game state into the model every turn via **context cards** — the
  "referee owns ground truth, model narrates" seam. `examples/extensions/world/`
  and `experiments/holodeck/starship-sim/` are near drop-in templates (the latter
  runs a `refreshSurfaces`-after-every-mutation loop — the exact "push state on
  every change" pattern a game wants).

## Rung 5 — VTT driver

The extension protocol (v5) is expressive enough: an extension is an unrestricted
subprocess with full network access, so it can talk to any VTT API or run its own
socket while exposing tools and holding state. The one thing terva does **not**
provide is the browser UI itself — its built-in UI surfaces (panels/cards/status)
are terminal-only, and its push-into-terva model is chat-reactive. So the division
of labor is clean but real:

- **terva** drives narration, tools, and authoritative state (via the extension +
  context cards).
- **the extension** embeds its own HTTP/WebSocket server and renders the VTT,
  pushing map/token/state deltas to the browser on each mutation.

Nothing in the protocol obstructs this; nothing assists it either. (Two notes from
the review: extensions have no egress sandbox — total capability, "trust the
extension" — fine for self-authored tools, relevant if distributed; and
extension-bundled *lore* is not first-class the way personas/skills are — author it
as context cards or via the native `lore` primitive.)

## Extension-as-frontend (the convergence)

Rungs 4–5 and the `ctrlproto` ext-tunnel carrier converge on a powerful endpoint:
once the control plane is exposed over extproto (authority-gated), an extension can
build **an entire frontend** — not just a tool provider or a chat connector, but a
full control-plane client. A VTT extension that both drives the game *and* serves
its own control-panel-grade web UI is then a first-class citizen, and "whole new
frontends built in extensions" and "a dedicated control plane driving a fleet"
become the same mechanism viewed from two ends.

## Sequencing & dependencies

1. `ctrlproto` v1 (conversation + session groups) + `terva web` v1 — *committed
   direction; see the two sibling proposals.*
2. `ctrlproto` control group + the cast-assembly extraction — enables the roleplay
   surfaces.
3. Multi-tenant shell (deltas 1–4, 6) — the hosted product. **Defer delta 5
   (OS-level sandboxing) until/unless a code-development tier is in scope.**
4. Tabletop extension (rung 4) — can be prototyped independently against
   single-user `terva web` at any point, since it is "just" an extension.
5. VTT UI (rung 5) — the extension's own web surface.
6. TUI migration onto `ctrlproto` + ext-tunnel carrier — unifies frontends and
   opens extension-as-frontend.

## Open questions / risks

1. **Where multi-tenancy actually starts.** The single-user `terva web` is
   genuinely useful standalone (it replaces OpenClaw). Multi-tenant is a
   deliberate, separate decision — do not let it creep into the single-user work
   beyond the cheap forward-compat down-payments (addressing in the envelope,
   config threading).
2. **The code-development tier is a different security universe.** If hosted
   multi-user *coding* is ever a goal, it needs real containment and should
   probably be its own deployment shape (one sandbox per session), not a mode of
   the chat/roleplay product.
3. **Distribution vs. self-hosted authority.** Everything above assumes
   self-hosted / trusted extensions. A distributed marketplace (extensions from
   strangers) would need the egress + authority story hardened well beyond
   today's "trust the extension."
4. **Product identity.** SillyTavern, a DM assistant, and a coding platform are
   three products sharing an engine. The platform's job is to keep the engine and
   `ctrlproto` general; the *skins* (as agent-dispatch.md frames them) are where
   each product's identity lives.
