# Positioning

The canonical statement of what terva is. Downstream copy — the README
intro, the GitHub "About", `cmd/terva` doc comment, AGENTS.md, and the
terva.sh site — should derive from this, not reinvent it. When the framing
changes, change it here first.

## Core statement

> **terva is a harness for tool-using agents** — a permissioned loop where a
> model drives tools, projected through many front ends (terminal, editor
> over ACP, chat, an embeddable RPC/SDK) and extensible in any language. It
> ships wired for coding — read, write, and run across your project — but the
> core is general: hand it extensions or MCP servers and it operates whatever
> they expose, from services to physical hardware, all under one permission
> and policy model. The breadth is safe because the core is *consolidated*:
> one agent loop, one event wire, one policy, one provider registry — typed
> and test-backed — so every surface is a thin projection of a single
> hardened core, not a reimplementation that drifts. A hard fork of zot
> focused on hardening the seams and widening the surface; its own project,
> not a replacement.

## Hero line (site / GitHub About)

> An agent harness — a coding agent out of the box, open to anything you can
> wire a tool to.

## Pillars

1. **One core, many front ends.** Terminal, editor (ACP), chat, an
   embeddable RPC/SDK — and soon agent-to-agent meshes (A2A) — are
   *projections of one agent loop and one event stream*, not separate
   reimplementations.
2. **A general tool-operator, pluggable in any language.** Coding tools are
   built in; everything else you wire up — services, hardware, your own
   systems — attaches over small versioned protocols (extensions, connectors,
   MCP, hooks) and runs under the same permission model.
3. **Consolidated, so it's safe to extend.** Hardening collapsed the
   duplicated, separately-reimplemented loops/serializers/policy switches
   into single typed contracts and cut variability; golden, end-to-end, and
   VT-emulator harnesses back every change. That consolidation is *why* the
   surface could grow with confidence.

## Relationship to zot

> A hard fork of zot, focused on hardening the seams and widening the
> surface. zot continues upstream as its own project; terva is its own
> project, not a replacement.

## Audience

Developers who want a fast, terminal-first agent they can drive from anywhere
(terminal, editor, chat), extend in any language, embed as a library, and —
above all — trust enough to build on.

## Flavor (use sparingly)

*terva* is Finnish for **pine tar** — the traditional preservative and
cure-all, and the sealant that made wooden boats seaworthy. It maps onto the
thesis almost too neatly: preserve and harden what works, then carry a broad
toolkit on top. The default agent persona leans on the same image: it is
**Mieli** (*MYEH-lee*), Finnish for "mind" — a mind in a preserved vessel,
with terva the craft that carries it and keeps it whole. A light touch is
plenty; keep it out of the core statement, and never let the metaphor crowd
out the engineering.

## Voice guardrails (what NOT to say)

- **Not "lightweight"** as the identity. It is a *single static binary* with
  a *lean, consolidated core* (footprint virtues worth stating) — but
  batteries-rich in capability. Don't let "lightweight" headline it.
- **Not boxed into "coding."** Coding is the default toolset, not the
  ceiling. The harness operates whatever tools you give it.
- **Not a zot replacement, successor, or rename.** A hard fork; zot lives on.
- **Never "Terva AI."** The brand is "terva".
- **Persona ≠ brand.** The default agent persona is *Mieli* (the mind); the
  product and binary are *terva* (the vessel). Don't rename the product to
  Mieli, and never "Mieli AI" either.
