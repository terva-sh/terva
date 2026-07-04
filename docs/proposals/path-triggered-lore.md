# Proposal — path-triggered lore for coding context

- **Status:** Draft for maintainer review (2026-07-02).
- **Audience:** the agent / maintainer responsible for managing terva's context system.
- **Scope:** extend the existing `packages/agent/lore` keyed-context primitive so a lore entry can optionally activate when recent path-bearing tool activity matches configured file globs.
- **Non-goal:** enforce file/tool policy. Lore remains prompt context; permissions and hooks remain the enforcement layer.

## TL;DR

terva's lore is already a good fit for human-authored, budgeted, keyword-relevant context. It could become more useful for coding sessions if entries can also be **path-triggered**:

```markdown
---
name: Test conventions
paths:
  - "**/*_test.go"
  - "**/testdata/**"
order: 120
---
When editing tests, prefer table-driven cases. Avoid sleeps; use fake clocks or
synchronization. Run the narrow package test before broader checks.
```

When the agent reads, edits, writes, greps, or globs paths matching `**/*_test.go`, the entry would fire on the next model call and ride the same uncached per-turn tail as keyword-triggered lore. This is the coding-agent analog of Cursor's path-scoped rules and Claude Code's path-specific rules, while staying inside terva's existing lore model.

The useful version is intentionally small:

1. add `paths: []` to lore entry frontmatter;
2. collect recent structured path observations from path-bearing tools;
3. activate entries by keyword **or** path match, unless an entry asks for stricter logic;
4. expose fired reasons in `/lore` and `--dump-prompt=json`;
5. validate with harness evals that compare lore on/off for known failure modes.

## Why consider this

Some project knowledge is not globally relevant and does not belong in always-on `AGENTS.md` / `CLAUDE.md` style context. It becomes relevant only when the agent works in a file area:

| Files touched | Useful context |
|---|---|
| `**/*_test.go`, `**/testdata/**` | test style, fixtures, race-test cautions, fake-clock policy |
| `packages/provider/**` | provider retry/auth behavior, model catalog cautions |
| `packages/agent/tools/**` | sandbox and permission invariants |
| `docs/**` | documentation tone and link/update requirements |
| `.github/workflows/**`, `.forgejo/workflows/**` | release/CI guardrails |
| `**/migrations/**` | migration reversibility and deployment sequencing |
| generated code paths | reminder to edit the source schema and regenerate, not patch generated output |

Today, these facts usually go in always-on repo instructions, are rediscovered by reading docs, or are omitted. Path-triggered lore lets us keep always-on context smaller while still surfacing narrow, non-obvious conventions when they matter.

## Relationship to current lore

Current lore, per `docs/personas.md`, `docs/debugging-prompts.md`, and `docs/proposals/character-cards.md`, is:

- human-authored Markdown + YAML frontmatter;
- discovered from `$TERVA_HOME/lore/`, trusted `.terva/lore/`, extension bundles, and card-imported `character_book` entries;
- selected by keyword relevance over recent conversation;
- ordered and budgeted;
- split by cache behavior:
  - `constant: true` → cached system prefix;
  - triggered entries → uncached per-turn tail.

Path-triggered lore should reuse that model. It is still **triggered lore**. The only change is that the activation haystack gains another structured signal: recent paths from tool calls.

## Proposed entry format

Add optional `paths` frontmatter:

```markdown
---
name: Provider tests
keys: [provider, auth, model catalog]   # optional: existing behavior
paths:
  - "packages/provider/**"
  - "packages/provider/auth/**"
order: 120
position: after
---
Provider tests should avoid live network calls. Prefer fake clients and local
httptest servers. Preserve subscription/auth edge cases when changing retries.
```

Open design knobs:

| Field | Suggested v1 behavior |
|---|---|
| `paths` | list of glob patterns matched against workspace-relative paths |
| `keys` + `paths` | default activation is **OR**: either keyword or path match fires |
| stricter logic | defer, or add later as `trigger_logic: any|all` / `require_key: true` |
| `constant: true` + `paths` | invalid or ignored; constant entries do not need triggers |
| `case_sensitive` | keep applying only to text keys; path matching should be platform-normalized |
| `scan_depth` | still text-message scan depth; path recency probably needs a separate small default |

The OR default is simple and matches user expectation: “include this note when the conversation mentions tests **or** when the agent touches test files.” If this over-fires in practice, add stricter entry-level trigger logic after measuring.

## Trigger source: recent path observations

Call this **path-triggered lore** in user-facing docs, not “tool-call lore.” Internally, the easiest source is structured tool-use metadata.

Initial path-bearing built-ins:

| Tool | Path signal |
|---|---|
| `read` | `path` |
| `write` | `path` |
| `edit` | `path` |
| `grep` | `path` and maybe `glob` when present |
| `glob` | returned paths and/or search root + pattern, depending on budget |

Be conservative with `bash`. Shell parsing is hard and unreliable. A command like `go test ./packages/agent/lore` is useful evidence, but extracting paths from arbitrary shell safely is a separate design. v1 can skip `bash` or only consume explicit structured metadata if the bash tool later emits it.

### Timing caveat

If an entry fires because of a tool call, it can only affect the **next** model step:

1. model calls `read packages/foo/foo_test.go`;
2. tool result returns;
3. next model call includes `Test conventions` in the per-turn tail.

That is still useful before edits, follow-up reads, or test execution. If the path appears in the user prompt, normal text matching or pre-extracted prompt paths can fire earlier, but v1 does not need to solve that to be valuable.

## Prompt placement and cache behavior

Path-triggered entries should be treated exactly like keyword-triggered entries:

- never cached in the static prefix;
- assembled per turn;
- rendered through the existing lore tail block;
- ordered and budgeted with the rest of triggered lore;
- visible in prompt dumps and `/lore`.

The cache contract from `docs/proposals/character-cards.md` remains: **static → cached prefix; dynamic → uncached tail**.

## Observability requirement

This feature will feel spooky without a reason surface. `/lore` and `--dump-prompt=json` should show not just that an entry fired, but why.

Example `/lore` display concept:

```text
Test conventions
  source: .terva/lore/tests.md
  fired last turn: path **/*_test.go matched packages/agent/lore/engine_test.go via read
```

Prompt dump segment metadata could similarly distinguish:

```json
{
  "source": "lore:triggered [files]",
  "reason": "path:**/*_test.go",
  "matched_path": "packages/agent/lore/engine_test.go",
  "matched_tool": "read"
}
```

Exact schema can vary; the important bit is preserving debuggability.

## Trust and authority

Keep the current lore trust model:

- `$TERVA_HOME/lore/` is user-owned and trusted.
- `.terva/lore/` remains workspace-trust gated.
- extension lore only comes from enabled extensions.
- card-imported lore remains ephemeral and in-memory.

Path matching does not grant capabilities. A malicious/untrusted repo must not be able to add `.terva/lore/generated.md` that changes agent behavior unless the workspace is trusted. Even when trusted, a lore entry is only prompt context. Hard boundaries still belong in permissions and hooks:

```text
Lore:   "Do not edit generated files; edit source schema and regenerate."
Hook:   deny edit/write on src/generated/** unless explicitly allowed.
```

This distinction should be explicit in docs.

## Related systems and findings

The web research did not find much using the roleplay term “lore” for coding agents. The same idea appears under **context engineering**, **rules**, **memory banks**, **project memory**, and **RAG**.

### OpenAI Codex `AGENTS.md`

OpenAI documents global and project `AGENTS.md` discovery, including nested directory loading, merge order, byte limits, verification commands, and troubleshooting. This is the mainstream static-context analogue: repo instructions are loaded before work starts.

Reference: <https://developers.openai.com/codex/guides/agents-md>

Relevance to this proposal: good evidence that repo-local context files are now a standard harness surface, but Codex's documented mechanism is primarily static/nested rather than path-triggered per turn.

### Claude Code memory, `CLAUDE.md`, and path rules

Claude Code documents two memory systems: user-written `CLAUDE.md` files and auto memory. It also supports `.claude/rules/` with path-specific rules that load when Claude works with matching files. The docs emphasize that memory files are context, not enforced configuration, and suggest hooks for hard policy.

Reference: <https://docs.anthropic.com/en/docs/claude-code/memory>

Relevance: confirms the “path-scoped context is useful, but not enforcement” design split.

### Cursor rules

Cursor's project rules live in `.cursor/rules/*.mdc` and can be:

- always applied;
- applied intelligently based on description;
- applied to specific file globs;
- manually invoked with `@rule`.

Reference: <https://cursor.com/docs/rules>

Relevance: closest production analogue to path-triggered lore. Cursor explicitly uses `globs` to include context when matching files are in context.

### Cline Memory Bank

Cline's Memory Bank is a structured Markdown methodology with files such as `projectbrief.md`, `activeContext.md`, `systemPatterns.md`, `techContext.md`, and `progress.md`. It is maintained across sessions and read when starting or resuming work.

Reference: <https://docs.cline.bot/prompting/cline-memory-bank>

Relevance: shows developer appetite for durable Markdown context, but Cline's model is more “read the bank” than deterministic per-turn key/path activation.

### Context engineering writeups

Kinde's memory-bank guide recommends human-readable project context covering identity, stack, conventions, decisions, avoid-lists, and current focus.

Reference: <https://www.kinde.com/learn/ai-for-software-engineering/ai-agents/how-to-write-a-memory-bank-for-your-ai-coding-agent/>

TanhDev's context-engineering article lays out rule files, session handovers, repo indexing, semantic memory banks, RAG, and MCP context infrastructure. It specifically warns that context quality is a bottleneck and that curated domain memory banks can prevent agents from generating code for “an imaginary system.”

Reference: <https://tanhdev.com/series/ai-code-review-vibe-coding/part-2-context-engineering-codebase/>

Relevance: supports the overall need for scoped project context; also cautions against dumping broad context into every turn.

### Harness memory and static-file limitations

Vectorize's “The Missing Layer in Every Agent Harness” argues that harnesses have tools, MCP, IDE integration, permissions, subagents, and skills, but often stop at static context files (`CLAUDE.md`, `AGENTS.md`, `.cursorrules`) rather than retaining/recalling relevant memory.

Reference: <https://hindsight.vectorize.io/blog/2026/05/04/agent-harness-needs-memory>

Relevance: reinforces that static files are useful but insufficient; terva's lore can remain deterministic and human-owned while still being more selective than always-on files.

### Evidence that context files need evaluation

The paper “Evaluating AGENTS.md: Are Repository-Level Context Files Helpful for Coding Agents?” reports that repository context files did not generally improve task success on their studied tasks and increased inference cost by over 20% on average. It also reports that agents follow instructions well, while broad repository overviews are not generally helpful. The authors conclude that context files are useful for specifying non-standard coding practices, but should be rigorously evaluated.

Reference: <https://arxiv.org/abs/2602.11988>

Relevance: this is the strongest caution. Path-triggered lore should avoid broad repo overview content. It should target narrow, non-standard, failure-preventing context and be tested with lore on/off.

### Agent eval guidance

Anthropic's “Demystifying evals for AI agents” recommends evaluating the full harness with tasks, trials, graders, traces, outcomes, tool-call checks, and token/cost metrics. For coding agents, deterministic tests and transcript/tool-use checks are natural graders.

Reference: <https://www.anthropic.com/engineering/demystifying-evals-for-ai-agents>

OpenAI's agent improvement loop shows a trace → feedback → Promptfoo eval → harness-change workflow.

Reference: <https://developers.openai.com/cookbook/examples/agents_sdk/agent_improvement_loop>

Huecki's “AGENTS.md is not enough” proposes harness evals such as generated-file traps, secret traps, dependency traps, review-mode traps, and external-action traps.

Reference: <https://huecki.com/en/blog/agents-md-coding-agent-harness-en/>

Relevance: path-triggered lore should ship with small regression tasks that prove it improves known failure modes without too much cost.

## Testing strategy

### Unit tests

Extend `packages/agent/lore` tests for:

- parse `paths` frontmatter;
- invalid `paths` types produce validation errors;
- path-only entry activates on matching recent path;
- path-only entry does not activate without a matching path;
- key-only behavior remains unchanged;
- key + path OR behavior;
- ordering and budget across keyword-triggered and path-triggered entries;
- path normalization across Unix/Windows separators;
- path reason recorded for observability.

### Prompt assembly tests

Add or update agent prompt assembly tests for:

- path-triggered entries land in the uncached tail;
- `constant: true` entries still land in the prefix;
- `--no-lore` suppresses path-triggered entries;
- untrusted `.terva/lore/` path entries do not load;
- card-imported lore behavior remains unchanged;
- prompt dump shows the fired source/reason.

### Harness evals

Create small, repeatable tasks that compare lore enabled vs disabled. Candidate evals:

1. **Generated-file trap**
   - Setup: generated client has a type mismatch.
   - Lore path: `src/generated/**`.
   - Expected: agent does not edit generated file; edits source schema or asks.

2. **Test-convention trap**
   - Setup: task asks to fix a flaky test.
   - Lore path: `**/*_test.go`.
   - Expected: agent avoids sleeps and uses synchronization/fake clocks.

3. **Dependency trap**
   - Setup: add CSV export.
   - Lore path: package or feature area.
   - Expected: agent uses stdlib/project helper; no new dependency.

4. **Provider-auth trap**
   - Setup: modify retry/auth behavior under `packages/provider/**`.
   - Lore path: `packages/provider/**`.
   - Expected: preserves token refresh/retry semantics and runs relevant package tests.

Track:

- pass/fail outcome;
- files touched;
- tool calls;
- tests run;
- token/cost delta;
- whether the matching lore fired;
- whether unrelated lore stayed out.

## Implementation sketch

One possible incremental path:

1. **Data model**
   - Add `Paths []string` to `lore.Entry` and frontmatter parser.
   - Add validation: no empty globs; maybe reject absolute paths in project lore.

2. **Selection input**
   - Replace or augment `Select(entries, cfg, scan []string, ...)` with a structured scan input, e.g.:

   ```go
   type Signal struct {
       Texts []string
       Paths []PathObservation
   }

   type PathObservation struct {
       Path string
       Tool string
   }
   ```

   Keep a compatibility wrapper for existing tests/callers if useful.

3. **Path matching**
   - Match workspace-relative, slash-normalized paths.
   - Use the same glob semantics users already see in terva's `glob`/path tooling; do not silently downgrade `**` patterns to `path.Match` semantics if that would surprise users.
   - Record the first/best matched path + pattern for display.

4. **Per-turn provider**
   - Gather recent path observations from structured tool-call metadata, the agent transcript, or tool-event history. Prefer structured metadata over scraping rendered text.
   - Start with built-in path-bearing tools only.
   - Keep the path recency window small; likely “since previous model call” plus maybe one prior turn.

5. **Rendering / observability**
   - Extend `loreFiredRecord` to hold reason metadata instead of only names, or add a parallel reason map.
   - Update `/lore` and prompt dump output.

6. **Docs**
   - Update `docs/personas.md` lore section.
   - Update `docs/debugging-prompts.md` source/reason table.
   - Mention path-triggered lore in `docs/cli.md` only if flags/CLI output change.

## Risks and mitigations

| Risk | Mitigation |
|---|---|
| False positives from broad globs | Encourage narrow globs; show fired reasons; budget applies |
| Token cost creep | Add eval cost metrics; keep entries short; prefer path-triggered over constant |
| Spooky action at a distance | `/lore` and `--dump-prompt=json` must show matched path/tool/pattern |
| Confusion with enforcement | Docs explicitly say lore is context; hooks/permissions enforce |
| Shell path parsing complexity | Do not parse `bash` in v1 |
| Untrusted repo injection | Keep Workspace Trust gating exactly as current project lore |
| Cross-platform path behavior | Normalize to slash-style workspace-relative paths before matching |

## Open questions

1. Should `keys` + `paths` default to OR, or require explicit `trigger_logic`?
2. Should path recency be “last tool batch,” “current turn,” or the same `scan_depth` counted over message turns?
3. Should `glob` results count as matching paths, or only explicit path arguments? Counting all returned paths can be noisy.
4. Should path-triggered entries support negative globs (`!generated/**`) in v1, or wait for evidence?
5. Where should fired reason metadata live so `/lore`, prompt dumps, and tests share the same source of truth?

## Recommendation

Prototype path-triggered lore behind the normal lore path, not as a new context subsystem. Keep v1 narrow:

- `paths` frontmatter;
- built-in path-bearing tools only;
- OR activation with existing keyword triggers;
- per-turn tail placement only;
- fired reason observability;
- evals that prove benefit and measure token cost.

The best case is a low-complexity feature that moves area-specific coding knowledge out of always-on context and into budgeted, inspectable, just-in-time lore. The main thing to avoid is recreating broad static context files under a new name; every useful entry should be specific, scoped, and tied to a failure mode we can test.
