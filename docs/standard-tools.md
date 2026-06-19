# terva standard tools — strategy and playbook

Canonical guidance for terva's model-visible tool surface: what belongs
in the always-on core, what ships as an opt-in **standard extension**,
and what stays a **recommended MCP preset**. Consult this before adding,
removing, or reclassifying any tool.

The near-term implementation roadmap (and what is deferred) lives in
[plans/standard-tools-bucket2.md](plans/standard-tools-bucket2.md). The
original landscape comparison that motivated this is
[plans/harness-landscape-2026.md](plans/harness-landscape-2026.md).

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
[plans/workspace-trust.md](plans/workspace-trust.md).)

## The four layers

When adding a capability, decide its layer first:

1. **Core built-in** — universal, high-frequency, low-dependency tools
   that benefit from tight permission/sandbox integration. Lives in
   `packages/agent/tools/`, registered in `buildToolRegistry`
   (`packages/agent/build.go`).
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
decides read-only classification — only `local-read` is auto-allowable, so
a `network-read` tool is gated like a side-effecting tool (it prompts in
`workspace`/`auto-edit`, is refused in `plan`) even if it also set the
legacy `read_only` bool. **Mark network tools `network-read`, not
`read_only`.** Still to come (bucket-2 Phase A): the shared egress guard
and an optional per-host allowlist that lets `workspace` auto-allow chosen
network hosts. See [plans/standard-tools-bucket2.md](plans/standard-tools-bucket2.md).

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

`grep`/`glob` are jailed exactly like the file tools (cwd containment,
symlink skip) and survive `plan` mode because they are classified
read-only.

### Standard extensions (opt-in, terva-blessed)

These are the designated official standard extensions. They run as trusted
local code; each must meet the acceptance bar below before being treated
as fully blessed (some promotion work is still tracked in the bucket-2
plan).

- **Tasks** — `task_create`/`task_list`/`task_update` plus a compact
  context card. Opinionated rules: exactly one active task; evidence
  required to mark done/blocked; never mark failed/partial work done.
  (Reference implementation: `examples/extensions/todo/`.)
- **Worktrees** — `terva-git-worktree` (`worktree_list`/`_create`/
  `_claim`/`_release`/`_remove`), integrating with swarm isolation via
  `--swarm-worktrees`. Mutates git state, so it is never marked read-only.
- **Web** (adoption pending — bucket-2 Phase C) — `web_search`/`web_fetch`/
  `web_images` are already implemented by the hardened `zot-web` extension
  (`github.com/terva-sh/zot-web`), which loads under terva via the preserved
  zot wire protocol. The plan is to **adopt and adapt** it (network-read
  authority, host egress policy, a `terva_version` handshake adapter), not
  reimplement it. See [plans/standard-tools-bucket2.md](plans/standard-tools-bucket2.md).

### Recommended MCP presets (docs + starter config only)

Browser/devtools (Playwright, Chrome DevTools), docs search, GitHub,
Sentry, Figma, database tools. These depend on local runtime/credentials
or third-party services and stay outside core. Several become much cleaner
once MCP gains an HTTP/OAuth transport (bucket-2 Phase D).

## Non-negotiables

- No tool without a permission story and an explicit authority class.
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
  `buildToolRegistry`; sandbox pointer rebound in `Resolved.UseSandbox`.
- Bounded JSON schema; description with concrete safety steering.
- Read-only classification where appropriate
  (`readOnlyTools`) and first-party classification (`builtinTools`) in
  `packages/agent/permissions.go`; added to the system-prompt tool order
  in `toolSummaries`.
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

- Policy ladder: `packages/core/policy.go`, `packages/agent/permissions.go`
- Tools: `packages/agent/tools/`
- Swarm/worktree: `packages/agent/swarm/`, `--swarm-worktrees`
- MCP: `packages/agent/mcp/`, [mcp.md](mcp.md)
- Extensions: [extensions.md](extensions.md)
- Permissions/jail: [permissions.md](permissions.md), [tui.md](tui.md)
