# terva chat connectors

terva bridges chat services (Telegram and Discord in-tree; Matrix out
of tree, as `terva-sh/terva-conn-matrix`; anything else the same way)
through one **Connector** contract and ONE wire protocol. A connector ships
three interchangeable ways: compiled in, as a standalone executable terva
spawns (newline-delimited JSON over stdin/stdout), or bundled inside an
extension. All three speak
the same connector protocol: even the compiled-in Discord connector
runs over an in-process pipe carrier, so the wire is exercised on
every run and cannot rot behind a native bypass. Like extensions,
connectors can be written in **any language**, and a connector built
with the Go SDK (`packages/agent/connsdk`) is about 50 lines.

The split is strict. A connector is *pure transport*: wire protocol,
auth, normalizing service messages, rendering the widgets its service
has (buttons, webhooks, threads). terva owns all policy: pairing
("first user to /start claims the bot"), group admission, queueing
behind in-flight turns, per-chat sessions, reply chunking, approval
policy, and the built-in commands (`/status`, `/stop`, `/approve`).
The host never sees your service token; the connector never sees
pairing or admission state.

In-tree and external connectors register into the same service
registry and are indistinguishable to the chat loop, the TUI's
`/connect`, and `terva bot`.

## Secrets in a connector's own state

A standalone connector keeps its credentials in
`$TERVA_HOME/connectors/<name>/config.json`. When the host has at-rest
encryption set up, `connsdk.SealedState` seals the values the connector
declares, **in place, per value**, not the whole file:

```go
var state = connsdk.SealedState{Name: "discord-ext", Paths: []string{"/bot_token"}}

connsdk.Main(connsdk.Config{
	// …
	Secrets: &state, // NOT optional — see below
})
```

Declaring the state in `Config.Secrets` is half the pattern, not a refinement of
it. `SealedState.Save` works perfectly well without it and writes real
ciphertext, so a connector that only seals looks healthy in every surface,
right up to a key rotation. The handshake is the **only** source terva trusts
for a connector's recipient (the state file is writable by anything that can
write `$TERVA_HOME`, so believing a recipient found there would turn write
access into future read access), so an undeclared connector is never recorded,
`terva secret rotate --revoke` skips its file, and the advice that command
prints for that case ("start it once and re-run to restore access") cannot
help: starting a connector that does not declare changes nothing.
`TestAConnectorThatSealsAlsoDeclares` fails the build on a connector that seals
without declaring.

`Paths` are JSON Pointers. Everything not declared stays plaintext and
inspectable, which is the point: sealing the file whole would hand terva the
connector's entire state (chat ids, pairing claims, message history) when all
it needs is the credentials.

Each declared value is sealed to **two** recipients: the connector's own key
(`connectors/<name>/secrets.key`, minted on first save) and terva's public
recipient, read from `config.json`. That means:

- terva never hands out its private key, and the connector never holds it;
- terva can still rotate or audit those values while the connector is stopped,
  disabled, or uninstalled, which is why the co-recipient is not optional;
- a sealed value is **bound to its path** and will not open anywhere else, so
  moving one within the file breaks it rather than redirecting it.

A host with no key configured writes plaintext exactly as before, because a
connector must stay configurable on a machine that never ran
`terva secret init`. It converts on the next save once a key exists.

Both key files (`secrets.key` in any component directory) are on the agent's
read deny-list. The config beside them stays readable.

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
match the manifest name). Discovery is **global only, never
project-local**: a cloned repository must not be able to register an
executable that receives your private chats.

Three ways to get one there:

- copy the manifest (and binary) into `$TERVA_HOME/connectors/<name>/`;
- `terva bot link path/to/connector.json` symlinks the manifest in,
  as a visible, auditable install artifact. `terva bot status` shows
  the link target; `terva bot reset` removes it;
- `terva --connector-manifest path/to/connector.json` (also accepted by
  `terva bot run`/`start`) loads it for **one invocation only**,
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
loaded, it becomes the default for that run, so passing the flag is
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

A connector can add operator verbs of its own, such as a device check
for an end-to-end-encrypted service. `terva bot` never invokes them. An
operator runs them on the connector binary directly. In the Go SDK, set
`Config.Verbs`:

```go
Verbs: map[string]func() error{"verify": verifyDevice},
```

`Main` dispatches these like the built-in verbs and lists them in its
usage line. The verb is the last argument, so a verb takes no positional
arguments. A name that is empty or collides with a built-in verb, or a
nil func, makes `Main` panic on every invocation.

Connector stderr during `run` lands in
`$TERVA_HOME/logs/connector-<name>.log`. Keep credentials in your own
state dir. The Go SDK's `connsdk.StateDir(name)` returns
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

Keep the `Session` that `NewTransport` receives. `s.Warn("...")` sends
an operator-facing line that terva shows live, such as a dropped
attachment or a room the connector cannot decrypt. Your stderr only
reaches the connector's log file. `Warn` is safe from any goroutine.

By default the SDK accepts protocol 1, and at protocol 1 it drops
message ids, true in-reply-to, and the chat-event streams. A transport
whose edits, reactions, or asks depend on message ids sets
`ProtocolMin: 2` in `Config`. An older terva then refuses the connector
at the handshake with an upgrade message, instead of running it wrong.
`cmd/terva-telegram-connector` is the worked example, the in-tree
telegram transport wrapped for the external path (it registers as
`telegram-ext` and keeps its token in its own `SealedState`, so it can
run next to the built-in and holds no key of terva's).
`cmd/terva-discord-connector` is a second in-tree external
connector, and the handshake examples in
[connector-protocol.md](connector-protocol.md) use it.

Note what the telegram example does **not** do: reuse the in-tree
package's `LoadConfig`/`SaveConfig` against the connector's own
directory. Those build the secret store with terva's codec, which
resolves terva's private key from the ambient home whatever directory
is passed, because the argument governs the file and never the key. A connector
that does that is a full holder of the host's at-rest key, which is
the posture `SealedState` exists to prevent.

## Protocol reference

The frame reference moved to its own page. See
[connector-protocol.md](connector-protocol.md) for every frame in both
directions, the feature-string vocabulary, and the version-2 additions.

## Using the bridge (telegram or any connector)

terva can run as a chat bot so you can DM it from your phone. Telegram and Discord are the built-in connectors (`terva bot setup --connector discord`, and DMs and @mentions work with no privileged intents); other services plug in as **external connectors**, standalone executables in any language speaking a small JSON protocol, installed with `terva bot link` (see
[connector-protocol.md](connector-protocol.md)); Matrix ships this way, as `terva-sh/terva-conn-matrix` (Rust on the native matrix-rust-sdk, E2EE included). Two ways to run it: **from inside the TUI** (the running session mirrors into the chat) or **as a standalone background daemon** (a headless bot with its own independent agent). The discord built-in speaks the connector protocol even when compiled in (an in-process carrier), which makes it the dogfood surface for protocol v2 (`docs/plans/discord-connector.md`).

### From inside the TUI

Type `/connect` in the running TUI to open a picker listing every configured chat service (the built-in connector, external connectors, and connector extensions, each tagged with its provenance) plus **disconnect** and **status**. `/connect <name>` connects to a specific service directly. When connected:

- DMs from the paired user become prompts in the **same** session you're typing in, so you can continue a conversation from the terminal on your phone and back again.
- Messages you type in the TUI are mirrored into the chat thread prefixed `you: ...`, and the assistant's replies are mirrored back, so the chat stays a complete record of both sides of the conversation.
- Messages sent from the chat show up as your own bubble there (no mirror) and the assistant's reply to them comes back bare.
- The status bar shows a `telegram connected` tag while the bridge is active.
- `/connect <name>` / `/connect disconnect` / `/connect status` (or the `/telegram` / `/tg` aliases) also work as direct commands without the picker; bare `/connect connect` still means the default service.

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

Connectors are compiled in via build tags: telegram and discord ship by default, and `go build -tags terva_no_telegram,terva_no_discord` (or `just build-min`) produces a leaner binary with no chat transport at all.

The background flavor writes the child's PID to `$TERVA_HOME/bot.pid` and redirects stdout and stderr to `$TERVA_HOME/logs/bot.log`. `terva bot stop` reads that PID, sends SIGTERM, waits up to five seconds, then escalates to SIGKILL if the child is still alive. Running two instances at once is refused at startup.

> **Use the installed binary for `start`.** `go run ./cmd/terva bot start` won't work. `go run` builds a binary in a temp directory and deletes it when it exits, which kills the detached child. Run `just install` (or `go build`) first and invoke the installed binary.

For a bot that should survive reboots, a persistent and resuming
capability-scoped service, run `bot run` under systemd instead of
`bot start`: see [deploy.md](deploy.md) and the unit files in
`examples/deploy/systemd/`.

Setup flow (telegram):

1. Talk to [@BotFather](https://t.me/BotFather) on telegram, run `/newbot`, copy the token it gives you.
2. Run `terva bot setup` and paste the token when prompted.
3. Run `terva bot run` in the directory you want the agent to operate in.
4. Open your bot on telegram, send `/start`. The first user to do this claims the bridge (stored as `allowed_user_id`); every other user is rejected.

Setup flow (discord): create an application + bot at the [developer
portal](https://discord.com/developers/applications), copy the bot
token, then `terva bot setup --connector discord` (it validates the
token and prints a **ready-to-click invite URL** with the permission
set preassembled; trimming it is safe, and features degrade gracefully.
**No privileged intents needed**: DMs and @mentions deliver content
without them, which is exactly the mention-gated group posture terva
defaults to). Run `terva bot run --connector discord`; config lives in
`$TERVA_HOME/discord.json` (0600).

From then on, any DM you send is forwarded to the agent as a user prompt. Attached photos or `image/*` documents are downloaded and passed to vision-capable models (other attachment kinds are staged as files the agent reads with its tools). In-bot commands: `/help`, `/status`, `/stop` (cancel the current turn), `/approve`/`/revoke` (groups). Telegram config lives in `$TERVA_HOME/bot.json` (mode 0600).

Bot mode respects the usual terva flags: `--provider`, `--model`, `--cwd`, `--thinking`, `--continue`, `--no-session`, `--no-tools`, and so on. Run `terva tg run -c --model claude-opus-4-1` to resume the latest session on Opus, for example. The paired DM's transcript persists **message by message** (the same durable hooks the TUI and ACP sessions use), so a daemon crash costs at most the in-flight turn and a restart with `--continue` picks the conversation back up; ask the bot to run `terva_status` to get the session id and file. Group chats are live-only (see below).

### Groups

Non-DM chats are **silent by default**. Dropping the bot into a group
gives it no voice and nobody there any reach until you, the paired
owner, approve that chat:

- Say `/approve` in the chat itself (prefix it with @the-bot on
  services that only deliver messages addressing the bot), or
  `/approve <chat-id>` from your DM. Add `all` to respond to every
  message; the default responds only when the bot is mentioned.
- `/revoke` (in-chat or `/revoke <chat-id>` from the DM) silences it
  again. Approvals persist under `$TERVA_HOME/chat/`.
- **The message that made you approve is not lost.** A chat you have
  not admitted yet keeps its last few messages waiting (5 per chat, for
  10 minutes, text and inline images only), and approving answers the
  ones that fit the mode you chose (only the mentions for the default,
  everything for `all`) in order, as the chat's first turns. Ignoring
  the question, letting it expire, `/revoke`, and being removed all
  drop what was waiting; so does a message its author deletes while it
  waits, and an edit is honoured before replay.
- On connectors that report admission (discord), being added to a
  server asks you directly in your DM: approve, approve-all, or
  ignore. Ignoring or letting it expire keeps the chat silent.
  Removal revokes approval and cancels any pending admission question.
  A re-invite asks again without a restart. Removing the bot from a container
  resets its chats' questions and held messages; other containers keep their state.
- Group members get **reach, not authority**: their messages start
  turns in approved chats, but `/approve`, `/revoke`, `/stop`, and
  `/status` answer only to you, and tool-approval questions go to your
  DM, never the group.
- Every approved chat gets its **own conversation**: a busy group
  can't pollute your DM's context (or another group's). Your DM keeps
  its persisted session; group contexts are held live for the ~8 most
  recently active chats and dropped least-recently-used beyond that.
  `/status` and `/stop` act on the chat you say them in.

### Threads

Connectors that negotiate `chat_parents` report each thread's containing chat.
Threads in your DM accept only your messages. Threads in an approved group follow that group's current mention/all mode.
Each thread keeps its own conversation and reply target. Permission questions still go to your main DM.

Say `/revoke` in a thread to mute only that thread. The mute survives restart.
Say `/approve` to restore mention-only replies, or `/approve all` to follow the parent's mode without an extra mention restriction.
A thread cannot widen its parent's policy. Revoking the parent stops all its threads.
Reapproving a group or channel parent after container removal restores inheritance and preserves each thread's own restrictions.
Container removal mutes only the DM threads in that scope. Use `/approve all` in each affected thread to restore it.
If the host cannot save a revocation, it still stops runtime access and warns that a restart may restore access.
Connectors without parent metadata retain separate thread approvals.
When an upgraded connector reports parent metadata, existing thread approvals also require the parent's permission.
Approve the parent chat to restore inherited access.

### Tools, extensions, and MCP

A bot is a **full agent**, not just a chat box. `terva bot run` hosts the same
capabilities the TUI does:

- **built-in tools** (read/write/edit/bash/grep/glob), sandboxed to the cwd, as
  in the TUI;
- **extensions**, discovered and spawned, so their tools and live context
  cards reach the model (run `terva bot run` in/with the extensions you want);
- **MCP servers**, started per your config.

Because a bot has no interactive prompt, it defaults to **yolo** approval (it
runs its tools). An explicit `--approval`, `--no-yolo`, or a config `approval`
still wins. When approvals are on, each resolution also leaves a
`[chat event: approval] tool "bash" approved by @you` note on the
turn's next prompt, so the model sees the permission flow instead of
inferring it from "the tool ran" (denials already reach it as the
refusal reason). A bot's capabilities are three independent
**building blocks**, and you can turn off any combination per run:

| flag | turns off |
|---|---|
| `--no-workspace-tools` | the built-in tools (read/write/edit/bash/grep/glob); its integrations stay, but it can't touch the host filesystem/shell (least-privilege) |
| `--no-ext` / `--no-extensions` | extension discovery (your `--ext` paths still load on top) |
| `--no-mcp` | MCP servers |

Each all-off block has a **narrowing** sibling: an allowlist instead
of a switch, for scoping rather than removing: `--tools read,grep`
(built-ins), `--extensions calendar` (installed extensions by name),
`--mcp git` (MCP servers by name). All three are restrict-only. This
matters most for a bot admitted to group chats: strangers get reach to
start turns there, so give the agent the integrations that chat needs
and nothing else. Use `--extensions calendar` for the planning channel,
never your mail extension.

The **mode flags** are shorthands built from those blocks, plus an identity change:

| flag | tools (in block terms) | identity |
|---|---|---|
| `--no-tools` | all three blocks together (and the `skill` tool), so nothing | unchanged |
| `--chat` | nothing (like `--no-tools`) | conversational, non-coding (see [personas](personas.md#chat-and-play-modes)) |
| `--play` | `--no-workspace-tools`, so extensions + MCP only | embodied/roleplay |

`--project` is a separate axis: it scopes data + extensions to the project
(`.terva/home`; login/trust stay global, see
[extensions](extensions.md#project-scoped-agents)), not a tool toggle.

```bash
terva bot run --no-workspace-tools                # integrations only — no host fs/shell
terva bot run --no-mcp --no-ext                   # built-in tools only
terva bot run --no-workspace-tools --extensions calendar --no-mcp
                                                  # ONE integration, nothing else — group-chat posture
terva bot run --persona kaiku --chat              # a pure conversation bot
terva bot run --persona wayfarer --play --ext ./world   # a world/roleplay bot
terva bot run --project                           # a self-contained project bot
```

### Proactive idle nudge

By default the bot is purely reactive: it only speaks in reply to a message.
`--idle-nudge <duration>` lets it **open a conversation when the chat goes
quiet**: a companion that comments, a watcher that checks in, a persona that
breaks the silence.

```bash
terva bot run --persona kaiku --idle-nudge 30m
terva bot run --persona kaiku --idle-nudge 45m \
  --idle-prompt "(It's quiet — open with a small question.)"
```

When the paired chat has been silent for `--idle-nudge` (no inbound message and
no turn running), the loop injects a cue as a synthetic prompt and the agent
replies in its persona's voice. `--idle-prompt` overrides the default cue.

It nudges, it doesn't nag: it fires **once per silence** and re-arms only when
the paired user speaks again. The paired chat is seeded from pairing, using the
user-id-as-chat-id guess that
[connector-protocol.md](connector-protocol.md) describes under
*owner-directed frames*, so it can open a conversation cold and a real
inbound DM corrects it. Pair this with a `--persona` so the nudge has a voice;
see [personas](personas.md). The nudge itself lives in the transport-agnostic
loop, so it works with any connector whose DM chat ids are user ids, or that
resolves a user id to the owner's DM; elsewhere it starts nudging once the owner
has sent one DM.

## Connector extensions (one process, both roles)

One process, both roles: an **extension** whose manifest declares
`"connector": true` can also be a chat connector, and its tools and its
message stream share state, credentials, and a live service connection.
The connector protocol is not duplicated for this: the extension
wire (protocol 5) adds only `register_connector` plus a tiny envelope
(`chat_open` / `chat` / `chat_close` / `chat_down`) that **tunnels the
connector protocol verbatim**, so hello, version negotiation, messages,
sends, and results all ride through opaquely. Your transport implements the
SAME `connsdk.Transport` interface as a standalone connector and is
declared with `ext.Extension.Connector(caps, newTransport)`; moving a
connector between the two packagings is a ~5-line change of `main`.
This packaging is **experimental** until a real connector ships over
the tunnel, because the envelope frames may still be reshaped without a
migration path; see
[extensions.md](extensions.md#connector-role-experimental) for the
graduation criterion.

Consent is layered deliberately: the manifest flag is install-time
visibility; only **globally-installed** extensions are offered as chat
services (never project-local ones); and nothing activates until you
select the extension by name with `terva bot run --connector <ext-name>`,
or `/connect <ext-name>` in the TUI (connector extensions appear in
the `/connect` picker tagged "extension"). Inbound messages still pass
the same pairing/allowlist gate as every other connector.

Try the demo: `examples/extensions/chat-loopback` (a filesystem-backed
chat: drop a file in `inbox/`, the agent replies to `outbox.txt`, plus
a `loopback_stats` tool reading the same live session). Design notes and
trade-offs: `docs/proposals/connector-extensions.md`.

Crashes get the same posture as standalone connectors: a budget of
reopens/respawns per minute, then permanently broken. A dead connector
HALF (fatal transport error) is redialed with the process and its
tools untouched; a dead PROCESS is respawned by the extension
subsystem, which re-registers its tools as it comes back.

Pure connectors (no tools) can still prefer the standalone protocol, which
gives one process per concern and nothing sharing fate with a chat
session.
