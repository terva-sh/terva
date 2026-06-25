# repo-aware-ts — example terva extension (TypeScript, source-run)

The TypeScript twin of [`repo-aware`](../repo-aware) (Go). Raw protocol, no
SDK, no build step — runs via `npx -y tsx ./index.ts`. It's the reference for
the **responsible per-session context/tool pattern** on the bare wire, the
thing the Go SDK hides behind `OnSession` + the `Session` handle.

Demonstrates:

- registering a read-only tool (`repo_root`) — note the honest
  `"read_only": true`
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
per turn and no-ops an unchanged set, so re-asserting the decision every
session is free — which is why this example doesn't track prior state.

## Requirements

Node 18+ and `npx` (bundled with npm). First run downloads `tsx` into npm's
cache; subsequent runs reuse it.

## Install

```bash
terva ext install .
```

## Try it

From inside this repository, ask:

> What's the repo root?

The model calls `repo_root` and gets the path. `/cd` to a non-repo directory
(e.g. `/tmp`) and the tool + repo context drop out of the model's view on the
next turn; `/cd` back and they return. Watch the context block come and go in
`/context` (Extensions tab); `/extensions` still shows the tool in the count
(withdrawn = hidden from the model, not unregistered). Tail the log with
`terva ext logs repo-aware-ts -f`.

## See also

- `examples/extensions/repo-aware` — the Go SDK version (same behavior via
  `OnSession` + the `Session` handle)
- `examples/extensions/repo-aware-py` — the Python twin (raw wire, stdlib only)
- `examples/extensions/scratchpad` — TypeScript commands + tool (no SDK)
- `docs/extensions.md` → "Responsible use: context & tools" — the full rationale
