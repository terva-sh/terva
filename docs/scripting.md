# In-engine scripting — the `code_execution` tools

terva can hand the model a JavaScript sandbox with the host's own tools
exposed as functions. The model writes a short program, calls those functions
as many times as it needs, and `print(...)`s only the answer — so a multi-step
task costs **one tool result** instead of N, and the intermediate output never
enters the model's context.

There are **two** such tools, one engine behind both:

| Tool | Reaches | Authority | Plan mode |
|---|---|---|---|
| `code_execution` | `read`, `grep`, `glob` | read-only (`local-read`) | survives |
| `code_execution_mutating` | those three **plus** `write`, `edit` | mutating | absent |

They are two tools rather than one tool with a flag, and that is deliberate
(see [Two tools, not one](#two-tools-not-one)). Neither can run commands:
there is no `bash` in either binding set.

It is a compile-time capability: binaries built with `-tags terva_scripting`
have both (the release builds do), lean builds have neither. There is nothing
to configure at runtime.

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

## Two tools, not one

`code_execution_mutating` could have been a flag on `code_execution` — a
`mutating: true` argument, one tool, one schema. It is a separate tool
instead, for three reasons that all point the same way:

- **Authority stays a property of the tool.** That is where the rest of the
  system already looks for it: the plan-mode prune, the permission rules,
  the authority classes in [permissions.md](permissions.md#authority-classes).
  `code_execution` keeps its read-only class unconditionally, rather than
  holding a classification that has to be computed from an argument.
- **A change of this size should not hide in an argument.** Going from
  "reads the repo" to "writes files" is not a parameter. An argument that
  silently widens authority is exactly the shape of thing a reviewer skims
  past.
- **The call site says what happened.** `code_execution_mutating` in a
  transcript, an audit line, or a permission rule is legible on its own.
  `mutating: true` buried in a JSON blob is not.

### What the mutating tool adds

The read-only three behave identically under both tools, so a script that
only looks at the workspace runs unchanged either way. On top of them:

```js
write(path, content)          // and write(path, content, "0755")
edit(path, [{ oldText, newText }])            // replaceAll optional per edit
```

```js
// "Add a missing license header to every Go file that lacks one."
const header = read("LICENSE-HEADER.txt");
let touched = 0;
for (const path of glob("packages/**/*.go").split("\n")) {
  if (!path) continue;
  const body = read(path);
  if (body.startsWith(header)) continue;
  write(path, header + "\n" + body);
  touched++;
}
print(`added the header to ${touched} files`);
```

There is **no `bash`**, and that is a deliberate ceiling rather than an
oversight. A command string is authority the pre-check below cannot read:
with `bash` in the set, "this script will call …" becomes a guess, and the
tool is `bash` with extra steps. Stopping at `write`/`edit` keeps a limit
that can actually be checked.

### The pre-check: a script it cannot account for does not run

Before the engine starts, the tool walks the script's AST and works out
which bindings it calls. When it can account for all of them it reports the
plan — `read x5, write x2` — on the tool's progress line and in
`details.binding_plan`.

When it **cannot**, the script does not run at all. Not "runs with a
warning": the refusal happens before the first binding call, so nothing
reaches disk. A tool that changes files should be able to say what it is
about to do, and when it cannot say, the honest move is to stop.

These defeat the analysis, and each one names itself in the refusal:

| Pattern | Why it is opaque |
|---|---|
| `eval`, `new Function(…)` | the code is built at runtime |
| `globalThis[…]`, `window`, `self` | any binding, under a name built at runtime |
| `with (o) { … }` | rebinds every name against a runtime object |
| `import(…)` | loads code the walker never sees |
| `const w = write` | the call site is `w(…)`, attributable to nothing |
| `const write = …`, a parameter named `write` | the name no longer means the binding |
| an AST node the walker does not know | sobek's AST is explicitly a work in progress |

The fix is always the same and is stated in the refusal: call each binding
directly by its name. That is also how ordinary scripts are already written,
so the constraint costs little in practice.

Note what this is *not*. It is not a defence against a hostile script — see
[the threat model](#notes-for-the-curious). `eval` still exists in the
language; the pre-check does not remove it, it declines to see through it and
refuses to proceed on a guess. **An accounted plan is exhaustive; there is no
such thing as a partially accounted run.**

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
- **Standard JavaScript built-ins** (`JSON`, `Math`, `String`, `RegExp`,
  `Promise`, `Proxy`, `Symbol`, `eval`, …) are available — the engine's own
  globals, unmodified.
- **No host capability beyond the three functions above.** No filesystem or
  network objects, no `require`/`import`, no environment access, no
  subprocess, and no timers (`setTimeout` does not exist). The three host
  functions are the entire capability surface — but note that "bare" here
  means *no ambient authority*, not a reduced language: the standard library
  is whole, `eval` included. That is harmless because the sandbox's threat
  model is the session's own model, not hostile third-party code (see the
  note at the end).

### Promises and async

Scripts run to completion synchronously; the engine has no event loop, so
there is no host operation a promise can be waiting on. Ordinary JavaScript
semantics otherwise apply — a `.then(…)` callback or the tail of an `await`
runs after the synchronous body finishes, exactly as it would in a browser or
in Node, so output printed from a continuation appears *after* everything the
body printed.

**An unhandled rejection fails the run.** A rejected promise that nothing
ever handles ends the script with `unhandled promise rejection: <reason>`,
carrying whatever the script printed before it. This matches Node, and it is
deliberate: the whole point of the tool is to return a small answer derived
from output the model never sees, so a rejection that vanished silently would
leave a truncated answer with nothing left to check it against. Handle it —
`.catch(…)`, or `await` inside `try`/`catch` — and it is ordinary control
flow again.

A promise that simply never settles is not an error; the script just ends,
as a Node program would.

### Limits

| Bound | Default |
|---|---|
| Host calls per script | 50 |
| Printed output | 32 KiB (then truncated, and marked as such) |
| Bytes one host call may return | 1 MiB (refused, not truncated) |
| JS recursion depth | 2048 frames |
| Run time | 30 s (the model may raise it per call, capped at 120 s) |

A script that exceeds its time budget is interrupted by a watchdog, not
killed by the OS — the tool result says it timed out and includes any partial
output, so the model can narrow the work or raise the timeout.

The per-call return cap is a **backstop against importing unbounded data**,
not a working constraint: `read` already caps its own result at 50 KiB, so
nothing the three bindings return comes near 1 MiB today. It exists because
the engine cannot meter VM heap (see the last note below), which leaves the
quantity a run may pull *in* as the only bound available — at most host calls
× this cap, 50 MiB. It **refuses** an oversized return rather than shortening
it, with a catchable error: a truncated result is indistinguishable in-script
from a genuinely short one, and would quietly corrupt whatever the script
computed from it. It does not bound what a script allocates on its own; only
the time limit does.

## Permissions

`code_execution` is classified **read-only** (`local-read` in
[permissions.md](permissions.md#authority-classes)), so it is auto-allowed in
`workspace` mode and survives `plan` mode — the same standing as `read`,
`grep`, and `glob` themselves.

`code_execution_mutating` is **not** read-only, and everything follows from
that one fact. It prompts in `workspace` mode like any other mutating tool,
and it never enters a `plan` mode registry at all — the model does not even
see it there, because plan mode promises read-only and a tool that writes
files cannot keep that promise. In the code this is a single omission: the
tool is registered as a builtin and simply not registered read-only. A guard
test (`TestMutatingScriptToolIsNotReadOnly`) fails if that ever changes,
because the regression would otherwise be invisible until it mattered.

Both classifications are honest because of two properties:

- **The class follows the binding set.** A tool is read-only exactly as long
  as everything it can reach is. That is why adding `write`/`edit` produced
  a second tool with its own class rather than a wider mode of the first.
- **Reach, not authority.** Every binding call a script makes — a `read` or
  a `write`, under either tool — passes through the **same approval gate** as
  a model-issued tool call: jail containment, path rules, everything. A
  script's `write` prompts exactly as a model-issued `write` does, with its
  own preview and its own audit line. The sandbox adds a way to *compose*
  the tools, never a way around their gates. A binary where the gate isn't
  wired fails closed: the tool refuses to run at all.

So a mutating script passes **two** independent checks: the tool call itself
is approved under its own authority, and then each `write` or `edit` inside
it is approved again on its own terms. The pre-check sits ahead of both, and
refuses anything it cannot describe.

## Visibility

Each tool sits in its own lazily-activated group — `code_execution` in
**`scripting`**, `code_execution_mutating` in **`scripting_mutating`**. With
`lazy_tools` on (see
[standard-tools.md](standard-tools.md#lazy-tool-visibility-lazy_tools)) a
group stays out of the advertised manifest until the model activates it;
without lazy tools both are advertised alongside the other built-ins whenever
the coding tool set is.

The two groups are separate on purpose. Sharing one would mean that a session
reaching for read-only scripting is handed a tool that writes files in the
same act — the quiet widening the two-tool split exists to prevent.

## Build flag

| Build | Has the `code_execution` tools? |
|---|---|
| Release binaries (`terva update`, goreleaser) | yes |
| `just install` / `just install-dev` | yes |
| plain `go build ./cmd/terva` | no |
| `terva-min` | no |

Both tools live behind the one tag, and neither leaves any trace in a build
without it.

The JS engine adds ~6 MB to the binary (measured 6.15 MB stripped, for
`terva_scripting` and `terva_workflows` together), which is the whole reason
for the tag: `-tags terva_scripting` links it, leaving it off builds it out
completely. A build without the tag has no trace of the tool — it simply
never registers.

## Notes for the curious

- The engine is in-process and pure Go (no cgo, no subprocess). It replaced
  an out-of-process code-execution extension, which is retired. To be exact
  about why, since the short version has been told the other way round: that
  extension never ran a script — its Starlark runner was always a stub — so
  the process boundary was never measured. It was abandoned for the costs
  that were plain without running it (JSON serialization at every hop, tool
  results flattened to text so structured detail was lost, no schema
  introspection so argument shapes had to be hardcoded) and for the language
  choice; see `docs/plans/jsengine-code-execution-and-workflows.md`.
- The sandbox's threat model is *the session's own model* — the same
  principal the approval gate already governs — not hostile third-party
  code. The engine caps calls, output, stack, and time; it does not meter VM
  heap. Scripts from untrusted strangers would want a process boundary; that
  is not what this tool is for.

### Engine surfaces behind the two tools

- **Typed bindings and globals.** `Binding` flattens every argument and
  return to a string. `TypedBinding` keeps JS types across the boundary, and
  `Options.Globals` hands a script input without encoding it into the
  source. `code_execution` uses only the string form; the mutating tool uses
  it for `read`/`grep`/`glob` and the typed form for `write`/`edit`, because
  an array of replacements does not survive being flattened to a string.
  One deliberate difference between them: a string binding drops a trailing
  `undefined`/`null` argument, where a typed binding keeps argument
  positions.
- **The binding-reference pre-check.** `AnalyzeBindings` walks the script's
  AST and reports which bindings it names. `code_execution_mutating` runs it
  as a precondition; `code_execution` does not use it, because a read-only
  tool has nothing to withhold.
