<div align="center">
  <a href="https://terva.sh">
    <img src="assets/brand/exports/terva-logo-256.png" alt="terva coding agent harness" width="130" height="130" />
  </a>
</div>
<p align="center">
  <a href="https://terva.sh">terva.sh</a>
</p>

## What is it?

An agent harness in a single static Go binary: a coding agent out of the box,
open to anything you can wire a tool to. One hardened, test-backed core — the
agent loop, event wire, permission policy, and two dozen model providers —
projected through many front ends and extensible in any language.

Two first-class front ends ship in the box, both driving that same core over
the control plane — same sessions, same permissions, same event stream:

- a **terminal UI** (`terva`) — the default: hand-rolled, fast, with slash
  commands, inline images, and message queueing. See [docs/tui.md](docs/tui.md).
- a **web UI** (`terva web`) — a browser control panel, not a viewer: drive
  sessions, approve tool calls, edit settings, watch the transcript stream.
  See [docs/web.md](docs/web.md).

Pick either, or both against the same session — the TUI can also drive a
running `terva web` daemon as a client (`terva attach`), so the terminal
becomes disposable and the session survives disconnects. The three shapes and
when to use each are laid out in [ways to run terva](docs/cli.md#ways-to-run-terva).
Beyond them: an editor integration over ACP, chat connectors, print/json for
scripting, and an embeddable RPC/SDK.

- one static binary.
- built-in providers for Anthropic, OpenAI/Codex/Responses, Kimi, DeepSeek, Google Gemini/Vertex, GitHub Copilot, Bedrock, Azure OpenAI, OpenRouter, Groq, Cerebras, xAI, Together, Hugging Face, Mistral, Moonshot, Z.AI, Xiaomi, MiniMax, Fireworks, Vercel AI Gateway, OpenCode, Cloudflare AI, Ollama, and any OpenAI-compatible local/custom endpoint.
- the built-in tool set: read, write, edit, bash, read-only grep/glob search, `terva_status` for agent self-introspection, a `task_*` board the agent plans with, and `session_inspect` for reading its own past transcripts. Under lazy tool visibility the model activates hidden groups on demand via `activate_tools`.
- a permission system: approval modes (`plan`/`ask`/`auto-edit`/`workspace`/`yolo`) plus typed permission rules. Interactive sessions default to **`workspace`** (built-in tools and reads run; foreign extension/MCP tools that can have side effects ask) and are **sandboxed to the working directory** by default. See [docs/permissions.md](docs/permissions.md).
- pre/post tool-use **hooks** (veto, rewrite, or observe tool calls with your own scripts; [docs/hooks.md](docs/hooks.md)) and an **MCP client** (attach Model Context Protocol servers as tools; [docs/mcp.md](docs/mcp.md)).
- run modes beyond the two UIs: an **editor integration over ACP** (Agent Client Protocol — drive terva from Zed and other ACP editors), print, json, and a JSON-RPC server for embedding. Every front end is a thin client of one agent loop and one event stream, spoken over the `ctrlproto` control plane ([docs/controllers.md](docs/controllers.md)).
- background subagents: fan work out to parallel **swarm** agents from within a session.
- **RAATI**: convene a panel of models to argue a decision to a recorded verdict, instead of trusting one model's first answer. See [docs/raati.md](docs/raati.md).
- chat connectors: built-in telegram and discord bridges, **external
  connectors in any language** (separate executables speaking a small
  versioned JSON protocol), and connectors bundled inside extensions —
  one wire for all three, with group admission, interactive approvals
  over chat (buttons on Discord), per-chat sessions, speakers, and
  threads. See [docs/connectors.md](docs/connectors.md).
- extensions in any language via subprocess + json-rpc. None installed by default; opt in with `terva ext install` or `terva --ext`. See [docs/extensions.md](docs/extensions.md).
- user and extension themes via JSON; see [docs/themes.md](docs/themes.md).
- localization: translate the UI into your language, or override terva's wording and even its model-facing prompts in place (works in English too) via per-key `$TERVA_HOME/locales` overlays. See [docs/localization.md](docs/localization.md).
- reusable instructions via `SKILL.md` files; see [docs/skills.md](docs/skills.md).
- optional **image generation**, two independent opt-in paths: the `generate_image` tool over a registry of hosted or self-hosted backends (OpenAI/LocalAI, AUTOMATIC1111/Forge, ComfyUI), rendered inline and optionally saved into the workspace; and **native image output** — the model drawing inline in its own reply via the OpenAI Responses `image_generation` tool over your Codex subscription, no extra endpoint. See [docs/image-generation.md](docs/image-generation.md) and [docs/native-image-output.md](docs/native-image-output.md).
- **chat & play modes**: reframe the harness away from coding — a conversation (`--chat`) or a roleplay/simulation (`--play`), fronted by a persona or a SillyTavern **character card** (`--card`), with a keyword-triggered **lore** context engine and a director that can voice a declared **cast** of actors. See [docs/personas.md](docs/personas.md).
- no community atm.

## Install

### One-liner (macOS, Linux)

```bash
curl -fsSL https://terva.sh/install.sh | bash
```

Detects your OS and architecture, downloads the latest release from GitHub, verifies the SHA-256 against the release's `checksums.txt`, extracts the binary, and drops it in `/usr/local/bin`, `~/.local/bin`, or `~/bin`, whichever is writable first. Pass a version or prefix to pin:

```bash
curl -fsSL https://terva.sh/install.sh | bash -s -- v0.0.1 ~/bin
```

### One-liner (Windows, PowerShell)

```powershell
iwr -useb https://terva.sh/install.ps1 | iex
```

Drops `terva.exe` into `$HOME\bin` and adds it to the user PATH if missing. Open a fresh terminal afterwards.

### Container

```bash
docker run -it --rm -v terva-data:/data ghcr.io/terva-sh/terva --help
```

Multi-arch images (linux amd64/arm64) publish with each release,
tagged per version and `latest`. All state lives on the `/data` volume
(`TERVA_HOME`); the agent's workspace is `/work`. Running connector
bots this way — and under systemd — is covered in
[docs/deploy.md](docs/deploy.md).

### go install

```bash
go install terva.sh/terva/cmd/terva@latest
```

### From source

```bash
git clone https://github.com/terva-sh/terva   # public mirror; development happens on the maintainer's Forgejo
cd terva
just build        # produces ./bin/terva
just install      # into $GOBIN (else $GOPATH/bin)
```

### Prebuilt binaries

Every release on the [releases page](https://github.com/terva-sh/terva/releases) ships archives for Linux, macOS, and Windows on amd64 and arm64 (except windows/arm64), plus a `checksums.txt` file. Each platform comes in two builds: **`terva`** (full — every feature compiled in, including the ACP editor mode; what the one-liner installs) and **`terva-min`** (lean — chat connectors left out). Both unpack the same `terva` command. Download, verify, `chmod +x`, and drop on your `$PATH`. [CHANGELOG.md](CHANGELOG.md) lists what changed in each version.

## Authenticate

Run `terva` and type `/login` — the TUI opens without credentials and walks
you through a browser-based flow, for an API key or a Claude Pro/Max,
ChatGPT Plus/Pro, Kimi Code, or GitHub Copilot subscription. Credentials
resolve in order: `--api-key` flag → provider env var (`ANTHROPIC_API_KEY`,
`OPENAI_API_KEY`, …) → `$TERVA_HOME/auth.json`. The OAuth flows, token
refresh, and the auth-file format are covered in
[docs/providers.md](docs/providers.md).

## Usage

```bash
terva                              # interactive tui
terva "fix the failing test"       # tui, pre-filled prompt
terva -p "list all go files"       # print final text, exit
terva --json "refactor main.go"    # newline-delimited json events, exit
terva --continue                   # resume the most recent session for this cwd
terva --resume                     # pick a session to resume
terva --list-models                # show supported models
terva --chat --persona kaiku       # talk with a companion persona (no tools)
terva --card ./aava.png            # chat as a SillyTavern character card
terva --play --ext ./world         # act in a simulated world extension
terva --help
```

`terva --help` lists every flag; the full reference is
[docs/cli.md](docs/cli.md).

## Documentation

| Doc | What's in it |
|---|---|
| [docs/cli.md](docs/cli.md) | Flags, tools (`read`/`write`/`edit`/`bash`/`grep`/`glob`/`terva_status`/`task_*`/`session_inspect`), run modes, the data directory |
| [docs/standard-tools.md](docs/standard-tools.md) | The tool-surface strategy: core vs standard extension vs MCP preset, and the roadmap for new tools |
| [docs/permissions.md](docs/permissions.md) | Approval modes (`plan`/`ask`/`auto-edit`/`workspace`/`yolo`, with `workspace` the interactive default), permission rules, and the jail-by-default sandbox |
| [docs/hooks.md](docs/hooks.md) | Pre/post tool-use hooks: veto, rewrite, or observe tool calls with your own scripts |
| [docs/mcp.md](docs/mcp.md) | Attaching MCP servers as tool providers (stdio, namespaced, permission-gated) |
| [docs/tui.md](docs/tui.md) | The terminal UI: slash commands, sessions, inline images, message queueing, key bindings |
| [docs/web.md](docs/web.md) | The web control panel (`terva web`): serving it, the panes, and how it drives the same core |
| [docs/models.md](docs/models.md) | Picking models, fallback/rescue, custom catalogs, per-provider notes (Kimi, DeepSeek, Gemini, ollama, OpenAI-compatible) |
| [docs/providers.md](docs/providers.md) | Login flows, endpoints, `models.json` reference, capability tags |
| [docs/connectors.md](docs/connectors.md) | Chat connectors: the telegram and discord bridges, external connectors in any language, group admission, approvals over chat |
| [docs/deploy.md](docs/deploy.md) | Running bots as services: systemd units and the container image, for persistent, resuming, capability-scoped connector agents |
| [docs/extensions.md](docs/extensions.md) | Extensions: installing, managing, and the full wire protocol |
| [docs/controllers.md](docs/controllers.md) | The control-plane protocol (`ctrlproto`): frames, method groups, events, carriers, and the management-plane horizon |
| [docs/skills.md](docs/skills.md) | `SKILL.md` reusable instructions: anatomy, discovery, authoring |
| [docs/personas.md](docs/personas.md) | Personas and immersive chat/play: charters, immersive identities, character cards, the cast + `actor_spawn`, and the mode flags |
| [docs/raati.md](docs/raati.md) | RAATI: the deliberation primitive — several models argue a decision to a recorded verdict |
| [docs/context-construction.md](docs/context-construction.md) | What actually goes into the model's context each turn, and in what order |
| [docs/debugging-prompts.md](docs/debugging-prompts.md) | Inspecting the assembled prompt (`--dump-prompt`), the lore engine, and card/lore/greeting troubleshooting |
| [docs/themes.md](docs/themes.md) | User and extension themes |
| [docs/localization.md](docs/localization.md) | Translating the UI into another language, and overriding terva's wording or model-facing prompts in place (even in English) |
| [docs/rpc.md](docs/rpc.md) | Embedding terva: the RPC wire schema and the JSON event stream (Go SDK: `packages/agent/sdk`, examples under `examples/`) |
| [docs/resource-limits.md](docs/resource-limits.md) | Bounding what a session can spend: turns, tokens, cost, and wall-clock |
| [docs/profiling.md](docs/profiling.md) | Performance-profiling the harness: the `terva_pprof` dev build, pprof/`GODEBUG` capture, and reading a TUI CPU profile |
| [docs/fork.md](docs/fork.md) | Lineage: how terva relates to zot, and the compatibility promises | <!-- rename:keep -->
| [CHANGELOG.md](CHANGELOG.md) | What changed in every released version, newest first |

## Development

`just` is the canonical local toolchain (`just --list` shows every recipe). The
command that matters before you push is the full gate — the same checks CI runs:

```bash
just ci        # gofmt + vet, the i18n catalog check, race tests, the
               # terva_acp / terva_web tagged build+test, the terva_pprof and
               # no-connector builds, and the release-overlay sync check
just lint      # gofmt + vet + i18n check (no tests)
just test      # race tests
just build     # ./bin/terva
just install-dev   # non-stripped, pprof-enabled build (see docs/profiling.md)
```

`just ci` is the full gate CI enforces — reproduce it before you push. A bare
`go test ./...` skips the tagged builds, the i18n check, and the overlay check,
so a green partial run is not a green CI.

Source layout (single Go module; the top-level packages are `provider`,
`core`, `tui`, and `agent`, with `agent` further split into focused
sub-packages):

```
cmd/terva/                              main()
packages/provider/                    LLM client surface, model catalog, streaming clients
packages/provider/auth/               credential store, api-key probe, oauth, login server
packages/core/                        agent loop, sessions, cost tracking, compaction
packages/tui/                         terminal raw-mode, input parser, editor, renderer, markdown, view
packages/agent/                       cli wiring, arg parsing, system prompt, config
packages/agent/extensions/            extension subprocess manager
packages/agent/extproto/              extension wire-format types
packages/agent/modes/                 interactive tui, print, json, dialogs
packages/agent/tools/                 read, write, edit, bash, terva_status, sandbox
packages/agent/skills/                skill discovery, frontmatter parser, skill tool
packages/agent/card/                  SillyTavern Character Card V2 parser (json + png)
packages/agent/lore/                  keyed-context (lore) engine: selection, budget, recursion
packages/agent/swarm/                 background subagent runtime
packages/agent/sdk/                   public Go SDK for embedding terva in-process (package sdk)
packages/agent/ext/                   public Go SDK for writing extensions (package ext)
```

Downstream consumers can depend on individual packages:
`go get terva.sh/terva/packages/core` pulls only `core` and its transitive deps (today: `provider`), no agent or TUI code.

## Lineage

terva began in May 2026 as a hard fork of
[patriceckhart/zot](https://github.com/patriceckhart/zot) and took its own <!-- rename:keep -->
name once it diverged too far to carry upstream's. It is **not** a
replacement, successor, or rename — zot lives on as its own project, and the <!-- rename:keep -->
two have long since gone their separate ways. Existing zot installs keep <!-- rename:keep -->
working: `ZOT_*` env vars, legacy data directories, and `.zotsession` import <!-- rename:keep -->
all still work, and `terva migrate` moves you over when you're ready.

[docs/fork.md](docs/fork.md) has the full story — what terva grew since the
fork, where the compatibility promises begin and end, and why upstream
tracking was retired.

## License

MIT
