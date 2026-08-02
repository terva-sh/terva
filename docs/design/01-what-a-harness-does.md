# 01 — What a harness does

## The gap a harness fills

A large language model is a function. Text in, text out, no memory between
calls, no hands. On its own it cannot read your file, run your test suite, or
remember what it did five minutes ago.

Modern models can, however, *ask* for those things. Given a list of available
tools — each a name, a description, and a JSON schema for its arguments — a
model can emit a structured request: `read({"path": "src/main.go"})`. It cannot
execute that request. Something has to receive it, decide whether it is allowed,
run it, and hand the result back in a form the model will understand on the next
call.

That something is the **harness**. terva is one; Claude Code, Codex CLI, Gemini
CLI, Crush, aider and Goose are others. The category is young enough that the
name is not settled, but the shape is: a program that turns a stateless
text-completion API into a stateful agent with hands.

The naive version is about forty lines of code:

```
messages = [user_prompt]
loop:
    reply = model.complete(system_prompt, messages, tool_schemas)
    messages.append(reply)
    if reply has no tool calls:
        break
    for each tool call in reply:
        result = execute(call)
        messages.append(result)
```

Everything difficult about a harness is what that sketch omits. It has no
concept of permission, so the model can `rm -rf /`. It grows `messages`
without bound until the provider rejects the request. It re-sends the entire
conversation on every iteration and pays full price each time. It has no way
to show a human what is happening, no way to stop it, no memory across
restarts, and no way to add a capability without editing its source.

## The eight jobs

Filling those gaps is the whole job, and it decomposes into eight
responsibilities. Each is a chapter of this tier or a section of one.

**1. Run the loop.** Assemble a request, stream the response, execute the
tool calls, append the results, repeat — and know when to stop. Sounds
trivial; is not. A turn can span dozens of model calls and hundreds of tool
executions. It must be cancellable mid-flight, resumable after a crash, and
capable of accepting new input from the user while it is still running.
→ [02](02-the-agent-loop.md)

**2. Manage the context budget.** Every model call re-sends the whole
conversation, and the conversation only grows. The window is finite; the
cost is proportional; and providers cache prefixes, which means the *shape*
of what you send matters as much as the size. A harness that ignores caching
can pay ten times what one that respects it pays for identical work.
→ [03](03-context-and-cost.md)

**3. Gate every side effect.** The model is not trusted, cannot be made
trustworthy by prompting, and will occasionally propose something
catastrophic — sometimes because it was confused, sometimes because text it
read told it to. The harness is the only thing standing between a generated
string and your filesystem.
→ [04](04-permission-model.md)

**4. Project the loop to humans and machines.** A terminal UI, a browser
panel, an editor plugin, a chat bot, an automated pipeline. Each needs to
see the same run, and several may need to see the *same* run at the same
time. The tempting design — one loop implementation per front end — is the
one that guarantees they drift apart.
→ [05](05-one-core-many-front-ends.md)

**5. Accept capability from outside.** No harness author can anticipate the
tools a user needs. The extension surface is where a harness either stays
small or becomes a monolith of everyone's special cases.
→ [06](06-extension-model.md)

**6. Persist.** A session must survive the process that created it: to
resume tomorrow, to audit what happened, to replay it for someone else, to
account for what it cost. Persistence is also what makes the loop
*restartable*, which is what makes long autonomous runs viable at all.
→ [07](07-state-and-identity.md)

**7. Report honestly.** Tokens consumed, cache hits and misses, money spent,
which model answered, which rule allowed a command. An agent that spends
your money without an itemized statement is not usable at scale, and the
data is only trustworthy if it is recorded by the engine rather than
reconstructed by a UI.
→ [03](03-context-and-cost.md) and [07](07-state-and-identity.md)

**8. Fail visibly.** Providers rate-limit, models loop, tools hang, context
windows overflow, credentials expire mid-run. Each of these has a correct
recovery, and each has a wrong one that looks like success.
→ [02](02-the-agent-loop.md) and [08](08-lessons.md)

## The vocabulary

These words appear throughout and mean specific things here. Several are
used loosely elsewhere in the industry.

**Turn** — one user prompt and the agent's complete response to it. A turn
may involve one model call or two hundred.

**Step** — one iteration of the loop inside a turn: one model call plus the
execution of whatever tools it requested. Turns are what users perceive;
steps are what costs money.

**Transcript** — the ordered list of messages sent to the model: user
prompts, assistant replies, tool results. It is the agent's entire memory,
and it is re-sent in full on every step.

**Context window** — the provider's hard limit on how large a single request
may be, counted in tokens. Exceeding it is an error, not a truncation.

**Tool** — a capability the model can invoke, described to it as a name, a
prose description, and a JSON schema. The description is prompt engineering
and matters more than most people expect.

**Tool call / tool result** — the model's structured request, and the
harness's reply. Both live in the transcript, which is why noisy tools are
expensive: their output is re-sent on every subsequent step of the turn.

**System prompt** — instructions that precede the transcript and frame the
whole conversation. Stable across a turn by design (see
[03](03-context-and-cost.md)).

**Event** — a typed record of something the loop did: text arrived, a tool
started, a tool finished, an approval is pending, usage was reported. Events
are the single output channel; screens, transcript files, and remote clients
are all just consumers of the same stream.

**Session** — the durable record of a conversation: an append-only file of
events and messages, bucketed by working directory, resumable and
replayable.

**Compaction** — replacing a long transcript with a model-written summary
plus a tail of recent messages, to reclaim context.

**Experience** — which product terva is being at the moment: coding (the
default), chat, or play. It gates which tools, prompts, and skills are
present.

**Carrier** — a transport that connects a front end to the engine: in the
same process, over a socket, or reading a recorded file. See
[05](05-one-core-many-front-ends.md).

**Extension / connector / MCP server / hook / skill** — the five ways
capability arrives from outside the binary. See [06](06-extension-model.md).

## What terva is, specifically

A single statically-linked binary — no runtime, no database, no container —
that implements all eight jobs, ships wired for software work, and is open
to any capability you can attach a tool to. It speaks to every major model
provider behind one interface, and projects one agent loop through a terminal
UI, a browser panel, an editor protocol, chat platforms, an embedding SDK, and
several headless modes.

*(Counts — of providers, tools, packages, protocol methods — live in the
implementation tier, where they carry a commit and a date. This tier avoids
them on purpose: a number in a conceptual document is a number nobody
re-measures.)*

The design commitments that follow from this are stated as
[decision records](../README.md#engineering-records-development-repository-only)
in the development repository, and the load-bearing ones are:

- **A small core with no knowledge of its surroundings.** The engine knows
  about models, messages, tools and events. It does not know what a terminal
  is, what a file is, or that extensions exist. Everything else plugs into it.
- **One binary, no CGO, few dependencies.** Cross-compilation and a
  copy-one-file install are worth real constraints elsewhere.
- **Out-of-process by default for anything foreign.** A plugin that crashes
  should take down a subprocess, not the agent.
- **Human-readable, append-only session files.** Not a database. You can
  `tail` a running agent's transcript.
- **Typed contracts at every seam**, versioned and test-backed, so that many
  front ends over one core stays a projection rather than a divergence.

The next chapter starts where every harness starts: the loop.
