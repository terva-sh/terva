# Positioning

The canonical statement of what terva is. Downstream copy should derive from
this rather than reinvent it: the README intro, the GitHub "About", the
`cmd/terva` doc comment, AGENTS.md, and the terva.sh site. When the framing
changes, change it here first.

## Core statement

> **terva is a harness for tool-using agents**: a permissioned loop where a
> model drives tools, projected through many front ends (terminal, browser,
> editor over ACP, chat, an embeddable RPC/SDK) and extensible in any
> language. It ships wired for coding, with read, write and run across your
> project, but nothing underneath is specific to code: hand it extensions or
> MCP servers and it operates whatever they expose, from services to physical
> hardware, all under one permission and policy model. The breadth is safe
> because it is *consolidated*: one agent loop and one event wire in the agent
> core, one policy and one provider registry in the harness around it, typed
> and test-backed, so every surface is a thin projection of one system, not a
> reimplementation that drifts.

The fork lineage is history, not identity: lead with what terva *is*. Where
the origin genuinely matters (compat questions, "how does this relate to
zot?"), point at [fork.md](fork.md) rather than restating it. See
*Relationship to zot* below.

## Three layers

terva is built in three layers, and the logo draws them. Use these names, in
this sense, wherever the docs, the README or the site describe terva's shape.

| Layer | Logo part | Holds |
|---|---|---|
| **Agent core** | The wildcard at the centre | The loop, the events, tools, the permission seam, compaction and cost, and the wire to every model provider. It reads no file and no environment variable. |
| **Harness** | The shell around it | terva's composition and conventions: configuration, the permission policy, the model registry, sessions on disk, credentials, and the daemon every front end talks to. |
| **Base** | The block beneath | Where terva meets the world: the front ends, and the tools, extensions, MCP servers, skills and connectors that give the agent its reach. |

**Each layer stands on its own**, and that is part of the pitch:
- A program can host the agent core alone, with its own conventions.
- It can take the core and the harness together, through the Go SDK or over
  `terva rpc`.
- It can keep terva's harness around another agent, through the worker
  backends, which are off by default.
- It can take the whole binary.

[Embedding terva](embedding.md) compares the ways in, and
[What a harness does](design/01-what-a-harness-does.md) draws the layers.

**"Core" means the agent core.** Do not call the permission policy or the
provider registry part of the core. The core holds the permission *seam*, and
terva's policy sits in the harness. For the whole system, say "terva" or
"one system", not "the core".

## Hero line (site / GitHub About)

> An agent harness: a coding agent out of the box, open to anything you can
> wire a tool to.

## Pillars

1. **One harness, many front ends.** Terminal and browser (both first-class),
   editor (ACP), chat, an embeddable RPC/SDK, and soon agent-to-agent meshes
   (A2A), are clients of one harness: *projections of one agent loop and one
   event stream* over the harness's `ctrlproto` control plane, not separate
   reimplementations.
2. **A general tool-operator, pluggable in any language.** Coding tools are
   built in, and everything else you wire up, whether services, hardware or your
   own systems, attaches over small versioned protocols (extensions, connectors,
   MCP, hooks) and runs under the same permission model.
3. **Consolidated, so it's safe to extend.** Hardening collapsed the
   duplicated, separately-reimplemented loops/serializers/policy switches
   into single typed contracts and cut variability; golden, end-to-end, and
   VT-emulator harnesses back every change. That consolidation is *why* the
   surface could grow with confidence.

## Relationship to zot

Lineage, not positioning. Keep it out of the lead; it belongs in a footer, an
FAQ answer, or [fork.md](fork.md). The one-line form, when asked:

> terva began as a hard fork of zot in May 2026 and has long since gone its
> own way, with its own control plane, front ends, protocols, and roadmap. zot
> continues upstream as its own project; terva is not a replacement for it.
> Existing zot installs keep working; upstream tracking was retired in July
> 2026.

Do not claim we track upstream, pull upstream changes on a cadence, or
promise that zot extensions/connectors stay in lockstep. None of that is
true anymore. [fork.md](fork.md) states exactly where the compatibility
promises begin and end.

## Audience

Developers who want a fast, terminal-first agent they can drive from anywhere
(terminal, editor, chat), extend in any language, embed as a library, and,
above all, trust enough to build on.

## Flavor (use sparingly)

*terva* is Finnish for **pine tar**, the traditional preservative and
cure-all, and the sealant that made wooden boats seaworthy. It maps onto the
thesis almost too neatly: preserve and harden what works, then carry a broad
toolkit on top. The default agent persona leans on the same image: it is
**Mieli** (*MYEH-lee*), Finnish for "mind": a mind in a preserved vessel,
with terva the craft that carries it and keeps it whole. A light touch is
plenty; keep it out of the core statement, and never let the metaphor crowd
out the engineering.

## Voice guardrails (what NOT to say)

- **Not "lightweight"** as the identity. It is a *single static binary* with
  a *lean, consolidated core* (footprint virtues worth stating), but
  batteries-rich in capability. Don't let "lightweight" headline it.
- **Not boxed into "coding."** Coding is the default toolset, not the
  ceiling. The harness operates whatever tools you give it.
- **Not a zot replacement, successor, or rename.** A hard fork; zot lives on.
- **Never "Terva AI."** The brand is "terva".
- **Persona ≠ brand.** The default agent persona is *Mieli* (the mind); the
  product and binary are *terva* (the vessel). Don't rename the product to
  Mieli, and never "Mieli AI" either.
