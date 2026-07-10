package ext

// The connector role (extension protocol 5, experimental): one extension
// process that is ALSO a chat connector, so its tools and its message
// stream share state, credentials, and a live service connection. The
// author implements connsdk.Transport — the SAME interface a standalone
// connector implements, so a connector moves between the two packagings
// by swapping ~5 lines of main — and declares it with Connector() before
// Run(). The session itself is connsdk.Serve, the standalone SDK's
// engine, running over an in-process pipe pair; its connproto frames
// ride the extension wire inside `chat` envelope frames. This file is
// only the pump between the two.
//
// Requirements beyond the code: the extension's manifest must declare
// "connector": true (the host refuses the role at the wire otherwise),
// and a chat consumer must select the extension by name (`terva bot run
// --connector <name>`, the TUI's /connect) — merely being installed
// never makes it a message source.
//
// Echo-loop hygiene is the transport's job: never deliver a message the
// bot itself sent (services usually surface your own outbound back to
// you), or the agent will converse with itself.
//
// See docs/proposals/connector-extensions.md.

import (
	"encoding/json"
	"io"
	"os"
	"sync"

	"terva.sh/terva/packages/agent/connsdk"
	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/lineframe"
)

// chatEngineBuffer bounds host→engine frames queued while the engine is
// busy (e.g. blocked in the transport's Connect). The read loop must
// never block feeding the engine — tool calls arrive on the same stdin.
const chatEngineBuffer = 256

// connectorConfig is the declared connector role, pending activation.
type connectorConfig struct {
	caps         connsdk.Capabilities
	newTransport func(connsdk.Session) (connsdk.Transport, error)
}

// Connector declares this extension is also a chat connector. Call
// before Run(), like Tool/Command. newTransport is invoked lazily — once
// per chat session, when a chat consumer selects this extension — so
// declaring the role costs nothing until it is actually used. The
// connsdk.Session it receives carries the host-assigned DataDir for
// inbound attachment files, exactly as for a standalone connector.
func (e *Extension) Connector(caps connsdk.Capabilities, newTransport func(connsdk.Session) (connsdk.Transport, error)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.connectorCfg = &connectorConfig{caps: caps, newTransport: newTransport}
}

// chatEngine is one live chat session: a connsdk.Serve engine on
// in-process pipes, its frames pumped through `chat` envelopes.
type chatEngine struct {
	id       string
	toEngine chan []byte
	stop     func() // idempotent: stops the input pump, which EOFs the engine
}

// --- Run-loop handlers (called from Run's read loop) --------------------

// handleChatOpen starts the connector engine for a new session. The
// engine dials and receives on its own goroutines, so a slow service
// dial never blocks a concurrent tool_call arriving on the same stdin
// (the reentrancy case: a turn started by this connector may call this
// same extension's tools mid-turn).
func (e *Extension) handleChatOpen(id string) {
	e.mu.Lock()
	cfg := e.connectorCfg
	name, version := e.name, e.version
	e.mu.Unlock()
	if cfg == nil {
		_ = e.send(extproto.ChatDownFromExt{Type: "chat_down", ID: id, Error: "extension does not provide a connector"})
		return
	}

	// Last-open-wins: tear down any stale session first. Envelope
	// session ids keep its stragglers (including its eventual
	// chat_down) from bleeding into the new session.
	e.teardownChat()

	inR, inW := io.Pipe()   // host → engine
	outR, outW := io.Pipe() // engine → host

	eng := &chatEngine{id: id, toEngine: make(chan []byte, chatEngineBuffer)}
	done := make(chan struct{})
	var stopOnce sync.Once
	eng.stop = func() { stopOnce.Do(func() { close(done) }) }

	e.chatMu.Lock()
	e.chatEngine = eng
	e.chatMu.Unlock()

	// Input pump: feeds inner frames to the engine's stdin. Owning the
	// blocking pipe write here (not on the read loop) means a wedged
	// engine wedges only its own session. Closing the pipe on stop is
	// what ends the engine (its scanner sees EOF; Serve returns).
	go func() {
		defer inW.Close()
		for {
			select {
			case <-done:
				return
			case frame := <-eng.toEngine:
				if _, err := inW.Write(append(frame, '\n')); err != nil {
					return
				}
			}
		}
	}()

	// Output pump: wraps each engine frame in a chat envelope. io.Pipe
	// is unbuffered, so the engine's writes rendezvous here; pumpDone
	// lets the exit path order chat_down AFTER the engine's last frame.
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		fr := lineframe.NewReader(outR, lineframe.DefaultMaxBytes, func(msg string) {
			e.Logf("connector %s: %s", name, msg)
		})
		for {
			line, err := fr.Read()
			if err != nil {
				break // io.EOF or a read error: the engine closed its output
			}
			_ = e.send(extproto.ChatFrame{Type: "chat", ID: id, Frame: json.RawMessage(line)})
		}
	}()

	// The engine itself: the standalone connector SDK's protocol loop,
	// verbatim. It returns nil on an orderly end (input EOF after
	// chat_close/teardown, or an inner shutdown frame) and an error when
	// the transport died fatally; either way the session is over, and
	// chat_down tells the host which it was.
	go func() {
		err := connsdk.Serve(connsdk.Config{
			Name:         name,
			Version:      version,
			Capabilities: cfg.caps,
			NewTransport: cfg.newTransport,
		}, inR, outW, os.Stderr)
		outW.Close()
		<-pumpDone
		eng.stop()
		e.chatMu.Lock()
		if e.chatEngine == eng {
			e.chatEngine = nil
		}
		e.chatMu.Unlock()
		down := extproto.ChatDownFromExt{Type: "chat_down", ID: id}
		if err != nil {
			down.Error = err.Error()
		}
		_ = e.send(down)
	}()
}

// handleChatFrame feeds one inner frame to the live session's engine.
// Frames for a stale session (a close/reopen race) are dropped.
func (e *Extension) handleChatFrame(id string, frame json.RawMessage) {
	e.chatMu.Lock()
	eng := e.chatEngine
	e.chatMu.Unlock()
	if eng == nil || eng.id != id {
		return
	}
	select {
	case eng.toEngine <- append([]byte(nil), frame...):
	default:
		// The engine stopped consuming long enough to blow the buffer;
		// its session is already broken (frames would go missing), so
		// end it loudly rather than block the read loop.
		e.Logf("chat engine backlog full; ending the session")
		eng.stop()
	}
}

// handleChatClose ends the session from the host side (the chat
// consumer left). The engine unwinds — Serve's context cancellation
// stops the transport's Receive — and its exit path sends the
// confirming chat_down. The process stays alive; a later chat_open
// starts a fresh session.
func (e *Extension) handleChatClose(id string) {
	e.chatMu.Lock()
	eng := e.chatEngine
	e.chatMu.Unlock()
	if eng == nil || eng.id != id {
		return
	}
	eng.stop()
}

// teardownChat ends any live session (Run teardown, or a stale session
// displaced by a new chat_open).
func (e *Extension) teardownChat() {
	e.chatMu.Lock()
	eng := e.chatEngine
	e.chatMu.Unlock()
	if eng != nil {
		eng.stop()
	}
}
