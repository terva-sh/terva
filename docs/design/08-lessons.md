# 08 — Lessons

Everything here was learned by getting it wrong first. Each entry names the
mistake, what it cost, and the rule we now hold ourselves to. They are grouped
by the kind of thinking that produced the error, because that is what
transfers — the specific bugs will not recur, the reasoning that caused them
will.

Generalized, prescriptive versions of several of these live in
[practices](../practices/README.md). This chapter is the case file.

---

## A. Failures that look like success

The recurring theme of this section: **the expensive bugs are not the ones that
crash.** They are the ones where every code path looks correct, the tests pass,
and the behavior is simply absent.

### A feature can ship dark

A control-plane feature was fully implemented on the server, fully requested by
the client, reviewed, merged, and released — and never ran once. The server
never *advertised* it in the connection handshake, so negotiation silently
failed and every client fell back to the old behavior for weeks.

Nothing was broken. The implementation was correct, the request was correct,
and the constant that should have listed the capability was simply absent from
a list. There is no natural reader for an absent list entry.

> **Rule.** When a capability is announced in one place and implemented in
> another, test the *announcement*. The implementation has consumers who will
> notice; the declaration has none.

### A skip reads as a pass

Two tests written to pin a live-trust behavior started with a bare fixture. A
bare fixture builds no permission policy (no rules) and no hook engine (no
hooks), so both tests hit a `skip` — and a skip in aggregate output is
indistinguishable from a pass at a glance.

> **Rule.** A test that can decline to run must fail instead. If the fixture is
> insufficient, that is a defect in the fixture.

### A proxy assertion can pass for the wrong reason

A test pinned a gap by asserting that an engine *pointer* was unchanged across
a trust flip. The fix made the code re-derive its configuration in place — and
the assertion still passed, because the object was mutated rather than
reallocated. A pointer-identity check cannot distinguish "never re-derived"
from "re-derived without reallocating."

> **Rule.** Assert the behavior, not the mechanism you expect to produce it.
> Then verify the assertion fails when the fix is removed.

### A truncated pipeline reports a subset with the confidence of a total

Four separate times in one workstream, a measurement ending in `head` produced
a number that was quoted as complete. A "25 members" figure was really 31 — the
pipeline had a `head -25`. A census claiming nothing outside a package used two
identifiers missed a production caller for the same reason. Each wrong number
supported a plan built on it.

> **Rule.** A count and a sample are different artifacts. If a command can
> truncate, report the count separately from the listing.

### The default build cannot see tag-gated code

Three broken references survived every ordinary build and the standard vet
pass, because they lived behind build tags. They were caught only by the full CI
matrix, minutes into a run.

> **Rule.** "It compiles" is scoped to the configuration you compiled. Any
> conditional compilation needs its configuration in the gate.

---

## B. Prose that did not survive measurement

Every entry here is a description of the system that everyone believed,
including the people who wrote it, that turned out to be false the moment
someone measured.

### Subject matter is not structure

A large package was described for a year as "three separable families." Measured
as an actual symbol graph, the two families called peripheral *were* peripheral
and could leave on their own terms; the one called most separable was
load-bearing. The framing had grouped files by what they were *about*, and that
was read as a claim about how they were *connected*.

The same error recurred four times in one workstream — including a cluster
described as three files that turned out to be two files plus an unrelated
third that referenced neither.

> **Rule.** Before acting on a "these belong together" claim, measure the
> graph. Grouping by subject is a documentation decision; grouping by
> dependency is an architectural one.

### Fan-in measures the cost of moving, not the ability to move

Ranking files by how many other files depend on them produced an ordering that
was exactly backwards for deciding what could be extracted. The most-depended-on
cluster was the *most* liftable — it imported almost nothing — while a
low-fan-in cluster could not move at all, because it depended on the two types
at the center of the package.

> **Rule.** Extractability is a question about a component's *outbound* edges.
> Fan-in tells you how much churn a move will cause, which is a different
> question and answers it in the opposite direction.

### Half of an apparent coupling can be one misplaced helper

A file appeared to be a hub with eight dependents. Six of them needed only four
small helper functions sitting at the bottom of it — about forty-six lines.
Moving those to their own home dropped its fan-in from eight to two, at
essentially no risk, and changed which follow-on work was worth doing.

> **Rule.** Look for the cheap measurement-changing move before the expensive
> structural one, and re-measure after. The graph you planned against may not
> be the graph you are now standing in.

### Instrumentation cannot answer a question about production

To find out whether a shipping binary could reach a suspicious branch, a
recording probe was added and the test suite run. It reported hundreds of
distinct call sites and zero production origins — which proves nothing, because
under test every origin is a test by construction. The question was answered by
enumerating the places that construct the relevant value: five, all of which
set the field.

> **Rule.** "Can production reach this?" is answered by a census of
> construction sites, not by running the suite.

---

## C. Failing open

### Unknown values must fail closed, and the asymmetry justifies it

A permission posture resolved through a chain of boolean conditions that
reached its permissive answer by *falling off the end* rather than by deciding.
An unrecognized run mode therefore got the most permissive posture available.

Nothing in the shipping binary could reach that branch — which is exactly why
it was cheap to fix and would have been expensive to discover later. The
argument for which direction to fail is not symmetric: an unknown mode wrongly
asked to confirm costs one prompt somebody can answer; an unknown mode wrongly
handed full autonomy runs every tool unconfirmed, anywhere on the filesystem,
and is silent about it.

> **Rule.** Where the two error directions have wildly different costs, the
> default is not a matter of taste. Write the asymmetry down next to the
> default so the next person does not re-litigate it.

The same rule shows up in three other places in this system: an unrecognized
tool authority is treated as side-effecting, an unknown session record kind
must not be silently skipped, and a permission rule that cannot be parsed is an
error rather than an omission.

### A redaction filter that guesses fails open

Inspecting a configuration by printing it with a filter that suppresses
"secret-looking" field names leaked a token twice — once from a field the
filter did not anticipate, once from a plugin's own configuration whose naming
the filter had never seen.

> **Rule.** Print the *shape*, never the values. A denylist over field names is
> a guess, and its failure direction is disclosure.

---

## D. Deleting things

### Deleting dead code can delete the only assertion about live code

A dead code path was removed. It carried the only test anywhere for a spawn
gate that was very much alive — the gate had never moved, but its sole coverage
rode the removed path out of the tree.

> **Rule.** Before deleting, check what the deleted code was the only witness
> for. Coverage lives in files, and files get deleted for reasons unrelated to
> what they cover.

### A dead twin swallows patches

Two near-identical implementations existed, one of them unreachable. A feature
was implemented — correctly — in the unreachable one, and never ran. It was
discovered only when the dead twin was finally deleted and the feature had to
be ported to make the deletion behavior-preserving.

> **Rule.** Duplication is not only a maintenance cost. It is a *destination*
> for work that then disappears. The tell is a comment in one copy naming the
> other.

### Sometimes the right outcome of a refactor is retiring a guard

Two tests existed to catch bug classes that a later refactor made structurally
impossible — a dispatch table cannot have the ambiguity a switch could, and a
package boundary enforces what an allow-list was approximating.

> **Rule.** A guard whose bug class the new shape forecloses should be replaced
> by an assertion about the new shape, not carried forward as ballast. But
> *replaced*, not merely deleted.

---

## E. Guards that cannot fail

### A guard that lists its subjects cannot fail when one is added

The recurring shape of a useless test: an explicit list of the things to check,
maintained by hand. It passes forever, because the failure mode is somebody
adding a thing and not adding it to the list.

The remedy that has repeatedly earned its keep is the **self-enrolling
allow-list**: the test discovers the full set from the source, and requires
every member to be either handled or explicitly excused with a written reason
— and a *stale* excuse fails too. Write it empty and let its first run be the
audit.

> **Rule.** A completeness guard must derive its subject list from the code, not
> from itself.

### A table the code consults cannot drift from itself

A per-mode property was encoded as a chain of boolean conditions, with a
hand-maintained mirror in a test file asserting the chain's behavior. The mirror
was the only way to pin a chain — and a mirror is a second source of truth by
construction.

Moving the property into a table that the production code *reads* eliminated
the drift class entirely, and changed what the guards could ask: exhaustiveness
became a real question, and the agreement test became a genuinely different one.

> **Rule.** Prefer a table the code consults to a chain the tests mirror. Then
> guard the roster the tables are checked against — because that roster is now
> the single point of silent failure, where a missing entry would be checked by
> nothing while every table kept passing.

### One production caller means: test through the caller

A capability was merged from two directions and correctly stood down from a
third, and the interaction left the session with no memory tool at all — two
correct halves that cancelled. Five tests covered the code and all five missed
it, because every one of them called the helper directly.

> **Rule.** When a unit has exactly one production caller, the test that matters
> goes through the caller. Testing the unit in isolation tests a configuration
> that does not exist.

---

## F. Agent-specific lessons

The entries above are software engineering. These are particular to harnesses.

### Prose cannot break a self-priming loop

A model that narrates the correct diagnosis and then repeats the identical
failing call — forty-five times — is not confused. It is priming itself on its
own repeated output, and every additional sentence of guidance is more of the
same input.

In the session that established this, the *same model in the same context*
recovered instantly the moment a different tool returned a real error.

> **Rule.** A tool that says no is a tool a model can recover from. Break a loop
> by changing what the environment returns, not by explaining harder.

### The cache is invalidated once and the bill arrives over many requests

A cache-efficiency regression was chased as if each expensive request had its
own cause. There was one terva-side invalidation, followed by a multi-request
window in which the provider re-established its prefix. That *window* was the
cost.

The same investigation produced the diagnostic that now saves the most time:
the floor of any request is the system prompt plus the tool schemas, and if a
cache miss is *exactly* that size the problem is routing; if it is larger, some
bytes genuinely changed.

> **Rule.** Measure the floor. Then a miss tells you which kind of problem you
> have, instead of only that you have one.

### Resuming a session must reconstruct the prefix, not just the messages

Session resume restored the conversation faithfully and dropped the record of
which tool groups had been activated. The tool schemas therefore came back
different, the request prefix diverged from the original run's, and cache hit
rate went to zero — twice, because the first fix addressed the symptom.

The correction has a shape worth remembering: the state is written as a
**replacement** at bind time, never a union with whatever was there, because a
union cannot represent "fewer than before."

> **Rule.** Anything that affects the request prefix is session state and must
> be persisted and restored with the messages. Restoring the conversation is not
> restoring the session.

### Delegated spend must be marked at the moment it is recorded

A subagent's token usage landed in the parent session's file, unmarked, in
exactly the shape of a parent cache miss. Diagnosing it consumed a day and
produced a wrong theory.

Three separate readers ended up needing the distinction, which is the part
worth generalizing.

> **Rule.** An accounting record needs a field for every question its readers
> will ask, and the readers arrive after the record does. Attribution and
> timestamps are cheap to write and impossible to reconstruct.

### The boundaries are where messages are lost

A message submitted during compaction was thrown away. The site that lost it
was the *pre-turn* compaction path — and the sibling path a few lines away
already had the correct handling, with the rationale written in a comment.

> **Rule.** When a mechanism runs at several points in a lifecycle, enumerate
> the points and check each. The argument for the fix is often already written
> at a sibling site.

### If a surface prints an identifier, it must accept that identifier back

A retrieval surface displayed keys in one form and accepted them in another.
The failure mode was silence — a lookup that returned nothing, indistinguishable
from a lookup with no matches.

> **Rule.** Round-trip every identifier a user or a model can see. And when a
> lookup can legitimately return nothing, make "not found" distinguishable from
> "found nothing."

---

## G. Structural tensions we have not resolved

Not lessons — open problems, recorded honestly because a design document that
only lists solved problems is marketing.

- **Mass re-concentrates faster than extraction relieves it.** Every
  decomposition of the last year worked, and the gravity moved rather than
  dissipating. The pattern to confront is that extraction has been moving
  *code*, not *state or ownership*.
- **Three protocol surfaces, three answers to the approval question.** The
  control plane, the RPC wire, and the editor protocol share an event
  vocabulary and diverge on approvals. Two of the three are standards we do not
  own, which is a real defense and not a complete one.
- **The jail is not a security boundary.** Path and command heuristics raise the
  cost of an accident a great deal and the cost of a determined escape very
  little. OS-level sandboxing is the largest known gap in the design.
- **Hand-maintained data at scale.** A model catalog of hundreds of rows, priced
  by hand, is the sole input to cost accounting and has no staleness signal.
- **Invariants enforced by convention.** Several load-bearing rules — lock
  ordering, a required assignment without which a host is ungated, a
  cross-process display invariant — are held by comments and single tests
  rather than by types.

The standing agenda that tracks these, with per-subsystem evidence, lives in the
development repository under `docs/architecture/`.
