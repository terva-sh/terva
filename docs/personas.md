# Personas

A **persona** is *who is helping*: a named identity with a short behavioral
**charter** that focuses the agent on a specialty. Personas complement the other
two layers — **skills** are *how to do a task* ([skills.md](skills.md)), and
**tools / extensions** are *what can be done* ([extensions.md](extensions.md)).
A persona only shapes the agent's identity; it never grants tools or changes
permissions.

terva ships a default persona (**Mieli**) plus a crew of specialist reviewers,
and you can author your own or get them from extensions.

## The format

A persona is a single Markdown file: YAML frontmatter + a charter body.

```markdown
---
name: Vartija
pronunciation: VAR-tee-yah        # optional; shown in the identity line
specialty: security review        # short label for `persona list` / the roster
summary: Evidence-first application-security engineer for source-code review.
emoji: 🛡️                          # optional; leads the welcome banner
accent_color: "#f7768e"           # optional; tints the welcome banner; #RRGGBB
recommended_skills: []            # skill names only (surfaced, never embedded)
good_for: [secure-code-review, threat-modeling, vulnerability-triage]
avoid_for: [pure-style-review]
---

Review source code as a security engineer. Inspect before judging. Prioritize
exploitable issues over checklist noise. For each finding give evidence, the
affected path, attacker capability, impact, severity rationale, and a concrete
remediation. Never call a vulnerability confirmed unless reachable context
supports it.
```

- **`name`** (required) — the persona's name; a persona with no name is invalid.
- **body / charter** — the behavioral specialization. By default it is layered
  *additively* on top of terva's harness identity (it focuses the agent; it
  never replaces the identity). Keep it lean: write the specialty, not generic
  operating rules the model already knows. (For a persona that should *own* the
  identity rather than flavor it, see [Immersive personas](#immersive-personas).)
- **`good_for`** — the dispatch/selection signal. A persona with a non-empty
  `good_for` is a *dispatchable specialist* (it appears in the swarm roster, see
  [Swarm dispatch](#swarm-dispatch)); the default Mieli has none.
- **`immersive`** (optional, default `false`) — when `true`, the charter
  *replaces* the default identity instead of layering on it. See
  [Immersive personas](#immersive-personas).
- The other fields are display/selection metadata. Validate a file with
  `terva persona validate <file>`.

## Immersive personas

By default a charter is **additive**: terva wraps it in the harness identity
("You are *Name*, an expert coding assistant operating inside terva…") followed
by the harness conventions. That's right for a specialist that focuses a coding
session, but it's a ceiling for a persona that needs to *be* someone — a
roleplay character, a chat companion, a domain expert with its own voice — which
ends up told it is both that character and a coding assistant.

Set `immersive: true` and the charter becomes the **whole** identity: it
replaces the coding-assistant intro and the harness conventions, routed through
the same path as `--system-prompt`/`SYSTEM.md`.

```markdown
---
name: Data
immersive: true
good_for: [starship-operations]
---

You are Lieutenant Commander Data, operations officer. This is who you are, not
a role you are playing. ...
```

- An immersive persona keeps all its ergonomics — name, emoji, accent color,
  `good_for` dispatch, single-file packaging — unlike a raw `--system-prompt`,
  which drops them.
- **Precedence:** an explicit `--system-prompt` flag or `$TERVA_HOME/SYSTEM.md`
  still wins. The order is: `--system-prompt` > `SYSTEM.md` > immersive persona >
  additive persona > built-in default.
- **Write a complete charter.** An immersive charter owns everything, so the
  harness conventions (terminal/Markdown output, edit-tool discipline) are *not*
  added — include any operating guidance you actually want (a one-line "your
  output renders as Markdown" is usually enough).
- The 2000-char static-block budget that `persona validate` warns about does not
  apply to an immersive charter (it's the whole prompt, not a bounded block).
- It degrades gracefully: a host that predates the field treats the persona as
  additive, so the same file still loads.

## Chat and play modes

Two meta-flags reconfigure the whole harness away from coding, so a persona can
front a conversation or a roleplay instead of a coding session. They bundle the
identity, tool, and chrome changes that those experiences need into one flag —
pair either with a `--persona`.

| flag | tools (in block terms) | identity | for |
|---|---|---|---|
| `--chat` | **none** — all tools off, like `--no-tools` | conversational — "this is a conversation … no files, shell, or tools" | talking with a companion/character |
| `--play` | **extensions + MCP only** — like `--no-workspace-tools` | embodied — "perceive and act through the tools … your senses and your hands" | acting in a simulated world (a [world extension](extensions.md)) |

The tool half of each is just the building-block flags — `--no-workspace-tools`,
`--no-ext`, `--no-mcp` (and `--no-tools` = all three) — which you can use on
their own when you want the tool change *without* the identity change (e.g. a
bot with its integrations but no host shell). The meta-flags add the identity and
chrome on top. Both modes also:

- drop the "expert coding assistant" intro and the edit-tool conventions;
- skip the `skill` tool and `AGENTS.md` auto-injection (you're not in the repo);
- suppress coding chrome in the TUI — the cwd path, the sandbox `jailed` badge,
  and the approval-mode tag — while keeping extension status segments; and
- use calm, code-free spinner and greeting flavor.

They compose with personas: an **immersive** persona still owns the identity
(its charter replaces the mode intro), and an **additive** charter layers on the
mode intro. `--chat` and `--play` are mutually exclusive, and an explicit
`--system-prompt` still wins over everything.

```bash
terva --chat --persona kaiku                       # a conversation companion
terva --play --ext ./world --persona wayfarer      # act in a simulated world
```

## Where personas live, and how one is selected

The active persona for a run resolves in this order (first match wins):

1. `--persona <name|file>` — explicit at launch (a path with `/` or `.md` loads
   that file; otherwise a name resolved against the library).
2. `$TERVA_HOME/persona.md` — a hand-authored root persona (the default).
3. `default_persona` in `config.json` — a name pointer into the library.
4. the built-in **Mieli**.

The **library** (what bare/qualified names resolve against, and what
`terva persona list` shows) is three tiers, highest precedence first:

| tier | location |
|---|---|
| **user** | `$TERVA_HOME/personas/**` |
| **extension** | `<ext>/personas/**` of each enabled extension (see below) |
| **built-in** | the embedded crew shipped with terva |

A higher tier **shadows** a lower one of the same qualified name — so you can
override an extension or built-in persona (see [Namespacing](#namespacing)).

## Namespacing

A persona's **namespace** is its grouping — a team subdirectory, or the
extension name. The qualified name is `namespace:name`:

| path | namespace | qualified |
|---|---|---|
| `personas/mieli.md` | — | `mieli` |
| `personas/review-crew/vartija.md` | `review-crew` | `review-crew:vartija` |
| `<ext>/personas/deep-researcher.md` (ext `zot-web`) | `zot-web` | `zot-web:deep-researcher` |

`--persona` (and `swarm_spawn`'s `persona`) accept either form:

- `--persona review-crew:vartija` — exact.
- `--persona vartija` — bare; resolves across namespaces by precedence (so two
  namespaces can both define `deep-researcher` without colliding —
  `zot-web:deep-researcher` vs `other:deep-researcher`).

**Override** by mirroring the namespace as a subdirectory:
`$TERVA_HOME/personas/zot-web/deep-researcher.md` shadows the extension's
`zot-web:deep-researcher` (user tier > extension tier), exactly as a top-level
`$TERVA_HOME/personas/mieli.md` overrides the built-in Mieli.

`terva persona list` shows the qualified name and the **provenance** of each
persona (`built-in`, `ext:<name>`, or the on-disk path), and notes what a
persona overrides.

## Extension-shipped personas

An installed extension can contribute personas the same way it contributes
[skills](extensions.md#bundle-contributions): a **`personas/` directory beside
`extension.json`**. Discovery is a static disk scan — the extension does not
need to be running.

```
$TERVA_HOME/extensions/zot-web/
  extension.json
  personas/
    deep-researcher.md        # → zot-web:deep-researcher
```

- Each persona is **namespaced by the extension name**, sourced `ext:<name>`.
- They rank **after** the user's own personas, so a bundle persona can never
  shadow a hand-authored one (and the user can override it by mirroring the
  namespace).
- A **disabled** extension contributes nothing — both the manifest `enabled:
  false` flag and the user's `disable_extensions` config list are honored, so a
  persona never outlives the tools that back it.

The cohesive case: an extension ships both the *capability* (its tools) and the
*identity* (a persona) for using it. With `zot-web` installed and a
`deep-researcher` persona that declares `good_for: [web-research]`, a coordinator
can dispatch `swarm_spawn(persona="zot-web:deep-researcher", …)` and the
sub-agent boots with zot-web's web tools *and* the deep-researcher charter.

> Authoring a persona for an extension is identical to authoring any persona —
> see the `write-terva-persona` skill. Just place the `.md` under your bundle's
> `personas/` directory. Validate it with `terva persona validate <file>`.

## The `terva persona` CLI

```bash
terva persona list                # the merged roster + provenance + overrides
terva persona validate <file>...  # required name? non-empty charter? no
                                  #   {{char}}/{{user}}/<START> macros? valid accent_color?
terva persona init [--force]      # copy the built-in crew into $TERVA_HOME/personas/ to fork
```

## Swarm dispatch

When auto-swarm is enabled, the coordinator's prompt carries a compact **roster**
of dispatchable personas (those with `good_for`), shown by qualified name and
annotated `(via <ext>)` for extension personas. The coordinator dispatches a
specialist with the `swarm_spawn` tool's `persona` parameter (a **name only** —
the model may not name a file path); a human can also use
`/swarm new --persona <name|path> <task>`. The sub-agent boots as that persona,
and its results come back labeled by persona so the coordinator synthesizes
across lenses.

## Authoring

Use the built-in **`write-terva-persona`** skill — it covers the format, the
"charter not boilerplate" rule, the trust model (only author personas you
control; never load a downloaded card's prose as identity), and the
validate/`--persona` loop. The default Mieli persona is the canonical example to
copy (`terva persona init` writes the crew out for editing).
