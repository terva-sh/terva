# Permissions: approval modes and rules

Two orthogonal controls decide what the agent may do:

- the **approval mode** and **permission rules** decide *whether a tool
  call runs at all* (this page);
- the **sandbox** (`/jail` in [tui.md](tui.md)) bounds *what a running
  tool can touch* (writes under granted workspace roots, command heuristics).

They compose: a call must pass the approval layer first, then run
within whatever the sandbox permits. Keeping the axes separate is
deliberate: a single conflated "trust level" is the known failure
mode in this design space. The interactive default pairs them: the
`workspace` approval mode trusts your built-in tools *because* the
sandbox (jailed by default) bounds their **writes** to the working
directory and any peer directories the user explicitly grants.

## What the jail does and does not confine

Use `--add-dir /absolute/peer` to grant writes in a peer directory for this launch.
Repeat the flag to add more peers. The default grants no peer directories.
terva validates each directory and rejects writes through symlinks that escape the granted roots.
The permission gate still decides whether each tool call may run.
The permissions inspector shows the workspace write roots in the TUI and web client.
Relative tool paths, shell startup, project configuration, and context discovery use the primary working directory.
Use absolute paths for peer files. Project configuration cannot grant peer directories.

ACP honors `additionalDirectories` on session creation and load.
ACP clients must supply absolute peer paths on each creation or load request.
An empty or omitted list grants no session peers; process-level `--add-dir` grants still apply.
ACP records canonical session peer paths for display. Transcript metadata cannot grant write authority.
Session listings omit these paths so clients cannot mistake historical metadata for current grants.
Session export and import discard machine-specific peer grants.
CLI launches must supply `--add-dir` again when they resume a session.
The experimental `terva_gosh` in-process shell remains confined to the primary directory.
The default host shell can enter peer directories.

The jail is a **write** boundary plus a set of command heuristics. It is
deliberately not a read boundary:


- **Writes** (`write`, `edit`, image output, chat attachments) are confined
  to the working directory and explicit peers, plus `$TERVA_HOME/handoffs/`, the one terva-owned
  surface that exists to be agent-written (the built-in `handoff` skill parks
  session-to-session documents there; requiring `/unjail` for a built-in
  skill would make the jail an obstacle to route around).
- **Git's own files are refused** to `write`, `edit` and image output, jailed
  or not: any path with a `.git` component, as given or after symlinks
  resolve. That covers a repository's `.git` directory and a linked
  worktree's `.git` file. A hook, a filter driver in `config` or
  `info/attributes`, `core.fsmonitor`, or a `.git` file pointed at another
  gitdir all run a command the next time git runs there, and git runs there
  from the daemon (the worktree engine) and from you. `write` needs no
  approval in `auto-edit`, so the change has to go through `bash`, which asks.
  Names that only contain `.git`, such as `.gitignore` or `.github/`, are
  ordinary files. The check is by path, so it does not cover git files that
  sit outside a `.git` component: a hooks directory named by
  `core.hooksPath`, or a bare repository whose directory has another name.
  Keep those out of a writable checkout, or set them through `bash`.
- **Ticket stores** are written through the git-ticket library rather than
  through `write` or `edit`, so the jail's write boundary never sees them. The
  store a workspace discovers sits inside the working directory anyway. A
  user-level store named in user configuration sits outside it by design: see
  [Ticket stores outside the workspace](#ticket-stores-outside-the-workspace).
- **Reads** (`read`, `grep`, `glob`, `share_file`) are not confined. The
  `bash` tool has never been path-jailed and cannot be, short of not
  shipping a shell, so a refused read was always one `cat` away. Enforcing
  containment on one tool and not the other confined nothing and cost turns:
  the model would hit the refusal, then route around it through `bash` on the
  next turn.
- **Secrets are denied outright**, to reads *and* to `bash`, jailed or not:
  `auth.json`, `config.json`, `trusted.json`, `unjailed.json`, `sessions/`,
  `swarm/`, `logs/` (except `ext-*.log`), and `shared/`. Every key file is
  denied by name wherever it sits under `$TERVA_HOME`: `secrets.key`, and each
  talkoot's `room.key`, which seals its room log, with the `room.head` that
  records the log's end and the `room.head.tmp` it is written through. `/unjail` does not
  lift these. Like the rest of the sandbox this is a speed bump rather than a
  boundary (a runtime-assembled path or an interpreter walks past it), but
  it is the same speed bump on every route.
- **Transcripts have one reader.** `sessions/` and `swarm/` stay denied above,
  and `session_inspect` is carved back out for the `.jsonl` files inside them.
  `read`, `grep`, `glob`, `share_file` and `bash` are all still refused, and
  the refusal they give now names `session_inspect` instead of ending at a
  dead end.

That asymmetry is deliberate, and it is worth stating why, because "let a tool
open the transcripts" reads like a hole. Denying them bought no confinement:
`cp` moved a transcript out of `$TERVA_HOME` the whole time, and the
shell-argument check matches literal paths, so `$TERVA_HOME/sessions` walked
straight past it. What the denial produced was a detour. Every analysis of a
session copied the file out first and then read the copy with no redaction at
all.

Opening the sanctioned reader while the raw ones stay shut therefore reduces
what leaks. `session_inspect` redacts credential shapes and bounds its output;
`cat` of a copy does neither. The carve-out is narrow on both axes: only
`.jsonl`, and only under those two roots, so `auth.json`, the secrets key, the
web token, `logs/` and `config.json` are refused on that route exactly as they
are on every other. It holds while jailed, because the jail confines writes and
routes risky reads past other safeguards, and a redacted, bounded read of
terva's own state is neither.

Project scoping went with it, for the same reason. `session_inspect` resolves a
session id in any project under `$TERVA_HOME`, trying the current project
first, and it no longer refuses a swarm sub-agent spawned elsewhere. Diagnosing
a cost problem in one project from another is exactly when you need this, and
it was the case with no workaround short of copying the file out.
`session_search` still enumerates this project only. Sweeping every project for
text is a different question, and it stays where it was.

**Listing is a third surface, and it is the one that answers "what exists".**
Resolving an id you already hold does not help a reader holding only a
question, so `session_list` enumerates the store: `scope: "project"` by
default, and `scope: "all"` for every project in `$TERVA_HOME`. An unknown
scope is refused rather than narrowed, because a caller who asked for more and
silently got less would read the short list as "there is nothing else".

It exposes less than the two readers above. A row carries the session id, the
time of the last change, the size, and the working directory, and nothing from
inside the conversation. The tool opens no transcript beyond the bounded
opening meta row of each listed row, which is where the working directory comes
from. Titles and content stay behind `session_inspect`, which redacts.

This is not new authority. `ls` over `$TERVA_HOME/sessions` was never actually
prevented, and the deny list never stopped a shell from counting the files. It
is a new default-visible surface, which is the part worth saying plainly: a
jailed agent can now see that other projects exist on this machine, and name
their working directories, without being told. If that is wrong for your
install, the tool is an ordinary built-in and a permission rule can deny it.

`grep` and `glob` check each candidate file against the sensitive-path rules.
A search from a parent directory skips denied files and retains allowed
exceptions, such as extension logs. A direct request for a denied path fails.
These checks also apply when the session starts unjailed.

## Approval modes

Pick one with `--approval <mode>`, or set a persistent default in
`$TERVA_HOME/config.json` (`"approval": "ask"`). Flag beats config
beats the built-in default; `--no-yolo` is a compatibility alias for
`--approval ask`.

In the TUI, `/settings` switches the mode live for the **current
session** (plan immediately withholds the mutating tools). It does
**not** write your config. The approval mode is a security posture, not a
preference, so it is never saved as a side effect: the persistent default
comes only from the `approval` config key or the `--approval` flag, so
the picker can never silently pin a mode (and a future default change is
never masked by an accidentally-saved value). `/jail` and `/unjail`
follow the same rule: session-only unless you explicitly say
`always` ([Unjailing a directory for good](#unjailing-a-directory-for-good)).
The current mode shows
in the status bar; `/permissions` shows the active mode and rules, and
lets you revoke this session's grants (see below).

| mode | behavior |
|---|---|
| `yolo` | every tool call runs without asking. `ask` rules don't prompt here, because yolo never prompts, but `deny` rules still block |
| `workspace` | **the interactive default.** Every built-in tool (`read`/`write`/`edit`/`bash`, bounded by the sandbox) runs freely, and so does any *extension/MCP* tool classified read-only; foreign tools that can have side effects (a writing extension tool, a mutating MCP tool) ask. The trust axis is origin: your own tools vs foreign code. A foreign tool with the `network-read` authority still asks, even though it reads nothing locally. That includes a read-only tool on a remote MCP server, reached directly or through `terva-mcp-bridge` (see below) |
| `auto-edit` | read-only tools (`read`, `terva_status`, `skill`) and the file editors (`write`, `edit`) run freely; everything else asks: `bash`, extension tools, chat sends. While routines are on, a talkoot member's two calls to the person, described below the table, do not ask |
| `ask` | every tool call asks, read-only included (exactly what `--no-yolo` always did). While routines are on, a talkoot member's two calls to the person do not ask |
| `plan` | only read-only tools run, and mutating tools don't even enter the registry, so the model is steered to present a plan. Mutating calls that arrive anyway are refused with a model-readable reason, not prompted. The one exception is a talkoot member's `talkoot_send` and `talkoot_handoff`, because a planning member's plan reaches its team as an envelope. Plan does not prompt for them, and your deny and ask rules still apply. An envelope starts the recipient's turn in the recipient's own posture, so a planning member can wake a member that writes. Write an ask rule for the two tools to approve each send |

**While routines are on, two calls of a talkoot member go to the person
with no approval card.** `talkoot_routines` in your user configuration turns
routines on, and the foot of the rail in the Talkoot app says so while this
holds. With routines off, `ask` and `auto-edit` ask for both calls, as they
did before.

One call is `talkoot_routine`, which only draws the card that you approve.
The other is a `talkoot_send` whose every recipient is `human` or a
`human:<name>` address. It writes to you, starts no turn and spends no
budget. A routine's member works with nobody present, and a card then ends
the work at its deadline, so such a member could not say what a run found.
`plan` already ran both calls, so `ask` and `auto-edit` were the stricter
postures and the looser ones for them.

A `talkoot_send` with a member among its recipients still asks, because it
starts that member's turn. An `ask` rule or a `deny` rule that you write for
either tool still decides the call. Write an `ask` rule for `talkoot_send`
when the team's messages to you leave the machine through a
[chat bridge](talkoot.md#reach-the-team-from-chat) and you want to read each
one first. A member whose turn read hostile text can put what it read into
such a message.

**Defaults differ by run mode.** An interactive session defaults to
`workspace` (a human is present to answer the foreign-tool prompts) and
is **jailed** by default so the built-in tools it trusts are confined
to the working directory. Headless modes (`-p`, `--json`, `--rpc`,
swarm agents) default to `yolo` and unjailed, so unattended automation
isn't surprised by prompts (which it can't answer) or path confinement.
Set `--approval` / config `approval` and `--jail`/`--no-jail` to
override either.

## The approval classifier

**Off by default.** When you turn it on, a cheap model screens the tool
calls that would otherwise prompt you, and answers some of them.

It is a **third axis**, not a sixth approval mode. The mode decides
*whether* a call runs, the sandbox bounds *what* it may touch, and the
classifier decides only **who answers** the question the mode raised: you,
or a model standing in for you. So it composes: `workspace` mode with
screening on is still `workspace` mode, and the status bar shows both.

> **This is not a security boundary.** It judges a call on its face, and a
> model's judgement is neither sound nor reproducible. It catches an agent
> that is *honestly mistaken*: the wrong directory, an overeager `rm`, a
> migration that looked reversible. That is the common failure. Against a
> **prompt-injected** agent it is close to worthless: whoever controls the
> agent controls the call being judged. Keep your `deny` rules, the confirm
> gate and the jail *underneath* it, never behind it.

### The two modes

| mode | what it may do | status bar |
|---|---|---|
| `off` | nothing; every prompt reaches you (**the default**) | — |
| `screen` | **refuse** a call, but never permit one. An approve verdict is discarded and you are asked anyway | `⚖ screened` |
| `approve` | also **answer yes on your behalf**, so the prompt never happens | `⚖! approving` |

`screen` can only ever *subtract* authority, so it is safe to leave on: the
worst a wrong verdict costs is a call you reissue. `approve` grants
authority you never saw, which is why it renders in the same warning colour
`yolo` gets: the riskiest posture must never be the one with no badge.

### Turning it on

For one run, from the command line:

```bash
terva --classifier screen
```

This is the cheapest way to try it, and the cheapest way out: `--classifier
off` turns screening off for a single run when a screener starts refusing
something it should not, with no config to edit and put back.

For every run, in your own `$TERVA_HOME/config.json`:

```json
{
  "classifier": {
    "mode": "screen"
  }
}
```

The flag wins over the config key, in both directions. It overrides the
**mode only**: `provider`, `model`, `host_model` and `timeout_ms` keep coming
from your config, so `--classifier screen` on a machine with no classifier
block at all still resolves the cheap rung described below. There is
deliberately no flag for the screening model: argv may choose among the
postures your config already permits, and may not point screening at an
endpoint of someone else's choosing.

A mistyped mode is refused at startup rather than warned about. Every *other*
classifier failure abstains into "screening stays off", which is right for a
provider outage and wrong for a typo. `--classifier sceen` is an unambiguous
request for screening, and answering it with a line that scrolls past would
leave you believing you had it.

**A project's `.terva/config.json` can never enable it, even in a trusted
workspace.** Unlike the project provider/model override, this is not a trust
gate but an absolute one: `approve` lets a model permit tool calls on your
behalf, so a cloned repository that could switch it on would have a
remote-code-execution foothold dressed as a preference. `/settings` shows the
mode read-only for the same reason: the web panel is reachable over a
network, and that is not a switch to expose to a remote peer.

| key | meaning |
|---|---|
| `mode` | `off` (default), `screen`, or `approve`; `--classifier MODE` overrides it for one run |
| `model` / `provider` | override which model screens; both empty is the intended shape |
| `host_model` | permit falling back to the full host model when no cheap rung resolves (default off) |
| `timeout_ms` | bound one screening call (default 8s) |

### It runs cheap by default

Screening happens on gated calls for the whole session, so an expensive
default would be silent and permanent. With no `model` set, the classifier
resolves the **weak rung of your `swarm_tiers` ladder**, the same ladder
`swarm_spawn`'s `tier` uses, which composes your overrides over terva's
built-in family tables. Most providers answer with nothing configured; on an
Anthropic host it picks a Haiku rather than the session's Sonnet.

```bash
terva models tiers    # exactly what will be picked, per provider
```

A provider with neither a built-in table nor an override (the gateways:
`opencode-go`, OpenRouter and LiteLLM) resolves nothing. Screening then
**stays off and says so** rather than quietly billing host price on every
gated call. Set `swarm_tiers.<provider>.weak`, or `classifier.model`, or
accept the cost with `classifier.host_model`.

### Where it sits, and how it fails

```
pre-tool-use hooks → deny/allow rules → session grants → CLASSIFIER → your prompt
```

The position is the design. It runs **after** your rules, so an explicit
`deny` outranks any verdict a model can produce. It runs **after** session
grants, so a tool you already answered "always" for costs nothing. It runs
**before** the prompt, because replacing the prompt is the point.

**Every failure is an abstention**: a timeout, a provider outage, an
unparseable answer. An abstention just shows you the prompt you would
have seen anyway. Failing *closed* was the alternative and it is worse: a
provider blip would refuse real work while handing the agent something
indistinguishable from a policy decision, which it would then try to route
around.

### Headless runs

This is the case it exists for. A headless mode (`-p`, `--json`, `--rpc`, a
swarm sub-agent) has no prompt to show, so a call needing confirmation is
refused outright today. With `screen`, refusals at least carry a reason the
agent can act on; with `approve`, the work proceeds. Headless runs print a
stderr note naming the mode, since they have no status bar to carry the
badge.

## Unjailing a directory for good

Some work genuinely lives outside its own folder: a dotfiles repo that
writes into your home, a build that emits above the tree. Passing
`--no-jail` every time is a poor way to say so, and `/unjail` forgets on
restart, so the decision can be recorded per directory:

```bash
terva unjail                    # this directory, from now on
terva unjail --parent ~/src     # and everything under it
terva unjail --list
terva jail                      # back to confined
```

In the TUI, `/unjail` is still session-only; `/unjail always` records it,
and `/jail always` forgets it again.

The list lives in `$TERVA_HOME/unjailed.json` (mode 0600), **outside any
project**, for the same reason the trust list does: a repo must not be
able to unjail itself by shipping a file. `--jail` and `--no-jail` still
settle a single run, so a saved rule never overrides what you just typed.

**Unjail is not trust, and neither implies the other.** Trust decides
whether a project's *own code and instructions* load; unjail decides
*where tools may write*. A repo you trust enough to run is not thereby a
repo you want writing to your home directory, and a directory you need
to write outside of is not thereby one whose code you want executing.

**One combination is worth saying out loud**, and terva says it at
startup rather than leaving you to infer it: a saved unjail rule
*together with* an auto-approving mode (`workspace`, `yolo`) means the
built-in tools may **write** anywhere without asking. The jail is what
made `workspace` safe to leave alone: built-ins are auto-approved
precisely *because* their writes were confined. Take the jail away and
that premise goes with it. (Reads are already unconfined either way; the
secret deny list above holds regardless.)

Tools that are not classified read-only, including every extension
tool, are treated as mutating. In headless modes (`-p`, `--json`,
`--rpc`, swarm agents) there is no prompt: anything that would ask is
**refused** with a model-readable reason instead. That keeps the
refuse-by-default posture while still letting `plan` mode and explicit
allow rules drive useful headless automation.

**Bot mode is the exception**: `terva bot run` defaults to `workspace`
and jailed, like an interactive session, because the paired owner is
there to answer. A tool call that needs confirmation is posed as a question in
the paired chat (buttons where the connector supports interactive
asks, a numbered plain-text question everywhere else) and the turn
blocks on the answer. Only the paired owner may answer; an unanswered
question denies after two minutes (fail closed); and "always (this
tool)", a durable session grant, requires a platform-attested answer
(a button click, not parsed text; a text "always" allows once and says
so). See docs/connectors.md for the wire side.

## Authority classes

A tool's *authority* is a finer classification than the read-only/mutating
split, because "side-effect-free" is not one thing. A web fetch reads
nothing on the local machine yet can leak data, hit a remote server, or
reach a private network, so folding it into the local-read auto-allow
would be wrong. The classes (`permission.Authority`):

| Authority | Meaning | Example |
|---|---|---|
| `local-read` | reads files/state (not the secret deny list); no process/network/external effect | `read`, `grep`, `glob`, `terva_status`, `session_inspect`, `skill`, `worktree_list`, `code_execution` (read-only exactly as long as every function it exposes is; see [scripting.md](scripting.md#permissions)) |
| `local-data` | reads **and writes** the tool's own host-managed data store (for an extension, its `$TERVA_HOME/ext-data/<name>`) and nothing else: never your workspace, a process, the network, or an external service. Auto-allowable like `local-read`, because the write never leaves private, host-controlled storage | `task_create`/`task_update`/`task_list`/`task_archive`; a memory/notes extension tool |
| `workspace-mutation` | writes files / edits workspace state | `write`, `edit`, `code_execution_mutating` (the same three read functions plus `write`/`edit`; classified by its widest binding, never read-only; see [scripting.md](scripting.md#permissions)) |
| `process-execution` | runs commands / subprocesses | `bash` |
| `network-read` | fetches URLs / search results (can leak, log, reach private nets) | a web-fetch extension tool |
| `external-mutation` | writes to third-party APIs, sends messages, changes remote resources | a chat-send / PR-open tool |
| `user-interaction` | blocks to ask the user; no other effect | `ask_user_question` |

One built-in sits outside the taxonomy: `activate_tools` changes only
which tool schemas are advertised this session, which is visibility and
never authority, so it auto-admits like a read, and every tool it reveals
still faces its own gate when actually called.

How each mode treats a class:

| Authority | `plan` | `auto-edit` | `workspace` | `ask` | `yolo` |
|---|---|---|---|---|---|
| `local-read` | run | run | run | ask | run |
| `local-data` | run | run | run | ask | run |
| `workspace-mutation` (built-in editors) | refuse | run | run (built-in) | ask | run |
| `process-execution` (built-in `bash`) | refuse | ask | run (built-in) | ask | run |
| `network-read` (foreign) | refuse | ask | ask | ask | run |
| `external-mutation` (foreign) | refuse | ask | ask | ask | run |
| `user-interaction` | run | run | run | run | run |

Two things to note. First, `workspace` trusts *first-party built-ins* by
origin (so built-in `bash`/`write`/`edit` run), but a **foreign**
`network-read` or `external-mutation` tool asks. Declaring `network-read`
(rather than the legacy `read_only` bool) is what keeps a web tool from
being mistaken for a local read. Second, `user-interaction` is permitted
in every mode including `plan`: gating a clarifying question behind an
approval prompt is nonsensical.

Extensions declare their class via the `authority` field on
`register_tool` (`ext.WithAuthority` in the Go SDK). A declared authority
decides read-only classification (`local-read` and `local-data` are
auto-allowable, everything else is not), and it **wins over** the legacy
`read_only` bool, which is consulted only when the authority is empty. An
unknown value is treated as side-effecting.

MCP has no authority field, so terva derives a class from the tool's
annotations and its server's transport:

| MCP tool | Class |
|---|---|
| `readOnlyHint: true` on a `stdio` server | read-only, like the legacy `read_only` bool |
| `readOnlyHint: true` on an `http` server | `network-read` |
| `readOnlyHint: true` on a `stdio` server marked as a remote relay | `network-read` |
| `readOnlyHint: true` and `destructiveHint: true` | side-effecting |
| no `readOnlyHint` | side-effecting |

A remote read-only tool reads nothing on this machine, but every call sends
the arguments the model chose to that server. That is the `network-read`
case, so the tool asks in `workspace` and `auto-edit` and does not register
in `plan`. A `stdio` server's read-only tool keeps the local-read treatment,
because installing the server was already consent to run it.

A `stdio` server can mark itself as a relay to a remote server. The mark is
`"terva.sh/remote": true` in the `_meta` of its `initialize` result.
`terva-mcp-bridge` sets it, and replaces any value that the remote server
sent at that key. terva then treats the server's read-only tools as
`network-read`. The mark can only add prompts, so terva needs no reason to
trust it. A server that sets it falsely asks more, and a server that leaves
it out keeps the local-read treatment. A third-party bridge such as
`mcp-remote` sets no mark, so [mcp.md](mcp.md#oauth-servers-terva-mcp-bridge)
gives an ask rule for its tools.

An allow rule trusts a remote tool you rely on;
[mcp.md](mcp.md#permissions-example) shows the shape.

An extension reload or MCP reconnect rebuilds read-only classification from
the current tools. A tool that becomes mutating loses its previous automatic
approval. Removal also removes its classification.

Each turn keeps its tool registry and classification together. A reload takes
effect on the next turn; approval rules and mode changes still apply at once.
Nested script calls use the calling turn's tools. A call whose original
extension or MCP process has stopped fails instead of reaching its replacement.

That precedence is what reconciles the two statements above. Whether
`workspace` auto-allows a foreign tool is decided by its *classification*,
not by its subject matter, so a tool declaring `network-read` asks even if
it also set `read_only: true`. The web extension's `web_search`/`web_fetch`
run freely in `workspace` today only because that extension still ships the
bare legacy bool and has not yet declared an authority; once it declares
`network-read`, which it should and which is tracked in
[standard-tools.md](standard-tools.md), those calls will ask like any other
foreign network tool. **Mark network tools `network-read`, not `read_only`.**

### Ticket stores outside the workspace

The `ticket_*` tools write through the git-ticket library, not through `write`
or `edit`, so the jail's write boundary never applies to them. What keeps the
default store local is `ticket.DiscoverWith`, which walks up from the working
directory and stops at the git root, because a store above that root belongs to
a different repository. Outside a git repository that break never fires and the
walk reaches the filesystem root, so a store at `~/.tickets` is already
reachable from any non-git directory under your home.

`ticket_stores` in user configuration names further stores, each one a name and
a path. A store named there can sit anywhere, including outside the working
directory. The key does not exist on the project layer, so a cloned repository
cannot name a path. `pack_registries` follows the same rule: configuration is
inside the trust boundary and an arbitrary path is not.

A write to a store outside the workspace stays `workspace-mutation`, the class
such a write already carried. The act does not change and only its destination
differs, so the mode table above applies with no special case. The write runs
in `workspace` and `auto-edit`, and `plan` refuses it. It does not prompt,
because the user records consent when they name the path.

`local-data` would have been the wrong class. It is auto-allowable in every
mode precisely because its destination is host-controlled, at
`$TERVA_HOME/ext-data/<name>`, and no tool argument can move it. A path from
user configuration is not host-managed, so giving it `local-data` would widen a
class that never asks until it covers a destination the host did not choose.

The feature ships off. With no `ticket_stores` key the tools discover the
workspace store as before. When the key is present, `ticket_store` switches the
active store for the session, the other ticket tools work against the most
recent selection, and the ticket context card names the active store.
`ticket_store` selects among destinations that configuration already named, so
it behaves like `activate_tools`: it changes what the tools reach, and it
grants no authority of its own.

Moving a ticket from one of these stores to another is not a tool. No
`ticket_*` tool reads two stores at once, and none carries a ticket between
them: `ticket_store` changes which single store the tools work against, and the
qualified-ref lookup stops at naming a store it does not open. The move is a
command-line operation, `terva ticket export` and `terva ticket import`, and
[cli.md](cli.md#moving-a-ticket-between-stores) documents it with the three
landing modes and what each one keeps.

That boundary is worth stating because the gate for a move is not obvious. An
export reads one store and writes files outside it, and an import writes a store
the session may never have selected. Leaving the move on the command line keeps
both acts under the permissions the shell already carries, rather than inventing
a class for a tool that does not exist.

### Outbound network safety (egress guard)

`packages/egress` is terva's shared SSRF and private-network guard. It blocks
loopback, private, link-local (including the `169.254.169.254` cloud-metadata
endpoint), unique-local, and multicast destinations by default. It enforces
that at dial time, so DNS rebinding cannot slip past a pre-check that passed.
It re-checks every redirect hop, and it strips the `Authorization` header when
a redirect crosses to a different host.

It strips that one header and no other. A credential in a custom header rides
the hop to the new host. That is deliberate, because the guard cannot tell
which of a caller's own headers carries a secret. Put a credential where the
guard knows to remove it. For an MCP server that means `auth.bearer_env` and
not a `headers` entry, which [mcp.md](mcp.md) states at the point of
configuration.

Three host-side fetches route through it:

<!-- egress-consumers:start -->
- `packages/agent/mcp/mcp_http.go` for the HTTP MCP transport.
- `packages/agent/extpack.go` for extension packs.
- `packages/agent/workspace/workspace_cards.go` for character cards.
<!-- egress-consumers:end -->

That list is a gate and not prose. `TestEgressDocumentedConsumersMatchCode` in
`packages/testsupport` fails when it disagrees with the imports in the tree,
and `TestEgressGuardHasImporters` fails if the last consumer goes away. Both
exist because this section has been wrong in both directions. It once promised
protection from a package that nothing imported. It later called that same
package staged while three files used it.

A caller can allowlist a host for an intentional local service, and it takes
that host from the configuration that names the destination. The HTTP MCP
transport allowlists its own configured server host, so a self-hosted MCP
server works wherever it lives. The pack fetcher allowlists the hosts named in
`pack_registries`, so `terva ext pack install` reaches a registry you named and
nothing else.

There is no global allowlist, and its absence is deliberate. One list cannot
say which fetch it applies to, so a single entry would exempt a host for every
guard in the process, and it would keep granting that long after the reason
expired. A config-named destination stops granting anything the moment you
delete it. `TestEgressAllowlistingStaysPerCallSite` fails when a new caller
appears, so widening the pattern stays a deliberate act.

Read `AllowHost` as a full exemption and not as a softening. An allowlisted
name dials with no IP check at all, because the user named that host on
purpose.

Outbound safety for a component that makes its own connections belongs to that
component. An out-of-process extension such as the web extension keeps its own
SSRF guard, because this in-process guard cannot see the dials of another
process.

## Steering a talkoot

A talkoot is a team of agents that runs without a person at each turn (see
[talkoot.md](talkoot.md)). A client reaches one through
the `talkoot.*` verbs of the control protocol ([controllers.md](controllers.md)).
The verbs that read a talkoot need only the read capability. The verbs that
create one, change its roster, post to it, pause it, or resume it also need
`steer`, a capability of its own. Every role that may prompt holds it, and a
`viewer` does not, so a viewer can watch a room and cannot steer it. A
restricted connection can prompt and still lack `steer`.

A session that a talkoot drives belongs to the team. That is a member's
session, and a recruiter's. A caller without `steer` can read such a session,
and every verb that writes to it or spends on it answers `forbidden`: a
prompt, a queued message, an answer or an approval, a transcript edit, a
rename, and a delete among them. Otherwise a caller could prompt a member into
a send or a roster proposal, and so steer the team through the member. A
worker member's approvals wait on the talkoot's own address, `#talkoot:<id>`,
and an approval there needs `steer` too. A
former member's session stays the team's, and so does the session of a member
that another process runs. The daemon checks the rule for every verb in one
place, before the verb's handler runs. A server that cannot tell which
sessions a talkoot drives refuses such a caller every session write, its own
sessions included, rather than guess.

A member reaches its team through its own tools, `talkoot_send` and
`talkoot_handoff`, which speak as that member. It never needs the verbs. A
Claude Code member calls the same tools through the Talkoot MCP bridge. The
runner opens a socket for that member's worker, and the socket names the
member. Each call takes a native member's path through the daemon: your
pre-tool-use hooks, then the policy a native member in the same posture
holds, your rules included, then your post-tool-use hooks. A call the policy
asks about opens a permission card in the talkoot's inbox. A config that the
daemon cannot read refuses the call. Extension intercepts do not run,
because the worker has no session. The socket's 0600 permissions keep
other users out, and not other processes of your user, as for the approval
socket. A `terva:portable` member's own policy lets the four bridge tools
through in every posture, as a Claude Code member's `--allowedTools` does,
because the daemon asks on its behalf. The
`steer` capability binds a control-protocol caller, though, and nothing
more. A member whose posture lets it run `bash` can run `terva ctl` against
its own daemon, as the daemon's own user, with whatever token that daemon
trusts. It then holds every capability that token grants. This is the same
custody gap the room seal has: a member that runs as the daemon's user can
reach what the daemon can. Posture limits it, since a `plan` member runs no
`bash`, and so does an ask rule on `bash`. The gap closes only when the
daemon's keys sit where a member cannot reach them, which is the work of
secrets at rest, §8.15 (`docs/proposals/secrets-at-rest.md`).

A member's policy also carries one built-in rule, ahead of your rules and in
every posture. It denies every `git ticket` command through `bash`, reads
included. It covers the command quoted, spaced, cased, or after git options. A member works tickets through the ticket tools instead. They write
under the member's own actor, record the member's claims, and refuse a close
of work the member authored. The CLI would write as you. The refusal names the
rule's source as `talkoot`. The rule reads command text, so it steers the
member and does not bind it. A git alias, a shell variable, eval, another
interpreter, or a direct edit of a ticket file gets past it. Each of those
writes a file under `.tickets/`, which the diff shows to the member's reviewer.

## The unattended merge rule

A talkoot [routine](talkoot.md#approve-work-on-a-schedule) starts work that no
person began. That work has an attended agent's authority minus the merge. It
may push its own branch and open a pull request. It never merges, and it never
moves a protected branch (decision 0029, rules 14 and 15).

The rule is a rung of the daemon's own, ahead of your hooks and your rules. A
hook's allow, an `allow` rule, and `yolo` all leave it in place. It reads each
call at the time of the call, because a member works in a chain that a routine
began on one turn and in a chain that you began on the next. In work that you
started the rule has no word, and a merge reaches the approval path that every
other command does. A refusal names `the unattended merge rule` and says what
the rule found, and the audit log records it.

A turn is yours when you typed into it. A report on sub-agents that terva
writes into a member's session is no such input. The turn that it begins is
unattended when the member still works in a chain that a routine began.

The rule refuses a call that does one of these:

| What the call does | Examples |
|---|---|
| Merges through a forge's CLI or a script: the word `pr`, `pull` or `mr`, and then `merge`. `tea` also has `m`, and `glab` has `accept` | `gh pr merge 12`, `tea pulls merge 12`, `glab mr accept 12`, `./scripts/pr.sh merge 12` |
| Runs a recipe whose name says that it merges | `just pr-merge 12`, `make merge-pr` |
| Names a forge's merge endpoint | `curl -X POST https://forge.example/api/v1/repos/o/r/pulls/12/merge`, the GraphQL mutation `mergePullRequest` |
| Pushes to a protected branch | `git push origin HEAD:main`, `git push origin --delete main` |
| Pushes more branches than it names | `git push --all`, `git push --mirror` |
| Pushes with an option that asks the forge to merge when the checks pass | `git push -o merge_request.merge_when_pipeline_succeeds`, `git push -o merge_request.auto_merge` |
| Pushes to a place that the rule cannot read | `git push origin "$branch"`, `git checkout main && git push` |
| Calls a tool of an extension or an MCP server whose name says that it merges: merge with a pull request, a branch or a queue, or the forge's own word for it, such as accept, complete or enqueue | `mcp_github_merge_pull_request`, `mcp_forgejo_merge`, `mcp_gitlab_accept_merge_request`, `add_to_merge_queue` |
| Calls a tool whose name says push, with no branch named or with a protected one | `mcp_git_git_push` with only a `repo_path` |
| Gives a tool a merge endpoint at any depth of its arguments, or a command that the rows above refuse | `{"command": "gh pr merge 12"}`, `{"program": "gh", "args": ["pr", "merge", "12"]}` |

The rule reads a command of such a tool under the name `command`, `commands`,
`cmd`, `script`, `shell`, `bash`, `sh`, `program`, `executable`, `exe` or
`binary`, as a text or as a list of words. It reads a list under `argv` too.
A program under one of those names takes its words from `args`, `arguments`,
`argv` or `params`. A tool whose name says get, list or search reads, and
the rule has no word on its name.

The rule reads the path of an endpoint as curl and the forge clean it. So
`/pulls/12/./merge` and `/pulls/12/%6derge` are the same endpoint as
`/pulls/12/merge`.

The rule does not ask which program gets the words. It refuses
`echo pr merge` too, because `$(echo gh pr merge 12)` runs the merge. A text
in quotes that is one word passes, as in `git commit -m 'pr merge notes'`.

A protected branch is `main`, `master`, `trunk`, and the default branch of
each remote. The rule reads that default from `refs/remotes/<remote>/HEAD`.
Run `git remote set-head origin -a` when that ref is missing or old.

A push that names no branch goes where git's configuration sends it. The rule
therefore reads `push.default`, the upstream of the branch at work,
`branch.autoSetupMerge`, and each remote's push refspec and mirror mark. A
topic branch whose upstream is trunk moves trunk under
`push.default = upstream`, and the rule refuses that push. A line that also
changes the directory or git's configuration leaves the rule with no way to
know where the push goes. So does a line that sets a variable that git reads,
such as `GIT_DIR` or `HOME`, and an option of git that the rule does not
know. It refuses, and it tells the agent to name the remote and the branch.
A `cd` to the top level of the repository changes nothing, and the rule lets
it pass. A `cd` to a directory inside the repository does not pass, because
a repository of its own can be there. Each call of `bash` starts in the
session's directory, so a `cd` in one call does not reach the push of the
next one.

The rule reads each change of the branch in front of a push. A new branch is
certain only when `&&` joins the push to it, as in
`git checkout -b fix/y && git push -u origin HEAD`. With a `;` between the two
the checkout can fail, and the push then runs on the first branch. Such a
push must pass on both branches. It passes on a topic branch, and the rule
refuses it on trunk. A checkout of a branch by its name tells the rule
nothing, because the same command restores a file of that name.

The rule reads a variable that the line itself sets. So
`b=feat/x; git push origin "$b:$b"` passes. A variable from outside the line
is a word that the rule cannot read. So is a variable that the line fills
with `read` or in a loop. A push with such a word is a push that the rule
cannot read.

A push that names a branch with one side, as `git push origin feat/x` does,
goes where a push refspec of the remote sends it. The rule lets it pass when
the remote has no such refspec. In any doubt it asks for both sides:
`git push origin HEAD:refs/heads/feat/x`. A tool of an extension runs its
command in a directory that the daemon does not know, so there the rule
always asks for both sides.

Two keys of `talkoot_routines` in your user configuration add to the rule:

```json
{
  "talkoot_routines": {
    "max": 5,
    "merge_commands": ["^just land\\b", "deploy\\.sh --prod"],
    "protected_branches": ["release", "release/*"]
  }
}
```

| Key | What it adds |
|---|---|
| `merge_commands` | RE2 patterns for a command that merges. Each one is tried against the whole line and against each command in it, with the quotes removed. In a line that the daemon cannot parse, each one is tried at the front of each word |
| `protected_branches` | Branches that such work may not push to. A name may hold the wildcards of a shell |

The rule reads a line inside a line: the argument of `sh -c` and of `eval`,
a here document that a shell reads, and the value of an alias. A shell at the
end of a pipe runs a text that the rule cannot know, so there a word of a
merge or of a push anywhere on the line is enough for a refusal.

A line has bounds, and the rule refuses a line past one. The refusal says
what to do in its place.

| Bound | What the refusal says |
|---|---|
| The bounds of every `bash` command: 128 KiB, and 16,384 marks that can nest | Write a file, and run a short command |
| Four shells inside each other | Run the command with no shell around it |
| 16 names of programs among the words of one command | Run each command by itself |
| A command in a variable that the line fills, where the rule does not see with what, as in `read c; $c` | Write the command itself |

It also refuses arguments that it cannot decode, and an object of a tool
that gives more than 8 commands beside a list of words.

A pattern that does not compile turns routines off, and so does an entry
that is empty. The foot of the rail in the Talkoot app says why. Each pattern that does compile stays in force for work of
a routine that has not ended yet.

**The rule explains, and the forge account binds.** The rule reads the text of
a command, so a shell can get past it. A script that the agent wrote passes,
and so do an alias or a variable that a file sets, and another
interpreter. The rule also does not reach the shell of a member that another
harness runs, such as Claude Code. Its job is
to tell an agent why the merge is not its to make, at each route that it
tries. The guard that holds is on the forge. Run the daemon of unattended
work under a forge account that has no right to merge to trunk, and protect
trunk there. The rule then covers a mistake, and the account covers the rest.

### A sub-agent of unattended work

A sub-agent that `swarm_spawn` starts is a process with a gate of its own.
When a turn that no person began starts one, the daemon marks it, and its
gate then holds the merge rule and
[the review count](#the-review-count-of-unattended-work). The sub-agent reads
the merge rule's two keys from your configuration itself. For a review
request it asks the daemon, so its requests count against the routine of the
chain that started it, as requests of the member whose turn started it.

The daemon writes the mark from the turn at work. The tool takes no argument
for it, so an agent cannot set it and cannot clear it.

Four limits follow from how a sub-agent works:

- **The mark lasts for the life of the sub-agent.** A sub-agent outlives the
  turn that started it. A message that you send it later, from the tasks
  pane, does not clear the mark. Start a new sub-agent from your own turn
  when you want one that may merge.
- **Such a turn cannot start an external worker.** A `backend` names a
  process with no terva gate, so neither rule can reach it. The tool refuses
  the call and says so.
- **A marked sub-agent marks what it starts.** Each agent that its process
  starts gets the mark, and it starts no external worker either. Such an
  agent has no daemon to count for it, so each of its review requests is
  refused.
- **A sub-agent that cannot reach the daemon asks for no review.** Each
  request is refused until the daemon counts again. The merge rule still
  holds, because the sub-agent reads it alone.

## The review count of unattended work

A model review runs on the forge, and the daemon cannot see what it costs.
It can count the requests. Each routine has a `reviews_per_day` limit, and
each review request in work that the routine began counts against it. That
covers the requests of a run and those of the member that reads the run's
report. The daemon refuses the request past the limit, and the count starts
again each day. A restart of the daemon keeps the count, because each
request is a line in the team's room.

A review request has no built-in form, because each forge and each
repository asks for a model review in its own way. You name the commands in
your user configuration:

```json
{
  "talkoot_routines": {
    "max": 5,
    "review_commands": ["^just pr-review\\b"]
  }
}
```

| Key | What it names |
|---|---|
| `review_commands` | RE2 patterns for a command that asks for a review. Each one is tried against each command of a line, with the quotes removed and with no runner such as `timeout` in front. It is also tried against the name of a tool of an extension |

**With no pattern, nothing counts.** The limit then binds nothing, and the
card of a routine and the list of routines each say so.

Anchor each pattern at the front of the command, as `^just pr-review\b` is.
A pattern with no anchor also matches a command that only names the request,
such as a `grep` for it, and that command then spends a request. A pattern
must match one command. The daemon refuses a line that a pattern matches
only as a whole, because it cannot count the requests in it.

The count is taken after your hooks and your rules allowed the call, and
before the command runs. So a call that a rule refused spends nothing, and a
request that then fails on the forge still spent one.

A count must be exact, so the daemon counts only a request that the shell
runs one time, as a command of its own. Two requests that `&&` joins count
as two. For a request in any other place the daemon has no number. It
refuses the call, and it tells the agent to ask in a command of its own:

- a request in a loop or in a function
- a request behind `xargs`, `watch`, `find -exec` or `parallel`
- a request in a shell in a shell, such as `sh -c` or `eval`, or in a here
  document that a shell reads
- a request in a variable that holds the command
- a request in a line that the daemon cannot parse

The daemon does not read a line past the bounds of a `bash` command, so it
cannot say that such a line holds no request. It refuses the call, with or
without a request in it.

The daemon finds a request with the reader of
[the unattended merge rule](#the-unattended-merge-rule), so it looks in each
place where that rule looks. It has the limits of that rule too. A script
that asks for a review passes with no count. So does a request in the shell
of a member that another harness runs. One request can also start more than
one reviewer, as a panel does, and it counts as one.
[A sub-agent of unattended work](#a-sub-agent-of-unattended-work) asks under
the same count.

**The count is no budget.** It bounds an agent that follows its
instructions, at each request that the daemon can see. It does not bound
what the forge spends on a review.

## Permission rules

Rules let you pre-answer the prompt for specific calls. They live in
`$TERVA_HOME/config.json` (user layer, trusted) and in a project's
`.terva/config.json` (project layer, untrusted):

```json
{
  "approval": "ask",
  "permissions": [
    { "tool": "bash", "args": "^git (status|diff|log)\\b", "decision": "allow" },
    { "tool": "bash", "args": "^git push", "decision": "ask" },
    { "tool": "mcp_*", "decision": "ask" },
    { "tool": "bash", "args": "rm -rf", "decision": "deny", "reason": "use explicit paths" }
  ]
}
```

- `tool`: exact tool name, or a prefix glob ending in `*`.
- `args`: optional RE2 regexp, matched against **each top-level
  string argument value** (so `^git status` matches the bash `command`
  without JSON escaping) and the raw args JSON as a fallback.
- `decision`: `allow`, `deny`, or `ask`. `ask` forces a prompt even
  in modes that would auto-allow (and therefore refuses in headless).
  **The exception is `yolo`, which never prompts: there an `ask` rule
  degrades to allow.** Use `deny` to block something even in yolo.
- `reason`: appended to the refusal the model sees on `deny`.

A `bash` command that runs **several commands** (`git diff && rm -rf /`,
pipelines, `;`, command substitution) is parsed and judged **one command
at a time**: a denied command anywhere on the line denies the whole call,
and the line auto-runs only if every command would on its own. So an
`allow` rule scoped to `^git ` cannot clear an `rm` that rides the same
line, and an anchored `deny` (`^rm `) still matches a command that isn't
first. An unparsable line falls back to matching the whole string.

A `bash` command has bounds: 128 KiB, and 16,384 marks that can nest, such
as `(`, `{`, `[`, `|` and `&`. A run of more than 16,384 `+` and `-` signs
is past a bound too, because a run of signs in a sum is as deep as it is
long. The text of the commands that a line splits into has a bound of 1 MiB,
which only a line of commands inside commands reaches, as in
`echo $(echo $(echo ...))`. terva refuses a command past a bound in every
approval mode, and it tells the agent to write a file and to run a short
command. A line that deep ends the process in the shell parser, and a line
that terva does not split into its commands would hide a command from a rule.
On Linux the shell gets a command as one argument of 128 KiB at most, so the
first bound stops no command that ran there before.

Rules come from three layers: your **user** config, a project's
`.terva/config.json`, and installed **extension** bundles (the
`permissions` key in `extension.json`). They are concatenated in that
order and evaluated first-match-wins, so:

1. **plan-mode refusal**: in plan mode no rule can authorize a
   mutating tool; the posture beats the rules.
2. **user rules**, then **project rules**, then **extension rules**,
   each in file order.
3. the **mode default** (the table above).
4. session memory ("always this tool…") and, interactively, the
   prompt. A `deny` rule beats remembered session grants: explicit
   config outranks convenience.

**You are sovereign on your own machine.** Your user rules are
evaluated first, so an explicit user `allow` overrides any restriction
a project or extension suggested for the same call. The two
lower-trust layers apply only where you are silent, and there a
repo-specific project rule beats a global extension default.

**The project and extension layers may only restrict.** `deny` and
`ask` are honored; an `allow` from either is dropped with a warning at
load time. A cloned repo, or an installed extension, must never be
able to grant itself tool access you didn't; only your user config can
`allow`. (Same trust posture as project `context_files` containment.)

**Extension rules from a project's own bundles are Workspace-Trust-gated.**
An untrusted project's extensions never load, so the rules they suggest do
not apply either; trust the directory (`terva trust`, or `--trust` for one
run) and they join the extension layer. Suggestions from extensions
installed globally under `$TERVA_HOME` always apply. The project's own
`.terva/config.json` rules stay honored either way, trusted or not: they
can only tighten, and "refuse more" is always safe to accept from a repo.

Broken rules (bad regexp, unknown decision) are dropped with a stderr
note; they never fail startup.

A project's `.terva/config.json` that exists but cannot be parsed applies
none of its settings, including every one that only restricts: its deny and
ask rules, `disable_extensions`, `disable_mcp`, `disable_context_extensions`,
`tickets: false`, and `project_scoped`. Extensions and MCP servers it
disables can start. terva says so wherever you are reading, naming the file
and each setting: on stderr in the headless modes, and as an error notice on
the first turn in the TUI and the daemon's clients. It does not refuse to
start, so a typo in a repository's file cannot take away your own tools. The
SDK, which has nobody to tell, refuses to start until the file parses.

## The confirmation dialog

When a call needs asking, the TUI shows the tool name and a one-line
args preview with five choices:

1. yes: run this call;
2. yes, always this tool (rest of the session);
3. yes, always this tool, **save**: also appends a permanent
   `{"tool": "...", "decision": "allow"}` rule to your user config;
4. yes, always: skip all prompts this session;
5. no: refuse, the model is told and can try something else.

For a `bash` call the daemon also derives the command's shape and, when
it can anchor it honestly, offers a sixth choice:

6. yes, always **this command**, save: appends a permanent rule scoped
   to the command instead of all of bash, e.g. `{"tool": "bash", "args":
   "^git status(?:\\s|$)", "decision": "allow"}`. A compound line saves
   one rule per command (`git status && ls` saves both anchors), and
   every command on the line must be allowed for a future line to skip
   the prompt. Unlike option 3, nothing session-wide rides along: bash
   calls the rules don't match keep asking. The option is withheld,
   never guessed, when the line resists honest anchoring (command
   substitution, an env-var prefix, an expanded command word).

Session grants reset when the session ends; only the **save** options
(3 and 6) persist. The same choices appear in the web panel's approval
card, which offers "Always allow “…”" whenever the daemon
derived a scope.

## Revoking a session grant

The "always this tool" and "yes, always" answers above last for the
rest of the session, which is handy until you grant one by accident. Open
`/permissions` to take it back without restarting: the **this session**
list shows each grant (and the blanket "yes, always" as its own entry);
`↑`/`↓` select, `r` or `del` revokes the selected one, and `R` clears
them all. The next call of a revoked tool prompts again (or follows the
mode default). Revoking the blanket "yes, always" grant leaves any
per-tool grants in place. Only session grants are revocable here: the
permission *rules* are read-only in the TUI and edited in config.

## Audit log

Every tool call is appended to `$TERVA_HOME/logs/audit.log`: the tool,
the approval mode in force, the gate's allow/deny decision (and the deny
reason), and a capped copy of the arguments. The session transcript
already records *what* ran, but not *why it was allowed*; the audit log
adds that, which matters most in **yolo**, where nothing prompts: it's the
durable, after-the-fact record that a command was auto-approved rather
than refused.

It's one JSON object per line (JSONL), so it's greppable and `jq`-able:

```bash
# every bash command this machine's terva has run, and how it was permitted
jq 'select(.tool=="bash") | {time, mode, decision, cmd: .args.command}' \
  "$TERVA_HOME/logs/audit.log"
```

Fields: `time` (UTC), `pid`, `via`, `tool`, `mode`, `decision`
(`allow`/`deny`), `reason` (on deny), `args` (truncated past ~2 KB).
**Every** call is recorded, reads included, so there are no blind
spots; filter at read time. All three doors into the gate record, and
`via` says which one: `tool_call` (a model-issued call through the
ladder), `host_tool_call` (an extension running a host tool), or
`code_execution` (a script binding's call, at up to one line per binding
call, and stamped the same for both scripting tools, so a script's
`write` records exactly as its `read` does). Lines written before `via` existed were all model
tool calls; the other two doors recorded nothing then. The file is created `0600` and, like the rest of `logs/`, sits on the
read **deny list**: it is never readable by `read` or by `bash`, and
`/unjail` does not lift it, because tool arguments (bash commands, file writes)
can carry secrets. It's append-only and lazily created, so a run
that calls no tools never writes one; rotate or delete it yourself if it
grows.
