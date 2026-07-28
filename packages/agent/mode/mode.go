// Package mode is terva's run-mode vocabulary: which loop the binary runs, and
// the identity every per-mode decision is keyed on.
//
// It is deliberately tiny and deliberately dependency-free. A Mode is produced
// once, by flag parsing, and then ASKED QUESTIONS OF by tables that live with
// whatever they answer for — where a run's output lands (build's modeSurface),
// what a session may do unattended (build's modePosture), which loop to start
// (cli.go's dispatch switch). Those tables belong to their own subjects; the
// vocabulary they share belongs here, so that sharing it does not mean
// importing the flag parser.
//
// # A new mode has to answer, not inherit
//
// Every table keyed on Mode is exhaustive, and each is guarded against All().
// That is the whole point of the roster being here rather than in one table's
// test file, where it used to live: a new constant added below fails the guards
// of every table that has not been told about it, in packages this one has
// never heard of. A mode that falls off the end of a chain inherits an answer
// instead of giving one, and that has shipped real bugs — a bot inheriting the
// interactive default got told its output rendered in a TUI, and was confined
// to the cwd for a reason nobody had written down.
package mode

// Mode is the CLI run mode.
type Mode string

const (
	Interactive Mode = "interactive"
	Print       Mode = "print"
	JSON        Mode = "json"
	RPC         Mode = "rpc"
	// ACP is the Agent Client Protocol mode (editor↔agent JSON-RPC 2.0
	// on stdio — Zed and other ACP clients). Routed via the `terva acp`
	// subcommand and selected by --acp. The wire implementation is an
	// opt-in build (-tags terva_acp); the no-tag binary routes here too but
	// exits with "acp mode not built in".
	ACP Mode = "acp"
	// SwarmAgent is the long-lived, headless daemon mode used by
	// swarm-spawned agents. The binary opens a unix-socket inbox at
	// the path provided by --swarm-agent, reads supervisor messages
	// off it ("user ...", "cancel", "shutdown"), runs each user turn
	// against a persistent session, and streams JSONL events on
	// stdout. See packages/agent/swarm/inbox.go for the wire protocol.
	SwarmAgent Mode = "swarm-agent"
	// Web is the browser control-panel mode: a local HTTP server that
	// speaks ctrlproto over a WebSocket to a self-hosted terva workspace
	// (chat + sessions + models). Routed via `terva web` / --web. The server
	// is an opt-in build (-tags terva_web); the no-tag binary routes here too
	// but exits with "web mode not built in".
	Web Mode = "web"
	// Attach runs the interactive TUI as a CLIENT of a running daemon
	// (`terva attach [URL]` / --attach): the same Interactive loop, but the
	// carrier is a reconnecting ctrlproto WebSocket client instead of an
	// in-process Workspace. Sessions, credentials, extensions, and tools all
	// live daemon-side; the TUI renders and controls. See
	// docs/proposals/archive/tui-attach.md (stage 2).
	Attach Mode = "attach"
	// Replay plays a recorded session transcript back through the
	// interactive TUI as a deterministic, transport-controlled scene (the
	// session player). Routed via `terva replay <file>` / --replay. It backs
	// the TUI with a read-only replay carrier instead of a live Workspace, so
	// it needs no credential and rejects prompts. See
	// docs/proposals/session-player.md.
	Replay Mode = "replay"
	// Bot is the chat-connector daemon (`terva bot run`): a full agent
	// whose user is a Discord/Slack conversation. It is NOT set by a flag —
	// `bot run` is routed before the mode switch and parses its own tail, so
	// runBotRun stamps it after ParseArgs.
	//
	// It exists because a bot is neither interactive nor headless and both
	// answers are wrong for it. It used to inherit ParseArgs's Interactive
	// default, which quietly made two decisions: the prompt told a Discord bot
	// its output rendered in a TUI (see build.Surface), and resolveJail
	// confined it to the cwd. The first was a bug; the second was right, and is
	// now said out loud rather than inherited.
	Bot Mode = "bot"
)

// all is the roster every per-mode table is checked against. It is a package
// var rather than a literal in All() so the two cannot drift, and unexported so
// no importer can append to the set of modes that exist.
var all = []Mode{
	Interactive,
	Print,
	JSON,
	RPC,
	ACP,
	SwarmAgent,
	Web,
	Attach,
	Replay,
	Bot,
}

// All returns every run mode terva has, in declaration order.
//
// It returns a copy: a caller ranging over the modes to check a table must not
// be able to change what the next caller's table is checked against.
func All() []Mode {
	out := make([]Mode, len(all))
	copy(out, all)
	return out
}
