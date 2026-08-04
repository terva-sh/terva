---
name: retrospective
description: Distill the reusable lessons of this session into the durable homes that shape future sessions. The lessons are corrections, conventions you discovered, and traps you hit. The homes are the project or global `AGENTS.md`, a project skill, and memory when available. Use when asked for a retro, to capture or remember lessons, or to make future sessions better.
---

# Session retrospective

Mine this session for what should outlive it, and write each piece into the
home where future sessions will actually encounter it. This is the complement
of the `handoff` skill: handoff carries *unfinished work* to the next
session; a retrospective carries *lessons* to all of them. Ephemeral task
state does not belong here — if the user also wants the work continued, do a
handoff separately.

## What to mine for

Walk the session with these filters:

- **Corrections** — every place the user redirected you. Extract the rule
  behind the correction, not the incident. If they corrected the same class
  of thing twice, that rule is the session's most important output.
- **Wrong turns** — where you lost time, and the *tell* that would have
  caught it sooner. The tell is the valuable part.
- **Discoveries** — commands, procedures, or repo facts you had to work out
  that the next session would otherwise re-derive.
- **Confirmed conventions** — choices the user approved that were not
  written anywhere ("we vendor deps", "tests through the caller").
- **Stated preferences** — how the user likes to work, phrased generally.

## Route each item to its home

| The lesson is… | Write it to |
|---|---|
| specific to this repo (commands, conventions, policy, gotchas) | the repo's `AGENTS.md` |
| a procedure with real depth (a runbook, a checklist) | a project skill: `.terva/skills/<name>/SKILL.md` |
| how this user works, across every repo | `$TERVA_HOME/AGENTS.md` (global) |
| a durable fact and a memory facility is available (e.g. a memory extension's tools) | memory, via its own conventions |
| world/story state in a lore-bearing project | lore, via its own conventions |
| unfinished work, next steps, open loops | **not here** — that's a `handoff` |

When a memory facility is present, prefer it for facts and AGENTS.md for
*standing instructions* — memory recalls; AGENTS.md commands.

## Quality bar

- Write the **rule**, include the **why**, one lesson per entry. A future
  session gets the entry without this conversation attached.
- Generalize honestly: "prefer X over Y when Z", not "on Aug 2 we did X".
- A command goes in only if it was run and worked in this session.
- Check for an existing entry saying the same thing — sharpen it in place
  rather than duplicating; delete entries this session proved wrong.
- Respect scope: repo facts never go global; personal preferences never go
  into a shared repo file without the user's say-so (other people may work
  in this repo too).
- AGENTS.md rides every prompt — keep additions to a line or two each; if a
  lesson needs paragraphs, it wants to be a skill instead.

## Finish

List what was written where, one line per item, and what you chose *not* to
record (with the reason — usually "derivable from the repo" or "session
ephemera"). The user should be able to veto any entry cheaply.
