# terva documentation

terva is a coding-agent harness — a single Go binary that drives an LLM agent
loop against your filesystem and shell, with a hand-rolled TUI, a browser
control panel, out-of-process extensions and chat connectors, and origin-aware
permissions. This page maps everything under `docs/`.

**New here?** Start with the [CLI reference](cli.md) — it opens with
[the ways to run terva](cli.md#ways-to-run-terva) (the terminal UI, the web
daemon, and a terminal attached to that daemon) and when to use each — then dig
into your front end: the [TUI guide](tui.md) or the [web panel](web.md).

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
| [fork.md](fork.md) | Lineage — how terva relates to zot, and where the compat promises end |
| [positioning.md](positioning.md) | Where terva sits in the landscape |

## Engineering records (development repository only)

The pages above are the shipped documentation. The engineering record — design
proposals, work plans, architecture notes, decision records, reviews — lives in
the development repository and is **not part of the public release tree**, since
it references internal infrastructure. It is named here, not linked, because the
links would 404 for most readers:

| path | what's in it |
|---|---|
| `docs/architecture/` | Subsystem-by-subsystem internals. A historical baseline (June 2026): specific file/line/size claims have drifted, but the mental model holds. |
| `docs/plans/` | Active work plans plus the living roadmap. Implemented plans move to `plans/archive/`. |
| `docs/proposals/` | Active design proposals; implemented ones move to `proposals/archive/`. |
| `docs/decisions/` | Architecture decision records (ADRs). |
| `docs/reviews/` | Point-in-time whole-project review findings. |
| `docs/vanity/` | The terva.sh vanity-site sources. |
| `docs/working-agreements.md` | Conventions for planning and validating larger changes. |

Partially-shipped items are **split**: the as-shipped design record sits in
`archive/`, and a slim "remaining work" doc stays active. Every archived doc
opens with an **Archived** banner pointing to where the feature shipped.
