# Permissions: approval modes and rules

Two orthogonal controls decide what the agent may do:

- the **approval mode** and **permission rules** decide *whether a tool
  call runs at all* (this page);
- the **sandbox** (`/jail` in [tui.md](tui.md)) bounds *what a running
  tool can touch* (paths under the cwd, command heuristics).

They compose: a call must pass the approval layer first, then run
within whatever the sandbox permits. Keeping the axes separate is
deliberate — a single conflated "trust level" is the known failure
mode in this design space. The interactive default pairs them: the
`workspace` approval mode trusts your built-in tools *because* the
sandbox (jailed by default) bounds them to the working directory.

## Approval modes

Pick one with `--approval <mode>`, or set a persistent default in
`$TERVA_HOME/config.json` (`"approval": "ask"`). Flag beats config
beats the built-in default; `--no-yolo` is a compatibility alias for
`--approval ask`.

In the TUI, `/settings` switches the mode live for the **current
session** (plan immediately withholds the mutating tools) — it does
**not** write your config. The approval mode is a security posture, not a
preference, so it is never saved as a side effect: the persistent default
comes only from the `approval` config key or the `--approval` flag, so
the picker can never silently pin a mode (and a future default change is
never masked by an accidentally-saved value). `/jail` and `/unjail`
follow the same rule — session-only unless you explicitly say
`always` ([Unjailing a directory for good](#unjailing-a-directory-for-good)).
The current mode shows
in the status bar; `/permissions` shows the active mode and rules, and
lets you revoke this session's grants (see below).

| mode | behavior |
|---|---|
| `yolo` | every tool call runs without asking. `ask` rules don't prompt here — yolo never prompts — but `deny` rules still block |
| `workspace` | **the interactive default.** Every built-in tool (`read`/`write`/`edit`/`bash`, bounded by the sandbox) runs freely, and so does any *extension/MCP* tool classified read-only; foreign tools that can have side effects (a writing extension tool, a mutating MCP tool) ask. The trust axis is origin: your own tools vs foreign code — but a foreign tool that declares the `network-read` authority asks even though it reads nothing locally (see below) |
| `auto-edit` | read-only tools (`read`, `terva_status`, `skill`) and the file editors (`write`, `edit`) run freely; everything else — `bash`, extension tools, chat sends — asks |
| `ask` | every tool call asks, read-only included (exactly what `--no-yolo` always did) |
| `plan` | only read-only tools run, and mutating tools don't even enter the registry — the model is steered to present a plan. Mutating calls that arrive anyway are refused with a model-readable reason, not prompted |

**Defaults differ by run mode.** An interactive session defaults to
`workspace` (a human is present to answer the foreign-tool prompts) and
is **jailed** by default so the built-in tools it trusts are confined
to the working directory. Headless modes (`-p`, `--json`, `--rpc`,
swarm agents) default to `yolo` and unjailed, so unattended automation
isn't surprised by prompts (which it can't answer) or path confinement.
Set `--approval` / config `approval` and `--jail`/`--no-jail` to
override either.

## Unjailing a directory for good

Some work genuinely lives outside its own folder — a dotfiles repo that
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
project** — for the same reason the trust list does: a repo must not be
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
built-in tools may read and write anywhere **without asking**. The jail
is what made `workspace` safe to leave alone — built-ins are
auto-approved precisely *because* they were confined. Take the jail away
and that premise goes with it.

Tools that are not classified read-only — including every extension
tool — are treated as mutating. In headless modes (`-p`, `--json`,
`--rpc`, swarm agents) there is no prompt: anything that would ask is
**refused** with a model-readable reason instead. That keeps the
refuse-by-default posture while still letting `plan` mode and explicit
allow rules drive useful headless automation.

**Bot mode is the exception**: `terva bot run` still defaults to
`yolo`, but a prompting mode (`--approval ask`, `workspace`, …) WORKS
there — a tool call that needs confirmation is posed as a question in
the paired chat (buttons where the connector supports interactive
asks, a numbered plain-text question everywhere else) and the turn
blocks on the answer. Only the paired owner may answer; an unanswered
question denies after two minutes (fail closed); and "always (this
tool)" — a durable session grant — requires a platform-attested answer
(a button click, not parsed text; a text "always" allows once and says
so). See docs/connectors.md for the wire side.

## Authority classes

A tool's *authority* is a finer classification than the read-only/mutating
split, because "side-effect-free" is not one thing. A web fetch reads
nothing on the local machine yet can leak data, hit a remote server, or
reach a private network — so folding it into the local-read auto-allow
would be wrong. The classes (`core.Authority`):

| Authority | Meaning | Example |
|---|---|---|
| `local-read` | reads files/state under the jail; no process/network/external effect | `read`, `grep`, `glob`, `terva_status`, `session_inspect`, `skill` |
| `local-data` | reads **and writes** the tool's own host-managed data store — for an extension, its `$TERVA_HOME/ext-data/<name>` — and nothing else: never your workspace, a process, the network, or an external service. Auto-allowable like `local-read`, because the write never leaves private, host-controlled storage | `task_create`/`task_update`/`task_list`/`task_archive`; a memory/notes extension tool |
| `workspace-mutation` | writes files / edits workspace state | `write`, `edit` |
| `process-execution` | runs commands / subprocesses | `bash` |
| `network-read` | fetches URLs / search results (can leak, log, reach private nets) | a web-fetch extension tool |
| `external-mutation` | writes to third-party APIs, sends messages, changes remote resources | a chat-send / PR-open tool |
| `user-interaction` | blocks to ask the user; no other effect | `ask_user_question` |

One built-in sits outside the taxonomy: `activate_tools` changes only
which tool schemas are advertised this session — visibility, never
authority — so it auto-admits like a read, and every tool it reveals
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
`network-read` or `external-mutation` tool asks — declaring `network-read`
(rather than the legacy `read_only` bool) is what keeps a web tool from
being mistaken for a local read. Second, `user-interaction` is permitted
in every mode including `plan`: gating a clarifying question behind an
approval prompt is nonsensical.

Extensions and MCP tools declare their class via the `authority` field on
`register_tool` (`ext.WithAuthority` in the Go SDK). A declared authority
decides read-only classification — `local-read` and `local-data` are
auto-allowable, everything else is not — and it **wins over** the legacy
`read_only` bool, which is consulted only when the authority is empty. An
unknown value is treated as side-effecting.

That precedence is what reconciles the two statements above. Whether
`workspace` auto-allows a foreign tool is decided by its *classification*,
not by its subject matter, so a tool declaring `network-read` asks even if
it also set `read_only: true`. The web extension's `web_search`/`web_fetch`
run freely in `workspace` today only because that extension still ships the
bare legacy bool and has not yet declared an authority; once it declares
`network-read` — which it should, and which is tracked in
[standard-tools.md](standard-tools.md) — those calls will ask like any other
foreign network tool. **Mark network tools `network-read`, not `read_only`.**

### Outbound network safety (egress guard) — staged, not yet wired

`packages/egress` implements terva's shared SSRF / private-network guard,
but **no terva-driven network feature consumes it yet**: the MCP transport
is stdio-only today and there is no built-in web-fetch tool. It is staged
for the MCP HTTP transport (`docs/plans/mcp-http-transport.md`) and
host-side web policy. Once wired, it blocks loopback, private, link-local
(including the `169.254.169.254` cloud-metadata endpoint), unique-local,
and multicast destinations by default — enforced at dial time so DNS
rebinding can't slip past — and re-checks redirect hops while stripping
credentials across a host change, with specific hosts or CIDRs
allowlistable for an intentional local service. Until then, outbound
safety belongs to whatever makes the connection: out-of-process
extensions (e.g. the web extension) keep their own SSRF guards.

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

- `tool` — exact tool name, or a prefix glob ending in `*`.
- `args` — optional RE2 regexp, matched against **each top-level
  string argument value** (so `^git status` matches the bash `command`
  without JSON escaping) and the raw args JSON as a fallback.
- `decision` — `allow`, `deny`, or `ask`. `ask` forces a prompt even
  in modes that would auto-allow (and therefore refuses in headless) —
  **except in `yolo`, which never prompts: there an `ask` rule degrades
  to allow.** Use `deny` to block something even in yolo.
- `reason` — appended to the refusal the model sees on `deny`.

A `bash` command that runs **several commands** (`git diff && rm -rf /`,
pipelines, `;`, command substitution) is parsed and judged **one command
at a time**: a denied command anywhere on the line denies the whole call,
and the line auto-runs only if every command would on its own. So an
`allow` rule scoped to `^git ` cannot clear an `rm` that rides the same
line, and an anchored `deny` (`^rm `) still matches a command that isn't
first. An unparsable line falls back to matching the whole string.

Rules come from three layers — your **user** config, a project's
`.terva/config.json`, and installed **extension** bundles (the
`permissions` key in `extension.json`). They are concatenated in that
order and evaluated first-match-wins, so:

1. **plan-mode refusal** — in plan mode no rule can authorize a
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
lower-trust layers apply only where you are silent — and there a
repo-specific project rule beats a global extension default.

**The project and extension layers may only restrict.** `deny` and
`ask` are honored; an `allow` from either is dropped with a warning at
load time. A cloned repo — or an installed extension — must never be
able to grant itself tool access you didn't; only your user config can
`allow`. (Same trust posture as project `context_files` containment.)

**Extension rules from a project's own bundles are Workspace-Trust-gated.**
An untrusted project's extensions never load, so the rules they suggest do
not apply either; trust the directory (`terva trust`, or `--trust` for one
run) and they join the extension layer. Suggestions from extensions
installed globally under `$TERVA_HOME` always apply. The project's own
`.terva/config.json` rules stay honored either way, trusted or not — they
can only tighten, and "refuse more" is always safe to accept from a repo.

Broken rules (bad regexp, unknown decision) are dropped with a stderr
note; they never fail startup.

## The confirmation dialog

When a call needs asking, the TUI shows the tool name and a one-line
args preview with five choices:

1. yes — run this call;
2. yes, always this tool (rest of the session);
3. yes, always this tool — **save**: also appends a permanent
   `{"tool": "...", "decision": "allow"}` rule to your user config;
4. yes, always — skip all prompts this session;
5. no — refuse, the model is told and can try something else.

Session grants reset when the session ends; only option 3 persists.

## Revoking a session grant

The "always this tool" and "yes, always" answers above last for the
rest of the session — handy until you grant one by accident. Open
`/permissions` to take it back without restarting: the **this session**
list shows each grant (and the blanket "yes, always" as its own entry);
`↑`/`↓` select, `r` or `del` revokes the selected one, and `R` clears
them all. The next call of a revoked tool prompts again (or follows the
mode default). Revoking the blanket "yes, always" grant leaves any
per-tool grants in place. Only session grants are revocable here — the
permission *rules* are read-only in the TUI and edited in config.

## Audit log

Every tool call is appended to `$TERVA_HOME/logs/audit.log` — the tool,
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

Fields: `time` (UTC), `pid`, `tool`, `mode`, `decision` (`allow`/`deny`),
`reason` (on deny), `args` (truncated past ~2 KB). **Every** call is
recorded — reads included — so there are no blind spots; filter at read
time. The file is created `0600` and, like the rest of `logs/`, is
excluded from the read jail, because tool arguments (bash commands, file
writes) can carry secrets. It's append-only and lazily created, so a run
that calls no tools never writes one; rotate or delete it yourself if it
grows.
