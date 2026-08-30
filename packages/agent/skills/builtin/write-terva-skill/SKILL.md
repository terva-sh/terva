---
name: write-terva-skill
description: Author a new terva skill, a reusable `SKILL.md` instruction set that terva loads on demand. Install it where terva discovers it, in the project `.terva/skills/` or the global `$TERVA_HOME/skills/`. Use for create/add/write-a-skill requests, not extensions.
---

# Writing a terva skill

Use this skill when the user asks to **create, add, or write a skill** for
terva. Skim it first, then collaborate with the user on the one skill they
want and write it to the right location so terva discovers it on the next
launch.

## Skill vs. extension, and picking the right one first

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
steps*, it's a skill, so stay here. If they need a new tool, command, or hook,
switch to `write-terva-extension`.

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
- `name` (recommended). The skill id the model invokes. It defaults to the
  directory basename if omitted. Use kebab-case and match the directory name.
- `description` (required in practice). A **one-line trigger**. This is the
  ONLY part of the skill the model sees up front (see below), so phrase it as
  *what the skill is for and when to use it*, and include the words a user
  would say ("review", "release", "deploy"). A vague description means the
  skill is never reached.
- `allowed-tools` / `permissions` (optional). Parsed for compatibility with
  related ecosystems but **advisory only in this version** (not enforced). If
  a skill must restrict itself to certain tools, also say so in the body so
  the model self-regulates.
- `disable-model-invocation` (optional). The invocation choice, and it
  spends one of two budgets:
  - **Context load** is what always-loaded material costs the model's window.
    Omit this field and the skill is model-invoked: its description sits in
    the manifest every turn, whether or not it ever fires, in exchange for
    the agent reaching it unprompted and other skills being able to name it.
  - **Cognitive load** is what it costs *you*. Set it to `true` and the skill
    leaves the manifest: no per-turn cost, but you become the index who has
    to remember it exists. In terva the flag scopes model *discovery* only, so
    `/skill <name>` still loads it.

  Choose model-invocation when the agent must reach the skill on its own. If
  it only ever fires by hand, turn it off and pay nothing per turn.

### A colon in a value silently empties the whole block

Frontmatter is parsed as real YAML, so a value holding a colon followed by a
space is a parse error rather than text:

```yaml
description: Apply before writing prose: a README, comments, commit messages.
```

terva does not report that error. It abandons the **entire** frontmatter block
and leaves every field empty, then falls back to the directory name for `name`.
So the skill still lists under the right name, and its manifest line renders the
fallback text `(no description)`. The model sees a name with nothing telling it
when to use it. `allowed-tools` and `disable-model-invocation` are lost in the
same silence, so a stray colon can also put a user-invoked skill back in front
of the model without saying so.

Either rephrase to drop the colon, or quote the whole value:

```yaml
description: Apply before writing prose, including a README and commit messages.
description: "Apply before writing prose: a README, comments, commit messages."
```

A built-in is caught by `TestBuiltinNamesAreNamespaceSafe`, which fails on an
empty description. **Nothing covers a skill you install yourself.** So if a
skill of yours never fires, look for `(no description)` against its name before
you rewrite the wording. That marker is this bug, and no amount of sharpening
will fix it.

The body is plain markdown; there is no template engine, so the model sees
exactly what you write. **Writing the body** below covers the craft.

## Where skills live, and which to choose

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
workspace is trusted**, so a cloned repo can't silently inject instructions
into the model. After writing a skill into the working dir, tell the user it
takes effect once the directory is trusted:

```bash
terva trust          # trust the cwd so its project skills/extensions/context load
terva --trust ...    # or trust just for one run (not persisted)
```

User/global skills under `$TERVA_HOME/skills/` load regardless of trust.

## How terva loads a skill (why the description carries the weight)

terva puts only the **manifest**, every skill's `name` plus `description`, into
the system prompt. The model reads the body **on demand** by calling the
built-in `skill` tool with the name, the turn it actually needs it. Two
consequences:

- The body should be **self-contained**. When it is pulled in, it is all the
  model gets, so don't assume earlier context.
- The `description` is the entire trigger, and it is loaded on every turn.

Write that description as a **context pointer**: a reference that names
out-of-context material and encodes the condition for reaching it. A pointer
does two jobs. It says what the material is, and it lists the **branches** that
should trigger it, a branch being a distinct case the skill handles. Its
*wording*, not its target, decides whether the skill is reached at all. A
skill the agent must not miss, sitting behind a vague description, is a
**variance bug**: sharpen the wording first, and move the material only if
sharpening fails.

Every word here costs on every turn, so prune it harder than the body:

- **Front-load the trigger word.** The pointer is where it does its work.
- **Make that word rare.** A common verb buys you nothing, even sitting in
  the description verbatim. Measured: a skill whose description contained
  "questioned" did not load for "Question me about this first", but did load
  for "Grill me". Words like *ask*, *check*, and *review* saturate the prompt
  and carry no signal. Pick a distinctive word and document it, so users know
  what to say.
- **Never trigger on self-assessment.** A description firing "when you are
  about to invent a value the user never stated" does not work, because the
  model does not notice it is doing that. Trigger on what the *user* says, or
  on an observable artifact. Never on the model spotting its own gap.
- **One trigger per branch.** Synonyms renaming a single branch are one
  branch written twice; keep only genuinely distinct cases.
- **Cut identity the body already carries.** The description says when to
  come, not what is inside. This is why "create a skill" should match a skill
  named for that, not an extension authoring guide.

## Writing the body

A body mixes two content types: **steps** (ordered actions) and **reference**
(definitions, rules, and facts consulted on demand). All steps is a recipe,
all reference is a rulebook, and most skills are both.

### Put each piece at the right rung

Rank material by how immediately the model needs it:

1. **In-file step.** What the agent does, in order.
2. **In-file reference.** Consulted on demand. A flat peer-set (every rule
   of a review on one rung) is a fine arrangement, not a smell.
3. **Disclosed reference.** Pushed into a separate file and named by path,
   read only when the skill says to.

**Progressive disclosure** is the move down that ladder. It protects the top
of the file rather than merely saving tokens. The test is branching: inline
what every branch needs, and push out what only some branches reach. In a
skill that has steps, in-file reference that should have been disclosed
buries them, and attending to them becomes a coin flip.

**Disclosure needs a real file.** A project or user skill can point at a
sibling (`see ./reference.md`) because it lives on disk. A **built-in** skill
cannot: its directory is embedded in the binary, only `SKILL.md` is ever
read, and its path is the pseudo-path `builtin:<name>`, which no tool can
open. Everything a built-in needs must be inline.

**Sprawl** is the failure mode: a body too long even when every line is live.
Attention thins across the excess. The cure is the ladder, plus a split by
branch or sequence so each path carries only what it needs.

### End every step on a completion criterion

The criterion tells the agent the work is done. Two properties make it a
lever:

- **Clarity.** Can it tell done from not-done? A vague bound ("understanding
  reached") invites **premature completion**: ending early, attention
  slipping to *being done*. The steps still visible ahead supply the pull;
  the criterion's clarity is the resistance. Sharpen the bound first, because
  that is local and cheap. Only if it stays irreducibly fuzzy *and* you watch
  the model rush it should you hide the later steps by splitting, and that
  works only across a real context boundary, which here means a `swarm_spawn`
  sub-agent or a handoff. Loading one skill from another leaves the later
  steps in context and clears nothing.
- **Demand.** How much it requires. "Every exported symbol accounted for"
  forces work that "list the changes" does not. Demand is not step-bound:
  "every rule applied" binds a page of flat reference just as "every step
  done" binds a sequence.

The strongest criteria are both checkable and exhaustive.

### Name the behaviour you want

A **leading word** is a compact concept the model already holds from
pretraining, reused as a token until it carries a whole region of behaviour
(*red*, *tight*, *frontier*, *fog of war*). Coining your own works only if you
define it, and a made-up word recruits no priors, so reach for an existing one
first. Hunt for passages that collapse into one: "fast, deterministic,
low-overhead" becomes a **tight** loop, and "a loop you believe in" becomes
one that goes **red** on the bug, which turns a fuzzy gate into a binary
observable.

**Prompt the positive.** Steering by prohibition drags the forbidden
behaviour into context and makes it *more* available: say *don't think of an
elephant* and the elephant is all there is. State the target instead ("write
one-line comments") so the banned form is never spoken. Keep a prohibition
only as a guardrail you cannot phrase positively, and pair it with the
positive target.

### Prune before you ship

- **One source of truth per meaning.** Duplication costs maintenance and
  tokens, and inflates a meaning's prominence past its real rank.
- **The environment is a source of truth too.** A skill restating `--help`,
  a config file, or the directory layout is a **cache**, and it earns its load
  only when the lookup is expensive. Cache what the agent cannot find by
  looking: the unwritten convention, the reason behind a choice, the gotcha no
  config confesses.
- **Cut no-ops.** An instruction the model already follows by default pays
  load to say nothing. The test is model-relative, not reader-relative: two
  people disagreeing about a no-op disagree about the default, and they settle
  it by running the skill, not by arguing. Delete the whole sentence rather
  than trimming words from it.
- **Check relevance,** or the file accretes **sediment**: stale layers that
  settle because adding feels safe and removing feels risky.

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
- Don't invent a version or date, because that happens at release time.
```

## Authoring checklist

- Directory name = `name` = kebab-case, unique.
- Description front-loads the trigger and carries one trigger per branch.
- Invocation chosen deliberately: model-invoked earns its permanent manifest
  line, or `disable-model-invocation` keeps it off the per-turn budget.
- Body is self-contained. Material only some branches need is disclosed by
  path, and never from a built-in, which has no readable path.
- Every step ends on a criterion the model can check.
- No sentence survives that the model would have obeyed anyway.
- Pick project vs. global deliberately; for project, mention the trust gate.
- Don't duplicate an existing skill name unless you intend to shadow it.

## Process to follow with the user

1. Confirm it's a skill they want, not an extension (see the split above).
2. Get the one-sentence purpose, and the trigger ("when should the agent reach
   for this?"). That sentence becomes the `description`.
3. Choose location: project (`./.terva/skills/<name>/`) by default, or global
   (`$TERVA_HOME/skills/<name>/`) if it's not repo-specific.
4. Write `<name>/SKILL.md` with frontmatter + a focused body, then take one
   pruning pass over it against the checklist above.
5. If it went into the project, remind them to `terva trust` the directory.
6. Tell them it loads on the next terva launch; verify with the steps below.

## Verify it loaded

- Run `/skills` in terva (or `terva skills`) to list discovered skills. The
  new one should appear with its description, alongside the built-ins, which
  are listed too and tagged as such.
- If it's missing: re-check the path and the `SKILL.md` filename, confirm the
  frontmatter parses (a malformed header yields an empty name/description), and
  for a project skill confirm the workspace is trusted.
- After editing a `SKILL.md`, relaunch terva (or re-run discovery) so the
  change is picked up.
