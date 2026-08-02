# 01 — The loop

The agent loop is forty lines and six months of edge cases. This page is about
the edge cases.

---

## Structure

### Distinguish turns from steps, in your types

**Converged.** A *turn* is one user prompt and everything that follows from it;
a *step* is one model call plus its tool executions. Users perceive turns. Money
is spent per step. Almost every budget, policy and metric you will want later
belongs to one or the other and applying it to the wrong one produces subtly
useless numbers — "average tokens per turn" over a corpus where one turn was two
hundred steps tells you nothing.

Name both in the code. A harness that only has "iterations" will conflate them.

### The step budget default should depend on who is watching

**Converged.** Interactive sessions can run unbounded: a human sees the tool
calls scroll past and can interrupt. Embedded and headless runs should have a
finite cap, because nobody will notice a loop until the invoice does.

This is one setting with two defaults, not a single "safe" number. A cap tuned
for unattended safety makes interactive work feel broken; a cap tuned for
interactive work is an unattended runaway.

### Accept input mid-turn; queue it, do not interrupt

**Scarred.** A long turn is exactly when a user most wants to say "also check
the tests" or "stop doing that." The naive options are both bad: rejecting the
input is hostile, and interrupting to inject it corrupts a partially-executed
step.

Queue it, and drain the queue at the top of the next step, where the message
enters the transcript as an ordinary user message.

Two details that are not optional. **The queue belongs to the engine, not the
UI** — otherwise a disconnecting client takes queued work with it, and two
clients disagree about what is pending. And **every boundary that can run while
a message is queued must drain it.** We lost a message submitted during
pre-turn compaction; the correct handling already existed a few lines away at a
sibling site, with the rationale in a comment.

### Deny by returning a result

**Converged.** When a tool call is refused — by policy, by a hook, by a sandbox
— the correct output is a *tool result message* saying so, appended to the
transcript like any other. Not an exception, not a terminated turn.

The model then sees the refusal, understands the reason, and proposes something
else. This is the single highest-leverage detail in error handling for agents:
the difference between an agent that adapts and one that stops is usually just
whether the failure was expressible in its input channel.

The same applies to tool *errors*. See
[the tool surface](03-tool-surface.md#write-errors-a-model-can-act-on).

### Kill process groups, not processes

**Scarred.** A shell tool that spawns children and is then cancelled leaves the
children running if you signal only the direct child. On Unix, start the command
in its own process group and signal the group.

Users discover this by noticing their machine is still compiling something they
cancelled ten minutes ago.

---

## Stopping a runaway

### Models loop, and it is not a small-model problem

**Scarred, measured.** The origin case for our detector: a small local model
repeated one failing call eighteen times before an operator hand-swapped to a
stronger one. Catching it at the third identical result rather than the
eighteenth would have saved roughly fifteen dispatches and 270,000 tokens.

A later case involved a frontier model making forty-five identical calls while
narrating the correct diagnosis each time.

Budget for this. It is not an exotic failure.

### Detect on two axes, and do not merge them

**Scarred.** Two different loops need two different keys, and each key misses
the other's loop:

- **Spin** — same tool, same canonical arguments, **same result**. This is
  redundant work: a call returning what the model already held.
- **Error churn** — same tool, same normalized error, **regardless of
  arguments**. This is the death loop, where the model varies its input each
  time and gets an identical failure.

Hashing tool, arguments and error *together* never trips on the death loop,
because the arguments vary. That is the exact mistake to avoid.

Two refinements we paid for:

**Include the result in the spin key.** "The same call" cannot be read off the
arguments alone. A bounded batch loop repeats a byte-identical query on purpose,
once per batch, because each preceding mutation removes that batch from the
matching set — ten identical calls, ten different results, correct throughout.
Keying on arguments alone nudged that loop with a claim that was false, and the
model spent a turn rebutting it.

**Drop free-text fields when canonicalizing arguments.** Many tools take a
`reason` or `thought` parameter. Cosmetic prose churn in those fields will
otherwise hide a structural repeat.

The trade this makes, stated plainly: a call whose output varies on its own — a
clock, a growing log — no longer trips the spin axis. Those are the least
harmful repeats available, since each returns information the model did not
have. Catching *that* is a different axis (aimless polling), not a wider reading
of this one.

### Carry the run length across turn boundaries

**Scarred.** A loop that outlives the turn it started in must not be refunded.
If a signature is still recurring at the boundary, carry its count forward so
the ladder resumes at the rung it reached.

### Escalate in rungs, and know that prose is the weakest one

**Scarred.** The response to a detected loop should be a ladder:

1. **Nudge** — a one-turn note naming the repetition, riding alongside the
   request rather than entering the transcript.
2. **A different answer from the environment.** The reliable break. A model
   priming itself on its own output cannot be talked out of it; in the origin
   session the same model in the same context recovered instantly when a
   different tool returned a real error.
3. **A stronger model**, offered explicitly. Ask consent — escalation may send a
   local transcript to a remote provider — and make every failure path
   non-fatal: no target, a declined offer, or a failed swap should leave the
   current model in place and the turn running.

Do not special-case tools in the detector. The tool that traps a model is
whichever one it happened to be holding.

---

## Recovering from provider failure

### Retry covers less than you think

**Scarred.** Ordinary request retry covers failures *before the response headers
arrive*. A stream that dies mid-response looks like success to the transport
layer, so it reaches no retry at all and surfaces as a broken turn.

Handle in-stream transient failure explicitly. It is a separate mechanism, not a
parameter of the first one.

### Offer a model swap instead of losing the turn

**Converged.** For errors a retry cannot fix — a retired model, a hard rate
limit, a context rejection on a small window — the recovery that preserves the
most work is switching models and continuing. The transcript is portable; the
provider is not the session.

### Treat a context-limit rejection as a compaction trigger

**Converged.** A request rejected for size should compact and retry once, not
fail. This is the backstop for the proactive threshold in
[context economy](02-context-economy.md), and it needs to exist because the
threshold is computed from a token estimate and estimates are wrong.

---

## Emitting

### One typed event stream, and everything is a consumer

**Converged.** Every serious harness in the field converged on a single
canonical event/command vocabulary with N front ends over it. Emit typed events
for everything the loop does — text deltas, tool start and finish, usage,
pending approvals, turn boundaries — and make the screen, the transcript file,
and any remote client all *consumers* of that one stream.

The alternative, where the UI renders from one path and the log is written from
another, guarantees that the log and the screen eventually disagree about what
happened. That divergence is discovered during an incident.

### Model interactions as protocol messages, not UI callbacks

**Converged.** Approval requests, permission escalations, plan updates, session
rollback — the temptation is to implement these as calls into whatever front end
is attached. Typed protocol messages instead means every front end, including
ones that do not exist yet, can drive them.

The harnesses that did this early (Codex's submission/event enums are the
clearest example) can add a front end without touching the engine. The ones that
did not have an approval flow that only the terminal supports.
