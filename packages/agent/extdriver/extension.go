// Package extdriver is the authoritative driver for terva's subprocess
// extension wire. It owns the transport — spawning an extension process,
// completing the hello handshake, framing newline-delimited JSON over
// stdio, correlating requests with replies, and fanning lifecycle events
// out to subscribers — with no dependency beyond extproto + the process
// hardening helpers + the standard library.
//
// The live host (packages/agent/extensions.Manager) embeds a *Driver and
// layers discovery, Workspace Trust, theme options, and the core.Tool
// registry wrapper on top. A conformance tool can embed the same Driver
// and drive the identical wire, so the two can never read the protocol
// differently. See docs/plans/extdriver-extraction.md.
package extdriver

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"terva.sh/terva/packages/agent/extproto"
)

// Manifest is the extension.json file shipped alongside an
// extension's executable. It tells terva how to launch the extension
// and provides display metadata.
type Manifest struct {
	Name        string   `json:"name"`
	Version     string   `json:"version,omitempty"`
	Exec        string   `json:"exec"`               // executable path, relative to manifest dir
	Args        []string `json:"args,omitempty"`     // extra argv passed to exec
	Language    string   `json:"language,omitempty"` // informational ("go", "python", "typescript", ...)
	Enabled     *bool    `json:"enabled,omitempty"`  // nil = enabled
	Description string   `json:"description,omitempty"`
	// Config declares the settings this extension accepts. The host reads
	// it WITHOUT spawning the extension (so /extensions can offer a config
	// dialog for a stopped or disabled extension), stores the user's values
	// under config.json `extensions.<name>`, and delivers the resolved
	// values back in the hello_ack handshake (and a config_update event on
	// change). Empty means the extension takes no host-supplied config.
	Config []ConfigField `json:"config,omitempty"`
}

// ConfigField is one declared setting in an extension's manifest. The host
// renders it in the /extensions config dialog and validates input against
// it. Keep this minimal but extensible — new optional fields are additive.
type ConfigField struct {
	Key         string   `json:"key"`                // config key (the map key under extensions.<name>)
	Label       string   `json:"label,omitempty"`    // human label for the dialog (falls back to Key)
	Type        string   `json:"type,omitempty"`     // "string" (default) | "bool" | "int" | "select" | "secret"
	Default     any      `json:"default,omitempty"`  // default value when the user hasn't set one
	Required    bool     `json:"required,omitempty"` // dialog rejects save while empty
	Secret      bool     `json:"secret,omitempty"`   // mask in the dialog and never log (implied by type "secret")
	Description string   `json:"description,omitempty"`
	Options     []string `json:"options,omitempty"` // allowed values for type "select"
}

// IsSecret reports whether the field's value must be masked. Either an
// explicit Secret flag or the "secret" type marks it.
func (f ConfigField) IsSecret() bool { return f.Secret || f.Type == "secret" }

// IsEnabled returns the manifest's effective enabled state. Default
// is true so adding a new extension folder Just Works without an
// extra terva ext enable command.
func (m Manifest) IsEnabled() bool {
	return m.Enabled == nil || *m.Enabled
}

// Extension is a running extension subprocess and the metadata terva
// tracks about it.
type Extension struct {
	Manifest Manifest
	Dir      string // absolute path to extension directory
	LogPath  string

	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	logFile  *os.File
	helloAck bool
	commands []extproto.RegisterCommandFromExt
	tools    []extproto.RegisterToolFromExt
	// withdrawnTools is the set of this extension's own tool names hidden
	// from the model for the session (set_withdrawn_tools, protocol 4).
	// Guarded by the Driver's mu (same as tools), since Driver.Tools reads
	// the two together. nil/empty = nothing withdrawn (all visible).
	withdrawnTools map[string]bool

	// stopping is set when the host initiates a clean teardown
	// (Stop / reload). The read loop checks it on exit to tell a
	// deliberate shutdown apart from an unexpected subprocess crash,
	// so only real crashes surface a notice to the user.
	stopping atomic.Bool

	// stdinMu guards the stdin pipe against a write racing its Close in
	// stopExtensions. The single writeLoop goroutine is now the only
	// thing that writes to the pipe (see outbox), so this lock no longer
	// guards against interleaving between writers — only against
	// writing into a pipe Close just yanked out.
	stdinMu sync.Mutex

	// outbox is the per-extension ordered outbound queue. Every
	// host->extension frame (lifecycle events, tool/command
	// invocations, intercepts, panel frames, hello_ack, shutdown) is
	// enqueued here and drained by a single writeLoop goroutine, so the
	// order frames are enqueued is the order they reach the extension.
	// That FIFO property is the contract a session-aware extension
	// relies on: session_start is enqueued before the turn that can
	// invoke its tools, so it always arrives before that session's
	// first tool_call (extension protocol v2). Buffered so EmitEvent
	// never stalls the agent loop on a slow extension; a wedged
	// extension overflows the buffer and its *events* are dropped with
	// a log (enqueueFrame non-blocking) rather than freezing terva.
	// nil for an extension that never spawned (e.g. theme-only).
	outbox chan []byte
	// quit is closed by stopExtensions to tell writeLoop to flush
	// whatever is already queued (the shutdown frame included) and
	// exit. enqueueFrame also selects on it so producers don't block
	// forever once teardown starts.
	quit     chan struct{}
	quitOnce sync.Once
	// writerDone is closed when writeLoop returns, so stopExtensions can
	// wait (bounded) for the queued frames to flush before closing the
	// pipe.
	writerDone chan struct{}

	// readyCh is closed when the extension sends a ReadyFromExt
	// frame, or when the host gives up waiting (registrationGrace).
	readyCh   chan struct{}
	readyOnce sync.Once

	// pending command invocations waiting on a CommandResponseFromExt
	// keyed by the id we sent in CommandInvokedFromHost.
	// pendingTool is the same idea for tool calls.
	// pendingIntercept is the same idea for event_intercept calls.
	mu               sync.Mutex
	pending          map[string]chan extproto.CommandResponseFromExt
	pendingTool      map[string]chan extproto.ToolResultFromExt
	pendingIntercept map[string]chan extproto.EventInterceptResponseFromExt

	// lastFrameTime is updated by the read loop on every frame it
	// processes. Used by the auto-ready idle watchdog so legacy
	// extensions (no `ready` frame) don't pin the WaitForReady wait
	// to its full grace.
	lastFrameTime time.Time

	// eventSubs and interceptSubs are the sets of event names this
	// extension subscribed to via SubscribeFromExt. Used by
	// EmitEvent / InterceptToolCall to filter recipients.
	eventSubs     map[string]struct{}
	interceptSubs map[string]struct{}

	// Host context contributions (protocol 2), guarded by mu:
	//   staticContext  — register_context: folded into the cached
	//                    system-prompt addendum.
	//   contextCards   — context_card: injected into the model's
	//                    context each turn at the cache-free tail.
	//   statusSegments — status_segment: rendered in the TUI status
	//                    line (not model-facing).
	staticContext  string
	contextCards   map[string]contextCard
	statusSegments map[string]string
}

// newExtension allocates an Extension with its maps and the ready
// channel initialised, ready to be spawned or treated as theme-only.
func newExtension(mf Manifest, dir string) *Extension {
	return &Extension{
		Manifest:         mf,
		Dir:              dir,
		readyCh:          make(chan struct{}),
		pending:          map[string]chan extproto.CommandResponseFromExt{},
		pendingTool:      map[string]chan extproto.ToolResultFromExt{},
		pendingIntercept: map[string]chan extproto.EventInterceptResponseFromExt{},
		eventSubs:        map[string]struct{}{},
		interceptSubs:    map[string]struct{}{},
	}
}

// Ready reports whether the extension has signalled ready (or the host
// gave up waiting and closed readyCh). Non-blocking; used by Reload to
// count how many extensions came up in time.
func (e *Extension) Ready() bool {
	select {
	case <-e.readyCh:
		return true
	default:
		return false
	}
}

// outboxCapacity is how many frames may queue for one extension before
// EmitEvent starts dropping events. Generous: real extensions drain
// their stdin continuously, so the buffer only fills if one is wedged,
// in which case dropping beats stalling the agent loop.
const outboxCapacity = 1024

// errOutboxFull is returned by the non-blocking enqueue path when the
// buffer is full (a wedged extension). errExtStopped is returned once
// teardown has been signalled.
var (
	errOutboxFull = errors.New("extension outbox full")
	errExtStopped = errors.New("extension stopped")
)

// writeFrame encodes v and enqueues it on the ordered outbox, blocking
// for room if necessary (the request/reply, panel, hello_ack and
// shutdown paths all want their frame delivered, and their callers are
// already prepared to wait). A nil outbox (extension that never
// spawned, e.g. theme-only) is treated as a no-op error.
func (e *Extension) writeFrame(v any) error {
	frame, err := extproto.Encode(v)
	if err != nil {
		return err
	}
	if e.outbox == nil {
		return errors.New("extension stdin not available")
	}
	return e.enqueueFrame(frame, true)
}

// enqueueFrame appends a pre-encoded frame to the ordered outbox. When
// block is false (lifecycle events via EmitEvent), a full outbox drops
// the frame (errOutboxFull) so the agent loop never stalls on a wedged
// extension. When block is true, it waits for room, bounded only by the
// extension's liveness (quit). Either way it returns errExtStopped once
// teardown has begun.
func (e *Extension) enqueueFrame(frame []byte, block bool) error {
	// Reject promptly once stop has been signalled, so a frame can't
	// sneak past a closing extension.
	select {
	case <-e.quit:
		return errExtStopped
	default:
	}
	if block {
		select {
		case e.outbox <- frame:
			return nil
		case <-e.quit:
			return errExtStopped
		}
	}
	select {
	case e.outbox <- frame:
		return nil
	case <-e.quit:
		return errExtStopped
	default:
		return errOutboxFull
	}
}

// writeLoop is the single goroutine that drains the outbox to the stdin
// pipe, preserving enqueue order on the wire. It exits when a pipe
// write fails (the extension is gone) or when quit is closed, in which
// case it first flushes whatever is already queued so a shutdown frame
// enqueued just before teardown still reaches the extension.
func (e *Extension) writeLoop() {
	defer close(e.writerDone)
	for {
		select {
		case frame := <-e.outbox:
			if !e.writeOne(frame) {
				return
			}
		case <-e.quit:
			for {
				select {
				case frame := <-e.outbox:
					if !e.writeOne(frame) {
						return
					}
				default:
					return
				}
			}
		}
	}
}

// writeOne writes a single frame under stdinMu (which guards against a
// concurrent stdin.Close in stopExtensions). Returns false if the pipe
// is gone or the write failed, signalling writeLoop to stop.
func (e *Extension) writeOne(frame []byte) bool {
	e.stdinMu.Lock()
	defer e.stdinMu.Unlock()
	if e.stdin == nil {
		return false
	}
	_, err := e.stdin.Write(frame)
	return err == nil
}
