---
name: init-workspace
description: Survey this repository and write (or refresh) its `AGENTS.md`, the instructions that terva loads into every session here. Use when asked to init, onboard, or set up terva, or to create or update `AGENTS.md` or project instructions.
---

# Initialize a workspace

Write the repository `AGENTS.md`: the standing instructions that terva loads
into every session that works here. The file rides every prompt, so every
line must earn its place. This skill is as much about what to leave out as
what to put in.

How terva loads it: `$TERVA_HOME/AGENTS.md` (the user's own cross-repo
preferences) first, then `AGENTS.md` files from the top-most parent directory
down to the working directory. A later file overrides an earlier one. So:
repository facts go in the repository `AGENTS.md`. Personal preferences go in
the global one. A subdirectory of a monorepo can layer its own on top. Do not
mix these scopes.

## Survey before you write

Read, do not guess. In order:

1. Existing instructions: `AGENTS.md` first, then any sibling instruction
   files other tools left behind (`CLAUDE.md`, `.cursorrules`,
   `CONTRIBUTING`). Mine them. If a `CLAUDE.md` duplicates what you will
   write, say so to the user. Do not silently fork the two.
2. The build system: justfile or Makefile, `package.json` scripts, CI
   workflow files. These are the source of truth for commands.
3. The layout: top-level directories, and where the code, the tests, and the
   documentation live.
4. Convention signals: linter and formatter configuration, and a few
   representative source files for naming and comment style.

**Verify every command you intend to record: run it.** When a run is too
heavy (a full test suite), confirm the command exists in the build file and
say which. A stale command in `AGENTS.md` is worse than none: it fails in
every future session.

## What belongs (and what does not)

In, roughly in this order:

- **Commands**: build, test (and how to run one test), lint, format, run.
  One line each.
- **Architecture in five lines**: the map a newcomer needs before their
  first edit. Major components and where they live. Not a tour.
- **Conventions that differ from defaults**: what a competent agent would
  otherwise get wrong. House style the linter already enforces needs no
  restatement.
- **Policy**: what must not change or go into a commit (generated files,
  vendored trees, secrets), and the required workflow (branch rules, how a
  PR opens).
- **Gotchas**: the traps that cost someone an hour. One line each, with the
  tell that identifies the trap.

Out: file inventories, tutorials, aspirations, anything one glob or one file
read can answer, and anything that will be false in a month. If a topic
needs depth (a release runbook, a review checklist), make it a project skill
in `.terva/skills/<name>/SKILL.md` and point to it by name. A skill loads on
demand; `AGENTS.md` is always on.

Write short, direct sentences in the imperative. The file is instructions a
model follows, not documentation a person browses: state each rule as a
command, put a prohibition before the detail it governs, and cut every word
that does not change what the reader does.

Keep the whole file around a screenful. If it grows past that, move depth
into skills or documentation and keep the pointer.

## Refresh an existing AGENTS.md

Edit surgically. Preserve the owner's voice and ordering. Re-verify claims
you did not write before you keep them: stale commands and renamed paths are
exactly what a refresh is for. Delete confidently anything the repository
contradicts. Flag deletions in your summary so the user can veto them.

## Finish

Show the user the result. Add one line on each choice that involved
judgment: what you left out, what you could not verify, and any
`CLAUDE.md`/`AGENTS.md` duplication you found. Remind them it takes effect
in new sessions from this directory.
