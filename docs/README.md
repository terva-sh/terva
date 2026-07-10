# terva documentation

terva is a coding-agent harness — a single Go binary that drives an LLM agent
loop against your filesystem and shell, with a hand-rolled TUI, a browser
control panel, out-of-process extensions and chat connectors, and origin-aware
permissions. This page maps everything under `docs/`.

**New here?** Start with the [CLI reference](cli.md) and the [TUI guide](tui.md),
then skim the [architecture overview](architecture/01-overview.md).

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
| [permissions.md](permissions.md) | Approval modes, typed rules, the sandbox |
| [context-construction.md](context-construction.md) | What goes into the model's context each turn |
| [image-generation.md](image-generation.md) | `generate_image` and its backends |
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
| [fork.md](fork.md) | terva & zot — the fork relationship |
| [positioning.md](positioning.md) | Where terva sits in the landscape |

## Architecture

[`architecture/`](architecture/README.md) is a **historical baseline
(June 2026)** — a working mental model of the system from `01-overview` through
the deep review and the permission model. Specific file/line/size claims may
have drifted since the major feature waves; the mental model holds. Start at its
[README](architecture/README.md).

## Plans & proposals

- [`plans/`](plans/README.md) — active work plans plus the living
  [roadmap](plans/roadmap.md). Implemented plans live in
  [`plans/archive/`](plans/archive/).
- [`proposals/`](proposals/README.md) — active design proposals. Implemented
  ones live in [`proposals/archive/`](proposals/archive/).

Partially-shipped items are **split**: the as-shipped design record sits in
`archive/`, and a slim "remaining work" doc stays active. Every archived doc
opens with an **Archived** banner pointing to where the feature shipped.

## Records

- [`decisions/`](decisions/README.md) — architecture decision records (ADRs).
- [`reviews/`](reviews/README.md) — point-in-time whole-project review findings.
- [`vanity/`](vanity/README.md) — the terva.sh vanity-site sources.
