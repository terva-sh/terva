// Package connlocal runs a connsdk transport IN-PROCESS while still
// speaking the connector protocol: connsdk.Serve on one end of a pipe
// pair, connhost.Session on the other —
//
//	connsdk.Serve(transport) ⇄ io.Pipe ⇄ connhost.Session ⇄ chat.Connector
//
// This is how compiled-in connectors (Discord; eventually telegram)
// ship: written once against connsdk.Transport like every other
// connector, but linked into the terva binary — and every run still
// exercises the full wire, both implementations, in both directions,
// so the protocol cannot rot behind a native bypass. The same
// transport wraps into a standalone binary (connsdk.Main) or an
// extension (ext.Extension.Connector) unchanged; this package is the
// third carrier next to child-process stdio (chat/external) and the
// extension tunnel (chat/extconn). See
// docs/plans/discord-connector.md.
//
// Lifecycle is deliberately simpler than the other carriers: there is
// no process to crash and no tunnel to drop, so an engine death here
// means the transport failed permanently (its own retries exhausted) —
// Receive surfaces that as the standalone semantics do, with no
// restart budget.
package connlocal

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/chat"
	"terva.sh/terva/packages/agent/chat/connhost"
	"terva.sh/terva/packages/agent/connproto"
	"terva.sh/terva/packages/agent/connsdk"
	"terva.sh/terva/packages/privfs"
)

const (
	helloTimeout   = 5 * time.Second
	connectTimeout = 30 * time.Second
	sendTimeout    = 30 * time.Second
)

// inboundBuffer bounds messages queued between the session's
// dispatcher and Receive; overflow drops with a warn, the same posture
// as every other carrier.
const inboundBuffer = 256

// Conn adapts one in-process connsdk transport to chat.Connector.
// Construction is cheap; the engine starts at Connect and lives until
// the ctx given to Connect ends (or the transport dies).
type Conn struct {
	name      string
	cfg       connsdk.Config
	tervaHome string
	warn      func(string)

	mu         sync.Mutex
	sess       *connhost.Session
	inbound    chan chat.Message
	engine     *engine
	membership func(chat.Membership)
	events     chat.ChatEventHandlers
}

// SetMembershipHandler installs the admission-event consumer
// (chat.MembershipHandlerSetter). Call before Connect.
func (c *Conn) SetMembershipHandler(fn func(chat.Membership)) {
	c.mu.Lock()
	c.membership = fn
	c.mu.Unlock()
}

func (c *Conn) deliverMembership(mb chat.Membership) {
	c.mu.Lock()
	fn := c.membership
	c.mu.Unlock()
	if fn != nil {
		fn(mb)
	}
}

// New builds the connector. name keys the data/log dirs (the same
// $TERVA_HOME/connectors/<name>/ convention external connectors use);
// cfg carries the transport factory and capabilities exactly as
// connsdk.Main would receive them. warn receives operational lines
// (nil defaults to stderr).
func New(name string, cfg connsdk.Config, tervaHome string, warn func(string)) *Conn {
	return &Conn{name: name, cfg: cfg, tervaHome: tervaHome, warn: warn}
}

func (c *Conn) Name() string { return c.name }

func (c *Conn) warnf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if c.warn != nil {
		c.warn(msg)
		return
	}
	fmt.Fprintln(os.Stderr, msg)
}

func (c *Conn) dataDir() string {
	return filepath.Join(c.tervaHome, "connectors", c.name, "data")
}

// engine is one running connsdk.Serve instance and its pipe plumbing.
type engine struct {
	// closing hostOut EOFs the engine's stdin, which ends Serve — the
	// in-process analog of the external proxy closing the child's
	// stdin after the shutdown frame.
	hostOut *io.PipeWriter
	done    chan struct{} // closed when Serve returned
	err     error         // Serve's return, valid after done
	logFile *os.File
}

// pipeFrames carries connproto frames over the pipe pair with LF
// framing — byte-identical to the child-process carrier, minus the
// process. Reads go through connproto.FrameReader, so one over-limit
// frame is skipped (with a warning) instead of killing the stream the
// way the old bufio.Scanner did.
type pipeFrames struct {
	fr *connproto.FrameReader
	mu sync.Mutex
	w  *io.PipeWriter
}

func (p *pipeFrames) ReadFrame() ([]byte, error) {
	return p.fr.Read()
}

func (p *pipeFrames) WriteFrame(b []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.w.Write(append(b, '\n'))
	return err
}

// Connect starts the engine and runs the protocol handshake + connect
// round trips through the pipes.
func (c *Conn) Connect(ctx context.Context) (chat.Identity, error) {
	if err := privfs.MkdirAll(c.dataDir()); err != nil {
		return chat.Identity{}, err
	}
	logPath := filepath.Join(c.tervaHome, "logs", "connector-"+c.name+".log")
	_ = privfs.MkdirAll(filepath.Dir(logPath))
	logFile, err := privfs.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY)
	if err != nil {
		return chat.Identity{}, fmt.Errorf("open connector log: %w", err)
	}
	fmt.Fprintf(logFile, "\n[terva] starting in-process connector %s at %s\n",
		c.name, time.Now().Format(time.RFC3339))

	hostIn, engineOut := io.Pipe() // engine stdout → host
	engineIn, hostOut := io.Pipe() // host → engine stdin

	eng := &engine{hostOut: hostOut, done: make(chan struct{}), logFile: logFile}
	go func() {
		defer close(eng.done)
		eng.err = connsdk.Serve(c.cfg, engineIn, engineOut, logFile)
		// Unblock both ends: the session's reader sees EOF, and any
		// writer into a dead engine errors instead of hanging.
		engineOut.Close()
		engineIn.CloseWithError(io.ErrClosedPipe)
	}()

	fr := connproto.NewFrameReader(hostIn, func(msg string) {
		c.warnf("connector %q: %s", c.name, msg)
	})

	inbound := make(chan chat.Message, inboundBuffer)
	sess := connhost.New(connhost.Config{
		Name:        c.name,
		DataDir:     c.dataDir(),
		HostVersion: hostVersion(),
		Conn:        &pipeFrames{fr: fr, w: hostOut},
		Deliver: func(m chat.Message) {
			select {
			case inbound <- m:
			default:
				c.warnf("connector %q: inbound queue full; dropping a message", c.name)
			}
		},
		DeliverMembership: c.deliverMembership,
		Events: chat.ChatEventHandlers{
			Edited: func(ev chat.MessageEdited) {
				if h := c.chatEvents().Edited; h != nil {
					h(ev)
				}
			},
			Deleted: func(ev chat.MessageDeleted) {
				if h := c.chatEvents().Deleted; h != nil {
					h(ev)
				}
			},
			Reaction: func(ev chat.Reaction) {
				if h := c.chatEvents().Reaction; h != nil {
					h(ev)
				}
			},
		},
		Warn:        func(msg string) { c.warnf("%s", msg) },
		Log:         func(msg string) { fmt.Fprintf(logFile, "[terva] %s\n", msg) },
		SendTimeout: sendTimeout,
	})
	if err := sess.Start(helloTimeout); err != nil {
		c.stopEngine(eng)
		return chat.Identity{}, err
	}
	id, err := sess.Connect(ctx, connectTimeout)
	if err != nil {
		c.stopEngine(eng)
		return chat.Identity{}, err
	}

	c.mu.Lock()
	c.sess = sess
	c.inbound = inbound
	c.engine = eng
	c.mu.Unlock()
	return id, nil
}

// stopEngine asks the engine to end (inner shutdown when the session
// is up; pipe close either way) and waits briefly for Serve to return
// so the transport's ctx unwinds before we move on.
func (c *Conn) stopEngine(eng *engine) {
	if eng == nil {
		return
	}
	_ = eng.hostOut.Close() // engine stdin EOF → Serve returns
	select {
	case <-eng.done:
	case <-time.After(2 * time.Second):
	}
	eng.logFile.Close()
}

// Receive delivers inbound messages until ctx ends or the engine dies.
// An engine death is permanent (the transport's own retries are the
// resilience layer, exactly as for a standalone connector process).
func (c *Conn) Receive(ctx context.Context, handle func(chat.Message)) error {
	c.mu.Lock()
	sess, inbound, eng := c.sess, c.inbound, c.engine
	c.mu.Unlock()
	if sess == nil {
		return fmt.Errorf("connector %q: not connected", c.name)
	}
	defer c.stopEngine(eng)
	for {
		select {
		case <-ctx.Done():
			sess.Shutdown() // orderly: the engine confirms and exits
			return ctx.Err()
		case m := <-inbound:
			handle(m)
		case <-sess.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := sess.Err(); err != nil {
				return fmt.Errorf("connector %q: %w", c.name, err)
			}
			<-eng.done
			if eng.err != nil {
				return fmt.Errorf("connector %q: %w", c.name, eng.err)
			}
			return fmt.Errorf("connector %q ended its session", c.name)
		}
	}
}

func (c *Conn) Send(ctx context.Context, out chat.Outgoing) error {
	sess, err := c.connected()
	if err != nil {
		return err
	}
	return sess.Send(ctx, out)
}

func (c *Conn) SendImage(ctx context.Context, chatID, path, caption string) error {
	sess, err := c.connected()
	if err != nil {
		return err
	}
	return sess.SendImage(ctx, chatID, path, caption)
}

func (c *Conn) SendFile(ctx context.Context, chatID, path, caption string) error {
	sess, err := c.connected()
	if err != nil {
		return err
	}
	return sess.SendFile(ctx, chatID, path, caption)
}

func (c *Conn) Typing(ctx context.Context, chatID string) error {
	sess, err := c.connected()
	if err != nil {
		return err
	}
	return sess.Typing(chatID)
}

// StopTyping withdraws the typing indicator (chat.TypingStopper).
// Callers gate on Capabilities().TypingStop.
func (c *Conn) StopTyping(ctx context.Context, chatID string) error {
	sess, err := c.connected()
	if err != nil {
		return err
	}
	return sess.StopTyping(ctx, chatID)
}

// Ask runs one interactive question through the wire (chat.Asker).
// Callers gate on Capabilities().Asks, as everywhere.
func (c *Conn) Ask(ctx context.Context, a chat.Ask) (chat.Answer, error) {
	sess, err := c.connected()
	if err != nil {
		return chat.Answer{}, err
	}
	return sess.Ask(ctx, a)
}

// StartThread opens a work-stream thread (chat.Threader). Callers
// gate on Capabilities().ThreadsOut.
func (c *Conn) StartThread(ctx context.Context, chatID, fromMessageID, name string) (string, error) {
	sess, err := c.connected()
	if err != nil {
		return "", err
	}
	return sess.StartThread(ctx, chatID, fromMessageID, name)
}

func (c *Conn) Capabilities() chat.Capabilities {
	c.mu.Lock()
	sess := c.sess
	c.mu.Unlock()
	if sess == nil {
		return chat.Capabilities{}
	}
	return sess.Capabilities()
}

func (c *Conn) connected() (*connhost.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess == nil {
		return nil, fmt.Errorf("connector %q: not connected", c.name)
	}
	return c.sess, nil
}

// hostVersion is reported in the inner hello_ack. Set once at CLI
// startup, same as the sibling carriers.
var (
	versionMu sync.Mutex
	version   string
)

// SetTervaVersion records the running binary's version for hello_ack.
func SetTervaVersion(v string) {
	versionMu.Lock()
	version = v
	versionMu.Unlock()
}

func hostVersion() string {
	versionMu.Lock()
	defer versionMu.Unlock()
	return version
}

// SetChatEventHandlers installs the stage-D inbound event consumers
// (chat.ChatEventsSetter). Call before Connect.
func (c *Conn) SetChatEventHandlers(h chat.ChatEventHandlers) {
	c.mu.Lock()
	c.events = h
	c.mu.Unlock()
}

func (c *Conn) chatEvents() chat.ChatEventHandlers {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.events
}

// EditMessage / React / DeleteMessage delegate the stage-D outbound
// commands (chat.Editor / chat.Reactor / chat.Deleter). Gate on the
// matching Capabilities field, as everywhere.
func (c *Conn) EditMessage(ctx context.Context, chatID, messageID, text string) error {
	sess, err := c.connected()
	if err != nil {
		return err
	}
	return sess.EditMessage(ctx, chatID, messageID, text)
}

func (c *Conn) React(ctx context.Context, chatID, messageID, key string, remove bool) error {
	sess, err := c.connected()
	if err != nil {
		return err
	}
	return sess.React(ctx, chatID, messageID, key, remove)
}

func (c *Conn) DeleteMessage(ctx context.Context, chatID, messageID string) error {
	sess, err := c.connected()
	if err != nil {
		return err
	}
	return sess.DeleteMessage(ctx, chatID, messageID)
}
