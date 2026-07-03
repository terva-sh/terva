# chat-loopback — one process, both roles

The smallest honest **dual-role plugin**: a single subprocess that is

- an **extension** — the agent can call its `loopback_stats` tool, and
- a **chat connector** — it delivers inbound messages that start agent
  turns, and carries the agent's replies back out.

The "chat service" is your filesystem, so there is nothing to sign up
for: drop a file into `inbox/` to send the bot a message; replies append
to `outbox.txt`.

The point of one process is **shared live state**: the tool and the
transport read and write the same in-memory counters. Ask the agent to
call `loopback_stats` mid-conversation and it inspects the very chat
session that delivered your message — no second process, no state-file
handshake between an extension and a separate connector.

## Run it

```sh
# install globally (the connector role is global-only by design)
terva ext install ./examples/extensions/chat-loopback

# start the bot on this connector — this is the explicit activation step;
# installing alone never makes an extension a message source
terva bot run --connector chat-loopback
```

In another shell (`$TERVA_HOME` defaults to `~/.terva`):

```sh
IN=~/.terva/ext-data/chat-loopback/inbox
echo "/start" > "$IN/000-start.txt"                       # pair (first user claims the bot)
echo "hi! call loopback_stats for me" > "$IN/001-hi.txt"  # a real turn
tail -f ~/.terva/ext-data/chat-loopback/outbox.txt        # watch replies
```

## What to look at

- `extension.json` — `"connector": true` is the install-time consent
  flag; without it the host refuses the role at the wire.
- `main.go` — `e.Tool(...)` + `e.Connector(caps, newTransport)` on one
  `ext.Extension`; the `stats` struct is the shared state. The
  transport is a plain `connsdk.Transport` — the SAME interface a
  standalone `terva bot` connector implements, so this code could ship
  either way (the extension wire tunnels the connector protocol
  verbatim rather than mirroring it).
- The transport's echo-loop hygiene: inbound only from `inbox/`,
  outbound only to `outbox.txt` — the bot can never hear itself.

## Port it to a standalone connector

The transport is a plain `connsdk.Transport`, so the SAME `fileTransport`
ships as a standalone `terva bot` connector by swapping `main`:

```go
func main() {
	connsdk.Main(connsdk.Config{
		Name:         "chat-loopback",
		Version:      "1.0.0",
		Capabilities: connsdk.Capabilities{MaxTextLen: 4000},
		NewTransport: func(s connsdk.Session) (connsdk.Transport, error) {
			return &fileTransport{
				st:     &stats{},
				inbox:  filepath.Join(s.DataDir, "inbox"),
				outbox: filepath.Join(s.DataDir, "outbox.txt"),
			}, nil
		},
	})
}
```

(installed with `terva bot link`, per [docs/connectors.md](../../../docs/connectors.md)).
That's the tunnel design's point: the extension wire carries the
connector protocol verbatim, so there is exactly one transport contract
— choose the packaging by whether you also want tools sharing the
process. Going the other way (standalone → extension) is the same swap
plus `"connector": true` in `extension.json`.

## References

- Author guide: the "Connector role" section of
  [docs/extensions.md](../../../docs/extensions.md)
- Inner protocol frame reference: [docs/connectors.md](../../../docs/connectors.md)
- Design & trade-offs: `docs/proposals/connector-extensions.md`
- Wire types: extension protocol 5 in `packages/agent/extproto/extproto.go`
  (the envelope) and `packages/agent/connproto/connproto.go` (everything
  riding inside it)
