# terva chat connectors

terva bridges chat services (Telegram and Discord in-tree; Matrix,
anything, out of tree) through one **Connector** contract and ONE wire
protocol. A connector ships three interchangeable ways — compiled in,
as a standalone executable terva spawns (newline-delimited JSON over
stdin/stdout), or bundled inside an extension — and all three speak
the same connector protocol: even the compiled-in Discord connector
runs over an in-process pipe carrier, so the wire is exercised on
every run and cannot rot behind a native bypass. Like extensions,
connectors can be written in **any language** — and a connector built
with the Go SDK (`packages/agent/connsdk`) is about 50 lines.

The split is strict: a connector is *pure transport* — wire protocol,
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
built-in). `cmd/terva-discord-connector` is a second in-tree external
connector (the handshake examples below use it).

## Protocol reference (versions 1 and 2)

One JSON object per LF-terminated line; max line 4 MiB. The schema is
pinned by golden tests in `packages/agent/connproto`; breaking it
means bumping the protocol version. (Version 1 was deliberately
DM-shaped — it began life as the built-in telegram bot's seam. The
design rationale for everything version 2 added lives in
`docs/proposals/connector-protocol-v2.md`, now an as-built spec; THIS
file is the maintained frame reference.)

**Protocol 2 is live and additive.** The handshake negotiates the
highest version both sides speak (hello carries
`protocol_min`/`protocol_max`; hello_ack answers with the pick). At
protocol 2: `message` gains `id` (the message's OWN id, stable within
its chat — REQUIRED), `ts` (unix ms), `chat_kind`
(`dm|group|thread|channel`), `chat_title`, and `scope_id` (the container
the chat belongs to — a Discord guild, say; empty means scopeless, and it
is what lets the admission gate approve or revoke a whole scope at once);
`reply_to` means truly in-reply-to in both directions; `result` gains
`message_id` (the id of what a send created); and both sides exchange feature strings
(`capabilities.features` on hello = what the connector produces or
accepts; on hello_ack = what the host consumes). Everything past that
is gated by feature strings, never version bumps. Protocol-1
connectors keep the old shape — the message's own id riding
`reply_to` — which hosts normalize automatically, so nothing breaks
either direction.

The full feature-string vocabulary (declare only what you implement):

| feature | you provide | detailed below |
|---|---|---|
| `entities` | message markup, `bot_mention` above all | entities block |
| `chat_membership` | the bot's own admission events | entities block |
| `edits_in` / `deletes_in` / `reactions_in` | inbound edit/delete/reaction streams | edits block |
| `edits_out` / `reactions_out` / `deletes_out` | the outbound commands (+ `min_edit_interval_ms`) | edits block |
| `asks` | interactive questions with attributed answers | ask block |
| `speaker:full` / `speaker:name_only` | alternate outbound identities | speaker block |
| `threads_out` | opening work-stream threads | threads block |
| `attachment_kinds` | labeled beyond-image attachments | attachment rules |

(`message_ids` and `chat_kinds` also appear in the wild as declared
features; hosts treat the protocol-2 identity fields as
presence-evident, so declaring them is informative, not load-bearing.)

Handshake — the connector speaks first:

```json
→ {"type":"hello","name":"discord","version":"1.0.0","protocol_min":1,"protocol_max":2,
   "capabilities":{"max_text_len":2000,"typing_refresh_ms":8000,"sends_images":true,"sends_files":true,
     "features":["entities","edits_in","reactions_out","asks"]}}
← {"type":"hello_ack","protocol":2,"zot_version":"1.2.3","terva_version":"1.2.3",
   "data_dir":"$TERVA_HOME/connectors/discord/data",
   "capabilities":{"features":["entities","edits_in","reactions_out","asks"]}}
```

`zot_version` and `terva_version` carry the same host version string <!-- rename:keep -->
(terva kept zot's legacy key for compatibility when it forked; see
[fork.md](fork.md)). Read `terva_version` and fall back to
`zot_version` — the old key is deprecated and will be dropped after the <!-- rename:keep -->
connector-SDK deprecation window; the Go SDK already handles this for you.

Versioning is negotiated, not announce-only: hello carries
`protocol_min`/`protocol_max`, and terva refuses the spawn with a clear
error when its own version falls outside the range. terva kills a child
that sends no hello within 3 seconds.

Connector processes start from a sanitized environment: terva strips
loader/interpreter injection vars (`LD_*`, `DYLD_*`, `PYTHONPATH`,
`NODE_OPTIONS`, `BASH_ENV`, …) before the spawn, same as extensions.
Everything else — `PATH`, `HOME`, tokens your connector reads — passes
through.

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
{"type":"message","chat_id":"...","scope_id":"...","user_id":"...","username":"...","reply_to":"...",
 "text":"...","attachments":[{"mime_type":"image/png","path":"<data_dir>/in/abc.png"}]}
{"type":"result","id":"42","error":""}
{"type":"warn","message":"gateway reconnecting"}
```

Interactive asks (protocol 2, feature `"asks"`) — declare the feature
in your hello `capabilities.features` AND implement the rendering, and
terva can pose constrained questions with the best widget your service
has. **Two kinds of question ride this one frame:**

- **Tool approvals** — the confirm gate asking whether a call may run.
  Fail-closed: an unanswered approval **denies** the call.
- **Agent questions** — `ask_user_question`, and the prefix-change
  guard's compaction offer. Fail-*open* by design: an unanswered
  question is a **dismissal**, so the agent decides for itself and the
  turn continues. A bot whose owner is asleep must not hang, and must
  not surface a failure the model would retry into a loop.

That difference is the only thing that distinguishes them on your side:
the frames are identical, so a connector implements `"asks"` once and
gets both.

```json
→ {"type":"ask","id":"a1","chat_id":"...","reply_to":"...",
   "text":"terva wants to run `rm -rf build/` — approve?",
   "options":[{"key":"approve","label":"Approve","style":"affirm","hint":"👍"},
              {"key":"deny","label":"Deny","style":"deny","hint":"👎"}],
   "restrict_to":["u1"],"expires_ms":120000}
← {"type":"result","id":"a1","message_id":"m-90"}
← {"type":"answer","ask_id":"a1","key":"approve",
   "user_id":"u1","username":"drew","attestation":"attested"}
→ {"type":"ask_close","id":"a2","ask_id":"a1","outcome":"Approve — @drew"}
← {"type":"result","id":"a2"}
```

Option KEYS ride the wire, never widgets: render buttons (Discord), an
inline keyboard (telegram), pre-seeded reactions, or numbered text —
your choice. The ask's `id` doubles as its identity: answers reference
it as `ask_id`, and its `result` acknowledges the RENDERING
(`message_id` of the posted question), not the human. Send `answer`
frames whenever users interact — zero or more — until `ask_close`
tells you to withdraw the controls and render `outcome` into the
question message (the audit trail lives in the channel). Set
`attestation` honestly: `"attested"` only when your platform proves
who answered (button interactions, callback queries); `"best_effort"`
for parsed text or reactions — durable grants (allow-always) require
attested answers, so an inflated grade is a security lie. Filter
`restrict_to` service-side where you can (an ephemeral "not for you"
beats silence); terva re-filters regardless. Connectors WITHOUT the
feature still work: terva falls back to a numbered plain-text question
and parses the next matching reply, so approvals-over-chat reach every
service from day one — the feature only upgrades the widget and the
attestation.

**One case where the fallback is the RICHER path, not the poorer one.**
The ask frame carries a fixed option list and returns one key, so a
question that wants a *written-in* answer, or one that lets the user
pick *several* options, cannot round-trip through the widget however
good your connector is. The numbered-text floor has no such limit —
"reply 1,3" is several choices and free text is just text — so terva
routes those two kinds to the floor **even on a connector that declares
`"asks"`**. You will see a plain-text question where you expected your
buttons; that is deliberate.

The reasoning is worth stating, because it decides which way every
future degradation goes: narrowing the RENDERING is visible to anyone
reading the chat and costs a nicer widget, while narrowing the QUESTION
would hand the model one choice where the user wanted three, with
nothing recording the difference. The first is recoverable, the second
is invisible. The cost is attestation — floor answers are
`best_effort` — which is safe here only because approvals never ask
these kinds, and approvals are the only decision that grants anything
durable. Do not build a policy that needs attested identity on top of a
multi-select.

Rendering these natively is tracked work and will arrive behind its own
feature string (select menus, modals), so nothing you implement today
changes.

Speaker identity (protocol 2, feature `"speaker:full"` or
`"speaker:name_only"`) — personas and the `--play` cast want different
characters speaking in one chat. Declare a grade and `send` may carry
an alternate identity:

```json
→ {"type":"send","id":"s1","chat_id":"...","text":"The airlock hisses open.",
   "speaker":{"key":"kaiku","name":"Kaiku","avatar_path":"..."}}
← {"type":"result","id":"s1","message_id":"m-90"}
```

Render it with whatever your service has — Discord keeps one managed
webhook per channel with per-message username overrides (the
PluralKit pattern); Matrix emits per-message profiles. `key` is stable
across the session and keys your per-speaker state; `avatar_path` (a
local file, same-host convention) only arrives at `speaker:full`
connectors. Platform limits are yours to absorb: webhook messages
can't be real replies (drop or quote `reply_to`), and asks NEVER carry
a speaker — they come from the bot principal. Still return
`result.message_id`. Connectors with no grade never see the field:
terva prepends `**Name:** ` itself, so the cast works everywhere from
day one.

Entities and membership (protocol 2, features `"entities"` /
`"chat_membership"`) — the group-admission signals. `message` may
carry minimum-viable markup (offsets in Unicode code points over
`text`; `bot_mention` is the load-bearing kind — it drives terva's
group mention-gating; offset and length both zero means "mentioned,
but not locatable in the text"):

```json
"entities":[{"kind":"bot_mention","offset":4,"length":9},
            {"kind":"mention","offset":20,"length":5,"user_id":"u9"}]
```

And the connector may report the BOT's own admission changing — the
hook that lets the owner approve a group the moment the bot lands in
it instead of at the first awkward message:

```json
{"type":"chat_membership","chat":{"id":"c9","kind":"group","title":"ops"},
 "change":"added","by_user_id":"u1","by_username":"drew"}
```

Both are optional enrichment: without entities terva falls back to
scanning for `@username`, and without membership events the owner
admits chats with `/approve` when the first message arrives. Group
admission itself is host policy (owner-only, silent-by-default,
mention-gated) — your connector stays deliberately dumb about trust.

Edits, deletes, reactions (protocol 2, features `"edits_in"` /
`"deletes_in"` / `"reactions_in"` inbound and `"edits_out"` /
`"reactions_out"` / `"deletes_out"` outbound — declare each side you
implement):

```json
← {"type":"message_edited","chat_id":"...","id":"m10","ts":1751469000123,"text":"fixed"}
← {"type":"message_deleted","chat_id":"...","id":"m10"}
← {"type":"reaction","chat_id":"...","message_id":"m-90","user_id":"u1","key":"👍","removed":false}
→ {"type":"edit","id":"e1","chat_id":"...","message_id":"m-90","text":"updated"}
→ {"type":"react","id":"r1","chat_id":"...","message_id":"m-12","key":"👀"}
→ {"type":"delete","id":"d1","chat_id":"...","message_id":"m-90"}
```

Edits always reference the ORIGINAL message id (collapse edit chains
latest-wins yourself), and `min_edit_interval_ms` in your capabilities
tells terva how fast it may stream edits. Reaction `key` is an opaque
string — unicode emoji is the interoperable subset (what terva emits);
key custom emoji on their platform ID, not their name. Removal is
first-class (`removed: true`); never recompute it by absence. Apply
echo hygiene to reactions exactly as to messages: the bot's own
toggles must not come back inbound. Reactions are a LOSSY channel on
every surveyed platform — terva treats them as context, never
authority. Host defaults: an edit that arrives before the message's
turn rewrites the queued prompt; after, it becomes a note on the
chat's next prompt; deletions withdraw queued prompts; reactions on
the bot's own messages become notes. Notes reach the model typed and
bracketed (`[chat event: message_edited] …`) so it reads them as
connector state rather than user text; no-op edits (embed unfurls) and
re-deliveries coalesce; and edits/deletes of the bot's OWN messages —
ask outcomes rendering, streaming edits — are dropped host-side, so a
connector that misses that echo case is still covered. Each chat's
first prompt also carries a one-time `[chat context]` line (service,
chat title, and a formatting reminder that chat apps don't render
tables), and messages in multi-user chats are attributed
(`@name: …`) so the model knows who is speaking.

Work-stream threads (protocol 2, feature `"threads_out"`) — one
request/response pair opens a thread so a busy chat stays readable:

```json
→ {"type":"thread_start","id":"t1","chat_id":"...",
   "from_message_id":"m-12","name":"refactor: extract session core"}
← {"type":"result","id":"t1","chat_id":"t-99"}
```

The result's `chat_id` is a NEW chat of kind `thread`; sends, asks,
and speakers target it like any chat, and messages inside it arrive as
ordinary `message` frames with the thread as their `chat_id`.
`from_message_id` anchors the thread where your service supports it
and may be absent. Flat services simply don't declare the feature.

Rules:

- Answer `connect` with exactly one `connected` or `connect_error`.
  `connect_error` means *permanently* broken (bad token); transient
  network trouble is yours to retry inside the session, surfaced via
  `warn`.
- Every `send`/`send_image`/`send_file` gets exactly one `result`
  echoing its `id` (empty `error` = success). terva times a send out
  after 30s. `typing` carries no id and gets no result. `ask` and
  `ask_close` are commands with results too; only `answer` flows free.
- **Attachments travel by path, both directions** (same-host
  assumption). Inbound: write the bytes under your `data_dir` and
  reference the path; terva takes ownership — images are read and
  deleted, other kinds are moved into a per-message directory (still
  under `data_dir`) that the agent can read with its normal tools and
  that terva cleans after the turn. Paths outside `data_dir` are
  refused. Declare `"attachment_kinds"` and label each attachment
  (`kind`: image | audio | voice | video | document | sticker, plus
  `name`, `size`, `duration_ms`, `caption` — captions join the message
  text host-side). Unlabeled attachments read as images, the v1
  assumption.
- On `shutdown` (or stdin closing), exit promptly; terva escalates to
  SIGTERM/SIGKILL after ~2s.
- Crashes are loud, not silent: terva restarts a crashed connector with
  backoff and surfaces every attempt, but gives up after 3 crashes in
  60 seconds and reports the bridge as broken.

Pairing state for external connectors persists host-side under
`$TERVA_HOME/connectors/<name>/pairing.json`; your process never sees
it.

## Using the bridge (telegram or any connector)

terva can run as a chat bot so you can DM it from your phone. Telegram and Discord are the built-in connectors (`terva bot setup --connector discord` — DMs and @mentions work with no privileged intents); other services plug in as **external connectors** — standalone executables in any language speaking a small JSON protocol, installed with `terva bot link` (see [docs/connectors.md](connectors.md)). Two ways to run it: **from inside the TUI** (the running session mirrors into the chat) or **as a standalone background daemon** (a headless bot with its own independent agent). The discord built-in speaks the connector protocol even when compiled in (an in-process carrier) — it is the dogfood surface for protocol v2 (`docs/plans/discord-connector.md`).

### From inside the TUI

Type `/connect` in the running TUI to open a picker listing every configured chat service — the built-in connector, external connectors, and connector extensions, each tagged with its provenance — plus **disconnect** and **status**. `/connect <name>` connects to a specific service directly. When connected:

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

For a bot that should survive reboots — a persistent, resuming,
capability-scoped service — run `bot run` under systemd instead of
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
set preassembled — trimming it is safe, features degrade gracefully.
**No privileged intents needed**: DMs and @mentions deliver content
without them, which is exactly the mention-gated group posture terva
defaults to). Run `terva bot run --connector discord`; config lives in
`$TERVA_HOME/discord.json` (0600).

From then on, any DM you send is forwarded to the agent as a user prompt. Attached photos or `image/*` documents are downloaded and passed to vision-capable models (other attachment kinds are staged as files the agent reads with its tools). In-bot commands: `/help`, `/status`, `/stop` (cancel the current turn), `/approve`/`/revoke` (groups). Telegram config lives in `$TERVA_HOME/bot.json` (mode 0600).

Bot mode respects the usual terva flags: `--provider`, `--model`, `--cwd`, `--reasoning`, `--continue`, `--no-session`, `--no-tools`, and so on. Run `terva tg run -c --model claude-opus-4-1` to resume the latest session on Opus, for example. The paired DM's transcript persists **message by message** (the same durable hooks the TUI and ACP sessions use), so a daemon crash costs at most the in-flight turn and a restart with `--continue` picks the conversation back up; ask the bot to run `terva_status` to get the session id and file. Group chats are live-only (see below).

### Groups

Non-DM chats are **silent by default** — dropping the bot into a group
gives it no voice and nobody there any reach until you, the paired
owner, approve that chat:

- Say `/approve` in the chat itself (prefix it with @the-bot on
  services that only deliver messages addressing the bot), or
  `/approve <chat-id>` from your DM. Add `all` to respond to every
  message; the default responds only when the bot is mentioned.
- `/revoke` (in-chat or `/revoke <chat-id>` from the DM) silences it
  again. Approvals persist under `$TERVA_HOME/chat/`.
- On connectors that report admission (discord), being added to a
  server asks you directly in your DM — approve, approve-all, or
  ignore; ignoring or letting it expire keeps the chat silent.
- Group members get **reach, not authority**: their messages start
  turns in approved chats, but `/approve`, `/revoke`, `/stop`, and
  `/status` answer only to you, and tool-approval questions go to your
  DM, never the group.
- Every approved chat gets its **own conversation**: a busy group
  can't pollute your DM's context (or another group's). Your DM keeps
  its persisted session; group contexts are held live for the ~8 most
  recently active chats and dropped least-recently-used beyond that.
  `/status` and `/stop` act on the chat you say them in.

### Tools, extensions, and MCP

A bot is a **full agent**, not just a chat box. `terva bot run` hosts the same
capabilities the TUI does:

- **built-in tools** (read/write/edit/bash/grep/glob) — sandboxed to the cwd, as
  in the TUI;
- **extensions** — discovered and spawned, so their tools and live context
  cards reach the model (run `terva bot run` in/with the extensions you want);
- **MCP servers** — started per your config.

Because a bot has no interactive prompt, it defaults to **yolo** approval (it
runs its tools) — an explicit `--approval`, `--no-yolo`, or a config `approval`
still wins. When approvals are on, each resolution also leaves a
`[chat event: approval] tool "bash" approved by @you` note on the
turn's next prompt, so the model sees the permission flow instead of
inferring it from "the tool ran" (denials already reach it as the
refusal reason). A bot's capabilities are three independent
**building blocks** — turn off any combination per run:

| flag | turns off |
|---|---|
| `--no-workspace-tools` | the built-in tools (read/write/edit/bash/grep/glob) — its integrations stay, but it can't touch the host filesystem/shell (least-privilege) |
| `--no-ext` / `--no-extensions` | extension discovery (your `--ext` paths still load on top) |
| `--no-mcp` | MCP servers |

Each all-off block has a **narrowing** sibling — an allowlist instead
of a switch, for scoping rather than removing: `--tools read,grep`
(built-ins), `--extensions calendar` (installed extensions by name),
`--mcp git` (MCP servers by name). All three are restrict-only. This
matters most for a bot admitted to group chats: strangers get reach to
start turns there, so give the agent the integrations that chat needs
and nothing else — `--extensions calendar` for the planning channel,
never your mail extension.

The **mode flags** are shorthands built from those blocks, plus an identity change:

| flag | tools (in block terms) | identity |
|---|---|---|
| `--no-tools` | all three blocks together (and the `skill` tool) — nothing | unchanged |
| `--chat` | nothing (like `--no-tools`) | conversational, non-coding (see [personas](personas.md#chat-and-play-modes)) |
| `--play` | `--no-workspace-tools` — extensions + MCP only | embodied/roleplay |

`--project` is a separate axis: it scopes data + extensions to the project
(`.terva/home`; login/trust stay global — see
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

By default the bot is purely reactive — it only speaks in reply to a message.
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
the paired user speaks again. The paired chat is seeded from pairing (for a DM
connector the chat is the user) so it can open a conversation cold. Pair this
with a `--persona` so the nudge has a voice — see [personas](personas.md). The
flag works with any connector (`--connector matrix`, …) since the nudge lives in
the transport-agnostic loop.

## Connector extensions (one process, both roles)

One process, both roles: an **extension** whose manifest declares
`"connector": true` can also be a chat connector — its tools and its
message stream share state, credentials, and a live service connection.
The connector protocol above is not duplicated for this: the extension
wire (protocol 5) adds only `register_connector` plus a tiny envelope
(`chat_open` / `chat` / `chat_close` / `chat_down`) that **tunnels the
connector protocol verbatim** — hello, version negotiation, messages,
sends, results all ride through opaquely. Your transport implements the
SAME `connsdk.Transport` interface as a standalone connector and is
declared with `ext.Extension.Connector(caps, newTransport)`; moving a
connector between the two packagings is a ~5-line change of `main`.
This packaging is **experimental** until a real connector ships over
the tunnel — the envelope frames may still be reshaped without a
migration path; see
[extensions.md](extensions.md#connector-role-experimental) for the
graduation criterion.

Consent is layered deliberately: the manifest flag is install-time
visibility; only **globally-installed** extensions are offered as chat
services (never project-local ones); and nothing activates until you
select the extension by name — `terva bot run --connector <ext-name>`,
or `/connect <ext-name>` in the TUI (connector extensions appear in
the `/connect` picker tagged "extension"). Inbound messages still pass
the same pairing/allowlist gate as every other connector.

Try the demo: `examples/extensions/chat-loopback` (a filesystem-backed
chat — drop a file in `inbox/`, the agent replies to `outbox.txt` — plus
a `loopback_stats` tool reading the same live session). Design notes and
trade-offs: `docs/proposals/connector-extensions.md`.

Crashes get the same posture as standalone connectors: a budget of
reopens/respawns per minute, then permanently broken. A dead connector
HALF (fatal transport error) is redialed with the process — and its
tools — untouched; a dead PROCESS is respawned by the extension
subsystem, which re-registers its tools as it comes back.

Pure connectors (no tools) can still prefer the standalone protocol
above — one process per concern, and nothing shares fate with a chat
session.
