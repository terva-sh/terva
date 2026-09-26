# examples/harness

A working agent built from terva's engine (`packages/core`) and wire
(`packages/provider`) alone, with none of terva's harness: no config files, no
session store, no `$TERVA_HOME`, and no tool set but its own. Start here when
terva's conventions are not the ones your host wants.
[docs/embedding.md](../../docs/embedding.md) compares this with the other two
ways to embed terva.

```bash
go run ./examples/harness "what is 2 + 3? remember to buy milk"
```

With no `ANTHROPIC_API_KEY` it talks to a scripted fake client, so it runs
anywhere and needs no network. The fake reads the prompt literally: numbers
become an `add` call and "remember X" a `remember` call. With no prompt, the
example asks it to remember a password, which the gate refuses. With one, it talks to Anthropic, and
`HARNESS_MODEL` picks the model.

## What each file shows

| File | The host's choice it makes |
|---|---|
| `main.go` | Where a credential enters. The only file that reads the environment; the wire takes the key as a value. It starts the turn with `Run` and a `core.PromptInput`, and prints a tool call's name but not its arguments, which the gate has not yet ruled on. |
| `harness.go` | The frame (`core.StaticSystem`), the tool registry, the gate, the catalog and the transcript store, each passed to `core.New` as an option. The gate refuses a note that mentions a password. `resume` builds a second agent that continues from the store. |
| `tools.go` | Two tools written here. `remember` writes to an in-memory notebook. |
| `fake.go` | A `provider.Client` that needs no network. The engine cannot tell it from a real one. |

Everything lives in memory, the transcript included: the agent writes it to a
`core.MemoryTranscriptStore` as it goes. A host that wants it on disk or in a
database implements `core.TranscriptStore` and runs its store through
`packages/core/transcripttest`, the suite terva's own JSONL store passes.

## What it holds itself to

`imports_test.go` fails on any import outside the standard library and the
packages `.api/packages.txt` lists as stable. That is rule 8 of
decision 0021 (the embeddable engine): if the example
needs anything else to do something basic, the engine's API is missing a piece,
and the fix goes in the API, not in an exception here.

`harness_test.go` runs a whole turn against the fake in CI: the gate allows one
call and refuses the other, the allowed tool runs, and the refusal reaches the
model as an error result.

The engine's API is not stable yet. Until phase 6 of the plan, anything the
example uses can change in a minor release, and the example changes with it.

## Phases it reflects

The phases are those of the engine extraction plan, which is kept with the
project's records rather than in the published docs. This example grows with it.

| Phase | Status here |
|---|---|
| 1. The first slice | Reflected. The gate is the one option `core.New` requires, and the frame comes from a `core.ContextAssembler`. |
| 2. Compaction as mechanics and policy | Reflected. The agent leaves `CompactionPolicy` nil and gets `core.DefaultCompactionPolicy`, the neutral one; a host sets its own there. |
| 3. A wire with no I/O | Reflected. The Anthropic client is built from a key passed as a value, and nothing in the wire reads a file or the environment. |
| 5. Transcripts behind a store | Reflected. The agent writes to an in-memory `core.TranscriptStore`, and `harness_test.go` resumes a second agent from it. terva's own JSONL store is `packages/session`, which a host may use as well. |
| 6. A stable engine API | Reflected. The wire holds no model catalog: `hostCatalog` builds one, and the example passes it to the agent (`SetCatalog`) and to the client (`provider.WithCatalog`). The stable packages are drawn (decision 0026), and `imports_test.go` reads them from `.api/packages.txt`. |
