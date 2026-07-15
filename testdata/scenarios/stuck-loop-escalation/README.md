# Scenario: stuck-loop-escalation

Exercises the whole stuck-loop escape hatch end to end: a weak local model wedges
on a tool loop, the detector nudges it, and — if it stays stuck — the harness
escalates to a stronger remote model to finish the work.

```
just dogfood stuck-loop-escalation
```

## The trap

`workspace/models.json` is a 20-model registry where the target
(`gemma-4-26b-…`) has the wrong `context_window` (`8192`), a value it **shares
with four decoys** — and it's the *second* one, so the edit tool's first-match
wins and a naive surgical edit changes a decoy. `prompt.txt` forbids asking and
forbids a full-file rewrite, so the weak model spirals (re-read, re-edit) rather
than escaping — which is exactly the thrash the escalator is there to catch. The
strong model, which understands "make the match unique," finishes cleanly, so the
recovery half of the test stays meaningful.

## Prerequisites (your real terva auth is inherited via symlink)

This scenario names specific models; point them at your own by editing
`config.json`:

- `provider`/`model` — a **stall-prone local model** on an `openai-compatible`
  endpoint you have a credential for. It must actually be serving.
- `escalation.provider`/`escalation.model` — a **stronger model** you have a
  credential for, capable of finishing the edit.

The runner symlinks your existing `auth.json` into the throwaway home, so
whatever endpoints/credentials you've logged into are what it uses. No tokens are
copied.

## What you'll see

With `escalation.auto: true` (the default) the swap happens automatically:

1. `⟳ loop detected — nudged the model N× this turn` (counts up as it thrashes)
2. `⇗ escalated to <strong-model> (<provider>) to break the loop`
3. the strong model reads the `[handoff]` note and completes the edit.

Set `"auto": false` in `config.json` to get the **ask** dialog instead — then try
"Keep trying" to watch it get a fresh window with a raised bar (breathing room),
or "Stop" to end the turn.

## Reset

Nothing to reset — every `just dogfood` run rebuilds the workspace from these
pristine fixtures, so `gemma-4-26b` is always back at `8192` and there are no
leftover sessions. The mutable copy lives in a throwaway `TERVA_HOME` under your
temp dir.

Afterward, confirm what happened in the session log:

```
grep -hoE '"type":"(stall|escalation)"' "$TMPDIR"/terva-dogfood-stuck-loop-escalation/sessions/*/*.jsonl
```
