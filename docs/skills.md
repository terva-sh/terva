# terva skills

A skill is a reusable instruction set written as a single
`SKILL.md` file with a YAML frontmatter header. terva discovers skills
at startup — and again on demand, see [Reloading](#reloading) — and
surfaces them to the model in two ways:

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
| `argument-hint` | optional | one-liner saying what the skill wants after its name |
| `disable-model-invocation` | optional | keep the skill out of the model's manifest; human-invoked only |

Multi-word fields accept both the hyphen and the underscore spelling
(`allowed_tools`, `argument_hint`, `disable_model_invocation`). SKILL.md
files travel between ecosystems that disagree about which is canonical,
and a key terva silently ignored would be a behaviour you thought you
had configured.

A `name` may not contain `:` — that character separates a namespace
from a name (see [Namespaces](#namespaces)). One that does is loaded
with the colon rewritten to `-`, and the reason is reported.

`disable-model-invocation: true` removes the skill from the
system-prompt manifest, so the model never picks it on its own. It does
**not** block the `skill` tool: `/skill <name>` works by priming the
editor with a directive the model then acts on, so refusing the tool
would break the only way left to invoke it. The flag scopes model
*discovery*, not the load path.

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

| location | scope | namespace |
|---|---|---|
| `./.terva/skills/<name>/SKILL.md` | project (native) | `terva` |
| `$TERVA_HOME/skills/<name>/SKILL.md` | global (native) | `terva` |
| (compiled into the binary) | built-in | `builtin` |
| `$TERVA_HOME/extensions/<ext>/skills/<name>/SKILL.md` | extension (global) | `ext:<ext>` |
| `./.terva/extensions/<ext>/skills/<name>/SKILL.md` | extension (project) | `ext:<ext>` |
| `./.claude/skills/<name>/SKILL.md` | project (claude-compat) | `claude` |
| `~/.claude/skills/<name>/SKILL.md` | global (claude-compat) | `claude` |
| `./.agents/skills/<name>/SKILL.md` | project (agent-compat) | `agents` |
| `~/.agents/skills/<name>/SKILL.md` | global (agent-compat) | `agents` |

Built-ins sit in the **middle** of that ladder, not at the bottom. Above
them are the native dirs, where shadowing a built-in is something you
did on purpose. Below them are the extension bundles and the
foreign-tool compat dirs — directories terva reads but does not own. A
`~/.claude/skills/handoff` written against a different runtime should
not silently replace the `handoff` terva ships and documents, which is
exactly what used to happen.

To override a built-in, write your version natively
(`.terva/skills/` or `$TERVA_HOME/skills/`), or drop the built-ins
entirely with `--no-builtin-skills`.

An installed, **enabled** extension may ship a `skills/` directory beside
its `extension.json` — a data-only bundle contribution. Those rank after
your own dirs and the built-ins, so a bundle can never shadow either; a
disabled extension contributes nothing, skills included.

The compat paths are deliberate: a `SKILL.md` written for an existing
skill ecosystem works in terva unchanged. Drop your existing
`.claude/skills/` or `.agents/skills/` directories into a project and
terva will pick them up.

`$TERVA_HOME` defaults to `~/Library/Application Support/terva/` on macOS,
`$XDG_STATE_HOME/terva` on Linux, `%LOCALAPPDATA%\terva` on Windows.

`--no-skill` skips discovery entirely for a run: no manifest in the
system prompt, no `skill` tool, not even the built-ins. The narrower
`--no-builtin-skills` drops only the compiled-in skills — user and
project skills keep working.

### Namespaces

Losing the bare name is not vanishing. Every skill also answers to a
namespace-qualified name built from its tier:

```
claude:handoff              the .claude/skills one
builtin:handoff             the one compiled into the binary
terva:handoff               the native one, falling back to the built-in
ext:web:web-research        a skill bundled by the "web" extension
```

Both `/skill <name>` and the model's `skill` tool accept either form,
case-insensitively. An unqualified name resolves the way it always
has — down the ladder, first match wins — which after the ordering
above means: your native skill, else the built-in, else the
highest-ranked foreign one. Qualifying is how you reach past that.

`terva:` is an alias **group** rather than a single tier: it tries the
native dirs first and then the built-ins, so `terva:handoff` resolves
whether or not you have written a `handoff` of your own.

Only the winner of a bare name goes into the model's system-prompt
manifest. Two entries called `handoff` whose descriptions differ in
nuance would make the model's choice a coin flip, so the shadowed one
stays out of the prompt and stays reachable — by you in `/skills` and
`/skill claude:handoff`, and by the model when you name it. The `skill`
tool notes the alternatives when it loads a contested name, which
teaches the syntax at the one moment it matters instead of spending
prompt tokens on every turn that it doesn't.

### When two skills share a name

Collisions are silent by nature — the loser simply never appears — so
terva reports them in two places:

- **`/skills`** lists a shadowed skill under its qualified name, tagged
  with what beat it. Its detail view spells out the name to load it by.
- **`terva doctor`** prints a `skills` line with the active count and
  every collision, which is the fastest way to answer "why is this
  skill behaving like a different one?"

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

Thirteen skills ship inside the binary. Seven teach the model how to
author terva's own artifacts (`write-terva-card`,
`write-terva-extension`, `write-terva-locale`, `write-terva-lore`,
`write-terva-persona`, `write-terva-skill`, `write-terva-themes`), and
six are standing workflows:

- `grilling` interviews the user before building, one round of open
  decisions at a time, so the choices are theirs rather than invented.
  Say **"grill me"** to invoke it, which is the phrasing it has been
  seen to load on. Other wordings may not reach it, so name the skill
  outright if it matters. Loading is not guaranteed in any case: a
  built-in reaches the model through the description manifest, and the
  model decides whether to call it.
- `handoff` compacts the current session into a handoff document under
  `$TERVA_HOME/handoffs/` that a fresh session or another agent can pick
  up ("hand this off", for when resuming the same session isn't the
  plan).
- `house-style` carries terva's writing standard for prose the project
  ships and prose the agent speaks ("clean this up", "this reads like a
  machine wrote it").
- `init-workspace` surveys a repository and writes or refreshes its
  `AGENTS.md` ("set terva up for this repo").
- `retrospective` distills a session's reusable lessons into the durable
  homes future sessions read: AGENTS.md, project skills, and memory when
  available ("what did we learn", "remember this for next time").
- `troubleshoot-terva` is the symptom-first runbook for terva itself,
  covering extensions, connectors, MCP, context weight, and sessions
  ("why isn't my extension loading").

They are fully active: they appear in the system-prompt manifest (tagged
`[builtin]`) and the model loads them through the `skill` tool like any
other. They are also listed in `/skills` and offered by `/skill`
completion, tagged `built-in` and sorted **after** your own skills, so
the first screen of the picker stays the set you installed. A skill of
your own in a **native** dir shadows the built-in of the same name; a
`.claude`/`.agents` skill or an extension bundle does not — see the
ladder above.

## Inspecting installed skills

In terva, run `/skills`. A picker lists every discovered skill with its
description and source — yours and your extensions' first, then the
built-ins. Press enter on a row to view the full body inline. Press esc
to go back.

Built-ins are listed because `/skill <name>` has always resolved them:
a picker that hid what you could type understated what was there. It
also has to show them to stay coherent — a row tagged `shadowed by
builtin` names a tier, and that tier needs to be somewhere you can look
at.

A skill that lost its name to a higher tier appears under its qualified
name (`claude:handoff`) with a `shadowed by …` tag, so the picker
doubles as the answer to "where did my skill go?".

`terva skills` reports the same thing non-interactively — name, tier,
file, and any shadowing — so it works in a pipe or a script. `terva
doctor` carries the summary line: the count of skills the current
directory actually loads and whether workspace trust is holding project
skills back.

## Reloading

Skills are discovered when a session starts, so a `SKILL.md` written
mid-session is not there yet. You do not have to relaunch.

**`/reload-skills`** re-runs the discovery ladder, swaps the session's
live catalog, and rebuilds the system prompt when the manifest changed.
After it, a skill you just wrote is both loadable by name and visible to
the model.

The rebuild is conditional because it is not free. The manifest lives in
the pinned prompt prefix, so changing it discards the provider's prompt
cache and the next turn re-reads the transcript uncached. terva pays
that only when the manifest actually differs:

| you changed | rebuilt? | why |
|---|---|---|
| added, deleted, or renamed a skill | yes | the list the model reads is different |
| edited `description` | yes | it is manifest text — it is how the model decides the skill applies |
| edited the **body** | no | the body is never in the prompt; it arrives as a tool result |

So the usual authoring beat — tweak the instructions, ask again — costs
nothing, and still serves the model the rewritten text the next time it
loads the skill.

Opening the `/skills` picker also refreshes the catalog (and `r`
re-scans without closing), but it never rebuilds the prompt: browsing
your skills must not cost you a cache. The consequence is that a skill
picked up that way is loadable when you *name* it, while the model has
not been told it exists.

Because project skills are trust-gated, `/reload-skills` re-reads the
trust verdict too — so `terva trust` followed by `/reload-skills` brings
a new project skill live without a relaunch.

Over ACP the command does only the first half: the catalog is swapped,
but the system prompt is fixed at session build and cannot be rebuilt
mid-session, so the model learns of the new skill on a new session. The
confirmation says so.

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
