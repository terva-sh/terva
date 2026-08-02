# terva documentation

terva is a harness for tool-using agents — a single Go binary running a
permissioned agent loop, projected through many front ends and extensible in
any language. It ships wired for coding — read, write, and run across your
project — but the core is general: hand it extensions or MCP servers and it
operates whatever they expose, all under one permission and policy model.
This page maps everything under `docs/`.

**Want to run it?** Start with the [CLI reference](cli.md) — it opens with
[the ways to run terva](cli.md#ways-to-run-terva) (the terminal UI, the web
daemon, and a terminal attached to that daemon) and when to use each — then dig
into your front end: the [TUI guide](tui.md) or the [web panel](web.md).

**Want to understand it?** Start with [terva by design](design/README.md) — how
the harness works and why, in ordinary programming vocabulary. It assumes no Go.

**Building your own harness?** [practices/](practices/README.md) is what we and
the field have learned about this category, generalized and evidence-graded.

## Understanding terva

| Doc | About |
|---|---|
| [design/](design/README.md) | **How terva works, and why** — the agent loop, context economics, the permission chokepoint, the control plane, the extension seams, and the lessons behind each. Language-agnostic |
| [practices/](practices/README.md) | **Best practices for agentic harnesses** — generalized guidance for anyone building one, with the evidence behind each claim graded |
| [positioning.md](positioning.md) | Where terva sits in the landscape |
| [fork.md](fork.md) | Lineage — how terva relates to zot, and where the compat promises end |

## User & operator guides

### Using terva

| Doc | About |
|---|---|
| [cli.md](cli.md) | CLI reference — subcommands, flags, run modes |
| [tui.md](tui.md) | The interactive terminal UI |
| [web.md](web.md) | `terva web` — the browser control panel |
| [models.md](models.md) | Models & providers in practice |
| [personas.md](personas.md) | Personas and crews |
| [raati.md](raati.md) | RAATI — the three-seat deliberation panel |
| [workflows.md](workflows.md) | Scripted multi-agent orchestration — `terva workflow run` |
| [permissions.md](permissions.md) | Approval modes, typed rules, the sandbox |
| [context-construction.md](context-construction.md) | What goes into the model's context each turn |
| [image-generation.md](image-generation.md) | `generate_image` and its backends |
| [native-image-output.md](native-image-output.md) | The model drawing images inline (`native_output`, Codex) |
| [scripting.md](scripting.md) | `code_execution` — the in-engine JavaScript sandbox |
| [themes.md](themes.md) | TUI themes |
| [skills.md](skills.md) | `SKILL.md` instruction files |
| [debugging-prompts.md](debugging-prompts.md) | Inspecting / debugging a prompt |

### Extending & integrating

| Doc | About |
|---|---|
| [extensions.md](extensions.md) | Out-of-process extensions + SDK |
| [connectors.md](connectors.md) | Chat connectors (Telegram, Discord, external) |
| [mcp.md](mcp.md) | MCP servers |
| [hooks.md](hooks.md) | Tool-call hooks |
| [standard-tools.md](standard-tools.md) | The standard tool set — strategy & playbook |
| [controllers.md](controllers.md) | `ctrlproto` control-plane reference |
| [rpc.md](rpc.md) | JSON-RPC server mode |
| [localization.md](localization.md) | Localizing / customizing strings |

### Operating

| Doc | About |
|---|---|
| [deploy.md](deploy.md) | Running terva bots as services |
| [providers.md](providers.md) | Provider setup & auth |
| [profiling.md](profiling.md) | Profiling terva |
| [resource-limits.md](resource-limits.md) | Resource limits |

## Engineering records (development repository only)

The pages above are the shipped documentation. The engineering record — the
Go-level implementation notes, design proposals, work plans, decision records,
reviews — lives in the development repository and is **not part of the public
release tree**, since it references internal infrastructure. It is named here,
not linked, because the links would 404 for most readers:

| path | what's in it |
|---|---|
| `docs/architecture/` | The **implementation tier**: subsystem-by-subsystem internals cited to Go files and symbols, refreshed 2026-07-26 as an as-built record (docs 08/09 stay frozen June-2026 reviews; removals are logged in `ARCHIVE.md`, and `07-observations.md` is the standing review agenda). The conceptual counterpart of each doc ships publicly in [`design/`](design/README.md). |
| `docs/plans/` | Active work plans plus the living roadmap. Implemented plans move to `plans/archive/`. |
| `docs/proposals/` | Active design proposals; implemented ones move to `proposals/archive/`. |
| `docs/decisions/` | Decision records — both the load-bearing calls we took and the directions we declined, each with what would reopen it. |
| `docs/reviews/` | Point-in-time whole-project review findings. |
| `docs/vanity/` | The terva.sh vanity-site sources. |
| `docs/working-agreements.md` | Conventions for planning and validating larger changes. |

Partially-shipped items are **split**: the as-shipped design record sits in
`archive/`, and a slim "remaining work" doc stays active. Every archived doc
opens with an **Archived** banner pointing to where the feature shipped.
