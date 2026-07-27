# Workflows — scripted multi-agent orchestration

A workflow is a JavaScript program that orchestrates sub-agents
**deterministically**: the script decides what runs next (loops, conditionals,
fan-out), the swarm runs the agents, and a journal remembers every agent's
result so an interrupted or edited run **resumes** instead of re-spending.
Intermediate results live in script variables, not in any model's context —
which is what lets a workflow take on scale a single conversation can't hold.

```
the script decides · the swarm executes · the journal remembers
```

Where the other orchestration surfaces are model-driven (`swarm_spawn` lets
the *model* choose to delegate) or fixed-protocol ([RAATI](raati.md) always
runs its two-round deliberation), a workflow is **your** control flow: fan out
twenty readers, verify every finding adversarially, loop until two rounds come
back empty — whatever the script says, exactly and repeatably.

It is a compile-time capability: binaries built with `-tags terva_workflows`
have it (release builds do). A lean build still recognizes `terva workflow`
and says why it's absent.

## Quick start

```js
// review.js
export const meta = {
  name: 'review-changes',
  description: 'Review files across dimensions, then verify each finding',
}

const DIMENSIONS = [
  { key: 'bugs', prompt: 'Review the uncommitted diff for correctness bugs. List each with file:line.' },
  { key: 'tests', prompt: 'Review the uncommitted diff for missing or weakened test coverage.' },
]

phase('Review')
const reviews = await parallel(DIMENSIONS.map(d => () =>
  agent(d.prompt, { label: 'review:' + d.key })))

phase('Verify')
const verified = await parallel(reviews.filter(Boolean).map((r, i) => () =>
  agent('Adversarially verify this review — refute what does not hold up:\n\n' + r,
        { label: 'verify:' + DIMENSIONS[i].key })))

return { reviews: verified.filter(Boolean) }
```

```
terva workflow run review.js
```

Narration streams to stderr (phases, agent lifecycle, cache hits); the
script's return value prints to stdout as JSON. Every run gets an id
(`wf_…`) — interrupt it, or edit the script, and

```
terva workflow run review.js --resume wf_9216bc671f6a
```

replays every already-completed agent from the journal in milliseconds and
runs only what's new.

## The script contract

The grammar is deliberately the same as a Claude Code dynamic workflow — a
script written for one is a small conversion away from the other (the known
differences are tabled [below](#claude-code-compatibility)).

### `meta` — required header

```js
export const meta = {
  name: 'find-flaky-tests',              // required
  description: 'Find flaky tests',       // required
  whenToUse: 'CI shows retry noise',     // optional
  phases: [                              // optional; matches phase() titles
    { title: 'Scan', detail: 'grep the CI logs' },
    { title: 'Fix', detail: 'one agent per flaky test' },
  ],
}
```

`meta` must be a **pure literal** — no variables, calls, spreads, or template
interpolation. It is extracted by parsing, not by running the script, so it
must be readable without executing anything.

### The body

The body below `meta` runs as one async function: **top-level `await` and
top-level `return`** are both legal, and the script's return value is the
workflow's result.

### `agent(prompt, opts?)` → Promise

Spawns one sub-agent on the swarm and resolves when its task ends.

| opt | meaning |
|---|---|
| `label` | the agent's name: narration, its state directory, and its journal row (defaults to a prompt prefix) |
| `phase` | progress-group title (display only) |
| `model` | model override for this agent |
| `provider` | provider override |
| `persona` | persona identity for the agent |
| `backend` | worker backend — refused by hosts that run native children only |
| `schema` | JSON Schema for a structured deliverable (object at the top level) |

Two failure behaviors, by design:

- **The agent failing is data, not an exception.** A spawn error, an agent
  dying mid-task, or an unmet deliverable contract resolves to **`null`** —
  the fan-out survives one bad agent. Filter with `.filter(Boolean)`.
- **You misusing the API throws.** An unknown opt, a refused backend, an
  exhausted budget, or the agent-cap backstop **rejects** the promise —
  catchable, but loud by default.

With `schema`, the resolved value is the sub-agent's **schema-validated
deliverable as an object** (the [structured-deliverable
contract](standard-tools.md#host-injected-skins-conditional) — the agent
reports through a `deliver_result` tool and validation failures are retried
on the agent's side, not yours). Without `schema`, it is the agent's findings
as a string.

### `parallel(thunks)` and `pipeline(items, ...stages)`

```js
const results = await parallel(items.map(x => () => agent('do ' + x)))
```

`parallel` is a **barrier**: it awaits all thunks; a thunk that throws
resolves to `null` in the result array (the call itself never rejects).

```js
const out = await pipeline(files,
  f => agent('summarize ' + f),
  (summary, f, i) => agent('verify this summary of ' + f + ':\n' + summary))
```

`pipeline` runs each item through all stages **with no barrier between
stages** — item A can be in stage 2 while item B is still in stage 1, so
wall-clock is the slowest single chain, not the sum of the slowest per stage.
Each stage receives `(previousResult, originalItem, index)`. A stage that
throws drops that item to `null` and skips its remaining stages.

Default to `pipeline`; use `parallel` only when the next step genuinely needs
*all* prior results at once (dedup across the set, early-exit on zero
findings).

### `phase(title)`, `log(message)`

Narration: `phase` opens a progress group, `log` emits one line. Neither
affects execution or the journal.

### `args`

The value passed via `--args`, verbatim (`undefined` when absent). This is
how a saved script becomes parameterized — and how anything nondeterministic
(timestamps, run labels) gets in from outside.

### `budget`

`budget.total` (the `--budget-usd` ceiling, or `null`), `budget.spent()`, and
`budget.remaining()` (`Infinity` with no ceiling) — **in US dollars**, the
number terva already tracks per agent. The ceiling is hard: once spend
reaches it, further `agent()` calls throw. Cost lands when an agent
*finishes*, so a concurrent burst can overshoot by the in-flight agents'
cost — set the ceiling with that margin in mind.

```js
const found = []
while (budget.total && budget.remaining() > 0.50) {
  const r = await agent('Find another flaky test not in: ' + found.join(', '))
  if (!r) break
  found.push(r)
}
```

### What a script cannot do

`Date.now()`, `new Date()`, and `Math.random()` **throw** (and are rejected
by a pre-run check before any agent spawns). Resume works by re-running the
script and matching each `agent()` call against the journal — that only holds
if replay re-derives *identical* calls. Pass timestamps in via `args`; for
prompt diversity, vary by index, not by randomness. There is also no
filesystem, network, or subprocess access: the script orchestrates agents;
the *agents* touch the world.

## The journal and resume

Every run writes `$TERVA_HOME/swarm/workflows/<run-id>/journal.jsonl`. Each
completed agent is recorded under a key derived from its **exact
`(prompt, opts)` pair** — all opts participate, `label` and `phase`
included. The semantics that follow:

- **Resume replays, then continues.** `--resume <id>` re-executes the script;
  every `agent()` call whose key has a journaled result returns it without
  spawning. Unchanged script ⇒ zero spawns; edit one prompt ⇒ exactly that
  call re-runs.
- **Failures are never journaled.** An agent that resolved `null` is retried
  on resume — resume is also the *heal* mechanism after a transient failure.
- **Identical calls share one slot.** Two `agent()` calls with byte-identical
  prompt and opts are the same key; if you want N independent attempts at the
  same prompt, vary the `label` by index.
- **A row carries its label.** `{type, key, agent_id, label, result}` — so a
  finished run can be read back to the slice that produced each report without
  the narration that printed both. Narration is a stderr stream, and an
  interrupted run leaves it wherever that stream happened to land.

**Label your agents.** The id is the handle for everything durable: the
`swarm/agents/<id>/` state directory, the journal's `agent_id`, and the argument
`session_inspect` takes to read a sub-agent's transcript. Unlabelled, it is
slugged from the prompt — and a fan-out shares its prompt preamble by
construction, so six agents mint six ids differing only in a numeric suffix.
Labelled, they read `core-engine-…`, `providers-cli-…`, and a sub-agent's full
transcript is one paged, redacted `session_inspect` call away.

## Bounds

| bound | default | override |
|---|---|---|
| concurrent agents | `min(16, cores − 2)` | `--concurrency` |
| spend | none | `--budget-usd` |
| run time | none | `--timeout` |
| agents per run | 1000 (runaway-loop backstop) | — |

The concurrency cap lives in the workflow runner, not the swarm — excess
`agent()` calls queue and run as slots free. Sub-agents are task-scoped:
each is stopped as soon as its task ends, not left attached like an
interactive `/swarm` agent.

## CLI reference

```
terva workflow run <script.js> [flags]
  --args <json|@file>   the script's `args` global
  --resume <run-id>     replay completed agents from an existing run's journal
  --budget-usd N        hard spend ceiling (0 = none)
  --concurrency N       max simultaneous agents (0 = min(16, cores−2))
  --timeout DUR         bound the whole run (e.g. 30m; 0 = none)
  --cwd DIR             working directory the agents share (default: current)

terva workflow list
  every run, newest first: id, status, name, completed/total agents, cost

terva workflow show <run-id> [--script]
  the run's record and its journaled results; --script prints the source
```

**Finding a run again.** Each run writes `run.json` beside its journal — the
script source, the launch coordinates, times, agent counts and cost. That is
what makes `list` and `show` possible without the launching terminal, which
matters most for a run that was **interrupted**: a failed run prints its
`--resume` hint on the way out, an interrupted one dies before it can, and its
completed agents sit on disk unreachable in practice.

A run with no `ended` reads as `incomplete` rather than `running` — telling
those apart needs liveness, and claiming "running" about a dead run is worse
than saying nothing. Either way the next action is the same: look at the counts
and decide whether to resume.

Starting a script that has an incomplete prior run in the same cwd says so
before spawning, matched on the script **source** rather than its path — resume
keys on content, so an edited file at the same path would replay nothing.

stdout carries exactly one thing: the script's return value as JSON (so runs
compose with `jq` and pipelines). Narration, the run summary (agents run /
replayed, cost, elapsed), and — on failure — the `--resume` hint all go to
stderr.

The CLI host drives **native sub-agents only**; an `agent()` call naming a
`backend:` fails loudly rather than running the work under the wrong
identity.

## Watching runs from the control panel

`terva web`'s board has a **Workflows** lane beside the sessions and the swarm:
every run on the host, newest first, with its status, completed-of-total agents,
cost, and — for a run worth resuming — what resuming would replay. Clicking one
opens it: the record, **the script as it ran**, and every report it journaled,
each collapsed behind its size so a 98 KB deliverable does not land in the page
uninvited.

Reading the script there is the point. A run's definition used to exist only in
whatever `.js` file the operator launched from, so inspecting one meant shell
access to the host and a file that may since have moved or been edited; the
source in the record is the copy that answers *what ran*.

Two honest limits, both consequences of `terva workflow run` being a separate
foreground process:

- **The daemon does not see the engine.** It reads what a run leaves on disk, so
  the lane knows a run exists only once it has written its opening record, and
  learns of progress by re-reading the journal. It polls while any run is
  unfinished and stops when they all close; a run that starts while the board is
  open needs the lane's **Refresh**.
- **No run is ever shown as "running."** Same reason as `list`: distinguishing
  live from crashed needs liveness the record cannot supply.

Serving the lane needs no workflow engine in the binary. The engine is behind
`-tags terva_workflows` because it links a JavaScript runtime; the artifacts it
writes are plain JSON, read through `packages/agent/workflow/runs`, which imports
nothing but the standard library. A lean `terva web` therefore serves runs made
by a full binary on the same machine.

Wire surface: `workflows.list` and `workflows.get`, both read-only and in the
**session** group rather than control — observing a run needs no authority to
change anything.

## Claude Code compatibility

Same grammar and shape, deliberately — but script-level compatibility, not
artifact-level (run journals are internal formats on both sides; neither
reads the other's). Known differences:

| | Claude Code | terva |
|---|---|---|
| `budget` | tokens | **USD** |
| agent identity opts | `agentType`, `effort` | `persona`, `model`, `provider` |
| isolation | `isolation: 'worktree'` per agent | the host's `--swarm-worktrees` setting |
| mixed backends | — | `backend` opt (workspace hosts) |
| nested `workflow()` calls | yes | not yet |
| structured output | `schema` opt | `schema` opt (the swarm deliverable contract) |

A Claude Code script using only the shared surface (`meta`, `agent` with
`label`/`phase`/`schema`, `parallel`, `pipeline`, `phase`, `log`, `args`)
runs unmodified. One that uses Claude-Code-only opts fails loudly with the
conversion mapping in the error rather than silently spawning defaults.

## Examples

Working examples live in [`examples/workflows/`](../examples/workflows/) —
and every terva install carries them at `$TERVA_HOME/examples/workflows/`, so
they are readable on a host with no source checkout (by you, and by a
sandboxed agent whose jail includes `$TERVA_HOME`). CI executes each example
against a stub engine on every commit: if an example ships, it runs.
