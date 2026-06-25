# repo-aware — example terva extension (Go, protocol 3–4)

Demonstrates the **responsible** way to make an extension's **model context**
and **tool set** adapt to the workspace — the pattern from
`docs/extensions.md` → "Responsible use: context & tools".

What it does:

- Registers one read-only tool, `repo_root`, and a standing context block.
- On every session boundary (`OnSession`, which also re-fires on `/cd`), it
  checks whether the workspace is a git repository and, via the **`Session`
  handle** (the cache-safe call site):
  - **inside a repo** — publishes the context block (`RefreshContext`) and
    restores the tool (`RestoreAllTools`);
  - **outside a repo** — clears the context block and withdraws the tool
    (`WithdrawAllTools`), so it stops spending tokens in the model's schema
    and stops tempting calls that can only refuse.
- Gates the tool half on host protocol ≥ 4 (`s.ProtocolVersion()`); an older
  host simply keeps the tool visible instead of breaking.

Why a session boundary? The context block and tool set live in the model's
**cached prompt prefix**. Changing them mid-session evicts that cache, so they
are changed only at a boundary, where it's cheap. The host also pins the
prefix per turn (a mistimed change lands on the next turn, never mid-turn) and
no-ops an unchanged set — so re-asserting the same decision each session is
free.

## Build

```bash
cd examples/extensions/repo-aware
go build -o repo-aware .
```

## Install

```bash
terva ext install .
```

## Try it

From inside this repository, ask:

> What's the repo root?

The model calls `repo_root` and gets the path. Now `/cd` to a directory that
isn't a git repo (e.g. `/tmp`): the tool and the repo context disappear from
the model's view on the next turn. `/cd` back into a repo and they return.

Watch it happen in `/context` (the **Extensions** tab shows the context
block coming and going) and in `/extensions` (the tool count stays — a
withdrawn tool is hidden from the model, not unregistered).

## See also

- `examples/extensions/repo-aware-ts` — the TypeScript twin (raw wire, no SDK)
- `examples/extensions/repo-aware-py` — the Python twin (raw wire, stdlib only)
- `examples/extensions/weather` — a plain LLM-callable tool (protocol 2)
- `examples/extensions/guard` — event subscription + tool-call interception
- `docs/extensions.md` — full protocol reference and the responsible-use guidance
