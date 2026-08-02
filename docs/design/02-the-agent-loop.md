# 02 — The agent loop

The loop is the smallest interesting thing in the system and the hardest to get
right. This chapter walks one turn from prompt to completion, then covers the
four things that can go wrong inside it and what the harness does about each.

## One turn, step by step

A **turn** begins when a user prompt is submitted and ends when the model
produces a reply that asks for no tools. In between it runs **steps**, each of
which is exactly one model call plus the execution of whatever that call
requested.

```
                 ┌──────────────────────────────────────────┐
   prompt ──────▶│  before the turn                         │
                 │   · is the transcript already too large? │
                 │     compact it now                       │
                 │   · did the stable prefix change?        │
                 │     (model swap, tool set change) — warn │
                 └────────────────┬─────────────────────────┘
                                  ▼
                 ┌──────────────────────────────────────────┐
                 │  STEP                                    │
      ┌─────────▶│  1. drain any messages queued mid-turn   │
      │          │  2. assemble the request                 │
      │          │  3. stream the model's reply as events   │
      │          │  4. no tool calls? → turn ends           │
      │          │  5. for each tool call:                  │
      │          │       gate it  ──▶ denied? synthesize a  │
      │          │                    refusal result        │
      │          │       run it                             │
      │          │       append the result to the transcript│
      │          │  6. is this a safe boundary to compact?  │
      └──────────┤  7. loop                                 │
                 └──────────────────────────────────────────┘
```

Four properties of that cycle are worth dwelling on.

**The transcript only grows, and it is re-sent in full every step.** Step 40
sends everything steps 1–39 produced. This is the single fact that drives most
of the design in [chapter 03](03-context-and-cost.md): a tool that returns
50 KB of noise on step 3 is not charged once, it is charged thirty-seven more
times.

**Tool results are messages.** A denied tool call does not throw. It produces a
result message saying it was denied, appended exactly like a successful one, so
the model sees the refusal, understands why, and can propose something else.
The loop never breaks on a permission failure — and an agent that is told "no"
in a form it can read will route around the obstacle, where one that hits an
exception simply stops.

**New user input is accepted mid-turn.** A message submitted while the agent is
working is not dropped and does not interrupt. It is queued, and drained at the
top of the next step, where it enters the transcript as an ordinary user
message. The queue is owned by the daemon, not by the UI, so every connected
client sees the same pending messages — and so that a client disconnecting
cannot lose them.

**Everything is emitted as a typed event.** Text arriving token by token, a
tool starting, a tool finishing, usage being reported, an approval pending, the
turn ending. The loop has exactly one output channel. The terminal renderer,
the browser panel, the session writer on disk, and any remote client are all
consumers of that same stream — which is what makes them projections rather
than reimplementations ([chapter 05](05-one-core-many-front-ends.md)).

## When does a turn stop?

Four ways, in rough order of how often they fire:

1. **The model stops asking for tools.** The intended path.
2. **The step budget is exhausted.** A configurable cap on steps per turn. The
   interactive default is unlimited — a human is watching and can interrupt —
   while the embedding SDK defaults to a bounded number, because nobody is.
3. **Cancellation.** The user interrupts. In-flight tool subprocesses are
   killed as a process group, not just signalled, so a `bash` call that spawned
   children does not leave orphans behind.
4. **An unrecoverable error.** Recoverable ones do not stop the turn; see below.

## Four things that go wrong

### The context window overflows

The transcript grows past what the provider accepts. Because this is
predictable, the harness watches for it rather than waiting for the rejection:
crossing a high-water mark triggers **compaction**, which replaces the
transcript with a summary plus a recent tail. Compaction can fire before a turn
(covering resumes), after one (so the next prompt does not pay the
summarization latency), or at the boundary *between steps* — which matters
because a single long agentic turn can blow the window without ever reaching a
turn boundary where anyone was checking.

If the provider rejects the request anyway, that is not a failure: it triggers
one compact-and-retry. [Chapter 03](03-context-and-cost.md) covers the
mechanics and the trade-offs.

### The model gets stuck

Models loop. Not just small ones — a frontier model that finds a tool whose
failure it cannot parse will call it identically forty-five times in a row,
narrating the correct diagnosis each time. Prose cannot break this: the model is
priming itself on its own repeated output, and adding another sentence of
context is more of exactly what is not working.

terva detects this with a **stall tracker** that watches the (call, result)
pairs of a turn on two independent axes:

- **Spin** — the same tool, the same canonical arguments, the *same result*.
  This is redundant work: a call that returned what the model was already
  holding.
- **Error churn** — the same tool and the same normalized error, regardless of
  arguments. This is the death loop, where the model varies its input each time
  and gets an identical failure.

Both axes are needed and neither subsumes the other. Keying on arguments alone
misflags a correct batch loop that repeats a byte-identical query once per
batch — each call returns something different because the previous mutation
removed that batch from the matching set. Hashing tool, arguments and error
together would never trip on the death loop it targets, because the arguments
vary. Getting this wrong is not free: an incorrect nudge asserts something false
("you already have this result"), and the model spends a turn rebutting it.

When a loop trips, the response is a **ladder**, not a single action:

1. **A nudge** — a one-turn note riding alongside the request, naming the
   repetition. Cheap, and often enough.
2. **A different answer from the tool.** The one reliable way to break
   self-priming is for the environment to change: a tool that says *no* is a
   tool a model can recover from. In the origin case for this feature, the same
   stuck session recovered instantly from a different tool the moment that tool
   returned a real error.
3. **Escalation** — offer to hand the live session to a stronger model to
   finish the stuck step. The engine decides *when*; the host owns the swap,
   because only the host can resolve a provider and a credential. Consent is
   asked for explicitly, since escalating may send a local transcript to a
   remote provider. Every failure path here is non-fatal: no target, a declined
   offer, or a failed swap all leave the current model in place and the turn
   running.

The detector is deliberately generic — no tool is special-cased — because the
tool that traps a model is whichever one it happened to be holding.

### The provider fails

Rate limits, transient network errors, expired credentials, a model that has
been retired. Most of these are retried inside the provider layer with backoff.
Two are special:

- **A recoverable error the retry cannot fix** surfaces as a **rescue** — an
  offer to swap models and continue rather than lose the turn.
- **An in-stream failure**, where the response headers arrived and the
  connection then died, is *not* covered by ordinary request retry, because
  from the transport's perspective the request succeeded. Handling this
  separately was a real bug we shipped ([chapter 08](08-lessons.md)).

### Work is lost at a boundary

The subtle class. A message submitted at the instant a compaction begins, a
result arriving as a turn is cancelled, an approval answered after the client
that asked disconnected. These are not exotic — they are what every user hits
eventually — and the pattern that catches them is to make the boundary an
explicit, named state rather than an implicit gap between two operations.

## What "one prompt" can actually mean

Because tool results feed back into the transcript, a single prompt can drive
an arbitrarily long autonomous run. A turn of two hundred steps that reads
forty files, runs the test suite six times, and edits twenty is ordinary. This
is the capability that makes harnesses useful and simultaneously the reason
every other chapter exists: at that scale, an ungated tool call is a real
incident, an uncompacted transcript is a hard failure, and an untracked cost is
a surprise invoice.

---

*Implementation: the loop, the event union, the stall tracker and the
escalation seam all live in `packages/core`, which depends on the provider
layer and nothing else — it has no idea what a terminal, a file, or an
extension is. The subsystem walkthrough is in the development repository under
`docs/architecture/`.*
