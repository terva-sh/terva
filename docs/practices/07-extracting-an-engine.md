# 07: Extracting an engine, and holding its line

A harness that works tends to grow its loop, its tools and its product features into one
package. Splitting the loop back out, so another host can embed it, is a change to every
caller at once. Then you have to say what the new engine promises and keep that promise.

We did this in September 2026. We moved terva's agent core out of the harness in three
days of pull requests, then shipped it in a release with a stability promise. One project
is a small sample. Most entries here are therefore **Measured** rather than
**Converged**, and the case file with the details is
[design 08, section G](../design/08-lessons.md#g-extracting-an-engine).

---

## Moving the code

### Let a generic host find the missing seams

**Measured.** Almost every feature we moved out of the engine needed a public hook the
engine did not have: eight in all, from a report of which ephemeral blocks a request
carried to a gate between tool steps. One feature needed none, because it reused a hook
an earlier move had added.

What decided each case was a small generic host kept beside our own, built from the
public API alone. When our harness needed something that host could not reach, a seam
was missing, and the repair was the seam, not a private back door for the product.

Two refinements followed. Three of the eight hooks were made private again once
components attached through a single entry point that asks a value which capability
interfaces it implements. A seam for a component need not be a method on the engine, and
a new one is then a new interface rather than a new method. And a proposed seam with no
measured problem behind it was declined.

### An observer's position is part of its contract

**Measured.** A detector that ran inline, after the engine recorded a request's usage,
became an observer that fired before the engine acted on each event. It then wrote its
row ahead of the usage row it depends on. Review caught it before merge.

When inline code becomes an observer, write down where in the sequence the observer
fires relative to the engine's own work. Test the order of what it produces, and prove
the test with a probe that restores the old order.

### Make the boundary deny by default

**Measured.** An engine that promises to read no file needs a test that says so. Our
first draft listed banned calls, and review caught the gap: a call nobody thought to ban
passes. The test that shipped denies every exported identifier of the operating-system
packages and admits a short allowlist, each entry with its reason.

The violations that existed on day one went into a baseline that may only shrink. It
started at 58 lines covering 130 references and reached zero. One gap stayed open for
two days: a pull request could grow the baseline beside the call it excused. A CI job
that refuses a growing baseline without an explicit trailer closed it.

### Remove a global in three green steps

**Measured.** Expand, migrate, contract. For our global model catalog that was three pull
requests: a seam that changed nothing, the harness holding its own catalog, then the
global's removal. The permission model moved the same way: a new package that aliased
the old names, then every caller moved by script, then the definitions moved and the
aliases went.

Each step merged green and each diff stayed small enough to review. The contract step is
where the hidden dependents surface. Thirteen tests failed the moment the global went,
and each was a test that had leaned on it without saying so. That is the step doing its
job, so budget for it.

### Make a mechanical change reviewable by its rule

**Measured.** One migration re-qualified 279 call sites in 72 files. We kept every
function name, because a rename would have made the diff non-mechanical: a reviewer could
no longer check one rule and trust the rest. On two other large moves a replay did the
checking. Re-running the rewrite script over the base reproduced 8 of 11 ported files
byte for byte, and a script confirmed 189 of 259 changed files were qualifier swaps and
nothing else.

Keep a large mechanical change free of incidental edits, and make it reproducible by
script, so review checks the rule and a replay checks the diff.

---

## Measuring the work

### Let a census steer, and check it with the compiler

**Measured.** We counted the engine's surface before each phase and after it. The agent
type went from 26 exported fields and 118 methods to none and 79. Two triage rules
decided most rows without discussion: an observer that only feeds the store is deleted,
and a getter takes the fate of its setter.

The census had errors of its own. Its first scope was too narrow, and widening it nearly
doubled the count of methods with no callers, from 17 to 32. A text match errs in both
directions, so every deletion was confirmed by the compiler, not by the count.

### A counting tool can hide its own bug

**Measured.** Our API-break census merged subpackages into one namespace, so a
subpackage's `New` masked the removal of its parent's. Against the previous release it
missed 21 removals out of 615. Nothing failed: the bug surfaced while we designed the
tool's replacement. A second tool passed with nothing to check, because a trailing
comment hid the module path it matched against.

A tool whose defect makes it count less still produces a plausible number. Give it a case
with a known answer, and make that case fail when the count comes up short.

### A mutation probe proves something only when it fails for the named reason

**Measured.** Probes lied to us in both directions. One "killed" a test only because the
mutated code no longer compiled. Seven first-pass kills in one pull request were invalid
for the same reason, since the edit removed the last use of an import. One probe
survived because the test data never reached the path: a model row that exists only in
the user layer is marked synthetic and skipped. Real survivors, meanwhile, found real
gaps.

A probe must compile, and its failure message must name the behavior you removed. Read it
before you count it.

---

## Holding the line

### Mark the exceptions in the code, not in a list

**Measured.** We needed to say which exported symbols of a stable package carry no
promise. A list of symbol patterns would have to agree with the code. A paragraph in the
symbol's own doc comment, in the style of Go's `Deprecated:`, cannot disagree with it,
and a host reads it where it reads everything else.

A check then refuses any stable signature that names an unstable symbol. Its first run
on the real tree found two leaks.

### Advisory until the backlog is zero, then required, in every lane

**Measured.** Our check that a stable break carries a migration note shipped advisory. It
served as the worklist for the notes of the first release: thirty notes covering 813
breaks. It became required hours later, once that backlog was empty.

The advisory period found the gap that mattered. The check sat in a job the
documentation-only lane skips, and the pull request that wrote the notes was
documentation-only, so it never ran the check. A required check must run in every lane a
change can take.

---

## Releasing it

### The last gate may be the only one that sees a platform

**Scarred.** Our own CI runs Linux containers. Windows ran only in the public mirror's CI,
the last gate before a tag. Two consecutive releases stopped there. The first stopped on
home-directory and path-quoting assumptions in five tests. The second stopped on
embedded files with no line-ending pin, where a Windows checkout gave them CRLF and golden
tests failed on output that printed identical. Neither reached the public tag, and it cost
two version numbers. Four earlier releases had stopped at the same job.

Pin line endings for every file you compare byte for byte. Run each platform you ship
somewhere before the gate that publishes, or accept that you test it at release time.

### A tool can fail only at release scale

**Measured.** A release tool wrote every request to a `git cat-file --batch` subprocess
before it read any answer. On small cuts that fit in the pipe buffers. On the first real
release, about a thousand paths filled both pipes and it deadlocked. The fix reads and
writes concurrently, and its test sends 620 KB of requests and takes back 12 MB.

Test a tool at the scale of its real input, above all one that talks to a subprocess over
pipes.

### Under pipefail, let the producer stop, not the consumer

**Measured.** `git tag | head -1` under `pipefail` failed 93 times in 100 in our CI image
and not once in 200 on a workstation. It exited 141 with no output. An earlier review had
rated the shape latent because the tag list was far smaller than a pipe buffer, and that
argument was wrong. Once `head` exits, the producer's next write takes `SIGPIPE`, whatever
the buffer size. Asking git for one ref failed none of a hundred runs.

Ask the producer for fewer lines. A consumer that stops early turns a timing accident
into an exit status.

---

## Review as a dependency

### An automated reviewer has a size limit, a memory, and outages

**Measured.** Ours gives up when the diff, the discussion and the attached files pass
256 KiB. During the extraction that stopped three pull requests: one was split, and two
were reviewed locally instead. A finding we had declined came back in new words on the
next run. A disposition recorded only in our ticket store came back too, because the
reviewer reads the pull request and nothing else. When the model provider failed three
times, a pull request merged without review on the maintainer's word.

Size pull requests to the reviewer. Record each disposition where the reviewer reads.
Decide in advance what happens when the reviewer is down, because the first time you
decide it will be under a release.
