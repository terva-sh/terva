---
name: write-terva-skill
description: Author a new terva skill, a reusable `SKILL.md` instruction set that terva loads on demand. Install it where terva discovers it, in the project `.terva/skills/` or the global `$TERVA_HOME/skills/`. Use for create/add/write-a-skill requests, not extensions.
---

# Writing a terva skill

Use this skill when the user asks to **create, add, or write a skill** for
terva. Skim it first, then collaborate with the user on the one skill they
want and write it to the right location so terva discovers it on the next
launch.

## Skill vs. extension — pick the right one first

These are different things, and asking for "a skill" should land here, NOT on
`write-terva-extension`:

- A **skill** is *just instructions*: a `SKILL.md` markdown file with a short
  YAML header. terva loads it for the model; nothing executes. Reach for a
  skill to capture a repeatable workflow, house style, checklist, or
  domain knowledge the agent should follow ("how we cut a release", "review
  rules for this repo").
- An **extension** is *code*: an executable terva launches as a subprocess to
  add a slash command, an LLM tool, or a guard hook. Reach for an extension
  when you need new behavior or a new capability, not just guidance.

If the user wants the agent to *know or do something by following written
steps*, it's a skill — stay here. If they need a new tool/command/hook, switch
to `write-terva-extension`.

## Anatomy of a skill

One directory per skill, containing a `SKILL.md`:

```
<skills-dir>/<name>/SKILL.md
```

`SKILL.md` is YAML frontmatter followed by a markdown body:

```markdown
---
name: cut-release
description: Steps and guardrails for cutting a versioned release of this repo. Use when the user asks to release, tag, or publish a version.
---

# Cutting a release

1. ...
2. ...
```

Frontmatter fields:
- `name` (recommended) — the skill id the model invokes; defaults to the
  directory basename if omitted. Use kebab-case and match the directory name.
- `description` (required in practice) — a **one-line trigger**. This is the
  ONLY part of the skill the model sees up front (see below), so phrase it as
  *what the skill is for and when to use it* — include the words a user would
  say ("review", "release", "deploy"). A vague description means the skill is
  never reached.
- `allowed-tools` / `permissions` (optional) — parsed for compatibility with
  related ecosystems but **advisory only in this version** (not enforced). If
  a skill must restrict itself to certain tools, also say so in the body so
  the model self-regulates.

The body is plain markdown — whatever the agent should read when the skill is
loaded. Keep it focused; for anything large, point at a file by path
(`see docs/foo.md`) rather than pasting it inline.

## Where skills live — and which to choose

terva searches these locations, **first match wins per name** (so a project or
user skill shadows a built-in of the same name):

| Location | Scope | Notes |
|---|---|---|
| `./.terva/skills/<name>/SKILL.md` | **project (this working dir)** | loads only in a **trusted** workspace |
| `$TERVA_HOME/skills/<name>/SKILL.md` | global (you, everywhere) | always loads |
| `./.claude/skills/<name>/SKILL.md`, `~/.claude/skills/<name>/SKILL.md` | claude-compat | a SKILL.md written for Claude works unchanged |
| `./.agents/skills/<name>/SKILL.md`, `~/.agents/skills/<name>/SKILL.md` | agents-compat | same idea |

Choosing:
- **"Create a skill here / in this project"** → write it to
  `./.terva/skills/<name>/SKILL.md` (relative to the working directory). This
  is the default for a project-specific workflow and travels with the repo.
- **"A skill I can use everywhere"** → write it to
  `$TERVA_HOME/skills/<name>/SKILL.md`. On macOS `$TERVA_HOME` defaults to
  `~/Library/Application Support/terva`; honor the env var if it's set.

### Trust gate (important for project skills)

Project skills (`./.terva/skills/`, `.claude`, `.agents`) load **only when the
workspace is trusted** — a cloned repo can't silently inject instructions into
the model. So after writing a skill into the working dir, tell the user it
takes effect once the directory is trusted:

```bash
terva trust          # trust the cwd so its project skills/extensions/context load
terva --trust ...    # or trust just for one run (not persisted)
```

User/global skills under `$TERVA_HOME/skills/` load regardless of trust.

## How terva loads a skill (why the description carries the weight)

terva puts only the **manifest** — every skill's `name` + `description` — into
the system prompt. The model reads the body **on demand** by calling the
built-in `skill` tool with the name, the turn it actually needs it. Two
consequences:

- The `description` is the entire trigger. Make it specific and include the
  user's likely phrasing. This is exactly why "create a skill" should match a
  skill named for that, not an extension authoring guide.
- The body should be **self-contained** — when it's pulled in, it's all the
  model gets. Don't assume earlier context.

## Minimal example

`./.terva/skills/changelog-entry/SKILL.md`:

```markdown
---
name: changelog-entry
description: Add a properly formatted entry to CHANGELOG.md in this repo's house style. Use when asked to update the changelog or record a change.
---

# Adding a changelog entry

- Put new entries under the `## Unreleased` heading, newest first.
- One line per change: `- <imperative summary> (#PR)`.
- Group by `Added` / `Changed` / `Fixed`; create the subheading if absent.
- Don't invent a version or date — that happens at release time.
```

## Authoring checklist

- Directory name = `name` = kebab-case, unique.
- Description is a sharp one-liner that names the trigger words.
- Body is focused and self-contained; large material is referenced by path.
- Pick project vs. global deliberately; for project, mention the trust gate.
- Don't duplicate an existing skill name unless you intend to shadow it.

## Process to follow with the user

1. Confirm it's a skill they want, not an extension (see the split above).
2. Get the one-sentence purpose, and the trigger ("when should the agent reach
   for this?"). That sentence becomes the `description`.
3. Choose location: project (`./.terva/skills/<name>/`) by default, or global
   (`$TERVA_HOME/skills/<name>/`) if it's not repo-specific.
4. Write `<name>/SKILL.md` with frontmatter + a focused body.
5. If it went into the project, remind them to `terva trust` the directory.
6. Tell them it loads on the next terva launch; verify with the steps below.

## Verify it loaded

- Run `/skills` in terva (or `terva skills`) to list discovered skills — the
  new one should appear with its description. (Built-in skills are hidden from
  this picker; user/project ones show.)
- If it's missing: re-check the path and the `SKILL.md` filename, confirm the
  frontmatter parses (a malformed header yields an empty name/description), and
  for a project skill confirm the workspace is trusted.
- After editing a `SKILL.md`, relaunch terva (or re-run discovery) so the
  change is picked up.
