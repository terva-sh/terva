package build

// Surface is what becomes of the agent's words once it has said them.
//
// terva's conventions segment used to open "Your output renders in a TUI that
// understands markdown" — a sentence that is true in exactly one of the run
// modes below and a plain falsehood in the rest. A bot posting to Discord, an
// embedder reading NDJSON off `--rpc`, a `-p` run piped into `wc` — none of
// them render markdown, and telling the model otherwise buys headings and
// tables nobody asked for.
//
// So the prompt asks the surface instead of assuming one. The classification
// rule is a question, not a vibe:
//
//	Does something render this text for a person to read — and do we KNOW it does?
//
// Both halves matter. The envelope is irrelevant: ACP and the swarm both wrap
// the text in JSON, but on the far side an editor pane and a swarm pane render
// it as markdown, so markdown is right. Conversely `--rpc` also carries text in
// JSON, and there the far side is an embedder we have never met — nobody
// promised to render anything, so we promise the model nothing.
//
// See docs/proposals/external-agent-workers.md (stage 1a) and Portability,
// which is the same shape of answer to the neighbouring question: portability
// says whether a segment may LEAVE terva, surface says where its output LANDS.
type Surface string

const (
	// SurfaceRendered: a person reads this as rendered markdown. The TUI, the
	// browser, an ACP editor pane — and a swarm child, whose text is read by
	// its supervising agent and shown in the swarm pane, both of which take
	// markdown happily.
	SurfaceRendered Surface = "rendered"

	// SurfaceChat: a chat client (Discord, Slack, …). Light markdown only —
	// emphasis, code spans, fenced blocks — and a message-sized budget.
	SurfaceChat Surface = "chat"

	// SurfacePlain: raw bytes on a stream, with nothing in the path that
	// renders them. `terva -p` into a terminal, or into a pipe.
	SurfacePlain Surface = "plain"

	// SurfaceProgram: a program consumes the text as structured events and
	// decides for itself how — or whether — to show it. The RPC embedder, the
	// SDK, `--json`. We do not know what it does, so we claim nothing.
	SurfaceProgram Surface = "program"
)

// modeSurface is the one table: every Mode constant has exactly one row, and
// TestSurfaceTableIsExhaustive pins that. A new run mode does not get to
// inherit an answer by accident — it has to say where its words go.
var modeSurface = map[Mode]Surface{
	// Rendered for a person.
	ModeInteractive: SurfaceRendered, // the TUI
	ModeAttach:      SurfaceRendered, // the same TUI, over a socket
	ModeReplay:      SurfaceRendered, // the same TUI, reading a recording
	ModeWeb:         SurfaceRendered, // the browser
	ModeACP:         SurfaceRendered, // an editor's agent pane (Zed renders markdown)
	// A swarm child's stdout is NDJSON, but the text inside it is read by the
	// supervising agent and shown in the swarm pane. Both want markdown; the
	// envelope is not the audience.
	ModeSwarmAgent: SurfaceRendered,

	// A chat message.
	ModeBot: SurfaceChat,

	// Bytes on a stream.
	ModePrint: SurfacePlain,

	// Somebody else's program.
	ModeJSON: SurfaceProgram, // also the SDK, which resolves as ModeJSON
	ModeRPC:  SurfaceProgram,
}

// SurfaceOf classifies a run mode's output surface.
//
// It fails closed to SurfacePlain — the one claim that is safe to make
// anywhere, because it claims nothing: the text is text, and nothing has
// promised to render it. The asymmetry is the whole reason this type exists.
// Wrongly telling a rendered surface "markdown will not render" costs some
// prettiness; wrongly telling an unrendered one "markdown will render" is the
// bug we are here to fix, and it is silent.
func SurfaceOf(m Mode) Surface {
	if s, ok := modeSurface[m]; ok {
		return s
	}
	return SurfacePlain
}
