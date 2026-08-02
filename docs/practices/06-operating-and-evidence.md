# 06 — Operating and evidence

A harness is a system whose central component is nondeterministic, expensive to
invoke, and changes underneath you when a vendor ships a new model. This page is
about the parts that make it operable anyway: what you persist, what you record,
and how you test something you cannot fully reproduce.

---

## Persistence

### Append-only files beat a database, for a single-user harness

**Contested.** Crush, OpenCode and Goose use SQLite. We and maki use
newline-delimited JSON files. Codex uses a compressed-JSONL-plus-index hybrid.
Everyone is right for their own scale, so here is the actual trade:

**What files buy.** You can `tail -f` a live agent — a debugging tool nobody has
to build. A process killed mid-write loses at most the last line. No dependency,
no schema migration, no lock contention. Diffable, greppable, mailable.

**What a database buys.** Queryability across many sessions, full-text search,
write-ahead logging, concurrent writers.

**The deciding question:** how many sessions must you query *across*, and by
whom? For a single-user harness the answer is "rarely, by one person," and the
inspection ergonomics of a plain file dominate. If you are building a
multi-tenant service, invert this.

The hybrid is real if size bites: compress the record, keep an index beside it.

### Version the format and answer the unknown-record question

**Scarred.** Ours was a switch over record kinds with no default arm. An
unrecognized record was silently skipped, and the transcript folded *wrong*
rather than failing — a newer version's file read by an older binary produced a
conversation missing pieces, with no error anywhere.

Refusing is fine. Warning is fine. Silently continuing with partial data is not,
because the reader has no way to know it happened.

### Repair on resume, do not fail

**Converged.** A session interrupted between a tool call and its result leaves an
unmatched call that providers reject outright. Synthesize stub results to close
them. The alternative is that an interrupted session is unresumable, which is
exactly the session you most want to resume.

### Errors go beside the transcript, not in it

**Converged.** A redacted sidecar file keeps diagnostics out of the conversation
record. Two benefits: the transcript stays a clean model-visible history, and
the error file can hold things the transcript must never contain.

### A repeatedly-written record is a timeline, not a value

**Scarred.** Session metadata written several times over a session's life is a
*sequence*. A reader that takes only the first row, or only the last, silently
drops everything superseded — ours lost fourteen fields that way. Decide
explicitly whether each field is first-wins, last-wins, or accumulated, and write
the reader to that decision.

---

## Accounting

### Record at the source, in the file

**Scarred.** Usage belongs in the session record at the moment the call returns,
not computed by whatever UI happened to be attached. Two consequences: any
reader can compute spend without that UI, and the numbers survive a front end
being rewritten.

### Record cache reads and writes separately

**Measured.** Input tokens, output tokens, **cache read tokens**, **cache write
tokens** — four numbers, not two. Without the split you cannot tell an expensive
session from a badly-cached one, and those have completely different fixes.

Stamp each row with the model that produced it and the time. Model, because a
session can swap models mid-run and per-model rates differ by an order of
magnitude. Time, because the questions you will eventually ask are about
*when* — and we shipped rows without timestamps and had to add them under
pressure.

### Give the record a field for every question a reader will ask

**Scarred.** Delegated spend, unmarked, looks exactly like a parent cache miss.
Three separate readers ended up needing that one flag. Attribution and
timestamps are cheap to write and impossible to reconstruct — write them
speculatively.

### Price data is a liability without a staleness signal

**Scarred.** A hand-maintained catalog of model prices that is the sole input to
cost accounting, with no marker for when a row was last verified, produces
confidently wrong numbers. If the catalog is partly machine-generated, mark it
as generated; if rows expire, say when.

---

## Observability

Three instruments have repaid their cost many times over. All are cheap.

**A dump-the-prompt mode.** Print exactly what would be sent, or just its sizes,
without calling the provider. This is how you learn your request floor
([context economy](02-context-economy.md#measure-the-floor-then-a-miss-tells-you-which-problem-you-have)),
and it needs no credentials and costs no money — which means anyone can run it,
including in CI.

**A prefix digest ladder.** Fingerprint the stable front of the request in
layers — identity, system prompt, tool set — rather than as one hash, so a
change names *which rung* moved. One hash tells you something changed; a ladder
tells you what.

**A per-turn context breakdown.** What fraction of this request was system
prompt, tools, transcript, injected tail. Users ask "why is this expensive" and
this is the answer.

---

## Testing a nondeterministic system

### Test the harness, not the model

**Converged.** Model behavior is not a property you can pin in a unit test, and
trying produces a suite that fails when a vendor ships an update. What *is*
testable is everything around it: request assembly, event fan-out, gate
decisions, session folding, protocol frames, rendering.

Drive those with recorded or synthesized provider responses. The model is an
input to your system, not part of it.

### Golden frames for every protocol

**Converged.** For a wire protocol, record canonical frames and compare. This
catches the two failure modes that matter — an unintended change to a frame
you emit, and a frame you stopped emitting — and it makes compatibility a
property you can point at rather than assert.

### Render the terminal in a terminal emulator

**Scarred.** For a TUI, assert against a real VT emulator, not against the
strings you meant to print. Two failures we shipped that only an emulator can
catch:

- **A predicate over a whole pane can match chrome.** Ours found its sentinel
  string inside a spinner phrase and passed for two weeks past go-live.
- **A predicate cannot see a line that is too wide.** The content was correct
  and clipped.

Scope the predicate to the region you mean, and assert the *rule* rather than
the specific line. And look at a rendered pane with your eyes before shipping —
a headless emulator makes that a command, not a ceremony.

### Prove the test fails without the fix

**Scarred.** A test written alongside a fix has not demonstrated anything until
you have seen it fail with the fix removed. Neuter the fix, run the test, watch
it go red, restore.

The trap: a probe that silently does not apply reads exactly like a passing
test. Two of four automated neuters in one session no-opped and we nearly
concluded the tests were worthless. Copy the file aside first, then **print the
neutered region** so you can see the change landed.

### A skip is not a pass

**Scarred.** A test that declines to run because its fixture was insufficient
reports as a skip, and a skip in aggregate output is indistinguishable from a
pass at a glance. Make insufficient fixtures a failure.

### Do not call a red gate a flake without a high iteration count

**Scarred.** `-count=50` proves nothing. In one case roughly ninety clean runs
had "proved" a flake before `-count=500` found a genuine race in seconds. Run
the race detector in CI, not just locally.

Read the duration on the failing line — a failure that took milliseconds when
the test normally takes seconds is a different bug than one that timed out.

### Test through the single production caller

**Scarred.** When a unit has exactly one production caller, the test that
matters goes through the caller. We had five tests over a helper and all five
missed an interaction bug, because every one of them called the helper directly
and the defect lived in how the caller composed it.

### Write completeness guards that enroll themselves

**Scarred.** The recurring shape of a useless test is a hand-maintained list of
things to check: it passes forever, because the failure mode is somebody adding
a thing and not adding it to the list.

The pattern that works: the test **derives the full set from the source**, and
requires every member to be either handled or explicitly excused with a written
reason — and a *stale* excuse fails too. Write it empty and let its first run be
the audit. Ours have repeatedly found real defects on that first run.

Prefer, where you can, a shape that removes the need: a table the production
code *consults* cannot drift from itself, where a chain of conditions mirrored
by a test always can. And when the roster those tables check against becomes the
single point of silent failure, guard the roster.

### Deleting dead code can delete live coverage

**Scarred.** A removed dead path carried the only test anywhere for a gate that
was very much alive. Before deleting, check what the deleted code was the only
witness for.

The related trap: a dead duplicate is a *destination* for work that then
disappears. We implemented a feature correctly in an unreachable twin and never
ran it. The tell is a comment in one copy naming the other.

---

## Release and operations

### Cost-gate your own CI

**Measured.** A harness's test suite can invoke models, and the bill is easy to
miss. Keep provider-touching tests behind an explicit tag, make the default
suite free to run, and state what a full run costs.

### Conditional compilation hides broken code

**Scarred.** Three broken references survived every ordinary build and the
standard vet pass because they lived behind build tags, and surfaced only in the
full CI matrix. "It compiles" is scoped to the configuration you compiled.

### Report what you skipped, loudly

**Scarred.** A gate that silently skips when a precondition is missing is a gate
that is not running, and it will read as green. Make the skip visible in the
output.

### A guard that lists what it checks cannot fail when something is added

Worth repeating in an operations context, because release checklists have the
same shape as tests. Derive the list; do not maintain it.
