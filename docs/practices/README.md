# Practices for agentic harnesses

**What we and the field have learned about building the layer between a model
and a machine.** These pages are written to be useful to someone building a
harness that is not terva. Where a practice comes out of our own scar tissue we
say so and say what it cost; where it comes from the wider field we say whose
and cite what.

This is the generalizing companion to [terva by design](../design/README.md),
which describes one specific system. If you want to know how terva works, read
that. If you want to know what to do in yours, read this.

## How a practice earns a place here

Advice about agents is cheap and mostly untested, so every entry carries a tag
naming the strength of the evidence behind it. Nothing appears here on
plausibility alone.

| Tag | Means |
|---|---|
| **Measured** | We have numbers from a real system — token counts, cost deltas, hit rates |
| **Scarred** | We shipped the bug. The cost is stated |
| **Converged** | Several independent harnesses arrived at the same answer without coordinating. Strong signal, weaker than a measurement |
| **Reported** | Someone else's number or claim. Attributed, and flagged when the source has an interest in it |
| **Contested** | Practitioners genuinely disagree. Both positions and the deciding factor are given |

A practice that is merely a good idea is not listed. There are plenty of those
and they are not worth your time.

## The pages

| | Page | Covers |
|---|---|---|
| 01 | [The loop](01-the-loop.md) | Turn and step structure, stopping, cancellation, stuck-loop detection, error recovery |
| 02 | [Context economy](02-context-economy.md) | The prefix cache, injection cost, compaction, tool-output discipline, retrieval over inclusion |
| 03 | [The tool surface](03-tool-surface.md) | How many tools, what shape, descriptions as prompt engineering, edit formats, error messages a model can act on |
| 04 | [Permission and sandboxing](04-permission-and-sandbox.md) | The two axes, the chokepoint, authority classification, durable grants, what a jail is and is not |
| 05 | [Extensibility and protocols](05-extensibility-and-protocols.md) | In- versus out-of-process, the footprint ladder, versioning a plugin wire, MCP as an adapter |
| 06 | [Operating and evidence](06-operating-and-evidence.md) | Sessions, accounting, observability, and how to test a system whose central component is nondeterministic |

## The short version

If you read nothing else:

1. **The context window is a recurring bill, not a container.** Every step
   re-sends the whole conversation. Design for the prefix cache before you
   design for anything else.
2. **One permission chokepoint.** Every harness that grew a second one shipped
   a bypass through it.
3. **Separate what may run from what it may touch.** Merging these two axes is
   the most common structural mistake in the category.
4. **Keep the built-in tool count small and defend it.** Always-on tools are
   paid for by every session forever, including the ones that never use them.
5. **Out-of-process for anything foreign.** A plugin crash should cost you a
   subprocess.
6. **Deny by returning a result, not by throwing.** An agent told "no" in a
   form it can read routes around the obstacle.
7. **Record evidence at the source.** Attribution, timestamps, cache reads and
   writes. Every one of these is cheap to write and impossible to reconstruct.
8. **The dangerous bugs do not crash.** Budget review effort for features that
   silently do not run.
