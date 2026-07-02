# Context construction in terva

This document describes how terva currently constructs the model context and the rules that exist today for loading file content into that context.

## High-level model request shape

Each model call is built by `packages/core.Agent.oneTurn` from four main pieces:

1. **System prompt** — `Agent.System`, assembled during `agent.Resolve`.
2. **Transcript messages** — a copy of the in-memory conversation history (`Agent.Messages()`).
3. **Tool specs** — JSON schemas from the currently registered tools (`a.Tools.Specs()`).
4. **Reasoning setting** — the normalized reasoning/thinking level, if any.

Conceptually:

```go
provider.Request{
    Model:     a.Model,
    System:    a.System,
    Messages:  a.Messages(),
    Tools:     a.Tools.Specs(),
    Reasoning: a.Reasoning,
}
```

Tool descriptions are not duplicated in the default system prompt. The provider request carries tool schemas separately.

## Startup resolution

Most startup context is assembled in `packages/agent/build.go` inside `Resolve(args, requireCred)`.

`Resolve` merges:

- CLI args parsed by `ParseArgs`;
- persisted config from `$TERVA_HOME/config.json`;
- environment credentials;
- auth state from `$TERVA_HOME/auth.json`;
- discovered skills;
- optional startup instruction files such as `SYSTEM.md` and `AGENTS.md`.

The effective cwd is set during `ParseArgs`:

- If `--cwd PATH` is provided, it is resolved to an absolute path and must exist as a directory.
- If `--cwd` is omitted, `os.Getwd()` is used.

This cwd is later used for:

- the system prompt footer;
- relative paths in built-in file tools;
- session bucketing;
- skill discovery in project-local skill directories;
- `AGENTS.md` discovery.

## System prompt construction order

`BuildSystemPrompt` constructs the final system string. The current order is:

1. **Base identity**
   - Built-in default identity, unless replaced by `--system-prompt` or `$TERVA_HOME/SYSTEM.md`.
2. **terva docs pointer**
   - If docs are installed, a sentence points the agent at `$TERVA_HOME/docs`.
3. **Append blocks**
   - Literal `--append-system-prompt` values.
   - Auto-loaded `AGENTS.md` instructions.
   - Skill manifest.
   - Auto-swarm instruction addendum, if enabled.
4. **Footer**
   - Current date.
   - Current working directory.

Important: `--system-prompt` and `$TERVA_HOME/SYSTEM.md` replace the base identity, but the later append blocks and footer are still added.

## Base identity precedence

The base identity is chosen in this order:

1. `--system-prompt TEXT`
2. `$TERVA_HOME/SYSTEM.md`
3. built-in default identity from `packages/agent/systemprompt.go`

`$TERVA_HOME/SYSTEM.md` is optional. If it is missing, unreadable, or empty, terva silently falls back to the built-in identity.

## Literal appended prompt text

`--append-system-prompt TEXT` is repeatable. Each non-empty value is appended as a separate block after the base identity/docs pointer and before automatic `AGENTS.md` / skills / swarm text.

This flag injects literal text only. It does not read a file.

## `AGENTS.md` loading rules

terva has built-in `AGENTS.md` loading via `readAgentsContext(cwd, tervaHome)`.

### Locations loaded

terva loads, in order:

1. Global instructions from `$TERVA_HOME/AGENTS.md` or `$TERVA_HOME/AGENTS.MD`.
2. Project instructions from ancestors of `cwd`, from the filesystem root down to `cwd`.

For each directory, terva looks for the first matching filename in this order:

1. `AGENTS.md`
2. `AGENTS.MD`

Only one file per directory is loaded.

### Ordering

Global comes first. Project files then proceed from least-specific to most-specific:

```text
$TERVA_HOME/AGENTS.md
/repo/AGENTS.md
/repo/subdir/AGENTS.md
/repo/subdir/deeper/AGENTS.md
```

The generated prompt text says later files are more specific and may override earlier ones.

### Missing, empty, and duplicate files

- Missing files are ignored.
- Unreadable files are ignored.
- Empty or whitespace-only files are ignored.
- Duplicate absolute paths are ignored after the first load.
- No default file is created.

### Prompt format

If any files are found, terva appends one block beginning with:

```text
Project context instructions loaded from AGENTS.md files. Follow them when working in this repository. Later files are more specific and may override earlier ones.
```

Each loaded file is then included under a heading containing its path.

## Skill context

Skills are discovered at startup unless `--no-skill` / `--no-skills` is passed.

Search locations, in priority order, are:

```text
./.terva/skills/<name>/SKILL.md
$TERVA_HOME/skills/<name>/SKILL.md
./.claude/skills/<name>/SKILL.md
~/.claude/skills/<name>/SKILL.md
./.agents/skills/<name>/SKILL.md
~/.agents/skills/<name>/SKILL.md
```

Project-local paths are checked under the effective cwd only; unlike `AGENTS.md`, skill discovery does not walk every ancestor directory.

First match wins per skill name. User-installed skills shadow built-in skills with the same name.

At startup terva does **not** inject every skill body into the prompt. Instead it appends a compact manifest containing skill names, descriptions, and source pointers. The full body is loaded later only if the model calls the built-in `skill` tool.

Parsed skill frontmatter includes fields such as `name`, `description`, `allowed-tools`, and `permissions`, but `allowed-tools` and `permissions` are currently parsed for forward compatibility and are **not enforced** by terva.

## Auto-swarm context

If auto-swarm is enabled in settings, terva appends `AutoSwarmSystemAddendum` to the system prompt and registers the `swarm_spawn` tool. This tells the model it may spawn subagents for independent parallel work.

If auto-swarm is disabled, that addendum and tool are absent.

## Transcript context

The message transcript is part of every provider request.

Messages enter the transcript from:

- user prompts;
- queued user prompts submitted while the agent is busy;
- assistant messages;
- tool-result messages;
- synthetic image mirror messages for OpenAI/OpenAI-Codex when a tool result contains images;
- restored session messages when resuming;
- compaction checkpoints.

On resume, terva reads the JSONL session file for the cwd/session and hydrates messages. If a compaction checkpoint exists, the checkpoint becomes the effective transcript. terva also repairs unmatched assistant tool calls by inserting stub tool results so providers do not reject the restored transcript.

## Compaction

When transcript context grows too large, terva can compact it. Compaction replaces the in-memory transcript with a synthetic summary plus a kept tail of recent messages. A compaction checkpoint is appended to the session file so future resumes use the compacted transcript.

## User prompt file references

The interactive editor has convenience features for referring to files, but these features generally insert paths, not file contents.

### `@` file picker

The TUI `@` picker lists files and directories under the effective cwd. Selecting an entry inserts a chip such as:

```text
[file:README.md]
[dir:docs/]
```

On submit, these chips expand to full paths relative to cwd. The resulting user prompt contains the path. The file content is not automatically loaded; the model must call `read` if it needs contents.

### Drag-and-drop file chips

Drag-dropped paths may be collapsed in the editor to placeholders like:

```text
[file:1:example.go]
[dir:1:docs/]
```

On submit, these placeholders expand back to the stored full path. Again, this inserts a path, not file contents.

### Large pastes

Large pasted text is visually collapsed in the editor, then expanded back into the submitted prompt. This is direct prompt text injection, not file loading.

### `/study`

`/study` creates a canned user prompt such as:

```text
Read and understand everything in the current directory.
Read and understand everything in the directory docs.
Read and understand the file README.md.
```

It does not itself read files. It asks the model to inspect the target, usually by using tools.

## Built-in file tool path rules

The built-in file tools are registered unless `--no-tools` disables all tools or `--tools` restricts the set.

All built-in file tools resolve paths the same way:

- absolute paths are used as provided;
- relative paths are joined against the effective cwd;
- if cwd is empty, the process cwd is used.

### `read`

`read` rules:

- `path` is required.
- Directories are rejected.
- If the extension is `.png`, `.jpg`, `.jpeg`, `.gif`, or `.webp`, the file is returned as an image block.
- Other files are read as text.
- Text reads are capped at 50 KiB.
- Text output is capped at 2000 selected lines.
- `offset` is 1-indexed and selects the starting line.
- `limit` restricts selected line count.
- Files that look binary are rejected if a NUL byte appears in the first 8 KiB.
- Returned text is raw content without injected line numbers; the TUI renders its own gutter.

### `write`

`write` rules:

- `path` and `content` are required.
- Parent directories are created as needed.
- Existing files are overwritten.
- The tool result returns the written content, so that content becomes part of the transcript.

### `edit`

`edit` rules:

- `path` and at least one edit are required.
- Each edit is an exact `oldText` -> `newText` replacement.
- `oldText` must be non-empty.
- `oldText` and `newText` must differ.
- Each `oldText` must appear exactly once in the original file, unless
  the edit sets `replaceAll: true` — then every occurrence is replaced.
- When `oldText` has no exact match, a **whitespace-tolerant** pass
  retries it: lines compare after right-trimming, shifted by one
  uniform leading-whitespace delta, with blank lines matching under
  any shift. A unique tolerant match applies (the indent delta is
  re-applied to `newText` so the replacement lands at the file's real
  indentation); an ambiguous one is an error.
- A not-found error anchors on `oldText`'s first line when that line
  exists in the file and quotes the file's actual block there, so the
  model can correct itself without a re-read; an ambiguity error lists
  the occurrence line numbers.
- Multiple edits are validated against the original content before any write happens.
- Overlapping edits are rejected.
- UTF-8 BOM is preserved.
- CRLF line endings are detected and restored.
- Tool output is a compact unified/context diff with unchanged sections collapsed.

### `bash`

`bash` runs a shell command in the effective cwd.

Rules:

- `command` is required.
- Optional `timeout` is in seconds.
- stdout and stderr are merged.
- Output streams to the UI and is captured for the model.
- Captured output is capped at 50 KiB and 2000 lines.
- Truncated full output may be written to a temp file and referenced in the result.
- On Unix, terva starts the command in its own process group so cancellation can terminate child processes.

## Jail / sandbox file rules

The sandbox starts **locked for an interactive session and unlocked for
headless modes** (`--jail`/`--no-jail` override; see
[permissions.md](permissions.md)). In the TUI, `/jail` locks it and
`/unjail` unlocks it at runtime.

When locked:

- `read`, `write`, and `edit` call `Sandbox.CheckPath`.
- Paths are resolved/canonicalized with symlinks considered.
- Paths outside the sandbox root are rejected.
- Nonexistent targets are checked by resolving the nearest existing parent so symlink escapes are still caught.
- `bash` calls `Sandbox.CheckCommand`, which blocks obvious escape/destructive patterns such as `sudo`, `su`, `rm -rf /`, `cd /`, `cd ~`, `cd ..`, recursive `chmod`/`chown`, `mkfs`, and dangerous `dd` forms.

The jail is a guardrail, not a hard security boundary.

## Extension effects on context

Extensions can affect context in several ways:

- add tools, which changes the tool specs sent with requests;
- intercept or rewrite tool calls before execution;
- block entire turns via `BeforeTurn`;
- rewrite or suppress assistant-visible text via `BeforeAssistantMessage`;
- receive mirrored events.

When extension tools are merged, terva rebuilds the system prompt using the saved append blocks, so existing `--append-system-prompt`, `AGENTS.md`, skills, and auto-swarm addenda are preserved.

## Explicit file loading at startup

At startup, terva auto-loads only these file-based instruction sources:

- `$TERVA_HOME/SYSTEM.md` as the optional base identity replacement;
- `$TERVA_HOME/AGENTS.md` / `AGENTS.MD`;
- ancestor `AGENTS.md` / `AGENTS.MD` files from root to cwd;
- discovered `SKILL.md` manifests, with skill bodies loaded on demand through the `skill` tool;
- terva's own embedded docs installed under `$TERVA_HOME/docs`, referenced by path but not injected wholesale.

For arbitrary files there is an explicit startup mechanism: the repeatable `--context-file PATH` flag (resolved against the working directory) and `context_files` lists in user/project `.terva/config.json`. Entries load fail-fast (a missing or unreadable file is an error, not a silent skip), config entries are injected before flag entries, and the untrusted project layer is contained to the project root so a cloned repo's config cannot point at files outside it. In the immersive modes (`--chat`/`--play`) the config layers are ambient coding context and are gated out exactly like AGENTS.md — only an explicit per-run `--context-file` injects there. See `docs/plans/startup-context-files.md` for the design.

Beyond those, arbitrary project files become model context only when:

- the user pastes their contents into the prompt;
- the model calls `read`;
- a tool result includes their content;
- a skill body is loaded via the `skill` tool;
- they are included in `SYSTEM.md` or an `AGENTS.md` file that terva already loads.
