# 02 — Context economy

This is the page that pays for itself. Everything here follows from one
property of the loop: **the entire transcript is re-sent on every step.**

A forty-step turn does not send forty messages. It sends the conversation forty
times, each time slightly longer. The context window is therefore two things at
once — a hard capacity limit, and a recurring bill — and most harness cost
questions are really questions about the second.

---

## The prefix cache

### Design for the cache before you design for token count

**Measured.** Providers serve repeated request prefixes from cache at a small
fraction of the input price — commonly around a tenth. Because a harness
re-sends a growing conversation, essentially every request *should* be a
near-total cache hit: step 40 is step 39 plus a bit.

The rule that gets you there is short and unforgiving:

> The bytes at the front of the request must not change.

Concretely, within a turn: pin the system prompt, pin the tool set, never
rewrite an earlier message, only append. The moment anything near the front
changes, everything behind it is uncached and the entire conversation is
re-read at full price.

Optimizing token *count* while ignoring cache *stability* is the common
failure. A change that saves 500 tokens of system prompt and makes it vary per
turn is a large net loss.

### Know which caching model your provider has

**Measured.** Two families, with very different levers:

**Explicit breakpoints.** The client marks positions as cache boundaries, from a
small fixed budget. Spend them deliberately — a reasonable allocation is the
stable identity block, the system prompt, the last tool definition, and the last
user message. This is precise, and it is why hit rates on these providers sit in
the 90s.

**Automatic longest-prefix.** No breakpoint mechanism; the provider matches the
longest prefix it recognizes. The only client-side lever is a **cache key**,
which affects *routing* rather than content — which shard the request lands in.

That routing lever matters more than it sounds. Without a stable key, concurrent
conversations on one account — a coordinator and the subagents it spawned —
hash into overlapping shards and evict each other. Send a stable per-conversation
identifier.

On automatic-prefix providers, the only real lever is **invalidating less
often**. Plan accordingly: features that are cheap on one family can be the
dominant cost on the other.

### Put transient text in an uncached tail, never in the system prompt

**Measured, converged.** A harness constantly wants to tell the model something
per-request: current context pressure, the live state of a task, a stuck-loop
nudge, the current time. Splicing any of it into the system prompt invalidates
the prefix on every single step.

Append it *after* the cached region as a trailing block instead. The cached
prefix still hits; only the small tail is reprocessed.

The general statement is the one to internalize:

> **The cost of injecting text is not the size of the injection. It is the size
> of everything behind the injection point.**

A peer harness rebuilds its system prompt every turn so that a mid-session
memory write takes effect immediately, and busts its prefix cache doing it. The
alternative pattern — **freeze the snapshot at session start, let writes apply
next session** — costs one session of staleness and saves the cache.

### Injected messages are a first-order cost knob on reasoning models

**Measured.** At least one major reasoning backend discards prior-turn reasoning
items when assembling a prompt. The effect: *every* user message — typed,
queued, or injected by the harness — reclassifies all reasoning items behind it
at once, so the canonicalized prompt diverges from the cache at the first
reasoning item and the next call re-reads essentially the whole conversation.

This is inherent to how encrypted reasoning is replayed, not something a client
can serialize around. Practical consequence: on these providers, each
user-message boundary in a large session costs roughly one full-context read. A
harness that helpfully injects a nudge every turn can be the single most
expensive thing in the system.

### Measure the floor, then a miss tells you which problem you have

**Measured, scarred.** The most useful number to have on hand is the size of the
request *floor* — the system prompt plus tool schemas, with no conversation.
Get it from a dump-the-prompt mode; it costs nothing to measure.

With that number, a cache miss becomes diagnostic:

- **Miss lands exactly on the floor** → the bytes did not change. This is a
  **routing** problem: which shard, which key, which account.
- **Miss lands mid-transcript** → the bytes really did change. Something
  rewrote history: a tool set that changed shape, a system prompt rebuilt
  mid-session, a resume that reconstructed the prefix differently.

Without the floor you know only that something is expensive.

### One invalidation bills over many requests

**Scarred.** A cache regression is not one expensive request with one cause. It
is a single invalidation followed by a multi-request window in which the
provider re-establishes its prefix — and that *window* is the cost. Chasing each
expensive request separately produces a different wrong theory each time.

### Anything affecting the prefix is session state

**Scarred, twice.** Session resume restored our conversation faithfully and
dropped the record of which tool groups had been activated. The tool schemas
came back different, the prefix diverged from the original run's, and hit rate
went to zero. Twice — the first fix addressed the symptom.

Persist and restore *everything that shapes the request*, not just the messages:
active tool set, model, system prompt inputs, reasoning level.

One shape detail from the fix: write that state as a **replacement**, never a
union with what was already there. A union cannot represent "fewer than
before," which is exactly the case that arises.

### Instrument the prefix

**Measured.** Fingerprint the stable front of the request — model identity,
system prompt, tool set — as a *ladder* of digests rather than one hash, so
that when it changes the instrument names *which rung* changed. This turns a
four-figure mystery into a one-line diagnosis.

Two caveats from ours: it is an instrument, not a fix, and it is blind across a
process restart — which is precisely where our worst regression lived.

---

## Compaction

### Fire at a high-water mark, at three places

**Converged.** Watch context use and compact past a threshold (we use 85% of the
window). The three places that need a check:

- **Before a turn**, when the transcript is already over — this covers resumed
  sessions.
- **After a turn**, while idle, so the next prompt does not pay the
  summarization latency.
- **Between the steps of a turn.** The one people forget, and the one that
  matters most: a single agentic turn can blow the window without ever reaching
  a turn boundary. A harness that only checks at turn boundaries has a check
  that never runs on exactly the turns that need it.

### Mid-turn compaction needs a different prompt

**Scarred.** The agent is mid-task. If the summary omits that a file was already
written or a command already run, the resuming agent *repeats the side effect*.

The mid-turn summarization prompt must demand an explicit ledger of actions
already taken — files written, commands run, messages sent. And set a standing
tripwire: one confirmed sighting of a summary omitting an executed side effect
is grounds to turn the feature off, not to tune the prompt.

### Warn the model before you compact it

**Converged.** Well below the compaction threshold, tell the model — through the
uncached tail — how much room is left. It can then wrap up, economize, or
delegate remaining large reads to a subagent, rather than being summarized
mid-thought. This is cheaper and better than any improvement to the summary.

### Compaction is expensive and destroys the cache by construction

**Measured.** It is a real model call over the whole transcript, and the
transcript it produces shares no bytes with the one it replaced. Correct at the
window limit; a bad trade before it. Keep the threshold high rather than eager,
and do not treat compaction as a routine cost-control measure — it is a capacity
measure that costs money.

### Keep the full transcript on disk

**Converged.** Compaction is lossy and no prompt fixes that. Record the
compaction point as a checkpoint in the session file, keep the messages behind
it, and support revealing them. The in-memory transcript is a working set; the
file is the record.

---

## Tool output discipline

### A noisy tool is charged once per remaining step

**Measured.** A tool returning 50 KB on step 3 of a 40-step turn is not charged
once. It is charged thirty-seven more times. Tool output is the largest
controllable component of transcript growth in agentic work.

Cap output, and cap it on two axes — bytes *and* lines — because either alone
has a pathological case.

### Offload rather than truncate

**Converged.** When output exceeds the cap, write the full result to a file and
return a stub containing the path and a summary. The model can re-read
selectively if it needs to; nothing is lost, and the transcript stays small.

Plain truncation throws away the tail, which is frequently where the error
message is.

### Return diffs, not files

**Measured.** An edit tool that returns the full post-edit file puts the entire
file in the transcript for the rest of the turn. Return a compact diff with
unchanged regions collapsed. Same information, an order of magnitude less
context, and easier for both the model and a human reviewer to read.

The same reasoning applies to a write tool: echoing back what was just written
is duplicating content the model already has.

---

## Retrieval over inclusion

### Index in context, body out of it

**Converged.** The dominant pattern across the field, in three instances:

- **Skills.** Put a compact manifest — names, one-line descriptions — in the
  prompt; load the body on demand when the model asks for it. A hundred
  available procedures cost roughly a hundred short lines.
- **Keyed context.** Entries with trigger keys whose bodies enter the request
  only when a key matches. Nothing is lost; it is simply not present until
  relevant.
- **Subagents.** A child that reads forty files and returns two paragraphs
  spent its *own* window. The parent pays for the summary, not the search.

### Do not merge keyed context with conversation memory

**Scarred.** They look similar — both inject text that was not in the
conversation — and they answer opposite questions. Keyed context is *authored,
static, retrieved by match*. Conversation memory is *accumulated, mutable,
retrieved by recency or salience*. Sharing an implementation makes both worse.

### Attribute delegated spend at the moment you record it

**Scarred.** A subagent's usage landed in the parent session file unmarked, in
exactly the shape of a parent cache miss. A day went into diagnosing a cache
problem that did not exist.

Three separate readers eventually needed the distinction — which is the
generalizable part. **An accounting record needs a field for every question its
readers will ask, and the readers arrive after the record does.** Attribution
and timestamps are cheap to write and impossible to reconstruct.
