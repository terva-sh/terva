<div align="center">
  <a href="https://www.terva.sh">
    <img src="assets/brand/exports/terva-logo-256.png" alt="terva coding agent harness" width="130" height="130" />
  </a>
</div>
<p align="center">
  <a href="https://www.terva.sh">terva.sh</a>
</p>

> **Formerly zot.** terva is a hard fork of <!-- rename:keep -->
> [patriceckhart/zot](https://github.com/patriceckhart/zot) — renamed (it's <!-- rename:keep -->
> Finnish for pine tar, the traditional preservative and cure-all) once it
> diverged too far to carry upstream's name. Existing zot installs keep <!-- rename:keep -->
> working unchanged — run `terva migrate` when you're ready to adopt the
> new data location; see [docs/fork.md](docs/fork.md) for the compat story
> and how the two projects relate.

## What is it?

A coding agent harness, lightweight and written in Go.

- one static binary.
- built-in providers for Anthropic, OpenAI/Codex/Responses, Kimi, DeepSeek, Google Gemini/Vertex, GitHub Copilot, Bedrock, Azure OpenAI, OpenRouter, Groq, Cerebras, xAI, Together, Hugging Face, Mistral, Moonshot, Z.AI, Xiaomi, MiniMax, Fireworks, Vercel AI Gateway, OpenCode, Cloudflare AI, Ollama, and any OpenAI-compatible local/custom endpoint.
- four core tools (read, write, edit, bash), plus `terva_status` for agent self-introspection.
- three run modes (interactive tui, print, json).
- chat connectors: a built-in telegram bridge, and **external connectors in
  any language** — separate executables speaking a small versioned JSON
  protocol, mirroring how extensions work. See [docs/connectors.md](docs/connectors.md).
- extensions in any language via subprocess + json-rpc. None installed by default; opt in with `terva ext install` or `terva --ext`. See [docs/extensions.md](docs/extensions.md).
- user and extension themes via JSON; see [docs/themes.md](docs/themes.md).
- reusable instructions via `SKILL.md` files; see [docs/skills.md](docs/skills.md).
- no community atm.

## How terva differs from zot <!-- rename:keep -->

terva is not a replacement for zot — it's an experiment in hardening <!-- rename:keep -->
an agent harness. The seams between harness parts are typed
contracts enforced by tests (one golden-tested event wire, typed provider
errors, declarative model capabilities, one chat-ops loop behind a
`Connector` contract); an end-to-end harness and golden protocol tests
back every change; and a long tail of upstream bugs and oddities has been
fixed along the way. We watch upstream and pull what's worth pulling, and
zot extensions remain a supported surface here permanently — with the <!-- rename:keep -->
intent to bridge any future upstream connector protocol so those tools run
on either harness. The full story, with specifics:
[docs/fork.md](docs/fork.md).

## Install

### One-liner (macOS, Linux)

```bash
curl -fsSL https://www.terva.sh/install.sh | bash
```

Detects your OS and architecture, downloads the latest release from GitHub, verifies the SHA-256 against the release's `checksums.txt`, extracts the binary, and drops it in `/usr/local/bin`, `~/.local/bin`, or `~/bin`, whichever is writable first. Pass a version or prefix to pin:

```bash
curl -fsSL https://www.terva.sh/install.sh | bash -s -- v0.0.1 ~/bin
```

### One-liner (Windows, PowerShell)

```powershell
iwr -useb https://www.terva.sh/install.ps1 | iex
```

Drops `terva.exe` into `$HOME\bin` and adds it to the user PATH if missing. Open a fresh terminal afterwards.

### go install

```bash
go install terva.sh/terva/cmd/terva@latest
```

### From source

```bash
git clone https://github.com/terva-sh/terva   # public mirror; development happens on the maintainer's Forgejo
cd terva
make build        # produces ./bin/terva
make install      # into $GOPATH/bin
```

### Prebuilt binaries

Every release on the [releases page](https://github.com/terva-sh/terva/releases) ships archives for Linux, macOS, and Windows on amd64 and arm64 (except windows/arm64), plus a `checksums.txt` file. Download, verify, `chmod +x`, and drop on your `$PATH`.

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
terva --help
```

`terva --help` lists every flag; the full reference is
[docs/cli.md](docs/cli.md).

## Documentation

| Doc | What's in it |
|---|---|
| [docs/cli.md](docs/cli.md) | Flags, tools (`read`/`write`/`edit`/`bash`/`terva_status`), run modes, the data directory |
| [docs/tui.md](docs/tui.md) | Slash commands, sessions, inline images, message queueing, key bindings |
| [docs/models.md](docs/models.md) | Picking models, fallback/rescue, custom catalogs, per-provider notes (Kimi, DeepSeek, Gemini, ollama, OpenAI-compatible) |
| [docs/providers.md](docs/providers.md) | Login flows, endpoints, `models.json` reference, capability tags |
| [docs/connectors.md](docs/connectors.md) | Chat connectors: using the telegram bridge, writing external connectors in any language |
| [docs/extensions.md](docs/extensions.md) | Extensions: installing, managing, and the full wire protocol |
| [docs/skills.md](docs/skills.md) | `SKILL.md` reusable instructions: anatomy, discovery, authoring |
| [docs/themes.md](docs/themes.md) | User and extension themes |
| [docs/rpc.md](docs/rpc.md) | Embedding terva: the RPC wire schema and the JSON event stream (Go SDK: `packages/agent/sdk`, examples under `examples/`) |
| [docs/fork.md](docs/fork.md) | How terva relates to zot, and the compatibility promises | <!-- rename:keep -->
| [docs/architecture/](docs/architecture/) | Subsystem-by-subsystem internals |

## Development

```bash
make build     # build ./bin/terva
make test      # go test -race ./...
make lint      # go vet + gofmt check
make fmt       # gofmt -w .
make release   # cross-compile linux/darwin/windows on amd64 and arm64
```

Source layout (single Go module, four packages under `packages/`):

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
packages/agent/swarm/                 background subagent runtime
packages/agent/sdk/                   public Go SDK for embedding terva in-process (package sdk)
packages/agent/ext/                   public Go SDK for writing extensions (package ext)
```

Downstream consumers can depend on individual packages:
`go get terva.sh/terva/packages/core` pulls only `core` and its transitive deps (today: `provider`), no agent or TUI code.

## License

MIT
