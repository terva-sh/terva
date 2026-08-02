# terva by design

**How terva works, and why it is built that way.** This tier explains the
machine in ordinary programming vocabulary — loops, queues, budgets,
protocols, chokepoints. It does not assume you write Go, and it names no Go
types. If you can read a sequence diagram and reason about a request/response
cycle, you can read all of it.

It sits between two other bodies of documentation:

- **[The user and operator guides](../README.md)** tell you how to *use*
  terva — flags, commands, configuration, what each front end does.
- **This tier** tells you how terva *works* — the loop, the context budget,
  the permission chokepoint, the control plane, the extension seams — and
  the reasoning behind each.
- **The implementation record** — subsystem-by-subsystem internals, cited to
  Go files and symbols — lives in the development repository under
  `docs/architecture/`, alongside the decision records in `docs/decisions/`.
  It is not part of the public release tree, so it is named here rather than
  linked.

There is a fourth body of writing next door:
**[practices](../practices/README.md)** — what we and the field have learned
about building agent harnesses *in general*, stated as guidance you could
apply to a harness that is not terva. This tier describes one system;
that one generalizes.

## Read in order

The chapters build on each other. Read 01–03 and you understand the engine;
everything after that is what was built around it.

| | Chapter | The question it answers |
|---|---|---|
| 01 | [What a harness does](01-what-a-harness-does.md) | Why anything sits between a model and your machine at all, and what the eight jobs of that layer are |
| 02 | [The agent loop](02-the-agent-loop.md) | The turn/step cycle: how one prompt becomes many model calls and tool executions, and how it stops |
| 03 | [Context and cost](03-context-and-cost.md) | The context window as a budget: what fills it, what caching rewards, and how compaction reclaims it |
| 04 | [The permission model](04-permission-model.md) | One chokepoint, two orthogonal axes, seven authority classes — and why the axes must not be merged |
| 05 | [One core, many front ends](05-one-core-many-front-ends.md) | The control plane: why the terminal UI is a network client of a daemon it usually hosts itself |
| 06 | [The extension model](06-extension-model.md) | Four seams for adding capability, the footprint ladder for choosing between them, and why most of them are out-of-process |
| 07 | [State and identity](07-state-and-identity.md) | What survives a restart: sessions, personas, experiences, keyed context, and the play domain |
| 08 | [Lessons](08-lessons.md) | What we got wrong, what the corrections cost, and the rules we now hold ourselves to |

## The shortest possible summary

A **prompt** arrives from a front end. The engine assembles a **request** —
system prompt, the conversation transcript, the tool schemas — and streams the
model's reply. When the model asks to call a tool, every such call passes
through a **single permission chokepoint** before it runs; the result is
appended to the transcript and the engine calls the model again. That cycle
repeats until the model stops asking for tools. Everything the loop does is
emitted as a **typed event**, which is simultaneously what the screen renders,
what the transcript file records, and what every other connected client sees.

Six ideas carry most of the weight, and each has a chapter:

1. **The loop is the product.** Everything else exists to feed it, bound it,
   observe it, or extend it.
2. **The context window is a budget**, not a container — and its economics are
   dominated by the provider's prefix cache, not by raw token count.
3. **There is exactly one place a tool call can be denied.** Not one per front
   end, not one per tool.
4. **Enforcement and approval are different axes.** What a tool may touch and
   whether it may run at all are separate questions with separate answers.
5. **Every front end is a projection of one loop**, speaking one protocol to
   one daemon — never a second implementation of the same behavior.
6. **Capability arrives from outside the process** wherever it plausibly can,
   because a crashing plugin should not be able to take the agent with it.
