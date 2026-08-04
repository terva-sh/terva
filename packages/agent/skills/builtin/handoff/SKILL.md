---
name: handoff
description: Compact the current session into a handoff document. A fresh session or another agent can pick it up with no need to re-derive context. Use when asked to hand off, hand over, wrap up for another session or agent, or park work mid-stream.
---

# Session handoff

Write a handoff document so the next session can continue this work cold. The
next reader has none of your context: no tool results, no reasoning, no
shorthand you coined along the way. Write for them, not for a log.

First check the cheaper tool: if the user just wants to *continue this same
conversation later*, `terva --continue` (or the `/sessions` picker) resumes it
with full context — no handoff needed. Reach for a handoff when the next
worker is a **different** session or agent: a fresh start in another worktree,
another machine, another model, or a context too grown to carry forward.

## Where

`<terva-home>/handoffs/<yyyy-mm-dd>-<short-slug>.md`, creating the directory
if missing. The terva home is `$TERVA_HOME` when set; otherwise the platform
default (macOS `~/Library/Application Support/terva`, Linux
`$XDG_STATE_HOME/terva` or `~/.local/state/terva`, Windows
`%LOCALAPPDATA%\terva`; long-standing installs may use the legacy `zot`
sibling — the `terva_status` tool's session-file path sits inside the live
home, so its `sessions/` parent settles any doubt).

Never write it into the workspace — it would dirty the repo's status and
outlive its usefulness there — and never into a temp dir a cleanup can eat.
If a handoff for this same stream of work already exists, update it in place
rather than minting a sibling.

The location is part of the design: `<terva-home>/handoffs/` is writable and
readable even in a jailed session — no `/unjail`, no extra trust. If a write
there is refused anyway, report it as a bug rather than routing around it.

## What goes in

Only what the next session cannot cheaply re-derive:

- **Goal** — the user's actual objective, one paragraph, in their words where
  possible; include the "why" when it shaped decisions.
- **State** — DONE and verified (with evidence: test run, commit, merged PR)
  vs IN FLIGHT vs NOT STARTED. Never present unverified work as done.
- **Repo state** — repository path, worktree if different, branch, HEAD sha,
  uncommitted/untracked files that matter. If meaningful work sits
  uncommitted, say so loudly — or better, make a WIP commit first and record
  its sha.
- **Next steps** — ordered; the first concrete enough to start on
  immediately.
- **Decisions & constraints** — choices already made (and why) that must not
  be relitigated; traps already hit that must not be re-fallen into.
- **Verify first** — the commands proving the recorded state is still true
  (test suite, build, `git status`), so drift is caught before work resumes.
- **Open loops** — background work still running, PRs awaiting CI or review,
  questions awaiting the user.
- **Suggested skills** — skills the next session should load, by name, with
  when and why.
- **Pointers** — the files to read first, in order.

## Rules

- Do not duplicate content already captured elsewhere — specs, plans, ADRs,
  issues, commits, diffs, lore, memory. Reference by path, sha, or URL; the
  handoff is the index, not the archive.
- If a durable memory facility is available in this session (for example a
  memory extension), put lasting lessons and facts there now; the handoff
  carries only the ephemeral task state that memory shouldn't.
- Redact secrets — API keys, tokens, passwords, PII. A handoff file is not a
  credential store.
- If the user said what the next session will focus on, weight the document
  accordingly — lead with what that session needs, compress the rest.

## Finish

End your reply with the handoff's absolute path and a one-line kickoff the
user can give the next session, e.g.:

> Read ~/Library/Application Support/terva/handoffs/2026-08-02-conn-matrix.md and continue from "Next steps".

(Works pasted into an interactive session or as `terva -p "…"`.)
