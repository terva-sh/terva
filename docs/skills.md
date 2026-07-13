# terva skills

A skill is a reusable instruction set written as a single
`SKILL.md` file with a YAML frontmatter header. terva discovers skills
at startup and surfaces them to the model in two ways:

1. The system prompt gains a short manifest — a header, then one line
   per skill with its source in brackets:

   ```
   Available skills (call the `skill` tool with a name from this list to load its full instructions):
   - code-review [~/.terva/skills/code-review/SKILL.md]: Run a thorough self-review pass on a recent change.
   - write-terva-skill [builtin]: Author a new terva skill and install it where terva discovers it.
   ```
2. A built-in `skill` tool lets the model load any one skill's full
   body on demand.

The on-demand-load model keeps token usage cheap: only the manifest
goes into every request; the body is fetched as a tool result the
one or two turns the model actually needs it.

## Anatomy

```markdown
---
name: code-review
description: Run a thorough self-review pass on a recent change.
allowed-tools: [read, bash]
permissions:
  bash: ["git diff*", "git log*"]
---

# Code review

When asked to review code, ...
```

### Frontmatter fields

| field | required | purpose |
|---|---|---|
| `name` | optional | skill identifier; defaults to the directory name |
| `description` | required | one-line summary shown in the system prompt |
| `allowed-tools` | optional | list of tool names the skill needs; loading the skill reveals them |
| `permissions` | optional | per-tool patterns; informational |

`allowed-tools` is a **visibility hint, not a grant**. Under lazy tool
visibility, loading the skill activates the capability groups of the
tools it names, so they are advertised to the model from its next turn
instead of staying hidden. It never confers authority: a revealed tool
still faces its normal permission and trust gate when called, and the
field is a no-op when lazy tool visibility is off.

`permissions` is still **parsed but not enforced**. It appears in the
rendered skill body so the model can see it and self-regulate. Future
versions may enforce.

The body (everything after the second `---`) is plain markdown.
There's no template engine; the model sees what you write.

## Discovery

terva looks in these directories, in priority order, and registers the
first `SKILL.md` it finds for each unique name:

| location | scope |
|---|---|
| `./.terva/skills/<name>/SKILL.md` | project (native) |
| `$TERVA_HOME/skills/<name>/SKILL.md` | global (native) |
| `$TERVA_HOME/extensions/<ext>/skills/<name>/SKILL.md` | extension (global) |
| `./.terva/extensions/<ext>/skills/<name>/SKILL.md` | extension (project) |
| `./.claude/skills/<name>/SKILL.md` | project (claude-compat) |
| `~/.claude/skills/<name>/SKILL.md` | global (claude-compat) |
| `./.agents/skills/<name>/SKILL.md` | project (agent-compat) |
| `~/.agents/skills/<name>/SKILL.md` | global (agent-compat) |
| (compiled into the binary) | built-in |

An installed, **enabled** extension may ship a `skills/` directory beside
its `extension.json` — a data-only bundle contribution. Those rank after
your own dirs, so a bundle can never shadow a skill you deliberately
wrote; a disabled extension contributes nothing, skills included.

The compat paths are deliberate: a `SKILL.md` written for an existing
skill ecosystem works in terva unchanged. Drop your existing
`.claude/skills/` or `.agents/skills/` directories into a project and
terva will pick them up.

`$TERVA_HOME` defaults to `~/Library/Application Support/terva/` on macOS,
`$XDG_STATE_HOME/terva` on Linux, `%LOCALAPPDATA%\terva` on Windows.

`--no-skill` skips discovery entirely for a run: no manifest in the
system prompt, no `skill` tool, not even the built-ins.

### Workspace trust gates the project tiers

Every **project** row above (the cwd-anchored ones: `./.terva/skills/`,
`./.claude/skills/`, `./.agents/skills/`, and project-extension bundles)
is dropped when the workspace is untrusted — the default for a workspace
you haven't trusted yet. A repo you clone therefore cannot inject
`SKILL.md` instructions into the model's prompt by merely being opened.
Global, user, and built-in skills load regardless.

If a project's skills seem to be missing, that is almost always why:
trust the workspace and they appear.

### Built-in skills

Seven skills ship inside the binary — `write-terva-card`,
`write-terva-extension`, `write-terva-locale`, `write-terva-lore`,
`write-terva-persona`, `write-terva-skill`, `write-terva-themes` — the
ones that teach the model how to author terva's own artifacts.

They are fully active: they appear in the system-prompt manifest (tagged
`[builtin]`) and the model loads them through the `skill` tool like any
other. They are deliberately **hidden from `/skills`** and the other
user-facing pickers, which show only skills you installed or shipped in
your project. Being last in priority, a skill of your own with the same
name shadows the built-in.

## Inspecting installed skills

In terva, run `/skills`. A picker lists the discovered skills — the ones
you installed or shipped, with their description and source path; the
built-ins stay out of it. Press enter on a row to view the full body
inline. Press esc to go back.

## How the model uses a skill

1. The system prompt tells the model that skills exist and what
   their names + descriptions are.
2. The model recognises a request that maps to a known skill and
   calls the `skill` tool with `name: "<skill-name>"`.
3. The `skill` tool returns the markdown body as the tool result.
4. The model follows the body's instructions.

You can prompt the model directly to use a skill (e.g. "use the
code-review skill") but you don't have to — the descriptions in the
manifest are enough for it to choose on its own.

## Writing good skills

- **Be procedural.** Number steps. Tell the model what to do in what
  order. Skills are habits, not knowledge dumps.
- **Be precise about boundaries.** "Stop after step 4" is more
  effective than "don't go too far".
- **Trim aggressively.** A 200-line skill bloats every turn the
  model uses it. Aim for 20–80 lines.
- **One skill per behaviour.** Don't pack three workflows into one
  SKILL.md; the model picks one path. Two separate skills work better.
- **Lead with the trigger.** First paragraph should make it
  obvious *when* to use the skill so the model self-selects correctly.

## Examples

See `examples/skills/` for two starter skills:

- `code-review/` — self-review pass on a recent diff
- `test-fix/` — diagnose + minimally fix a failing test

## Comparison to other discovery layouts

| ecosystem | path | terva reads it? |
|---|---|---|
| (native) | `.terva/skills/<name>/SKILL.md` | yes |
| (claude-style) | `.claude/skills/<name>/SKILL.md` | yes |
| (agent-style) | `.agents/skills/<name>/SKILL.md` | yes |

Cross-pollination is intentional: pick whichever convention you're
already using and terva tags along.
