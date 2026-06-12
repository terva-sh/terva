# terva chat connectors (external)

terva bridges chat services (Telegram today; Discord, Matrix, anything)
through one **Connector** contract. Telegram is compiled in; every
other service can be an **external connector**: a separate executable
terva spawns, speaking newline-delimited JSON over stdin/stdout. Like
extensions, connectors can be written in **any language** — and a
connector built with the Go SDK (`packages/agent/connsdk`) is about
50 lines.

The split is strict: a connector is *pure transport* — wire protocol,
auth, normalizing service messages. terva owns all policy: pairing
("first user to /start claims the bot"), allowlisting, queueing
behind in-flight turns, reply chunking, and the built-in commands
(`/status`, `/stop`). The host never sees your service token; the
connector never sees pairing state.

In-tree and external connectors register into the same service
registry and are indistinguishable to the chat loop, the TUI's
`/connect`, and `terva bot`.

## Installing a connector

A connector ships a manifest:

```json
{
  "name": "discord",
  "version": "1.0.0",
  "exec": "./terva-discord-connector",
  "args": [],
  "enabled": true,
  "description": "discord gateway connector"
}
```

`exec` resolves like extension manifests: absolute paths as-is,
relative paths against the manifest's directory, bare names via
`$PATH`. terva appends the lifecycle verb as the **last** argument.

Installed connectors live at
`$TERVA_HOME/connectors/<name>/connector.json` (the directory name must
match the manifest name). Discovery is **global only — never
project-local**: a cloned repository must not be able to register an
executable that receives your private chats.

Three ways to get one there:

- copy the manifest (and binary) into `$TERVA_HOME/connectors/<name>/`;
- `terva bot link path/to/connector.json` — symlinks the manifest in,
  as a visible, auditable install artifact. `terva bot status` shows
  the link target; `terva bot reset` removes it;
- `terva --connector-manifest path/to/connector.json` (also accepted by
  `terva bot run`/`start`) — loads it for **one invocation only**,
  announced loudly at startup and tagged `(dev)` in status output.
  This is the iteration loop while developing a connector; nothing is
  discovered and nothing persists.

Then the usual surface works, with `--connector <name>`:

```
terva bot setup  --connector discord     # runs `<exec> setup` on your tty
terva bot run    --connector discord     # foreground bridge
terva bot start  --connector discord     # detached daemon
terva bot status --connector discord
terva bot reset  --connector discord     # `<exec> reset` + clears pairing (+ unlink)
```

In the TUI, `/connect` mirrors DMs into the running session the same
way it does for the built-in telegram connector. With a dev connector
loaded, it becomes the default for that run — passing the flag is
explicit intent.

## Lifecycle verbs

`terva bot` drives the executable git-credential-helper style; only
`run` speaks the protocol:

| verb | stdio | meaning |
|---|---|---|
| `run` | protocol on stdin/stdout, logs on stderr | the bridge session |
| `setup` | inherits your tty | provision credentials interactively |
| `status` | stdout captured | one config block, tokens masked |
| `reset` | inherits your tty | forget credentials |
| `configured` | none | exit 0 = configured, anything else = not |

Connector stderr during `run` lands in
`$TERVA_HOME/logs/connector-<name>.log`. Keep credentials in your own
state dir — the Go SDK's `connsdk.StateDir(name)` returns
`$TERVA_HOME/connectors/<name>`, the conventional spot.

## Go SDK quick start

```go
package main

import (
	"time"

	"terva.sh/terva/packages/agent/connsdk"
)

func main() {
	connsdk.Main(connsdk.Config{
		Name:    "myservice",
		Version: "1.0.0",
		Capabilities: connsdk.Capabilities{
			MaxTextLen:    2000,                  // terva chunks longer replies
			TypingRefresh: 8 * time.Second,       // 0 = no typing indicator
			SendsImages:   true,
			SendsFiles:    true,
		},
		NewTransport: func(s connsdk.Session) (connsdk.Transport, error) {
			return newMyTransport(s.DataDir) // your service client
		},
		Setup:      promptForToken,            // optional
		Configured: haveToken,                 // optional; nil = always
	})
}
```

`Transport` is six methods (`Connect`, `Receive`, `Send`,
`SendImage`, `SendFile`, `Typing`); the SDK handles framing,
handshake, verb dispatch, and result correlation.
`cmd/terva-telegram-connector` is the worked example — the in-tree
telegram transport wrapped for the external path (it registers as
`telegram-ext` with its own token store, so it can run next to the
built-in).

## Protocol reference (version 1)

One JSON object per LF-terminated line; max line 4 MiB. The schema is
pinned by golden tests in `packages/agent/connproto`; breaking it
means bumping the protocol version.

Handshake — the connector speaks first:

```json
→ {"type":"hello","name":"discord","version":"1.0.0","protocol_min":1,"protocol_max":1,
   "capabilities":{"max_text_len":2000,"typing_refresh_ms":8000,"sends_images":true,"sends_files":true}}
← {"type":"hello_ack","protocol":1,"terva_version":"1.2.3","terva_version":"1.2.3",
   "data_dir":"$TERVA_HOME/connectors/discord/data"}
```

`terva_version` and `terva_version` carry the same host version string
(the product is being renamed; see `docs/plans/rename-terva.md`).
Read `terva_version` and fall back to `terva_version` — the old key is
deprecated and will be dropped after the connector-SDK deprecation
window; the Go SDK already handles this for you.

Versioning is negotiated, not announce-only: hello carries
`protocol_min`/`protocol_max`, and terva refuses the spawn with a clear
error when its own version falls outside the range. terva kills a child
that sends no hello within 3 seconds.

Session — host to connector:

```json
{"type":"connect"}
{"type":"send","id":"42","chat_id":"...","reply_to":"...","text":"..."}
{"type":"send_image","id":"43","chat_id":"...","path":"/abs/file.png","caption":"..."}
{"type":"send_file","id":"44","chat_id":"...","path":"/abs/report.pdf","caption":"..."}
{"type":"typing","chat_id":"..."}
{"type":"shutdown"}
```

Connector to host:

```json
{"type":"connected","id":"1234","username":"tervabot"}
{"type":"connect_error","error":"bad token"}
{"type":"message","chat_id":"...","user_id":"...","username":"...","reply_to":"...",
 "text":"...","attachments":[{"mime_type":"image/png","path":"<data_dir>/in/abc.png"}]}
{"type":"result","id":"42","error":""}
{"type":"warn","message":"gateway reconnecting"}
```

Rules:

- Answer `connect` with exactly one `connected` or `connect_error`.
  `connect_error` means *permanently* broken (bad token); transient
  network trouble is yours to retry inside the session, surfaced via
  `warn`.
- Every `send`/`send_image`/`send_file` gets exactly one `result`
  echoing its `id` (empty `error` = success). terva times a send out
  after 30s. `typing` carries no id and gets no result.
- **Attachments travel by path, both directions** (same-host
  assumption). Inbound: write the bytes under your `data_dir` and
  reference the path; terva reads and deletes the file, and refuses
  paths outside `data_dir`. This keeps frames far below the line cap.
- On `shutdown` (or stdin closing), exit promptly; terva escalates to
  SIGTERM/SIGKILL after ~2s.
- Crashes are loud, not silent: terva restarts a crashed connector with
  backoff and surfaces every attempt, but gives up after 3 crashes in
  60 seconds and reports the bridge as broken.

Pairing state for external connectors persists host-side under
`$TERVA_HOME/connectors/<name>/pairing.json`; your process never sees
it.

## Using the bridge (telegram or any connector)

terva can run as a chat bot so you can DM it from your phone. Telegram is the built-in connector; other services plug in as **external connectors** — standalone executables in any language speaking a small JSON protocol, installed with `terva bot link` (see [docs/connectors.md](connectors.md)). Two ways to run it: **from inside the TUI** (the running session mirrors into the chat) or **as a standalone background daemon** (a headless bot with its own independent agent).

### From inside the TUI

Type `/connect` in the running TUI to open a picker with **connect**, **disconnect**, and **status**. When connected:

- DMs from the paired user become prompts in the **same** session you're typing in, so you can continue a conversation from the terminal on your phone and back again.
- Messages you type in the TUI are mirrored into the chat thread prefixed `you: ...`, and the assistant's replies are mirrored back, so the chat stays a complete record of both sides of the conversation.
- Messages sent from the chat show up as your own bubble there (no mirror) and the assistant's reply to them comes back bare.
- The status bar shows a `telegram connected` tag while the bridge is active.
- `/connect connect` / `/connect disconnect` / `/connect status` (or the `/telegram` / `/tg` aliases) also work as direct commands without the picker.

The in-TUI bridge refuses to start while the standalone daemon (below) is running, since two concurrent long-poll consumers of the same bot race on every update and silently drop messages.

### Standalone daemon

For headless servers or long-running bots unattached to a TUI:

```bash
terva bot setup     # paste a BotFather token, verify, save
terva bot run       # foreground: long-poll in this terminal (ctrl+c to stop)
terva bot start     # background: detach and return immediately
terva bot stop      # SIGTERM the background bot (SIGKILL after 5s)
terva bot logs -f   # tail $TERVA_HOME/logs/bot.log (omit -f to just cat)
terva bot status    # config (token masked) + running/stopped
terva bot reset     # forget the token and paired user
# every subcommand accepts --connector NAME (default: telegram)
# aliases: `terva telegram-bot ...` and `terva tg ...` pin --connector=telegram
```

Connectors are compiled in via build tags: telegram ships by default, and `go build -tags terva_no_telegram` (or `just build-min`) produces a leaner binary with no chat transport at all.

The background flavor writes the child's PID to `$TERVA_HOME/bot.pid` and redirects stdout and stderr to `$TERVA_HOME/logs/bot.log`. `terva bot stop` reads that PID, sends SIGTERM, waits up to five seconds, then escalates to SIGKILL if the child is still alive. Running two instances at once is refused at startup.

> **Use the installed binary for `start`.** `go run ./cmd/terva bot start` won't work. `go run` builds a binary in a temp directory and deletes it when it exits, which kills the detached child. Run `make install` (or `go build`) first and invoke the installed binary.

Setup flow:

1. Talk to [@BotFather](https://t.me/BotFather) on telegram, run `/newbot`, copy the token it gives you.
2. Run `terva bot setup` and paste the token when prompted.
3. Run `terva bot run` in the directory you want the agent to operate in.
4. Open your bot on telegram, send `/start`. The first user to do this claims the bridge (stored as `allowed_user_id`); every other user is rejected.

From then on, any DM you send is forwarded to the agent as a user prompt. Attached photos or `image/*` documents are downloaded and passed to vision-capable models. In-bot telegram commands: `/help`, `/status`, `/stop` (cancel the current turn). Config lives in `$TERVA_HOME/bot.json` (mode 0600).

Bot mode respects the usual terva flags: `--provider`, `--model`, `--cwd`, `--reasoning`, `--continue`, `--no-session`, `--no-tools`, and so on. Run `terva tg run -c --model claude-opus-4-1` to resume the latest session on Opus, for example.
