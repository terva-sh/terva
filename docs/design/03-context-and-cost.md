# 03 — Context and cost

Everything in this chapter follows from one fact established in
[chapter 02](02-the-agent-loop.md): **the entire transcript is re-sent on every
step.** A forty-step turn does not send forty messages. It sends the
conversation forty times, each time slightly longer than the last.

That makes the context window two things at once — a hard capacity limit, and a
recurring bill.

## What is in a request

Every model call is assembled from four persistent pieces and two that ride
alongside without ever joining the conversation:

| Piece | Lifetime | Notes |
|---|---|---|
| **System prompt** | Pinned for a whole turn | Identity, project instructions, the skill manifest, the working directory and date |
| **Transcript** | Grows forever, until compacted | User prompts, assistant replies, tool results |
| **Tool schemas** | Pinned for a whole turn | Name, description and JSON schema per available tool |
| **Reasoning setting** | Per request | The normalized thinking level, where the model has one |
| *Ephemeral tail* | **This request only** | Host-assembled notes — a live task card, a context-pressure warning, a stall nudge |
| *Cache key* | Per conversation | A stable identifier used by some providers for cache routing |

The last two are the interesting ones, and both exist because of caching.

## The prefix cache is the whole game

Providers cache request prefixes. If a request begins with the same bytes as a
recent one, the shared prefix is served from cache at a small fraction of the
input price — often a tenth. Because a harness re-sends a growing conversation,
essentially every request *should* be a near-total cache hit: step 40 is step
39 plus a bit.

Getting that means honoring one rule, which sounds trivial and is not:

> **The bytes at the front of the request must not change.**

Concretely, across the steps of a turn: the system prompt is pinned, the tool
set is pinned, nothing rewrites an earlier message, and results are only ever
appended. Once anything near the front changes, every byte behind it is
uncached, and the whole conversation is re-read at full price.

This is why the ephemeral tail exists. A great deal of what a harness wants to
tell the model is per-request and transient: *you are at 78% of your context
window*, *here is the current state of the task card*, *you have now called this
tool five times with the same result*. Splicing any of that into the system
prompt would invalidate the prefix on every single step. Instead it is appended
*after* the cached region, as a trailing block — so the cached prefix still
hits, and only the small tail is re-processed.

The corollary is worth stating plainly, because harnesses get it wrong
routinely: **anything a harness injects into the conversation is not free, and
its cost is not the size of the injection.** It is the size of everything behind
the injection point.

### The providers do not agree on how

Two families, and the difference determines what a harness can control:

**Explicit breakpoints (Anthropic, and the Bedrock equivalent).** The client
marks specific positions as cache boundaries — a small, fixed budget of them.
terva spends its four on the identity block, the system prompt, the last tool
definition, and the last user message. This is precise, and it is why cache hit
rates on these providers sit in the 90s.

**Automatic longest-prefix (OpenAI and compatible).** There is no equivalent of
an explicit breakpoint; the provider matches the longest prefix it recognizes.
The only client-side lever is a **cache key**, which affects *routing* rather
than content: it decides which cache shard a request lands in. This matters more
than it sounds. Without a stable key, concurrent conversations on one account —
a coordinator and the subagents it spawned, say — hash into overlapping shards
and evict each other. terva sends the session's identifier as that key.

On these providers, the only real lever is **invalidating less often**.

### One provider behavior worth knowing about

The OpenAI Responses backend discards prior-turn reasoning items when it
assembles a prompt. The effect is that *every* user message — typed, queued, or
injected by the harness — reclassifies all reasoning items behind it at once,
so the canonicalized prompt diverges from the cache at the first reasoning item
and the next call re-reads essentially the whole conversation.

This is inherent to how encrypted reasoning is replayed, not something a client
can serialize around. The practical upshot: on reasoning models, **the frequency
of injected messages is a first-order cost knob**, and a harness that helpfully
nudges the model every turn can be the most expensive thing in the room.

### Reading a cache miss

When cache performance collapses, the diagnostic question is *where* the miss
landed, and there are only two answers:

- **The miss is at the very front**, on the system prompt and tool schemas.
  Those bytes did not change — so this is a **routing** problem. Something about
  which cache shard the request reached, not what was in it.
- **The miss is somewhere in the middle of the transcript.** Then the bytes
  really did change, and something rewrote history: a tool set that changed
  shape, a system prompt rebuilt mid-session, a resumed session that
  reconstructed its prefix differently than the original run did.

Distinguishing the two requires knowing the size of the front matter — the
system prompt plus tool schemas — as a number. If the miss is *exactly* that
size, it is routing. If it is larger, it is bytes. That measurement has paid for
itself more than once; see [chapter 08](08-lessons.md).

## Reclaiming the window: compaction

Caching controls cost. It does nothing about capacity — the transcript still
grows, and the window is finite. **Compaction** is the valve: ask the model to
summarize the conversation, then replace the transcript with that summary plus
a tail of recent messages.

terva watches context use and fires automatically past a high-water mark, at up
to three places:

- **Before a turn**, when the transcript is already over the line — this is
  what covers a resumed session.
- **After a turn**, while idle, so the next prompt does not pay the
  summarization latency.
- **Between the steps of a turn.** The one people forget. A single agentic turn
  can run long enough to blow the window without ever reaching a turn boundary,
  so a harness that only checks at turn boundaries has a check that never runs
  on exactly the turns that need it.

Mid-turn compaction is the dangerous variant, and it gets a different
summarization prompt for a specific reason: the agent is *mid-task*. If the
summary omits that a file was already written or a command already run, the
resuming agent repeats the side effect. So the mid-turn prompt demands an
explicit ledger of actions already taken — files written, commands run, messages
sent — and one confirmed sighting of a summary that omits an executed side
effect is the standing trigger to turn the feature off.

Two smaller pieces complete the picture. Well before the valve fires, the model
itself is warned each step through the ephemeral tail, so it can wrap up,
economize, or delegate remaining large reads to a subagent rather than being
summarized mid-thought. And if the provider rejects a request for size anyway,
that rejection triggers one compact-and-retry rather than failing the turn.

How much of this runs is configurable — all of it, turn boundaries only, or
nothing — and the setting is read live, so changing it applies to the session
already running.

### What compaction costs

Compaction is a real model call over the entire transcript, so it is expensive,
and it *destroys the cache prefix by construction* — the transcript it produces
shares no bytes with the one it replaced. This is the correct trade at the
window limit and a bad one before it, which is the whole reason the threshold is
high rather than eager.

It is also lossy in a way summarization always is. The mitigation is not a
better prompt; it is that the full transcript remains on disk. The compaction
point is recorded as a checkpoint in the session file, the messages behind it
are still there, and they can be revealed.

## Keeping the front matter stable

Two mechanisms guard the invariant that makes all of this work.

**The prefix guard.** The stable front of a request — model identity, system
prompt, tool set — is fingerprinted, and when it changes between turns the
harness notices and can say so. This is an instrument rather than a fix: it does
not prevent divergence, it *names which rung diverged*, which turns a
four-figure mystery into a one-line diagnosis. It is also blind across a process
restart, which is precisely where one of our worst cache regressions lived.

**Deliberate pinning.** Anything that would change the front matter mid-turn is
deferred to a turn boundary: a model swap, a tool set change, a system prompt
rebuild. The rule is not "never change these" — it is "change them at a
boundary, all at once, and record that you did."

## Accounting

Every model call reports usage: input tokens, output tokens, and — critically —
cache reads and cache writes separately. terva records these as rows in the
session file at the moment they happen, timestamped, alongside the model that
produced them.

Recording at the source rather than reconstructing later matters for two
reasons. It survives the UI: any reader can compute spend from the file without
the front end that was attached at the time. And it can attribute correctly —
when a subagent spends money, that spend is marked as delegated, because
otherwise a child's tokens land in the parent's file looking exactly like a
parent cache miss, and you spend a day debugging a cache problem you do not
have.

The generalization, learned the hard way: **an accounting record needs a field
for every question its readers will ask, and the readers arrive later than the
record does.**

---

*Implementation: request assembly, compaction and the prefix guard live in
`packages/core`; caching, breakpoints and usage parsing live in
`packages/provider`. Practical guidance for a harness that is not terva is in
[practices/context-economy](../practices/02-context-economy.md); the user-facing
description of what lands in your context each turn is in
[context-construction.md](../context-construction.md).*
