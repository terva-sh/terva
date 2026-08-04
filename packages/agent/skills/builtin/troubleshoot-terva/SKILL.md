---
name: troubleshoot-terva
description: Diagnose terva itself. Symptoms include an extension that will not load, a connector or bot that is down, missing tools, or MCP trouble. A bloated context or session/resume confusion also qualifies. Use when terva misbehaves, or when something that should be available is absent. Also use when the user asks why terva is slow, expensive, or broken.
---

# Troubleshooting terva

Work symptom-first, and mind one boundary throughout: **some diagnostic
surfaces are yours to read, others are deliberately denied to tools** —
credentials, transcripts, and most of `$TERVA_HOME/logs/`. When a step below
says "ask the user", that denial is why; ask for the command's output instead
of trying to route around it.

First moves for any symptom: `terva_status` (who/where/model/session), and
the relevant picker — `/skills`, `/extensions`, `/mcp`, `/sessions`.

## Extension won't load or its tools are missing

1. `terva ext list` — is it discovered at all? The install-dir basename and
   the manifest are different names; `ext list` shows both.
2. Not listed: project-local extensions load **only in a trusted workspace**
   (`terva trust`); global ones (`$TERVA_HOME/extensions/`) always load.
   Check `enabled` in its manifest and the user's `disable_extensions` list.
3. Listed but dead: read its log — `$TERVA_HOME/logs/ext-<name>.log` is
   readable by your tools (the one carve-out from the logs denial), or
   `terva ext logs <name> [-f]`. Build failures from its launcher land there.
4. **Empty log + "off (not running)"** is the classic launcher trap: the
   manifest's `exec` isn't executable (a plain file-write drops the exec
   bit), so it dies on EACCES before writing a byte.
5. Tools present earlier but gone now: check the run's scoping flags
   (`--extensions`, `--mcp`, `--tools` restrict per run) and the approval
   mode — plan-style modes withhold mutating tools on purpose.

## Bot / connector down

Connector logs (`logs/connector-<name>.log`) are **not** readable by your
tools — ask the user for `terva bot status` output and the log tail.
Useful structure for the ask: the connector's `status`/`configured` verbs
report credential state; repeated crash-restarts exhaust a 3-per-60s budget
and the bridge reports broken; a `connect_error` means bad credentials
(re-run setup), while transient network trouble self-retries and only warns.
Pairing and group admission are host-side: a silent bot in a group chat is
usually *unapproved* (owner `/approve` in-chat), not broken.

## MCP server trouble

`/mcp` shows configured servers and live state. Server stderr goes to
`$TERVA_HOME/logs/` (denied — ask the user for the tail). A server that
works elsewhere but not here: check `--mcp` scoping for this run and
project-vs-global config precedence.

## Context too big / session too expensive

`--dump-prompt=sizes` prints the assembled prompt's segment sizes without
spending tokens or needing credentials — it names which segment (tools,
skills manifest, AGENTS.md, transcript) is the weight. Long sessions
compact automatically; a fresh start with a handoff (see the `handoff`
skill) beats dragging a huge transcript.

## Sessions and resume

`terva --continue` resumes the most recent session in this directory;
`/sessions` picks among them. The `--resume` key is the session **file
basename**, which is not the internal session id — `terva_status` shows
both. Transcripts under `$TERVA_HOME/sessions/` are tool-denied; inspect
sessions through the picker or ask the user.

## Rules while diagnosing

- The read denials (credentials, `sessions/`, `logs/` minus `ext-*.log`,
  `shared/`) are absolute — `/unjail` does not lift them and bash is bound
  by them too. Never try to route around one; ask.
- Inspecting configuration: report the **shape** (which keys exist, which
  are set), never the values — a "redacted" dump that guesses field names
  fails open.
- Once the failing subsystem is identified, read its doc before acting:
  `extensions.md`, `connectors.md`, `mcp.md`, `permissions.md`,
  `profiling.md`, `debugging-prompts.md` — installed under
  `$TERVA_HOME/docs/` and readable by your tools.
