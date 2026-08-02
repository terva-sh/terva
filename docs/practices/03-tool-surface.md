# 03 — The tool surface

Tools are how the model touches the world, and the tool catalog is usually the
largest fixed cost in a harness's prompt. This page is about how many to have,
what shape they should be, and how to write the parts a model actually reads.

---

## How many

### Every always-on tool is paid for by every session, forever

**Measured.** A built-in tool ships its name, description and full JSON schema
in every request of every session. Across comparable harnesses the tool catalog
is the biggest single component of the cold prompt. Prefix caching amortizes it
*after the first call*, so the first call of every session pays in full — and
the schema then sits in front of the entire conversation for its whole life.

A built-in tool is therefore not paid for by the users who need it. It is paid
for by everyone.

### The bar for a built-in is "the loop needs it"

**Converged.** The field supplies the cautionary data directly: one peer carries
twenty-five built-in tools each with its own permission plumbing; another is a
hundred-and-twenty-crate workspace. Meanwhile the harness that ships subagents
and plan mode as *example extensions* demonstrates that even headline features
can live outside the core.

Set the bar at *the loop itself needs this* or *this meaningfully reduces risky
shell use* — not *this is useful*. "Useful" admits everything.

### Work down a footprint ladder

**Converged.** When a capability is requested, stop at the first rung that can
carry it. The ordering is by cost to everyone who is not asking.

| | Rung | Cost to non-users |
|---|---|---|
| 1 | A prompt or a skill file | One manifest line |
| 2 | Existing tools plus a recipe | Zero |
| 3 | A hook | Zero unless configured |
| 4 | An MCP server | Zero unless configured |
| 5 | A plugin / extension | Zero unless installed |
| 6 | A built-in tool | **Every session, forever** |

The ladder's value is that it makes rung 6 require an argument.

### Skills are underrated because the ratio is unintuitive

**Measured.** A skill — a named, described markdown procedure — costs one line
in a manifest at startup and its full length only when the model calls for it.
A hundred available procedures cost roughly a hundred short lines of context.

Anything that is *knowledge* rather than a new execution primitive should be a
skill. In practice this is a large fraction of what people first propose as
tools.

---

## What shape

### Use the field-standard edit format

**Reported, converged.** Aider maintains thirteen edit formats and selects
per-model, because edit reliability varies by roughly an order of magnitude
across models. For a tool-calling harness that finding compresses to a
prescription:

> Offer string search-and-replace as the primary edit shape:
> `{path, old_string, new_string}` with a uniqueness check.

Line-number-based editing is the outlier that the field moved away from. Models
are bad at line arithmetic and good at quoting text.

### Make matching tolerant and failures instructive

**Measured.** An exact-match-only editor fails constantly on indentation the
model reproduced slightly differently. Two mechanics fix most of it:

- **A whitespace-tolerant retry.** Compare after right-trimming, allow one
  uniform leading-whitespace delta, treat blank lines as matching under any
  shift. Re-apply the indent delta to the replacement so it lands at the file's
  real indentation. A unique tolerant match applies; an ambiguous one is an
  error.
- **Errors that carry the answer.** On no match, anchor on the first line of
  the search text where that line does exist in the file, and quote the file's
  actual content there. The model can correct itself without re-reading the
  file — saving a step and the tokens of a full re-read. On ambiguity, list the
  occurrence line numbers.

### Validate a batch before applying any of it

**Converged.** Multiple edits in one call should be validated against the
original content up front, and overlapping edits rejected. A half-applied batch
leaves the file in a state neither the model nor the user predicted.

Preserve what you found: byte-order marks, line-ending style. A tool that
silently normalizes line endings produces diffs nobody wanted.

### Cap results on two axes and make them pageable

**Converged.** Cap bytes *and* lines — either alone has a pathological case (one
enormous line; a million empty ones). Provide offset and limit so the model can
page rather than re-run with different arguments.

For output that exceeds the cap, prefer offloading to a file with a stub
containing the path over truncation. See
[context economy](02-context-economy.md#offload-rather-than-truncate).

### Prefer structured tools to shelling out

**Converged.** For common read-only operations — search, glob, read — a
structured tool beats instructing the model to compose a shell command. It is
cheaper (no shell in the loop), safer (no injection surface in the argument
string), portable, and gives the harness a place to put caps and permissions.

The same holds for writes: an `edit` tool beats shell redirection, which is
opaque to the permission layer and unreviewable as a diff.

### Namespace foreign tools

**Converged.** Tools arriving from external servers should carry their origin in
their name. Two servers will eventually offer a `search`, and the resulting
collision is resolved silently and wrongly if names are flat. A prefix also
tells the model — and the person reading the transcript — where a capability
came from.

---

## What the model reads

### The description is prompt engineering, not documentation

**Converged.** The description is in the context window of every request. It is
read by the model, not by a developer, and it is the primary steering mechanism
for safe and correct use. Write it as instruction:

- State when to use the tool **and when not to**. Negative guidance is
  disproportionately effective and routinely omitted.
- Encode the safety constraints you would otherwise hope the system prompt
  covers.
- Keep it as short as it can be while still steering. Every word is billed per
  session.

Treat a description change like a behavior change, because it is one.

### Bound the schema

**Converged.** Enumerated values where the set is closed, required fields marked
required, no free-form object parameters. A loose schema produces malformed
calls, and a malformed call costs a full round trip to discover.

### Write errors a model can act on

**Scarred.** This is the highest-leverage error handling in the system, and it
follows a simple test: *could the model fix this on the next call using only
what the message says?*

- Bad: `edit failed`.
- Better: `no match for old_string`.
- Right: `no match; line 42 contains "func Foo(a int)" — the file has
  "func Foo(a int, b int)"`.

The same principle covers denial. A tool call refused by policy should return a
result that says so and why, not raise. An agent told "no" in a form it can read
routes around the obstacle; one that hits an exception stops.

There is a second-order reason to care: a tool that fails uninformatively is a
tool a model will retry identically, which is the exact input to the stuck-loop
detector in [the loop](01-the-loop.md#models-loop-and-it-is-not-a-small-model-problem).
Uninformative errors manufacture death loops.

### A tool that stamps its result opts itself out of spin detection

**Scarred.** This is the tool-author's half of the two-axis stall detector, and
it is a real limitation rather than an implementation detail. The spin axis keys
on the tool name, its canonical arguments, and a digest of what came back — so
**if no two of your results are ever byte-identical, your tool can never trip
it.** A timestamp, an elapsed time, a request id, or a freshly minted handle is
enough to opt out.

What is lost is exactly one case: a *successful* call repeated
productively-looking forever — a filter that stopped narrowing, re-querying
position zero against a set that never shrinks. Failure is still caught by the
error-churn axis, and a call budget still bounds the turn.

We considered and rejected both obvious fixes. Normalizing volatile substrings
out before hashing is guesswork about someone else's output format, and
over-normalizing puts back the false nudge the digest was added to remove.
Letting a tool declare its volatile fields in the schema puts the judgement
where the knowledge is, but costs every author a new concept and **fails
silently when left unset** — the worst property a safety mechanism can have.

So the trade is stated rather than solved, which is the transferable part: if
your result is stable when the underlying state is unchanged, you get spin
detection; if it stamps, you do not, and you must bound the work yourself with a
cursor that provably advances or a filter that provably self-excludes.

### Make an unset argument expressible as a value

**Scarred.** An argument whose behavior depends on whether the key is *present*
is unusable by a model that fills every key in the schema — a common habit, and
one JSON Schema gives it no way to know is wrong.

Two of ours failed in a single session. An `expand` field selected expand-mode
by presence, so `expand: 0` could not reach the plain listing at all: four
rejections in a row, then the agent gave up. A `cursor` field selected the
window by presence, so `cursor: 0` silently returned the *oldest* events to a
caller asking for the most recent — wrong, and quiet about it.

Note what did not work: a clearer error message had already been tried on the
first of these and did not survive contact with the model, **because the
correction it asked for was an omission.** A model can act on "use a different
value"; it struggles to act on "send fewer keys."

> **Rule.** Prefer a sentinel the schema can display (`0`, `""`, `-1` with a
> stated meaning) over pointer nil-ness, and make a padded value inert rather
> than active. Our fix was to move indices to 1-based so that `0` was free to
> mean unset.

---

## Non-negotiables

Our own tool playbook states these as rules with no exceptions. They generalize:

- **No tool without a permission story and an explicit authority class.** Absent
  a class, a tool must fall through to the side-effecting default — never to an
  auto-allow.
- **Untrusted layers may only restrict, never grant.** Project configuration and
  plugin bundles can tighten permissions; they cannot widen them.
- **Treat all external content as an injection surface.** Anything a tool
  fetches — a web page, an issue, a file someone else wrote — is a channel
  through which a third party can address your model.
- **Headless behavior must be explicit.** A tool needing interactive approval
  must emit a host-answerable event or fail with a model-readable refusal. It
  must never silently hang or assume a human is present.
- **One semantics across front ends.** Every surface observes the same event and
  policy behavior. A tool that behaves differently under RPC than in the
  terminal is two tools with one name.

## A checklist for a proposed tool

Adapted from ours; the questions are the point, not the format.

- **Frequency and replacement** — needed in most sessions? Does it replace a
  risky or verbose shell pattern? Could a skill do it with no new primitive?
- **Authority** — which class? Which approval modes auto-allow, prompt, or
  refuse it? Does the sandbox need to mediate its paths, commands, or network
  destinations?
- **Token cost** — how long must the description be to steer safe use? Can it
  live behind an opt-in layer so only consenting sessions pay? Are results
  capped and resumable?
- **Lifecycle** — progress events? cancellation? sane behavior headless? correct
  attribution when several subagents are running?
- **Fit** — cross-platform? external binaries? credentials? Would an external
  server be the better boundary? Does it fail soft when its dependencies are
  missing?
