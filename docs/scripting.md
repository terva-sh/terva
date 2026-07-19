# In-engine scripting — the `code_execution` tool

terva can hand the model a JavaScript sandbox with the host's own read-only
tools exposed as functions: the built-in **`code_execution`** tool. The model
writes a short program that calls `read`/`grep`/`glob` as many times as it
needs and `print(...)`s only the answer — so a multi-step lookup costs **one
tool result** instead of N, and the intermediate output never enters the
model's context.

It is a compile-time capability: binaries built with `-tags terva_scripting`
have it (the release builds do), lean builds don't. There is nothing to
configure at runtime.

## What it's for

The tool exists for exactly one pattern: *a small answer derived from large
output*. Counting matches, extracting one field from many files, joining
results across a sweep — anything where the model would otherwise burn context
on intermediate tool results it only needed in passing.

Ask for something that spans many files and the model can do this:

```js
// "How many TODO comments are in each top-level package?"
const hits = grep("TODO", "packages");
const counts = {};
for (const line of hits.split("\n")) {
  const pkg = line.split("/")[1];
  if (pkg) counts[pkg] = (counts[pkg] || 0) + 1;
}
print(JSON.stringify(counts, null, 2));
```

One tool call, one small result — where the direct route is a `grep` whose
full output lands in context just to be counted.

## The script environment

Scripts run **in-process** on a pure-Go JavaScript engine
([sobek](https://github.com/grafana/sobek), modern ECMAScript). The
environment is deliberately bare:

- **Host functions**: `read(path[,offset,limit])`, `grep(pattern[,path])`,
  `glob(pattern[,path])` — each returns the corresponding tool's text output
  as a string, and throws on tool failure (catchable with `try`/`catch`).
- **Output**: `print(...)` (and `console.log`) append to the result buffer.
  Only printed output returns to the model; a script that prints nothing gets
  told so.
- **Standard JavaScript built-ins** (`JSON`, `Math`, `String`, `RegExp`, …)
  are available.
- **Nothing else.** No filesystem or network objects, no `require`/`import`,
  no environment access, no subprocess. The three host functions are the
  entire capability surface.

### Limits

| Bound | Default |
|---|---|
| Host calls per script | 50 |
| Printed output | 32 KiB (then truncated, and marked as such) |
| JS recursion depth | 2048 frames |
| Run time | 30 s (the model may raise it per call, capped at 120 s) |

A script that exceeds its time budget is interrupted by a watchdog, not
killed by the OS — the tool result says it timed out and includes any partial
output, so the model can narrow the work or raise the timeout.

## Permissions

`code_execution` is classified **read-only** (`local-read` in
[permissions.md](permissions.md#authority-classes)), so it is auto-allowed in
`workspace` mode and survives `plan` mode — the same standing as `read`,
`grep`, and `glob` themselves.

That classification is honest because of two properties:

- **Every binding is read-only.** The tool's class follows its binding set:
  it stays read-only exactly as long as everything it can reach is. A future
  mutating binding would move the tool out of this class, not quietly widen
  it.
- **Reach, not authority.** Each `read`/`grep`/`glob` call a script makes
  passes through the **same approval gate** as a model-issued tool call —
  jail containment, path rules, everything. The sandbox adds a way to
  *compose* the tools, never a way around their gates. A binary where the
  gate isn't wired fails closed: the tool refuses to run at all.

## Visibility

The tool sits in the lazily-activated **`scripting`** group: with
`lazy_tools` on (see
[standard-tools.md](standard-tools.md#lazy-tool-visibility-lazy_tools)) it
stays out of the advertised manifest until the model activates it; without
lazy tools it is advertised alongside the other built-ins whenever the coding
tool set is.

## Build flag

| Build | Has `code_execution`? |
|---|---|
| Release binaries (`terva update`, goreleaser) | yes |
| `just install` / `just install-dev` | yes |
| plain `go build ./cmd/terva` | no |
| `terva-min` | no |

The JS engine adds ~6 MB to the binary, which is the whole reason for the
tag: `-tags terva_scripting` links it, leaving it off builds it out
completely. A build without the tag has no trace of the tool — it simply
never registers.

## Notes for the curious

- The engine is in-process and pure Go (no cgo, no subprocess): the earlier
  experiment with an out-of-process code-execution extension showed the
  process boundary cost more than it protected, and is retired in favor of
  this.
- The sandbox's threat model is *the session's own model* — the same
  principal the approval gate already governs — not hostile third-party
  code. The engine caps calls, output, stack, and time; it does not meter VM
  heap. Scripts from untrusted strangers would want a process boundary; that
  is not what this tool is for.
