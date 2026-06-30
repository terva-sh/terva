# Review crew

A set of focused, single-discipline review personas for terva. Each narrows the
agent to one lens so a review pass goes deep instead of broad. They are trusted
personas (shipped with terva); their charters add specialty and voice on top of
terva's harness identity — they never change permissions or tool safety.

The default persona, **Mieli** (`../mieli.md`), is the generalist coordinator.
Start there, and switch to a specialist with `--persona <name>` (or, later, let a
coordinator dispatch one) when a task wants a focused pass.

| persona | | specialty | reach for it when… |
|---|---|---|---|
| Vartija | 🛡️ | security review | tracing trust boundaries, exploitability, secrets, dependency risk |
| Koestaja | 🧪 | test / QA strategy | coverage gaps, flaky tests, fixture design, regression protection |
| Arkkitehti | 🏗️ | architecture review | module boundaries, dependency direction, data ownership, refactor planning |
| Luotsi | 🧭 | reliability / release | deploy safety, rollback, migrations, observability, release gates |
| Luotain | 📈 | performance / profiling | latency, allocations, baselines, benchmark design, regression isolation |
| Kirjuri | 📝 | documentation | READMEs, examples, release notes, decision records vs. actual behavior |
| Huoltaja | 🛠️ | maintainability / DX | clarity, hidden coupling, onboarding friction, small high-payoff refactors |

This table is a convenience; the authoritative roster is each file's frontmatter
(`terva persona list` derives it). To fork one, run `terva persona init` and edit
the copy under `$TERVA_HOME/personas/review-crew/`.
