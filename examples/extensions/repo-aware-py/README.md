# repo-aware-py — example terva extension (Python, source-run)

The Python twin of [`repo-aware`](../repo-aware) (Go) and
[`repo-aware-ts`](../repo-aware-ts) (TypeScript). Raw protocol, **stdlib only**,
no SDK. It's the reference for the **responsible per-session context/tool
pattern** in Python — what the Go SDK does behind `OnSession` + the `Session`
handle, written straight to the wire.

Demonstrates:

- registering a read-only tool (`repo_root`) — note the honest
  `"read_only": True`
- `subscribe`-ing to `session_start` (raw protocol must ask; the Go SDK's
  `OnSession` does it for you)
- on each `session_start` (which re-fires on `/cd`), deciding by cwd:
  - **inside a git repo** — `refresh_context` with repo guidance + restore the
    tool (`set_withdrawn_tools` with an empty set)
  - **outside a repo** — clear the context block + withdraw the tool
    (`set_withdrawn_tools` `"all": true`)
- **feature-detecting** `protocol_version >= 4` before sending
  `set_withdrawn_tools` (protocol 4); older hosts ignore it and the tool stays
  visible

Why only at `session_start`? The context block and tool set live in the
model's **cached prompt prefix**; changing them mid-session evicts the cache.
`session_start` is the boundary where it's cheap. The host also pins the prefix
per turn and no-ops an unchanged set, so re-asserting the same decision each
session is free — which is why this example doesn't track prior state.

## Requirements

Python 3.8+. No third-party packages.

## Install

```bash
terva ext install .
```

(`main.py` ships executable with a `#!/usr/bin/env python3` shebang; if you
copy it around and lose the bit, `chmod +x main.py`.)

## Try it

From inside this repository, ask:

> What's the repo root?

The model calls `repo_root` and gets the path. `/cd` to a non-repo directory
(e.g. `/tmp`) and the tool + repo context drop out of the model's view on the
next turn; `/cd` back and they return. Watch the context block come and go in
`/context` (Extensions tab); `/extensions` still shows the tool in the count
(withdrawn = hidden from the model, not unregistered). Tail the log with
`terva ext logs repo-aware-py -f`.

## See also

- `examples/extensions/repo-aware` — the Go SDK version (same behavior via
  `OnSession` + the `Session` handle)
- `examples/extensions/repo-aware-ts` — the TypeScript twin (raw wire)
- `docs/extensions.md` → "Responsible use: context & tools" — the full rationale
