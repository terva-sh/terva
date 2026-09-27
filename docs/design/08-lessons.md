# 08: Lessons

Everything here was learned by getting it wrong first. Each entry names the
mistake, what it cost, and the rule we now hold ourselves to. They are grouped
by the kind of thinking that produced the error, because that is what
transfers: the specific bugs will not recur, and the reasoning that caused them
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
the client, reviewed, merged, and released, and it never ran once. The server
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
hooks), so both tests hit a `skip`, and a skip in aggregate output is
indistinguishable from a pass at a glance.

> **Rule.** A test that can decline to run must fail instead. If the fixture is
> insufficient, that is a defect in the fixture.

### A proxy assertion can pass for the wrong reason

A test pinned a gap by asserting that an engine *pointer* was unchanged across
a trust flip. The fix made the code re-derive its configuration in place, and
the assertion still passed, because the object was mutated rather than
reallocated. A pointer-identity check cannot distinguish "never re-derived"
from "re-derived without reallocating."

> **Rule.** Assert the behavior, not the mechanism you expect to produce it.
> Then verify the assertion fails when the fix is removed.

### A truncated pipeline reports a subset with the confidence of a total

Four separate times in one workstream, a measurement ending in `head` produced a
number that was quoted as complete. A "25 members" figure was really 31, because
the pipeline had a `head -25`. A census claiming nothing outside a package used
two identifiers missed a production caller for the same reason. Each wrong
number supported a plan built on it.

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

The same error recurred four times in one workstream, including a cluster
described as three files that turned out to be two files plus an unrelated
third that referenced neither.

> **Rule.** Before acting on a "these belong together" claim, measure the
> graph. Grouping by subject is a documentation decision; grouping by
> dependency is an architectural one.

### Fan-in measures the cost of moving, not the ability to move

Ranking files by how many other files depend on them produced an ordering that
was exactly backwards for deciding what could be extracted. The most-depended-on
cluster was the *most* liftable, because it imported almost nothing, while a
low-fan-in cluster could not move at all, because it depended on the two types
at the center of the package.

> **Rule.** Extractability is a question about a component's *outbound* edges.
> Fan-in tells you how much churn a move will cause, which is a different
> question and answers it in the opposite direction.

### Half of an apparent coupling can be one misplaced helper

A file appeared to be a hub with eight dependents. Six of them needed only four
small helper functions sitting at the bottom of it, about forty-six lines.
Moving those to their own home dropped its fan-in from eight to two, at
essentially no risk, and changed which follow-on work was worth doing.

> **Rule.** Look for the cheap measurement-changing move before the expensive
> structural one, and re-measure after. The graph you planned against may not
> be the graph you are now standing in.

### Instrumentation cannot answer a question about production

To find out whether a shipping binary could reach a suspicious branch, a
recording probe was added and the test suite run. It reported hundreds of
distinct call sites and zero production origins, which proves nothing, because
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

Nothing in the shipping binary could reach that branch, which is exactly why
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
"secret-looking" field names leaked a token twice: once from a field the
filter did not anticipate, once from a plugin's own configuration whose naming
the filter had never seen.

> **Rule.** Print the *shape*, never the values. A denylist over field names is
> a guess, and its failure direction is disclosure.

---

## D. Deleting things

### Deleting dead code can delete the only assertion about live code

A dead code path was removed. It carried the only test anywhere for a spawn
gate that was very much alive. The gate had never moved, but its sole coverage
rode the removed path out of the tree.

> **Rule.** Before deleting, check what the deleted code was the only witness
> for. Coverage lives in files, and files get deleted for reasons unrelated to
> what they cover.

### A dead twin swallows patches

Two near-identical implementations existed, one of them unreachable. A feature
was implemented correctly in the unreachable one, and never ran. It was
discovered only when the dead twin was finally deleted and the feature had to
be ported to make the deletion behavior-preserving.

> **Rule.** Duplication is not only a maintenance cost. It is a *destination*
> for work that then disappears. The tell is a comment in one copy naming the
> other.

### Sometimes the right outcome of a refactor is retiring a guard

Two tests existed to catch bug classes that a later refactor made structurally
impossible: a dispatch table cannot have the ambiguity a switch could, and a
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
every member to be either handled or explicitly excused with a written reason,
and a *stale* excuse fails too. Write it empty and let its first run be the
audit.

> **Rule.** A completeness guard must derive its subject list from the code, not
> from itself.

### A table the code consults cannot drift from itself

A per-mode property was encoded as a chain of boolean conditions, with a
hand-maintained mirror in a test file asserting the chain's behavior. The mirror
was the only way to pin a chain, and a mirror is a second source of truth by
construction.

Moving the property into a table that the production code *reads* eliminated
the drift class entirely, and changed what the guards could ask: exhaustiveness
became a real question, and the agreement test became a genuinely different one.

> **Rule.** Prefer a table the code consults to a chain the tests mirror. Then
> guard the roster the tables are checked against, because that roster is now
> the single point of silent failure, where a missing entry would be checked by
> nothing while every table kept passing.

### One production caller means: test through the caller

A capability was merged from two directions and correctly stood down from a
third, and the interaction left the session with no memory tool at all: two
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
failing call forty-five times over, is not confused. It is priming itself on its
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
rate went to zero, twice, because the first fix addressed the symptom.

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
was the *pre-turn* compaction path, and the sibling path a few lines away
already had the correct handling, with the rationale written in a comment.

> **Rule.** When a mechanism runs at several points in a lifecycle, enumerate
> the points and check each. The argument for the fix is often already written
> at a sibling site.

### If a surface prints an identifier, it must accept that identifier back

A retrieval surface displayed keys in one form and accepted them in another.
The failure mode was silence: a lookup that returned nothing, indistinguishable
from a lookup with no matches.

> **Rule.** Round-trip every identifier a user or a model can see. And when a
> lookup can legitimately return nothing, make "not found" distinguishable from
> "found nothing."

---

## G. Extracting an engine

In September 2026 terva split its agent core out of the harness, over three days of
pull requests, and then shipped the split in a release with a promise about what stays
stable. None of the defects below reached a public release. The two Windows stops cost
two version numbers. The generalized versions, with their
evidence tags, are in [practices 07](../practices/07-extracting-an-engine.md).

### Every feature that left needed a seam, and some seams did not need to stay

Feature after feature moved out of the engine into components, and almost every move
needed a public hook the engine did not have. There were eight in all: a report of which
ephemeral blocks a request carried, a gate between tool steps, an observer of each
request sent, a way to write a diagnostic row, and four more. The context-pressure note
needed none, because it reused the hook the first move had added. The rule that decided each case was that a hook terva needed and a generic
host could not reach meant a missing seam, and the repair was the seam.

Three of those hooks were later made private again. Once components attached through
one entry point, which asks a component which capability interfaces it implements, a
seam for a component no longer had to be a method on the engine. A ninth seam, to order
components' prompt segments, was proposed and declined as new API for no measured
problem.

> **Rule.** Keep a generic host that uses only the public API, and let it find the
> missing seams. Then attach components through one point, so a new seam is a new
> interface rather than a new method.

### An observer that moved changed the order of the rows

The cache-cliff detector used to run inside the engine, after the engine recorded a
request's usage. As a dispatch observer it heard each stream event before the engine
acted on it, so it wrote its row ahead of the usage row it depends on. Review caught it
before merge. The observer now fires after the engine acts, and a store test asserts
that the first cliff row follows the usage rows. A probe with the old order fails it.

> **Rule.** When inline code becomes an observer, its position in the sequence is part
> of the contract. Test the order of what it writes.

### The default was load-bearing in thirteen tests

The global model catalog left in three pull requests: a seam that changed nothing, then
terva holding its own catalog, then the global's removal. The third step failed exactly
thirteen wire tests, and each was a test whose client had leaned on the global without
saying so. Moving the permission model later took the same three steps. It was planned as two
pull requests and became three when the reviewer could not read the second one whole.

> **Rule.** Remove a global by expanding, migrating, then contracting, each step merged
> green. Expect the contract step to find the hidden dependents. That is the step doing
> its job.

### The census counted less than was there

The release census, which counts API breaks, read each package directory recursively.
A subpackage's `New` therefore masked the removal of the parent's `New`. Against the
previous release it reported 594 removals, and the fixed census reports 615: twenty-one
removals were hidden. Nobody saw a failure. The bug surfaced while the per-directory
snapshot was being designed. A second tool had the same shape: the check that a stable
signature names only stable symbols passed with nothing to check, because a trailing
comment on the module line hid the module path from it.

> **Rule.** A counting tool whose bug makes it count less still looks healthy. Give it a
> case with a known answer, and make that case fail when the count is short.

### Two releases stopped at the Windows job

The fork's own CI runs Linux containers only. Windows runs in the public mirror's CI, at
go-live, before the tag. v0.139.0 stopped there on five tests: `HOME` against
`USERPROFILE`, a path quoted with `%q`, and a path separator in golden output. v0.139.1
stopped on the embedded skill files. They had no line-ending pin, so a Windows checkout
gave them CRLF, and the prompt goldens failed on output that printed identical. Neither
version reached the public tag, and four earlier releases had stopped at the same job.

> **Rule.** A platform that only the last gate runs is a platform you test at release
> time. Pin line endings for every file you compare byte for byte, and run each
> platform before the gate that publishes.

### A pipe that had been judged safe

A CI step read the newest tag with `git tag | head -1` under `pipefail`. It failed 93 times
in 100 in the CI image and not once in 200 on a workstation, exiting 141 with no output.
An earlier review had rated the same shape latent, because a tag list is far smaller than
a pipe buffer. That reasoning was wrong. Once `head` has its line and exits, the next
write git makes takes `SIGPIPE`, whatever the buffer size. Asking git for one ref failed
none of a hundred runs.

> **Rule.** Under `pipefail`, do not let a consumer stop early. Ask the producer for
> fewer lines.

### The reviewer's limits shaped the pull requests

The automated reviewer gives up when the diff, the discussion and the attached files
pass 256 KiB. That stopped three extraction pull requests: one was split, and two were
reviewed locally instead. A declined finding came back in new words on a later run, and
a disposition written only in a ticket came back because the reviewer reads the pull
request. The pull request that wrote the first release's migration notes merged without a model
review after three provider failures, on the maintainer's word.

> **Rule.** Size pull requests to the reviewer, record each disposition where the
> reviewer reads, and decide before you need it what happens when the reviewer is
> down.

---

## H. Structural tensions we have not resolved

Not lessons but open problems, recorded honestly because a design document that
only lists solved problems is marketing.

- **Mass re-concentrates faster than extraction relieves it.** Every
  decomposition of the last year worked, and the gravity moved rather than
  dissipating. The pattern to confront is that extraction has been moving
  *code*, not *state or ownership*. The engine extraction (section G) is the one
  exception so far: each feature took its state with it into a component. The
  harness's own hubs are unchanged.
- **Three protocol surfaces, three answers to the approval question.** The
  control plane, the RPC wire, and the editor protocol share an event
  vocabulary and diverge on approvals. Two of the three are standards we do not
  own, which is a real defense and not a complete one.
- **The jail is not a security boundary.** Path and command heuristics raise the
  cost of an accident a great deal and the cost of a determined escape very
  little. OS-level sandboxing is the largest known gap in the design.
- **Hand-maintained data at scale.** A model catalog of hundreds of rows, priced
  by hand, is the sole input to cost accounting and has no staleness signal.
- **Invariants enforced by convention.** Several load-bearing rules are held
  by comments and single tests rather than by types: lock ordering and a
  cross-process display invariant. A third, the assignment without which a host
  ran ungated, became a required constructor argument in the extraction.

The standing agenda that tracks these, with per-subsystem evidence, lives in the
development repository under `docs/architecture/`.
