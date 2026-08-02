# 06 — The extension model

No harness author can guess which tools a given user needs. The extension
surface is where a harness either stays small or accretes everybody's special
case until it is a monolith with a plugin API bolted on.

This chapter covers the two questions that decide it: **what should be built
in**, and **how should everything else attach**.

## Why the built-in tool count is the number to watch

The instinct is that adding a tool is cheap — it is a few hundred lines and
nobody has to use it. That is wrong, for a reason specific to this domain.

Every always-on tool ships its name, its description, and its full JSON schema
in **every request of every session**. The tool catalog is typically the largest
single component of a cold prompt. Prefix caching amortizes it after the first
call, but the first call of every session pays in full, and the schema sits in
front of the entire conversation forever after.

So a built-in tool is not paid for by the users who need it. It is paid for by
every user, every session, whether they use it or not. That is why the bar for
a new built-in is *"the loop itself needs this"* rather than *"this is useful."*

The field supplies the cautionary data. One peer harness carries twenty-five
built-in tools, each with its own permission plumbing; another is a
hundred-and-twenty-crate workspace. Meanwhile the harness that ships subagents
and plan mode as *example extensions* rather than core features demonstrates
that even headline capabilities can live outside.

terva's always-on set is the six the loop cannot function without — read,
write, edit, run a command, and two search tools — plus a handful that are
there because the loop works materially better with them: a way for the agent
to ask you a question, a way to inspect its own runtime state, a way to read
back its own past transcripts, and a small task board it plans with.

That is more than the minimum, and the honest reading is that the bar has been
cleared by argument several times rather than never. Each of the additions has
one: `grep` and `glob` exist to keep search out of `bash`, where it would be
opaque to the permission layer; `ask_user_question` exists because a model with
no way to ask is a model that guesses; the rest earn their place by removing
work the agent would otherwise do badly and expensively through the shell. The
point of a high bar is not that nothing clears it — it is that clearing it
requires saying why, in writing, where someone can disagree.

Everything else attaches.

## The footprint ladder

When a capability is requested, work down this ladder and stop at the first
rung that can carry it. The ordering is by cost to everyone who is *not* asking.

| | Rung | Cost to non-users | Use when |
|---|---|---|---|
| 1 | **Prompt or skill** — a `SKILL.md` procedure loaded on demand | One line in a manifest | The capability is knowledge, not a new execution primitive |
| 2 | **Existing tools + a shell recipe** | Zero | It composes from what is already there |
| 3 | **Hook** — an external program on the tool-call path | Zero unless configured | It is policy or lifecycle, not capability |
| 4 | **MCP server** | Zero unless configured | An implementation already exists in the ecosystem |
| 5 | **Extension** — an out-of-process plugin | Zero unless installed | It needs real code, terva-specific semantics, or its own lifecycle |
| 6 | **Built-in tool** | **Every session, forever** | The loop itself needs it, or it meaningfully reduces risky `bash` use |

The default answer is rung 5, and the ladder's value is that it makes rung 6
require an argument.

Skills deserve a note because they are the cheapest rung and the least
understood. A skill is a markdown file with a name and a description. At
startup only a **compact manifest** — names, descriptions, where they came from
— enters the prompt; the body is loaded only if the model calls for it. So a
hundred available procedures cost roughly a hundred short lines of context, and
the one that gets used costs its full length exactly when it is relevant. That
ratio is why "make it a skill" beats "make it a tool" far more often than
people expect.

## Four seams

Beyond skills and hooks, capability arrives through four mechanisms with
genuinely different shapes.

**Extensions** are separate processes speaking a typed line-delimited protocol
over stdin/stdout. Any language. They register tools, intercept tool calls,
observe events, contribute configuration and UI, and can supply their own
persistent data directory. This is terva's primary seam and the most capable
one.

**MCP servers** are the ecosystem's shared standard for the same idea, over
stdio or streamable HTTP. terva consumes them as an *adapter into* the
extension machinery rather than as a parallel subsystem — same registration
path, same permission gate, same audit trail. That choice matters: the
alternative, a second tool pipeline with its own gating, is how you end up with
a permission bypass.

**Connectors** carry a conversation in from somewhere else — Telegram, Discord,
anything you write. A chat platform is not a tool; it is a front end with a
weird input device, and treating it as its own protocol rather than forcing it
through the tool abstraction is what keeps the tool abstraction clean.

**Hooks** are external programs invoked on the tool-call path. They can allow,
deny, or force a prompt, and they run *before* the policy gate, which is what
makes them the zero-protocol on-ramp: a shell script and a JSON blob on stdin
buys you organization-specific policy with no plugin to write.

One distinction is worth preserving explicitly, because a peer harness
conflated it and shipped bypass bugs: **decision hooks that can change an
outcome must be a different mechanism from observe-only notifications.** If the
same callback can both watch and veto, every observer becomes a security
surface.

## Why out-of-process

The dominant reason is blast radius: an in-process plugin that panics takes the
agent with it, and one with a memory leak is indistinguishable from a leak in
the harness. Out-of-process, a misbehaving extension is a subprocess you can
kill and restart while the session continues.

The secondary reasons matter nearly as much. Any language, so an extension
author is not required to learn Go. A versioned wire protocol, so an extension
built against an older version keeps working — and so compatibility is a
testable property of frames rather than a hope about types. And a real
interface to defend, which is the thing that keeps a plugin API from becoming
"whatever the internals happen to expose this month."

The costs are equally real and worth naming: startup latency for tool
discovery, multi-process debugging, and configuration sprawl. Peers who went
all-in on out-of-process report exactly these. The mitigation is the ladder
above — keep the hot path compiled in, and speak protocols outward.

### The honest boundary

One thing must not be overstated, and our own tool documentation states it
plainly:

> Extension **tool calls** are mediated — permission-gated, hook-interceptable,
> classified by authority. The extension **subprocess itself** runs as trusted
> local code with your normal filesystem and network privileges.

Installing an extension is consent to run a local program. The permission model
governs what the *model* can invoke through it, not what the program does on
its own. Anyone claiming their plugin system is a sandbox because its tool calls
are gated is describing a different property than the one people will assume.

Project-local extensions, skills, hooks and MCP servers are additionally gated
by Workspace Trust ([chapter 04](04-permission-model.md)) — which is what
prevents a cloned repository from registering a plugin merely by being opened.

## Subagents: extension by delegation

The last seam is different in kind. Rather than adding a tool, **spawn another
agent**: a subprocess running its own loop, with its own transcript, reporting a
summary back.

This is a context-economics move as much as a capability one. A subagent that
reads forty files and returns a two-paragraph answer has spent its own context
window, not the parent's — the parent pays for the summary, not the search. The
same idea generalizes to *foreign* backends: a subagent need not be terva at
all, since the contract is spawn-with-a-task and return-a-summary.

Two things this design has to get right, both learned by getting them wrong:
a child's spend must be **attributed as delegated** in the parent's accounting,
or it looks exactly like a parent cache miss; and a child's permission posture
must be **derived and fail closed**, because a child that inherits an
unrecognized mode and defaults to permissive is an ungated agent you did not
know you started.

---

*Implementation: extensions, MCP, connectors, hooks, skills and the subagent
host live under `packages/agent` in `extensions/`, `mcp/`, `chat/`, `hooks/`,
`skills/` and `swarm/`. User-facing guides:
[extensions.md](../extensions.md), [mcp.md](../mcp.md),
[connectors.md](../connectors.md), [hooks.md](../hooks.md),
[skills.md](../skills.md), and the tool-surface playbook in
[standard-tools.md](../standard-tools.md).*
