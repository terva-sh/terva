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
`read_only`.** Still to come (bucket-2 Phase A): the shared egress guard
and an optional per-host allowlist that lets `workspace` auto-allow chosen
network hosts. See `docs/plans/standard-tools-bucket2.md`.

## Current standard bundle

### Core built-ins (always on)

| Tool | Authority | Notes |
|---|---|---|
| `read` | local read-only | files + inline images for vision models |
| `write` | workspace mutation | overwrites; description steers toward `edit` for partial changes |
| `edit` | workspace mutation | exact-match replacements, whitespace-tolerant fallback |
| `bash` | process execution | merged stdout/stderr, timeout; description carries git/secrets/file-tool-preference guardrails |
| `grep` | local read-only | RE2 content search; `.gitignore`-aware, binary-skipping, paged |
| `glob` | local read-only | path glob (`**` recurses); `.gitignore`-aware, paged |
| `ask_user_question` | user interaction | structured clarifying question; permitted in every mode; interactive-only (headless returns a proceed-anyway result) |
| `terva_status` | local read-only | session self-introspection |
| `session_inspect` | local read-only | bounded, filterable view over a session transcript: this session, another session in the project, or a swarm sub-agent's (by its id); `expand` reads one event's full text in pages. Secrets redacted, input scan and output both capped. |
| `task_create` / `task_update` / `task_list` / `task_archive` | local data | the built-in task board (folded in from the former `terva-tasks` extension): one active task at a time, evidence to close, archive generations, `task_list format:"markdown"` exports a checkbox worklog. The board persists per session under `$TERVA_HOME/tasks` (private modes) and its live state rides each turn as a context card. |
| `activate_tools` | visibility only | present only when `lazy_tools` is on (see below); brings a hidden capability group into the advertised set. The advertised set is pinned while the model replies, so an activated group can never join the current reply's remaining calls — instead, activation continuation (on by default) automatically re-prompts the model with the tools live the moment it finishes that reply; with continuation off, the group lands on the NEXT turn. Its result echoes the group's schemas (capped at a 4 KB budget; past that, names only) so the model can compose that next call without waiting to see them. Grants no authority — revealed tools keep their normal permission gates. |

`grep`/`glob` are jailed exactly like the file tools (cwd containment,
symlink skip) and survive `plan` mode because they are classified
read-only.

The task tools ship exactly when the base coding tools do — `--chat`, `--play`,
`--no-tools`, and `--no-workspace-tools` drop them together — and there is no
standing config switch beyond that: a per-run `--tools` allowlist that omits
them is the way to run coding tools without the board. Boards written by the
old `terva-tasks` extension migrate forward automatically on their next write
(the store reads through the legacy `ext-data/tasks` layer).

#### Lazy tool visibility (`lazy_tools`)

With many extensions/MCP servers attached, most of the tool surface is noise
most of the time. Setting `lazy_tools: true` in `config.json` advertises only
the core group plus the groups named in `lazy_tool_active` (e.g.
`["mcp:github"]`); everything else is summarized in a per-turn
`[inactive tool groups]` note the model can act on with `activate_tools`.
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
| `swarm_spawn` | auto-swarm on (coding sessions only) | process execution | fire-and-forget parallel coding sub-agents; a `tier` picks a cheaper model, never stronger than the host. See [tui.md](tui.md) (auto-swarm). |
| `actor_spawn` | `--play` with a declared cast | process execution | synchronous "director voices an actor" — hands the actor a situation, waits, returns its line. Cast is closed and named; the model dispatches by name, never a path. See [personas.md](personas.md#cast-and-actor-dispatch). |

Because they wrap the same engine they share its lifecycle, session
persistence, and tier resolution, and differ only in the *skin* (fire-and-forget
vs. synchronous director-pull) and the gate that injects them (the auto-swarm
setting vs. `--play` + a cast). New dispatch front-ends should follow this
pattern — another skin over the one engine — rather than adding a parallel
engine. Both are gated out of the wrong context: `actor_spawn` never appears in a
coding session, and `swarm_spawn` never appears in an immersive one.

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

**`terva_restart` is the acknowledged exception to "no tool without an explicit
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

### Standard extensions (opt-in, terva-blessed)

These are the designated official standard extensions. They run as trusted
local code; each must meet the acceptance bar below before being treated
as fully blessed (some promotion work is still tracked in the bucket-2
plan).

Four of them ship in the built-in **core pack** (`packages/agent/packs/core.json`,
installed by `terva ext pack install`): `index`, `git-worktree`, `memory`, and
`web`. That pack is the blessed set — a starting point an operator opts into, not
something terva loads on its own.

- **Index** — `index` (`github.com/terva-sh/terva-ext-index`): a workspace code
  index and search. It exists to replace repeated `bash grep`/`rg` sweeps — and
  the whole-file reads they lead to — with a structured, indexed lookup: exactly
  the "replaces a risky/verbose `bash` pattern" case the candidate checklist
  below asks about.
- **Worktrees** — `terva-git-worktree` (`worktree_list`/`_create`/
  `_claim`/`_release`/`_remove`), integrating with swarm isolation via
  `--swarm-worktrees`. Mutates git state, so it is never marked read-only.
- **Memory** — `memory` (`github.com/terva-sh/terva-ext-memory`): cross-session
  memory, scoped per workspace and per user, so an agent can carry facts forward
  instead of re-deriving them every session. The natural home for the
  **local-data** authority — a store confined to the extension's own
  `$TERVA_HOME/ext-data/<name>` dir, never the user's workspace, which is what
  makes that class auto-allowable in the first place.
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
