# Example workflows

Working scripts for `terva workflow run` (the full guide is
[docs/workflows.md](../../docs/workflows.md)):

| Script | Teaches |
|---|---|
| [01-hello-fanout.js](01-hello-fanout.js) | the minimum: `meta`, `parallel` fan-out, null-on-failure, a return value |
| [02-review-verify.js](02-review-verify.js) | `pipeline` staging and `schema` — structured deliverables back as objects |
| [03-args-budget.js](03-args-budget.js) | `--args` parameterization, the USD `budget`, why determinism is the rule |

Run one from a checkout:

```
terva workflow run examples/workflows/01-hello-fanout.js
```

Every terva install also carries these at `$TERVA_HOME/examples/workflows/`,
so they are readable without a source checkout — including by a sandboxed
agent whose jail includes `$TERVA_HOME`.

These are not just prose: CI executes every script in this directory against
a stub engine on each commit (`examples/workflows_test.go`), so a shipped
example is a running one. If you add an example, the test fails until you
give it a table entry there.
