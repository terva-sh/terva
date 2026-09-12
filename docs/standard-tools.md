# terva standard tools — strategy and playbook

Canonical guidance for terva's model-visible tool surface: what belongs
in the always-on core, what ships as an opt-in **standard extension**,
and what stays a **recommended MCP preset**. Consult this before adding,
removing, or reclassifying any tool.

The near-term implementation roadmap (and what is deferred) lives in
`docs/plans/standard-tools-bucket2.md`. The
original landscape comparison that motivated this is
`docs/plans/harness-landscape-2026.md`.

## Guiding principle

terva does not win by having the smallest prompt or the largest product
surface. It wins by pairing a **small, hardened core** with **opt-in
standard extensions** whose model-visible tool calls all share one
permission, event, and policy model.

The token economics back this up: across comparable harnesses the biggest
cold-prompt cost is the tool catalog and its descriptions, and prompt
caching amortizes that cost only after the first turn. So every always-on
tool must earn its tokens for *every* session; specialized workflows
belong behind opt-in layers that only consenting sessions pay for.

## Trust boundary (read this before "just shipping an extension")

Extension **tool calls** are mediated by terva — permission-gated,
hook-interceptable, classified read-only or not. The extension
**subprocess itself** currently runs as trusted local code with the
user's normal filesystem and network privileges. Installing or loading an
extension is therefore consent to run a local program. Do **not** imply an
extension is sandboxed merely because its LLM-callable tools are
permission-gated. (Project-local extensions/skills/hooks/MCP are
additionally gated by Workspace Trust; see
`docs/plans/workspace-trust.md`.)

## The four layers

When adding a capability, decide its layer first:

1. **Core built-in** — universal, high-frequency, low-dependency tools
   that benefit from tight permission/sandbox integration. Lives in
   `packages/agent/tools/`, registered in `BuildToolRegistry`
   (`packages/agent/build/build.go`).
2. **Standard extension** — a terva-maintained or explicitly blessed,
   documented, easy-to-install **opt-in** extension. Still runs as
   trusted local code (see above).
3. **Recommended MCP preset** — third-party services or tools whose
   implementation already exists outside terva; we ship docs and starter
   config, not code.
4. **Skill** — a reusable procedure/instruction set that needs no new
   execution primitive.

Default answer: **extension first**, unless the tool clearly reduces risky
`bash` usage, needs harness-level lifecycle/UI semantics, or belongs in the
core safety substrate.

## Authority classification

A single `read_only` boolean does not describe every tool. Classify each
tool's authority explicitly, and do **not** collapse "read-only" and
"safe" into one idea:

- **local read-only** — reads files/state under the jail; no process,
  network, or external side effects. (`read`, `grep`, `glob`,
  `terva_status`.)
- **local data** — reads *and writes* only the tool's own host-managed
  data dir (for an extension, its `$TERVA_HOME/ext-data/<name>`); no
  user-workspace, process, network, or external effect. Auto-allowable
  like local read-only because the write never leaves private,
  host-controlled storage — for a memory/notes/state tool.
- **workspace mutation** — writes files, edits tasks, creates worktrees.
  (`write`, `edit`.)
- **process execution** — starts commands or long-running subprocesses.
  (`bash`; a future `monitor`.)
- **network read** — fetches URLs/search results; can still leak metadata,
  trigger server logs, or reach private networks. *Not* equivalent to
  local read-only.
- **external mutation** — writes to third-party APIs, sends messages,
  opens PRs, changes cloud resources.
- **user interaction** — blocks to ask the user (`ask_user_question`);
  no other side effect, so it is permitted in every mode and never
  prompts.

The taxonomy now exists as `core.Authority` (`packages/core/policy.go`),
and an extension/MCP tool can declare its class via the `authority` field
on `register_tool` (`ext.WithAuthority` in the Go SDK). Declared authority
decides read-only classification — `local-read` and `local-data` are
auto-allowable, so a `network-read` tool is gated like a side-effecting tool (it prompts in
`workspace`/`auto-edit`, is refused in `plan`) even if it also set the
legacy `read_only` bool. **Mark network tools `network-read`, not
`read_only`.** The shared egress guard now ships, and three host-side
fetches route through it (see [permissions.md](permissions.md)). Still to
come: a user-layer config key naming hosts the guard may dial even when
they resolve to a private address. It widens the guard alone, and it does
not change which calls prompt. See `docs/plans/standard-tools-bucket2.md`.

## Current standard bundle

### Core built-ins (always on)

| Tool | Authority | Notes |
|---|---|---|
| `read` | local read-only | files + inline images for vision models |
| `write` | workspace mutation | overwrites; description steers toward `edit` for partial changes; optional `mode` (octal ≤ `0777`) sets permission bits — e.g. an executable script — in one reviewable step |
| `edit` | workspace mutation | exact-match replacements, whitespace-tolerant fallback |
| `bash` | process execution | merged stdout/stderr, timeout; description carries git/secrets/file-tool-preference guardrails |
| `grep` | local read-only | RE2 content search; `.gitignore`-aware, binary-skipping, paged |
| `glob` | local read-only | path glob (`**` recurses); `.gitignore`-aware, paged |
| `ask_user_question` | user interaction | structured clarifying question(s); permitted in every mode; interactive-only (headless returns a proceed-anyway result). `questions[]` asks up to 8 at once as ONE interruption — the TUI shows them as tabs with a submit pane, the web client stacks them in one card — and returns every answer together; the singular `question` form still works. Set `recommended_options` to exact option text when the model recommends one or more choices; clients mark those choices and never infer a recommendation from position. |
| `terva_status` | local read-only | session self-introspection |
| `session_inspect` | local read-only | bounded, filterable view over a session transcript: this session, any other session in `$TERVA_HOME` (by its id, this project first), or a swarm sub-agent's; `expand` reads one event's full text in pages, and `stats` returns a whole-session rollup (cost, cache hit rate, dead turns, tool-call and failure histograms, provider errors) in one pass instead of paging for it. Event kinds cover `tool_call`, `tool_result`, `message`, `usage` (a turn's cost and cache accounting), `error` (a provider failure from the `.errors.jsonl` sidecar, placed against the turn it killed), and the pair `prefix`/`cliff` (the rung where a cacheable prefix was rebuilt, and a run of dispatches whose cache reads collapsed) — these four are how a session's cost, its outages and its cache behaviour become answerable at all; nothing else records them. The transcripts stay on the read deny list for every other tool, and this one is carved back out for them: see [permissions](permissions.md). Event indices and `cursor` are **1-based**, and `0` means "not set" on both — so a caller that fills every optional key with its zero value gets the default listing of the most recent window rather than an error or the wrong end of the transcript (see "Optionality" below). Secrets redacted, input scan and output both capped. A sub-agent's transcript streams as it works, so a running one is inspectable mid-task; before its first message lands the result names that state rather than blaming the filters. |
| `session_list` | local read-only | enumerates recorded sessions, newest first — the discovery half of the session surface, and the answer to "what exists" that neither of the readers above gives. `scope` is `"project"` by default and `"all"` for every project in `$TERVA_HOME`; an unknown scope is refused rather than narrowed, because a caller who asked for more and silently got less would read the short list as "there is nothing else". A row carries the session id, the time of the last change, the size, and the working directory, and nothing from inside the conversation — `Details.session_ids` carries the ids so a sweep consumes them without parsing prose. Cheap by construction: it stats rather than reads, and opens only the bounded opening meta row of each listed row (which is where the working directory comes from). A title would need the folded meta and therefore a full scan, since a rename lands in a later row, so titles and content stay behind `session_inspect`. Paged with `limit`/`offset`, default 20, max 100. Sessions only: swarm sub-agent ids come from `swarm_spawn`. See [permissions](permissions.md) for what it exposes by default. |
| `task_create` / `task_update` / `task_list` / `task_archive` | local data | the built-in task board (folded in from the former `terva-tasks` extension): one active task at a time, evidence to close, archive generations, `task_list format:"markdown"` exports a checkbox worklog. The board persists per session under `$TERVA_HOME/tasks` (private modes) and its live state rides each turn as a context card. |
| `activate_tools` | visibility only | present when `lazy_tools` is on, which is the default (see below); brings a hidden capability group into the advertised set. The advertised set is pinned while the model replies, so an activated group can never join the current reply's remaining calls — instead, activation continuation (on by default) automatically re-prompts the model with the tools live the moment it finishes that reply; with continuation off, the group lands on the NEXT turn. Its result echoes the group's schemas (capped at a 4 KB budget; past that, names only) so the model can compose that next call without waiting to see them. Grants no authority — revealed tools keep their normal permission gates. |

`grep`/`glob` are jailed exactly like the file tools (cwd containment,
symlink skip) and survive `plan` mode because they are classified
read-only.

**Optionality: a zero value must be inert, not a second meaning.** A tool
argument whose behaviour depends on whether a key is *present* is unusable
by a model that fills every key in the schema — a common habit, and one
JSON Schema gives it no way to know is wrong. `session_inspect` had two
such fields and both failed in one session: `expand` chose expand mode by
presence, so `expand: 0` could not reach the listing at all (four
rejections in a row, then the agent gave up); `cursor` chose the window by
presence, so `cursor: 0` silently returned the *oldest* events to a caller
asking for the most recent. A clearer error message had already been tried
for the first of these and did not survive contact with the model, because
the correction it asked for was an omission.

So the rule for new tool arguments: **make the "unset" case expressible as
a value.** Prefer a sentinel the schema can show (`0`, `""`, `-1` with a
stated meaning) over pointer nil-ness, and where a padded value must be
accepted, make it inert rather than active. `session_inspect` is the
worked example: indices moved to 1-based so `0` is free to mean unset.

**A tool that stamps its result opts itself out of spin detection.** The
stuck-loop guard has two axes. The *churn* axis counts repeated failure; the
*spin* axis counts repeated **redundant work**, keyed on the tool name, its
canonical arguments, and a digest of what came back. That last part is
deliberate — keying on arguments alone nudged a correct loop whose identical
query returned something new each time, such as polling a job or re-reading a
file being written.

The cost of that correctness is exact and worth knowing before you design a
result: **if no two of your results are byte-identical, your tool can never trip
the spin axis.** A timestamp, an elapsed time, a request id, or a freshly minted
handle is enough. Among built-ins that describes `bash` running a volatile
command. It also describes the shape this document otherwise recommends for
bulk work — a search that returns a selection handle mints a new one per call,
so two identical searches against an unchanged mailbox are two different
results.

The consequence is not that such a tool is unguarded. The churn axis still
catches it failing, the per-turn call budget still bounds it, and a human or an
`activate_next`-style structure still sees the loop. What is lost is the
specific case of a *successful* call repeated productively-looking forever —
a filter that stopped narrowing, re-querying position zero against a set that
never shrinks.

Terva does not try to guess which parts of an arbitrary result are incidental.
Normalizing volatile substrings out before hashing was considered and rejected:
it is guesswork about someone else's output format, and over-normalizing puts
back the false nudge the digest was added to remove. Declaring volatile fields
in the schema was considered too — it puts the judgement where the knowledge is,
but costs every tool author a new concept to learn and fails silently when left
unset.

So the trade is stated rather than solved. If your tool's result is stable when
the underlying state is unchanged, it gets spin detection. If it stamps, it does
not, and you should not rely on the loop guard to catch a runaway caller —
bound the work yourself, with a cursor that provably advances or a filter that
provably self-excludes. `TestStallSpinIgnoresTimestampedResults` pins the
behaviour so it stays a known limitation rather than becoming a surprise.

**Git-conditional built-ins**: the five `worktree_*` tools (folded in from the
retired `terva-git-worktree` extension) join the registry only when the session
cwd is — or has an immediate child that is — a git repository, decided once per
registry build: managed worktrees with an available/claimed reuse model,
`worktree_list` (read-only; the pre-decision call) plus
`create`/`claim`/`release`/`remove` (git-state mutating, classified like
`write`/`edit`). A session outside any repo pays no tokens for them. They sit
in the lazy group `worktree` under `lazy_tools`, share their engine
(`packages/agent/worktree`) with the swarm's `--swarm-worktrees` lease, and
keep state under `$TERVA_HOME/worktrees/` (extension-era state migrates on
first touch; existing checkouts stay at their old paths).

**Store-conditional built-ins**: the `ticket_*` tools (slices 2 and 3
of `docs/plans/git-ticket.md`) join the registry only when a `.tickets/`
store governs the session cwd, probed with `ticket.Discover` once per
registry build. The read five — `ticket_list`, `ticket_search`,
`ticket_get`, `ticket_ready`, `ticket_check` — are local read-only and
survive `plan` mode. The write six — `ticket_create`, `ticket_update`,
`ticket_transition`, `ticket_claim`, `ticket_comment`, `ticket_fix` —
classify as workspace mutation like `write`/`edit`: the store lives in the
user's repository and lands in their next commit, so plan mode prunes them
and a headless non-yolo gate refuses them. `ticket_fix` is the one to read
twice: it is `ticket_check`'s other half and its name rhymes with a read,
but it moves and rewrites files in the store, so it sits with the writes.
`ticket_store` is a twelfth, and it rides a further gate: it registers only
where the user config key `ticket_stores` names at least one store that
opens. It selects which store the other tools work against, so it
classifies like `activate_tools` rather than like a read or a write. It
changes what the tools reach and grants no authority of its own, every
destination it can select is one user configuration already named, and each
write to the selected store still faces its own gate. See
[permissions.md](permissions.md#ticket-stores-outside-the-workspace).
They all call git-ticket's `ticket` package directly for structured
values — the `terva ticket` CLI subcommand embeds the same library's `cli`
package, so the two surfaces cannot drift from each other. Both track the
version `go.mod` pins, currently v0.14.3. A `git-ticket` binary installed
separately on the user's `PATH` is a third thing and can be any version, so
that one *can* drift from both.

`ticket_claim` is the one that also writes outside the store. A claim seeds
the session task list with one task per acceptance criterion that is not
checked yet, and each seeded task records the ticket id and the 1-based
criterion index it came from. `seed_tasks: false` claims without touching
the list. The claim also records the terva session id, which git-ticket
stores at schema 3 and above, so a ticket indexes back into transcript
history.

The task list is where that mapping lives, and nothing in the ticket store
holds it. The index counts every criterion, checked ones included, because
that is what `ticket.SetChecklistItem` addresses; numbering off the
unchecked subset would check the wrong box and say nothing. A claim seeds
nothing when the list still holds open tasks, and reports why, because a
mix of two tickets' tasks is hard to undo.

Because a claim writes the board, `ticket_claim` binds to it through
`tools.TaskBinder` wherever the task tools bind: at registration, in
`UseTasks` across a rebuild, and in `freshTasksRegistry`. That last one is
not optional. The per-conversation registry is a shallow copy, so a
`ticket_claim` that was not rebound would seed the owner DM's durable board
from an admitted group chat.

The bridge runs the other way too. Closing a task with `evidence` checks the
acceptance criterion that task was seeded from. Evidence is the gate rather
than the close alone, because an unevidenced close is what the evidence nudge
already asks the model to repair, and a ticked box on a shared ticket is much
harder to walk back than a task status. A check that fails to reach the ticket
store never fails the task update. The result says why, and it names
`git ticket ac <id> --check <n>` as the manual repair.

That direction inverts the dependency, so `tasks.CriterionChecker` is declared
on the tasks side and implemented on the ticket side.
`packages/agent/tools/tasks` imports `privfs` and `core` and nothing else, and
a direct import of the ticket tools would close a cycle. `bindTaskBoard` binds
both halves in one place, so the forward and reverse bindings cannot end up at
different call sites and drift apart.

`ticket_transition` writes a worklog note when a ticket reaches `done`,
`archived`, or `blocked`. Blocked counts because a park is when the record of
what was tried is worth most. The note holds only the tasks that carry this
ticket, filtered inside each archived generation as well as across them, so
closing one ticket never copies another one's work into a permanent record.
Tasks the session never archived land as a final section, because unarchived
work still happened. A close with nothing to record writes no note and reports
the reason, since an absent note has several causes and silence does not tell
them apart.

One interaction is worth knowing. Closing a seeded task rewrites the ticket
file, so a revision read before that close is stale by the time a transition
runs. The refusal names the current revision, so this recovers on its own, but
reading the ticket again first is cheaper.

The session also gets a per-turn ticket card, beside the task card and on the
same footing: `EphemeralTail.Tickets`, a first-party field rather than something
an unrelated subsystem can switch off. It is composed last, so the model reads
it first. The ticket is why this session is working, and the task board is what
it is doing about it now.

The card names the ticket this session claims, its title, and the queue behind
it: how many tickets an actor could pick up, and how many drafts wait on a
person. Id and title only, because the model calls `ticket_get` when it needs
the criteria, and a card that carried them would repeat a long body on every
turn.

It renders nothing in two cases, and both are the point. A workspace with no
store pays no context cost, which is why the whole ticket surface gates on the
store. A session that claims nothing pays none either, because a card that only
counts other people's work is a standing tax on every turn of every session that
is not working a ticket. That second rule also pays for the cache below: the scan
only ever runs for a session holding a claim.

The match is on the claim's session id, not its actor. Two sessions of one
persona share an actor, so an actor match would show each of them the other's
work and call it their own. An expired claim does not count, because a claim
lapses rather than being released when its holder dies.

The counts are cached. A write through any `ticket_*` tool calls `Invalidate`,
so the next render is exact, and `ticket_fix` skips that under `dry_run` because
a dry run writes nothing. A 30-second throttle bounds the rescan otherwise. That
interval is the catch-up path for a write terva did not make: the `git ticket`
CLI, another agent in another worktree, or a merge that moved files underneath.
Those stay stale until it lapses, which is the cost of not scanning every turn.

One wiring rule carries real risk. `WireEphemeralTail` runs once at session
build, but `rebuildTools` mints a fresh `TicketCore` carrying a fresh card. Left
alone, the write tools would invalidate a card nobody renders and the model's
card would freeze at whatever the store held when the session started, which is
worse than no card because it is confidently out of date. `Resolved.UseTicketCard`
preserves it across the rebuild, the same treatment `UseTasks` and `UseFiles`
get, for the same reason.

There are two rebuild paths, and both carry it. The workspace daemon keeps its
own `rebuildTools`, and rpc and acp share `build.LiveToolSet.Rebuild`, which now
has a `TicketCard` field. `TestTheSharedRebuildCarriesEverySurvivorTheWorkspaceDoes`
holds the two in step and will fail on a survivor added to only one of them. It
caught this one. Three hosts wire the card, and they go through
`tools.TicketCardFor` rather than each repeating the lookup, because a host doing
this its own way is how every survivor bug here shipped. The headless print and
json path is the deliberate omission: a single-shot run holds no claim, so the
card would render nothing anyway.

The card carries no prompt guidance. The task card has some because the model
has to maintain the board. This one only reports, and `claimed:` and `queue:`
read without instructions. A ticket title is authored text that reaches the model
verbatim, so it goes through the same targeted escaper the task card uses, and a
title cannot close the frame or forge a system block.

`swarm_spawn` takes an optional `ticket`. The supervisor claims it for the
sub-agent before anything spawns, renders the ticket into the task text, and
refuses the spawn when it cannot claim it. The refusal names the reason, so a
draft says who may promote it and a blocked ticket names what comes first. No
sub-agent starts on work it cannot hold.

The claim names the child, not the host: `agent:terva/<subagent-id>`, with the
agent's worktree and a two-hour expiry. The expiry is the point. A claim on
behalf of a process has no holder who can release it, so a crashed sub-agent
would keep a ticket forever, and a lapse is better than litter. The supervisor
does not renew it, because the claim is a coordination hint and not a lock.

The child signs its own writes with that same id. `build.ticketActor` reads
`TERVA_SWARM_AGENT_ID`, which the runner already puts in every child
environment, so no flag and no new variable had to reach the child. Without it
the child would sign as its persona while the claim named the subagent, and the
ticket would disagree with itself about who did the work. `ticket_init` is the
deliberate exception and keeps the persona: it seeds a new store's first actor,
which is a durable identity, and a subagent id is gone by the next spawn.

The brief is the ticket itself, because a sub-agent starts with no conversation
context and the ticket already holds what a briefing would repeat. Criteria
arrive as numbered text rather than checkboxes, since a checkbox invites a child
to tick something it may not. Closure belongs to the dispatcher.

The criteria also become the report contract. When the caller writes no
`deliverable_schema`, `swarm_spawn` derives one: an object with a `criteria`
array, one entry per criterion, each carrying the criterion text, whether the
work met it, and the evidence. The description numbers the criteria in the order
they went out. That makes the dispatcher's review a structured check against each
criterion rather than a reading of prose, and a criterion the child skipped comes
back missing rather than merely unmentioned.

An explicit `deliverable_schema` wins, because a caller who wrote one was
specific about the report it wants. A ticket with no criteria derives nothing,
and the sub-agent reports in prose as an unassigned one does. `spawnSchema` holds
that choice. The top level is an object because a provider tool schema must be
one, so the array hangs off a single property.

The task text leads with `Ticket <id>. `, and that is what puts the ticket in the
swarm recap. The recap prints a truncated task line, so a brief appended at the
end falls outside that window and a dispatcher reading the recap would not learn
which ticket the report belongs to. Leading with the id costs no new field on
`Agent` and nothing in the persisted `meta.json`.

A sub-agent never closes a ticket. `ticket_transition` refuses `done` and
`archived` whenever `TicketCore.SubagentID` is set, which is exactly inside a
swarm child. Every other move stays open, because a sub-agent may park a ticket
it cannot finish and `blocked` is how it says so. The guard runs before
`applyMutation`, so a refused close writes nothing.

The tool stays in the registry and refuses rather than being removed. An absent
tool teaches nothing: a child cannot tell a capability this host lacks from one
it is being denied, so it retries or invents a way around. The refusal names the
reason and the next action, which is `ticket_comment` and a report against each
criterion. Whether the work is finished is a judgement about that report, and no
actor approves its own output.

`ticket_list` and `ticket_search` page (default 50 rows, cap 200,
`next_offset` cursor), per the paging requirement above. Every mutation
except create and fix requires `if_revision`, the revision `ticket_get`
returned; a stale value refuses with the current revision in the message.
`ticket_fix` is exempt because it walks the whole store rather than one
ticket, so no single revision gates it. `dry_run: true` previews the
repairs instead. Mutations record terva's own actor
(`agent:terva/<persona>`), and no schema offers the identity, because a
model does not choose who it is. A repository with no store pays no schema
cost. They sit in the lazy group `ticket` under `lazy_tools`.

`ticket_init` registers on the *inverse* of that gate, so no session ever
carries it and the eleven together: where there is no store it is the only
ticket tool present, and where there is one it is absent. It runs
git-ticket's embedded `init --instructions`, creating `.tickets/` and writing
the agent workflow block to `AGENTS.md`, so it classifies as workspace
mutation and plan mode prunes it. It rides the same two opt-outs, because a
user who turned the ticket tools off did not ask to be offered a ledger.

Two things make it more than a wrapper around the CLI. It calls
`Agent.RequestToolRefresh`, the seam a tool uses when it has changed what
registration itself can offer, and the host re-resolves the registry so the
eleven land on the model's next step rather than the next session. The
workspace daemon, rpc, and ACP install that callback; a one-shot print or cli
run does not, and the tool's result says so instead of promising tools that
will not arrive. It also refuses to pick the store's first actor on its own.
git-ticket signs any later actor-less write with that entry, so seeding the
agent there would sign the user's own commands with the agent's name. It asks
instead, suggests a `human:` id built from `git config user.name`, and
remembers the answer in the user-layer config key `ticket_actor` so the next
repository on the machine asks nothing.

Nothing proposes creating a store. Where a repository has no `.tickets/`,
the agent carries `ticket_init` and no guidance beyond that tool's own
description, and terva stays quiet about it. A prompt that offered to start a
ledger in every storeless git repository would be a nag, and creating one is a
bid to restructure somebody's repository. What changed in 2026-09 is only who
types the command when the user does ask. The agent used to run `terva ticket
init` through `bash`, which meant a session that had already been told to
track work in tickets found no ticket system and invented one. Now it has a
tool, and the store it creates is usable in the same session.

Two switches turn all twelve off: `--no-ticket` for one run, and a `tickets`
config key that a user sets in `$TERVA_HOME/config.json` and a project may
also set in `.terva/config.json`. The project layer is restrict-only in the
`disable_mcp` shape, so a cloned repository can refuse the tools for its own
directory and can never grant back what the user disabled. Neither switch
touches `terva ticket`, which reaches the store through git-ticket's own
command surface: the opt-out removes the model's tools, never the ledger.

A session that keeps the tools also carries a prompt segment saying to prefer
them over that command line. It is labeled `ticket` in a prompt dump, and it
lands after the `agents-md` segment on purpose. A repository with a `.tickets/`
store almost certainly documented the store before terva had tools for it, so
its `AGENTS.md` still says `git ticket create ...`; recency is the only lever
terva has over a file it does not own. The segment is gated on the built
registry rather than on the store probe, so `--tools` and plan mode cannot
leave the prompt naming a tool the model cannot call, and the half that
teaches `if_revision` rides only where the write five survived.

The task tools ship exactly when the base coding tools do — `--chat`, `--play`,
`--no-tools`, and `--no-workspace-tools` drop them together — and there is no
standing config switch beyond that: a per-run `--tools` allowlist that omits
them is the way to run coding tools without the board. Boards written by the
old `terva-tasks` extension migrate forward automatically on their next write
(the store reads through the legacy `ext-data/tasks` layer).

#### Lazy tool visibility (`lazy_tools`)

With many extensions/MCP servers attached, most of the tool surface is noise
most of the time. Lazy tool visibility advertises only the core group plus the
groups named in `lazy_tool_active` (e.g. `["mcp:github"]`); everything else is
summarized in a per-turn `[inactive tool groups]` note the model can act on
with `activate_tools`.

**On by default since 2026-08-01.** Set `"lazy_tools": false` in `config.json`
(or turn it off in the settings pane) to advertise everything up front. It
shipped opt-in while flipping it was unsafe — hiding could engage in sessions
where `activate_tools` was never registered, leaving no reveal path — and that
is fixed, so the default is now the one the feature was built for.

**A session with nothing beyond the core group is unaffected**: there is
nothing to hide, so lazy mode is a no-op. The change is only visible to setups
that actually have extension or MCP tools — which is exactly who pays for them
in context every turn.
Visibility only: hidden tools remain callable and permission-gated, so no
authority changes hands. Activation never lands mid-reply (the advertised set
is pinned per segment); by default the model is automatically continued with
the tools live the moment it finishes the reply that activated them —
activation continuation (the `activation-continuation` design record) — and
with the feature off they arrive on the next turn. The toggle lives in the
settings pane (web and TUI `/settings`) once `lazy_tools` is on, or as
`"engine_features": {"activation_continuation": false}` in `config.json` for
headless runs. Extension names may not squat the reserved group namespace
(`core`, `mcp:*`).

### Host-injected skins (conditional)

Two model-visible tools are **not** always-on: the host injects them only when a
session opts in, and both are thin **skins over one dispatch engine**
(`packages/agent/swarm/`) rather than new primitives. They are not the only
conditional tools, though — the rest are tabled further down.

| Tool | Injected when | Authority | Skin |
|---|---|---|---|
| `swarm_spawn` | auto-swarm on (coding sessions only) | process execution | fire-and-forget parallel coding sub-agents; a `tier` picks a cheaper model, never stronger than the host. An optional `deliverable_schema` (JSON Schema, object at the top level) demands a structured report back — see below. See [tui.md](tui.md) (auto-swarm). |
| `actor_spawn` | `--play` with a declared cast | process execution | synchronous "director voices an actor" — hands the actor a situation, waits, returns its line. Cast is closed and named; the model dispatches by name, never a path. See [personas.md](personas.md#cast-and-actor-dispatch). |

Because they wrap the same engine they share its lifecycle, session
persistence, and tier resolution, and differ only in the *skin* (fire-and-forget
vs. synchronous director-pull) and the gate that injects them (the auto-swarm
setting vs. `--play` + a cast). New dispatch front-ends should follow this
pattern — another skin over the one engine — rather than adding a parallel
engine. Both are gated out of the wrong context: `actor_spawn` never appears in a
coding session, and `swarm_spawn` never appears in an immersive one.

**Structured deliverables** (`deliverable_schema` → `deliver_result`): when a
`swarm_spawn` call carries a `deliverable_schema`, the child session gains one
extra tool, `deliver_result`, whose argument schema is exactly the spawn's
schema — the sub-agent reports by calling it once, and validation failures are
retryable errors, not silent acceptances. `deliver_result` exists only inside
a sub-agent session spawned with a schema (it is never part of the host
session's surface) and is classified local read-only: it records the agent's
own report in its own state directory and touches nothing else. Workers that
cannot carry tools (external harnesses) get the same contract as briefing
text and report via a fenced ` ```json ` block instead; the supervisor
re-validates either route when the task ends and surfaces the parsed
deliverable — or its absence, with the reason — on the task record and in the
auto-swarm recap. See [tui.md](tui.md) (auto-swarm) for the operator's view.

`generate_image` is likewise conditional — injected only when an `image` config
block resolves a backend (opt-in, off by default). It turns a prompt into an
image via a registry of backends (hosted or self-hosted, adapter-per-protocol —
separate from the model catalog), returns it inline, and optionally writes it
into the workspace through the sandbox. Workspace-mutating and it spends money on
hosted backends, so it is approval-gated and absent in plan mode. See
[image-generation.md](image-generation.md).

The remaining conditional tools are injected by
`packages/agent/workspace/workspace_session.go`, each from a declarative
input — connecting a bridge or flipping a config key re-derives the registry
rather than patching a live one:

| Tool | Injected when | Authority | Notes |
|---|---|---|---|
| `generate_image` | an `image` config block resolves a backend | workspace mutation | see above |
| `raati_convene` | `raati.convene_tool` is set, in base workspace sessions only | *(unclassified — always prompts)* | the agent convenes its own deliberation panel. A convening spends real sub-agent turns, so every call hits the approval gate; the run mirrors onto the live raati pane. Skin-gated out of `--chat`/`--play`. See [raati.md](raati.md). |
| `chat_send_image` / `chat_send_file` | a chat bridge is connected **and bound to this session**, and the connector advertises the capability | external mutation | sends into the paired chat. Bound per session, so a second session never sees another's chat tools. See [connectors.md](connectors.md). |
| `terva_restart` | self-restart is enabled (`--allow-restart`) on a platform with `exec(2)`, in the TUI as well as web | *(unclassified — always prompts)* | re-execs the running binary in place, preserving the session. See below. |
| `terva_arm_restart` | self-restart is enabled (`--allow-restart`), web session | *(unclassified — always prompts)* | declares that an imminent **supervisor** restart is planned for this session, just before the agent runs the supervisor command itself (e.g. `systemctl --user restart` to apply a changed unit — which `terva_restart`'s self-exec cannot do). Writes a short-lived on-disk marker so the SIGTERM that replaces the process is treated as planned: the interrupted command reconciles as expected (not a failure) and the exact session resumes. terva stays supervisor-agnostic — this only records intent. Shares `terva_restart`'s unclassified treatment for the same reason. |

**`terva_restart` (and its sibling `terva_arm_restart`) is the acknowledged exception to "no tool without an explicit
authority class."** It is deliberately left out of the permission tables — not
overlooked. There is no honest class for "replace the process image": it is not
workspace mutation, not process execution in the `bash` sense, and any class we
gave it would make *some* mode auto-allow it. Being unclassified means it falls
through to the side-effecting default in every mode, so it **always** prompts —
yolo included. The prompt is the feature. Two gates stand in front of it: the
capability is off unless an operator passes `--allow-restart`, and web mode
additionally refuses to enable it at all on an unauthenticated non-loopback
listener. If a
future tool wants the same treatment, it must earn it the same way — by having
no class that is truthful, not by skipping the classification step.

### Build-gated built-ins (compile-time conditional)

Two built-ins have their condition decided at *compile* time, not at session
setup — a third kind of conditionality next to "always on" and
"host-injected":

| Tool | Present when | Authority | Notes |
|---|---|---|---|
| `code_execution` | the binary was built with `-tags terva_scripting` (release builds are; `terva-min` and a plain `go build` are not) | local read-only | runs a short JavaScript program with `read`/`grep`/`glob` exposed as functions; only `print`ed output returns, so N-step read-only lookups cost one tool result. Read-only **because** every binding is — the classification follows the binding set, and each host call a script makes still passes the normal permission gate. Sits in the lazy group `scripting` under `lazy_tools`. See [scripting.md](scripting.md). |
| `code_execution_mutating` | the same tag — both tools ship or neither does | mutating (workspace writes) | the same engine and the same gated crossing, with `write`/`edit` added to the binding set. A **separate tool rather than a flag** on `code_execution`, so authority stays a property of the tool: the read-only sibling keeps its class unconditionally, and this one is simply never registered read-only, which is what keeps it out of a `plan`-mode registry entirely. **No `bash`** — a command string is authority the pre-check cannot read, and with it the tool would be `bash` with extra steps. Before running, it walks the script's AST and reports the calls it will make (`read x5, write x2`); a script it cannot account for — `eval`, global-object reach, `with`, aliasing or shadowing a binding — **does not run at all**, refused before the first binding call rather than warned about. Its own lazy group `scripting_mutating`, so activating read-only scripting never also hands over the tool that writes. See [scripting.md](scripting.md). |

A build without the tag has no trace of either tool: nothing registers, no
config key exists to turn them on. The tag exists because the embedded JS
engine costs ~6 MB of binary — capability follows the build, and the gate
semantics stay in the permission tables like every other tool.

### Standard extensions (opt-in, terva-blessed)

These are the designated official standard extensions. They run as trusted
local code; each must meet the acceptance bar below before being treated
as fully blessed (some promotion work is still tracked in the bucket-2
plan).

Two of them ship in the built-in **core pack** (`packages/agent/packs/core.json`,
installed by `terva ext pack install`): `index` and `web`. That pack is the
blessed set — a starting point an operator opts into, not something terva loads
on its own. It offers nothing superseded, which
`TestTheCorePackOffersNothingSuperseded` keeps true.

- **Index** — `index` (`github.com/terva-sh/terva-ext-index`): a workspace code
  index and search. It exists to replace repeated `bash grep`/`rg` sweeps — and
  the whole-file reads they lead to — with a structured, indexed lookup: exactly
  the "replaces a risky/verbose `bash` pattern" case the candidate checklist
  below asks about.
- **Worktrees** — **folded into core built-ins** (the `worktree_*` five: see
  the git-conditional note under the core table above). The standalone
  `terva-git-worktree` extension is superseded — an installed copy is skipped
  at load with a pointer — and its state migrates on first touch (existing
  checkouts stay valid at their extension-era paths). `--swarm-worktrees`
  now leases directly from the built-in engine.
- **Memory** — **folded into core built-ins** (the `memory` tool, its injected
  block, `/memory` and the status glance; see `docs/proposals/memory-in-core.md`).
  The standalone `terva-ext-memory` extension is superseded — an installed copy
  is skipped at load with a pointer, and `ext doctor` recommends removing it.
  Removal is safe: it deletes the extension directory only, so
  `ext-data/memory/` survives and the built-in copies it forward on first use.
  It remains the reference case for the **local-data** authority — a store
  confined to `$TERVA_HOME`, never the user's workspace, which is what makes
  that class auto-allowable in the first place.

  Each scope has **two tiers**, split by which side of the prompt cache they sit
  on (`docs/proposals/memory-archive-retrieval.md`):

  | | active | archived |
  |---|---|---|
  | verbs | `add` / `replace` / `remove` | `archive` / `search` / `recall` / `promote` / `forget` |
  | where it rides | the cached system prefix, every request | the uncached per-turn tail, only when its keys match |
  | shape | one terse line, 1024 runes | multi-line, 8 KiB |
  | scope cap | 16 KiB project / 4 KiB user | 2 MiB per scope |
  | on disk | `memory.md` / `user.md` bullets | `archive/<id>.md`, YAML frontmatter + body |

  Archiving is a cache split, not a file move: an archived entry costs nothing
  until a turn's own words reach it, which is why the archive can be two orders
  of magnitude larger. The price of not being always-on is a **retrieval spec** —
  `keys`, optionally `secondary_keys` — supplied by whoever archives the entry.
  Key on what someone would *type* when they need the fact, not on the
  identifiers inside it: the entry holds the cause and the question describes the
  symptom, and an entry keyed on its own vocabulary is measurably the way this
  fails. Matching is `lore.Select` (whole words, scan depth 6, a per-turn token
  budget), so activation, priority and budget behave exactly as they do for lore.
  Archive files are ordinary markdown and hand-editable; a file that will not
  parse is reported by the tool rather than skipped, because an entry that cannot
  fire has no other symptom.
- **Web** (adopted; niceties pending — bucket-2 Phase C) — `web_search`/`web_fetch`/
  `web_images` are implemented by the hardened `zot-web` extension
  (`github.com/terva-sh/zot-web`), which loads under terva via the preserved
  zot wire protocol and **ships in the core pack as `web`**. The
  remaining niceties (network-read authority declaration, host egress policy, a
  `terva_version` handshake adapter) are tracked in
  `docs/plans/standard-tools-bucket2.md`. Until it
  declares `network-read`, its bare legacy `read_only` bool is what lets
  `workspace` mode auto-allow a web fetch — the gap that declaration closes.
- **Tasks** — **promoted to core built-ins** (see the table above); the
  standalone `terva-tasks` extension is retired, and legacy boards migrate
  forward on their next write. (The historical reference implementation
  remains at `examples/extensions/todo/`.)

### Recommended MCP presets (docs + starter config only)

Browser/devtools (Playwright, Chrome DevTools), docs search, GitHub,
Sentry, Figma, database tools. These depend on local runtime/credentials
or third-party services and stay outside core. Several become much cleaner
once MCP gains an HTTP/OAuth transport (bucket-2 Phase D).

## Non-negotiables

- No tool without a permission story and an explicit authority class. The
  one deliberate exception is `terva_restart`, and it proves the rule: it is
  unclassified *because* no class is truthful for "replace the process image",
  and being unclassified is what makes it always prompt (see above). Absent a
  class, a tool must fall through to the side-effecting default — never to an
  auto-allow.
- Untrusted layers (project config, extension bundles) may only **restrict**
  — never grant new authority.
- Respect Workspace Trust: untrusted project-local extensions, skills,
  hooks, MCP, and context files must not execute or inject authority.
- Prefer structured tools over telling the model to shell out for common
  read-only operations; prefer `edit`/`write` over shell redirection.
- Treat web/external content as an untrusted prompt-injection surface.
- Keep tool results capped and pageable (offset/cursor).
- Preserve one-core/many-frontends: TUI, RPC, JSON, ACP, connectors, and
  swarm observe the same event/policy semantics.
- Headless/RPC behavior must be explicit: a tool that needs interactive
  approval or a user answer must emit a host-answerable event or fail with
  a model-readable refusal — never silently hang or assume a human.

## Candidate-tool checklist

For each proposed tool, answer:

- **Frequency/replacement** — needed in most sessions? Does it replace a
  risky/verbose `bash` pattern? Already table-stakes elsewhere? Could a
  skill do it without a new primitive?
- **Authority** — which class (above)? Which approval modes auto-allow,
  ask, hide, or refuse it? Should it be unavailable in `plan`? Does the
  jail need to mediate paths/commands/network destinations?
- **Token cost** — how long must the description be to steer safe use?
  Will the prompt cache amortize it? Can it live behind an extension so
  only opted-in sessions pay? Are results compact, capped, resumable?
- **UX/lifecycle** — progress events? cancellation? a TUI panel/dialog?
  sane behavior in `-p`/`--json`/RPC? interaction with swarm/worktrees?
  attribution when multiple subagents are active?
- **Implementation fit** — cross-platform? external binaries? long-running
  children? credentials? Is MCP the better boundary? Does it fail soft
  when dependencies are missing?

## Acceptance criteria

### A new core tool

- Implementation under `packages/agent/tools/`, registered in
  `BuildToolRegistry`; sandbox pointer rebound in `Resolved.UseSandbox`.
- Bounded JSON schema; description with concrete safety steering.
- Read-only classification where appropriate
  (`readOnlyTools`) and first-party classification (`builtinTools`) in
  `packages/agent/build/permissions.go`; added to the system-prompt tool
  order in `toolSummaries`.
- Authority class documented here.
- Permission-policy tests, including plan/headless allow/refuse behavior.
- Jail/sandbox tests if it touches paths or commands (symlink and
  nonexistent-parent escape cases where relevant).
- Result cap/truncation/cursor tests for long output.
- RPC/JSON event compatibility; sane TUI rendering for long results.
- Docs in `docs/cli.md` (+ this file's table) and any relevant context docs.

`grep`/`glob` are the worked example of this bar — see
`packages/agent/tools/{grep,glob,walk}.go` and
`packages/agent/tools/grep_glob_test.go`.

### A standard extension

- An explicit statement that the subprocess is trusted local code unless/
  until per-extension sandboxing exists.
- `extension.json` with restrictive **suggested** permissions only; no
  bundle/project layer may grant authority.
- `read_only: true` only on genuinely side-effect-free tools; network-read
  tools carry an explicit docs/permission stance.
- Context contribution only when genuinely useful and compact; tool-call
  interception only when necessary.
- No network/credential/filesystem side effects at startup beyond
  registration/handshake; none afterward without explicit user config or
  tool invocation.
- Workspace-trust behavior documented and tested for project-local
  installation; logs/errors fail soft, not fatal.
- Headless/RPC behavior documented for every interactive or
  approval-dependent workflow.
- Docs covering install/load, disable/uninstall, permissions, examples,
  security notes, and support status.

## References

- Policy ladder: `packages/core/policy.go`, `packages/agent/build/permissions.go`
- Tools: `packages/agent/tools/`
- Conditional-tool injection: `packages/agent/workspace/workspace_session.go`
- Swarm/worktree: `packages/agent/swarm/`, `--swarm-worktrees`
- Dispatch skins: `swarm_spawn`/`actor_spawn` in `packages/agent/tools/`, over `packages/agent/swarm/`; cast wiring in `packages/agent/build/actorcast.go`
- MCP: `packages/agent/mcp/`, [mcp.md](mcp.md)
- Extensions: [extensions.md](extensions.md)
- Permissions/jail: [permissions.md](permissions.md), [tui.md](tui.md)
