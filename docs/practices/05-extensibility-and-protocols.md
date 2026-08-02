# 05 — Extensibility and protocols

Two decisions determine whether a harness stays small: what runs inside your
process, and what your plugin contract actually promises.

---

## In-process or out

### Contested, and here is the deciding factor

**Contested.** The field is genuinely split, and both positions are held by
serious projects.

**In-process** (OpenCode's plugins; maki's Lua against a Neovim-compatible API)
buys ergonomics that are hard to match. Plugin authors get direct access, no
serialization, no protocol to learn, and — in maki's case — the ability to
copy-paste from an existing ecosystem. This is a real advantage and it shows up
in plugin count.

**Out-of-process** (terva's extensions; Goose's all-MCP model; Claude Code's
declarative bundles) buys isolation. The costs are equally real, and the
harnesses that went all-in report them consistently: startup latency for tool
discovery, multi-process debugging, configuration sprawl.

The factor that should decide it:

> **Can a plugin crash take down an active session?**

If the answer is yes, you have a support burden that is indistinguishable from a
bug in your own code, and you will spend your time triaging other people's
plugins. OpenCode's in-process plugins can crash the server, and that is the
documented trade they accepted.

Secondary factors that mostly point the same way: language independence (an
author should not have to learn yours), a versioned wire (compatibility becomes
a testable property of frames rather than a hope about types), and a real
interface to defend — which is what keeps a plugin API from becoming "whatever
the internals happen to expose this month."

### Keep the hot path compiled in regardless

**Reported.** Goose went all-MCP, including in-process servers over an in-memory
pipe for the built-ins. Their own verdict is that it works with known costs, and
they mitigated by keeping hot built-ins in-process.

The lesson generalizes past their architecture: **speak protocols outward, do
not rebuild your core around one.** The four or five tools your loop uses
constantly should not pay serialization and process-hop cost per call.

---

## The plugin contract

### Declarative bundles beat linked code

**Converged.** Claude Code plugins and Gemini extensions independently arrived
at the same shape: a plugin is a **file tree** — a manifest plus commands,
skills, hook configuration, tool-server declarations, themes — installed from a
git URL, updated by pull, with no registry infrastructure. Data, not linked
code.

This is worth internalizing because it is not the obvious design. It means most
plugins contain no executable at all, which makes them reviewable, diffable,
and safe to install in a way a binary is not. Let one bundle contribute several
kinds of thing, and let most of them contribute no code.

### Untrusted layers restrict only

**Converged.** A bundle or project layer may **suggest tighter** permissions. It
may never grant authority. Gemini enforces exactly this — extension-contributed
policy rules cannot auto-approve — and it is the single most important rule in a
plugin permission story.

### Version the wire, and answer the unknown-frame question explicitly

**Scarred.** A versioned protocol needs a stated answer for a frame it does not
recognize. Ours silently ignored unknown frames, which is a defensible choice
that must be *chosen* — the failure mode is a newer plugin whose feature simply
does not happen, with no error anywhere.

Whatever you pick — ignore, warn, refuse — write it in the protocol document and
test it. And set sunsets on compatibility paths when you add them, because a
compat path with no expiry is permanent.

### Beware version numbers that stop meaning anything

**Scarred.** One of our two plugin protocols abandoned its version number in
favor of an unbounded set of feature strings. That is a reasonable evolution and
it has a cost: there is no longer a single thing to compare, so "which version
does this speak" stops having an answer, and negotiation bugs get harder to see.

If you go feature-string, keep an *advertised set* that both sides can print,
and test the advertisement (see below).

### Do not build two of the same protocol

**Scarred.** We have two parallel line-protocol plugin systems whose overlap was
eventually resolved by *tunneling one inside the other*. That is a workable
outcome and a bad sign.

The judgment to apply before adding a second: is this genuinely a different
*shape* of thing — a conversation transport is not a tool provider — or is it the
same shape with a different audience? The second case is a role within the
existing protocol.

### Adopt the ecosystem standard as an adapter, not a parallel system

**Converged.** MCP is the field's shared tool protocol; every serious harness now
speaks it. The right integration is an **adapter into your existing extension
machinery** — same registration path, same permission gate, same audit trail —
rather than a second pipeline beside it.

A second pipeline means two places to gate, and [permissions](04-permission-and-sandbox.md)
covers what happens next.

Transport note: stdio plus streamable HTTP is the de-facto pair. SSE is
deprecated across the ecosystem; skip it.

---

## Front-end protocols

### The core/front-end split is the architectural consensus

**Converged.** Codex (submission/event enums), OpenCode (server plus attach),
Goose (a daemon), Gemini CLI (core and cli packages), OpenHands (app and agent
servers), terva (a control plane) — everyone arrived here independently.

The shape: one canonical protocol of verbs and events, N front ends as clients.
Not one loop per front end.

Two things to get right early, because retrofitting them is painful:

**Model interactions as protocol messages.** Approvals, permission escalations,
plan updates, session rollback — not UI callbacks. Otherwise your approval flow
works only in the front end that existed when you wrote it.

**The state belongs to the server.** Pending message queues, approval state,
session ownership. If the UI owns the queue, a disconnecting client takes queued
work with it and two clients disagree about what is pending.

### Test the advertisement, not just the implementation

**Scarred.** Our worst protocol bug: a feature fully implemented on the server,
fully requested by the client, and never listed in the connection handshake. It
was dark in production for weeks. Every code path involved looked correct in
review, because the defect was an absent list entry — and an absent list entry
has no reader.

> **When a capability is announced in one place and implemented in another, test
> the announcement.** The implementation has consumers who will notice. The
> declaration has none.

### Mirror types by generation, not by hand

**Scarred.** If a second language needs your protocol types — a browser client,
say — a hand-maintained mirror will drift. Ours did, and the shape test we
eventually added found six view types already missing fields on its first run.

Generate them, or failing that, write a test that compares the field sets in
**both directions** and fails on either kind of drift.

---

## Delegation as an extension mechanism

### A subagent is a context-economics primitive

**Converged.** Spawning a child agent that does bounded work and returns a
summary spends the *child's* window, not the parent's. The parent pays for the
summary, not the search. This is often a better answer than a new tool.

Generalize the contract to *spawn with a task, return a summary* and the backend
stops needing to be your own harness — a foreign agent can fill the same slot.

### Get two things right or it will hurt

**Scarred.** Both of these cost us real time:

- **Attribute the child's spend as delegated**, at the moment you record it.
  Unmarked, a child's tokens in the parent's file look exactly like a parent
  cache miss.
- **Scope the child by argument, not by mutable shared state.** Ours was a field
  on a shared object, which is a race waiting for a second concurrent child.

### Give the model tiers, and cap them

**Reported.** maki lets the model choose a weak, medium or strong model for a
delegated task, capped at the parent's tier, with summary-only return. Cheap to
implement, and it moves a cost decision to the place with the most information
about the task.

---

## Zero-context tool composition

**Reported, contested in intent.** Two harnesses independently built the same
primitive: a sandboxed script interpreter, exposed to the model as a tool, whose
*own* tool calls proxy back to the harness and whose intermediate output is
suppressed. Only what the script returns enters the context.

The effect is collapsing a fifty-step pipeline into one turn. maki built it to
*spend less*; Hermes built it to *remember everything*. Same primitive, opposite
motivation — which is a good sign that the primitive is real.

The caution is that this is an execution sandbox and inherits every question in
[permissions](04-permission-and-sandbox.md). Both implementations hard-reject OS
calls from the interpreter; do not build one that does not.
