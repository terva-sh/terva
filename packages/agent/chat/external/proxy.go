package external

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"terva.sh/terva/packages/agent/chat"
	"terva.sh/terva/packages/agent/chat/connhost"
	"terva.sh/terva/packages/agent/connproto"
	"terva.sh/terva/packages/agent/procenv"
	"terva.sh/terva/packages/privfs"
	"terva.sh/terva/packages/secretstore"
)

// Tunables. Fields on the Proxy (not consts) so tests can shrink them.
const (
	// A connector proxy is dialled on demand, not at startup, so its hello
	// grace stays short and independent of extdriver.DefaultHelloTimeout —
	// which was raised once extension loading moved off the startup path.
	defaultHelloTimeout   = 3 * time.Second
	defaultConnectTimeout = 30 * time.Second // service dial + auth round-trip
	defaultSendTimeout    = 30 * time.Second // per send/send_image/send_file result
	defaultRestartMax     = 3                // crashes tolerated per window
	defaultRestartWindow  = 60 * time.Second
	defaultRestartDelay   = time.Second // base backoff, doubles per attempt
)

// inboundBuffer bounds how many normalized messages can queue between
// the protocol session and Receive. The session's dispatcher must never
// block on it — it also delivers send results — so overflow drops with
// a warn.
const inboundBuffer = 256

// Proxy adapts one external connector executable to chat.Connector.
// It spawns `<exec> <args...> run` and owns everything process-shaped
// about it: spawning, the crash/restart budget, logs, and reaping. The
// connector protocol itself — handshake, version negotiation, frame
// dispatch, attachment containment — is a connhost.Session over the
// child's stdio (the same session core the extension tunnel uses, so
// the wire has one host implementation). Crashes are loud: the child
// is restarted with backoff and every attempt is surfaced via Warn;
// past RestartMax crashes in RestartWindow, Receive returns an error —
// permanently broken, no silent deregistration.
//
// Construction is cheap and spawns nothing (status surfaces build
// connectors just to inspect pairing); the child starts at Connect
// and lives until the ctx given to Connect ends.
type Proxy struct {
	manifest  Manifest
	dir       string // child working dir = real manifest dir
	tervaHome string

	// Warn receives operational lines (crash/restart notices, child
	// warn frames, dropped attachments). Optional; defaults to stderr.
	Warn func(string)

	helloTimeout   time.Duration
	connectTimeout time.Duration
	sendTimeout    time.Duration
	restartMax     int
	restartWindow  time.Duration
	restartDelay   time.Duration

	mu       sync.Mutex
	child    *child
	session  *connhost.Session
	caps     chat.Capabilities // last-known; survives a dead child for status surfaces
	identity chat.Identity

	inbound    chan chat.Message
	drops      atomic.Uint64 // messages lost to a full inbound queue (chat.DropCounter)
	childExit  chan error
	restarts   []time.Time
	membership func(chat.Membership)
	events     chat.ChatEventHandlers
}

// child is one spawned connector process and its pipes.
type child struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdinMu sync.Mutex // serializes frame writes, the extension pattern
	logFile *os.File
	// waited closes once cmd.Wait returned. Wait is called exactly
	// once, by the reaping goroutine that runs when the child's
	// session ends (calling it earlier would close the pipe under the
	// session's reader).
	waited chan struct{}
}

// NewProxy builds the proxy for a loaded manifest. manifestDir is the
// REAL manifest directory from LoadManifest (symlinks resolved).
func NewProxy(m Manifest, manifestDir, tervaHome string, warn func(string)) *Proxy {
	return &Proxy{
		manifest:       m,
		dir:            manifestDir,
		tervaHome:      tervaHome,
		Warn:           warn,
		helloTimeout:   defaultHelloTimeout,
		connectTimeout: defaultConnectTimeout,
		sendTimeout:    defaultSendTimeout,
		restartMax:     defaultRestartMax,
		restartWindow:  defaultRestartWindow,
		restartDelay:   defaultRestartDelay,
		inbound:        make(chan chat.Message, inboundBuffer),
		childExit:      make(chan error, 4),
	}
}

func (p *Proxy) Name() string { return p.manifest.Name }

func (p *Proxy) warnf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if p.Warn != nil {
		p.Warn(msg)
		return
	}
	fmt.Fprintln(os.Stderr, msg)
}

// recordSecrets registers what the connector declared in its handshake: its own
// age recipient and the paths in its state file that hold sealed values.
//
// This is the only source terva trusts for a recipient. The state file is
// writable by anything that can write $TERVA_HOME — the model reaches it
// through bash regardless of the write jail — so a recipient read from there
// would turn write access into future READ access: plant a recipient, wait for
// a rotation, open the ciphertext.
//
// A failure here is logged and dropped rather than failing the connection. The
// cost of not registering is that a later `terva secret rotate --revoke` cannot
// re-seal this connector's file, which `terva secret status` reports; the cost
// of refusing the connection is a chat service that will not start.
func (p *Proxy) recordSecrets(decl connproto.SecretsDecl) {
	err := secretstore.NewRegistry(p.tervaHome).Record(secretstore.Component{
		Name:      p.manifest.Name,
		Kind:      "conn",
		Recipient: decl.Recipient,
		Paths:     decl.Paths,
		File:      filepath.Join("connectors", p.manifest.Name, "config.json"),
		LastSeen:  time.Now().UTC(),
	})
	if err != nil {
		p.warnf("connector %q: could not record its secrets declaration: %v", p.manifest.Name, err)
	}
}

// dataDir is the child's scratch directory for inbound attachments,
// announced in hello_ack. Separate from the connector's own state so
// the host's read-and-delete sweep can't eat credentials.
func (p *Proxy) dataDir() string {
	return filepath.Join(ConnectorsDir(p.tervaHome), p.manifest.Name, "data")
}

func (p *Proxy) logPath() string {
	return filepath.Join(p.tervaHome, "logs", "connector-"+p.manifest.Name+".log")
}

func (p *Proxy) Capabilities() chat.Capabilities {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.caps
}

// Connect spawns the child, handshakes, and asks it to establish its
// service session. The child's lifetime is tied to ctx: both Loop and
// Bridge pass the same ctx here and to Receive, so cancellation
// (daemon shutdown, /connect disconnect) reaps the process even when
// Receive was never reached.
func (p *Proxy) Connect(ctx context.Context) (chat.Identity, error) {
	if err := p.spawnAndConnect(ctx); err != nil {
		return chat.Identity{}, err
	}
	go func() {
		<-ctx.Done()
		p.shutdownChild()
	}()
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.identity, nil
}

// Receive delivers inbound messages until ctx ends, restarting the
// child on crashes. handle is called from this goroutine only, in
// arrival order.
func (p *Proxy) Receive(ctx context.Context, handle func(chat.Message)) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case m := <-p.inbound:
			handle(m)
		case exitErr := <-p.childExit:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := p.restart(ctx, exitErr); err != nil {
				p.shutdownChild()
				return err
			}
		}
	}
}

// restart applies the crash budget and respawns. A respawn failure is
// pushed back onto childExit so it consumes budget like a crash —
// a connector that dies during handshake must not retry forever.
func (p *Proxy) restart(ctx context.Context, exitErr error) error {
	now := time.Now()
	keep := p.restarts[:0]
	for _, t := range p.restarts {
		if now.Sub(t) < p.restartWindow {
			keep = append(keep, t)
		}
	}
	p.restarts = keep
	if len(p.restarts) >= p.restartMax {
		return fmt.Errorf("connector %q: process keeps crashing (%d restarts in %s; last error: %v; see %s)",
			p.manifest.Name, len(p.restarts), p.restartWindow, exitErr, p.logPath())
	}
	p.restarts = append(p.restarts, now)
	attempt := len(p.restarts)

	delay := p.restartDelay << (attempt - 1)
	p.warnf("connector %q: process exited (%v); restarting in %s (attempt %d/%d)",
		p.manifest.Name, exitErr, delay, attempt, p.restartMax)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(delay):
	}
	if err := p.spawnAndConnect(ctx); err != nil {
		p.warnf("connector %q: restart failed: %v", p.manifest.Name, err)
		select {
		case p.childExit <- err:
		default:
		}
	}
	return nil
}

// spawnAndConnect launches the child, runs the protocol session's
// handshake over its stdio, and completes the connect round-trip. On
// any failure the process is killed and the error explains itself at
// /connect time.
func (p *Proxy) spawnAndConnect(ctx context.Context) error {
	logPath := p.logPath()
	_ = privfs.MkdirAll(filepath.Dir(logPath))
	logFile, err := privfs.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY)
	if err != nil {
		return fmt.Errorf("open connector log: %w", err)
	}
	fmt.Fprintf(logFile, "\n[terva] starting connector %s/%s at %s\n",
		p.manifest.Name, p.manifest.Version, time.Now().Format(time.RFC3339))

	if err := privfs.MkdirAll(p.dataDir()); err != nil {
		logFile.Close()
		return err
	}

	// `<exec> <manifest args...> run` speaks the protocol; the other
	// verbs (setup/status/reset/configured) run interactively from
	// the chat.Service hooks, git-credential-helper style.
	argv := append(append([]string{}, p.manifest.Args...), "run")
	cmd := exec.Command(resolveExec(p.manifest.Exec, p.dir), argv...)
	cmd.Dir = p.dir
	// Loader/interpreter injection vars must not cross the trust
	// boundary into connector processes (see procenv).
	cmd.Env = procenv.Inherited()
	cmd.Stderr = logFile

	stdin, err := cmd.StdinPipe()
	if err != nil {
		logFile.Close()
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		logFile.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("spawn connector %q: %w", p.manifest.Name, err)
	}
	c := &child{cmd: cmd, stdin: stdin, logFile: logFile, waited: make(chan struct{})}

	fr := connproto.NewFrameReader(stdout, func(msg string) {
		p.warnf("connector %q: %s", p.manifest.Name, msg)
	})

	session := connhost.New(connhost.Config{
		Name:              p.manifest.Name,
		DataDir:           p.dataDir(),
		HostVersion:       getTervaVersion(),
		OnSecrets:         p.recordSecrets,
		Conn:              &childConn{c: c, fr: fr},
		Deliver:           p.deliverInbound,
		DeliverMembership: p.deliverMembership,
		Events: chat.ChatEventHandlers{
			Edited: func(ev chat.MessageEdited) {
				if h := p.chatEvents().Edited; h != nil {
					h(ev)
				}
			},
			Deleted: func(ev chat.MessageDeleted) {
				if h := p.chatEvents().Deleted; h != nil {
					h(ev)
				}
			},
			Reaction: func(ev chat.Reaction) {
				if h := p.chatEvents().Reaction; h != nil {
					h(ev)
				}
			},
		},
		Warn:        func(msg string) { p.warnf("%s", msg) },
		Log:         func(msg string) { fmt.Fprintf(logFile, "[terva] %s\n", msg) },
		SendTimeout: p.sendTimeout,
	})

	// The handshake has its own deadline (a child that never prints
	// hello must not hang /connect with no diagnostic); killing the
	// child on failure closes stdout, which unblocks the session's
	// reader. Before the session starts, this abort owns the reap.
	if err := session.Start(p.helloTimeout); err != nil {
		_ = cmd.Process.Kill()
		go func() { _ = cmd.Wait(); close(c.waited); logFile.Close() }()
		return fmt.Errorf("%w (see %s)", err, logPath)
	}

	p.mu.Lock()
	p.child = c
	p.session = session
	p.caps = session.Capabilities()
	p.mu.Unlock()

	// From here the session-end watcher owns the reap. Only an organic
	// exit of the CURRENT child counts as a crash: shutdownChild
	// detaches the child before stdin closes, so deliberate shutdowns
	// and failed-connect cleanups don't burn restart budget.
	go func() {
		<-session.Done()
		fmt.Fprintf(logFile, "[terva] connector session ended at %s (err=%v)\n", time.Now().Format(time.RFC3339), session.Err())
		go func() { _ = cmd.Wait(); close(c.waited); logFile.Close() }()

		p.mu.Lock()
		wasCurrent := p.child == c
		if wasCurrent {
			p.child = nil
			p.session = nil
		}
		p.mu.Unlock()
		if !wasCurrent {
			return
		}
		exitErr := session.Err()
		if exitErr == nil {
			exitErr = fmt.Errorf("stdout closed")
		}
		select {
		case p.childExit <- exitErr:
		default:
		}
	}()

	id, err := session.Connect(ctx, p.connectTimeout)
	if err != nil {
		p.shutdownChild()
		return fmt.Errorf("%w (see %s)", err, logPath)
	}
	p.mu.Lock()
	p.identity = id
	p.mu.Unlock()
	return nil
}

// SetMembershipHandler installs the admission-event consumer
// (chat.MembershipHandlerSetter). Call before Connect.
func (p *Proxy) SetMembershipHandler(fn func(chat.Membership)) {
	p.mu.Lock()
	p.membership = fn
	p.mu.Unlock()
}

func (p *Proxy) deliverMembership(mb chat.Membership) {
	p.mu.Lock()
	fn := p.membership
	p.mu.Unlock()
	if fn != nil {
		fn(mb)
	}
}

// deliverInbound buffers one message for Receive without ever blocking
// the session's dispatcher (which also delivers send results).
func (p *Proxy) deliverInbound(m chat.Message) {
	select {
	case p.inbound <- m:
	default:
		p.drops.Add(1)
		p.warnf("connector %q: inbound queue full; dropping a message", p.manifest.Name)
	}
}

// InboundDrops reports the messages lost to a full inbound queue this
// process run (chat.DropCounter).
func (p *Proxy) InboundDrops() uint64 { return p.drops.Load() }

// currentSession returns the live protocol session, or a clear error
// when no child is running.
func (p *Proxy) currentSession() (*connhost.Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.session == nil {
		return nil, fmt.Errorf("connector %q: process not running", p.manifest.Name)
	}
	return p.session, nil
}

func (p *Proxy) Send(ctx context.Context, out chat.Outgoing) error {
	s, err := p.currentSession()
	if err != nil {
		return err
	}
	return s.Send(ctx, out)
}

func (p *Proxy) SendImage(ctx context.Context, chatID, path, caption string) error {
	s, err := p.currentSession()
	if err != nil {
		return err
	}
	return s.SendImage(ctx, chatID, path, caption)
}

func (p *Proxy) SendFile(ctx context.Context, chatID, path, caption string) error {
	s, err := p.currentSession()
	if err != nil {
		return err
	}
	return s.SendFile(ctx, chatID, path, caption)
}

func (p *Proxy) Typing(ctx context.Context, chatID string) error {
	s, err := p.currentSession()
	if err != nil {
		return err
	}
	return s.Typing(chatID)
}

// StopTyping withdraws the typing indicator (chat.TypingStopper).
// Callers gate on Capabilities().TypingStop.
func (p *Proxy) StopTyping(ctx context.Context, chatID string) error {
	s, err := p.currentSession()
	if err != nil {
		return err
	}
	return s.StopTyping(ctx, chatID)
}

// Ask runs one interactive question through the wire (chat.Asker).
// Callers gate on Capabilities().Asks, as everywhere. An ask does not
// survive a child restart — the respawn fails the in-flight Ask via
// the session's death and the caller's fail-closed posture takes it
// from there.
func (p *Proxy) Ask(ctx context.Context, a chat.Ask) (chat.Answer, error) {
	s, err := p.currentSession()
	if err != nil {
		return chat.Answer{}, err
	}
	return s.Ask(ctx, a)
}

// StartThread opens a work-stream thread (chat.Threader). Callers
// gate on Capabilities().ThreadsOut.
func (p *Proxy) StartThread(ctx context.Context, chatID, fromMessageID, name string) (string, error) {
	s, err := p.currentSession()
	if err != nil {
		return "", err
	}
	return s.StartThread(ctx, chatID, fromMessageID, name)
}

// childConn carries connproto frames over the child's stdio: one JSON
// object per LF-terminated line. The session core neither knows nor
// cares that a process is on the other end. Reads go through
// connproto.FrameReader, so one over-limit frame is skipped (with a
// warning) instead of killing the stream the way the old bufio.Scanner
// did.
type childConn struct {
	c  *child
	fr *connproto.FrameReader
}

func (cc *childConn) ReadFrame() ([]byte, error) {
	return cc.fr.Read()
}

func (cc *childConn) WriteFrame(b []byte) error {
	return cc.c.writeRaw(append(b, '\n'))
}

// writeRaw sends pre-framed bytes to the child, serialized against
// concurrent writers.
func (c *child) writeRaw(frame []byte) error {
	c.stdinMu.Lock()
	defer c.stdinMu.Unlock()
	if c.stdin == nil {
		return fmt.Errorf("connector stdin closed")
	}
	_, err := c.stdin.Write(frame)
	return err
}

// shutdownChild gracefully stops the current child: shutdown frame,
// stdin close, then SIGTERM/SIGKILL on a deadline. Safe to call when
// no child is running, and safe against a concurrent session end (the
// reaping goroutine owns cmd.Wait; we only watch c.waited).
func (p *Proxy) shutdownChild() {
	p.mu.Lock()
	c := p.child
	s := p.session
	p.child = nil
	p.session = nil
	p.mu.Unlock()
	if c == nil {
		return
	}
	if s != nil {
		s.Shutdown()
	}
	c.stdinMu.Lock()
	if c.stdin != nil {
		_ = c.stdin.Close()
		c.stdin = nil
	}
	c.stdinMu.Unlock()

	select {
	case <-c.waited:
		return
	case <-time.After(2 * time.Second):
	}
	_ = c.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-c.waited:
		return
	case <-time.After(time.Second):
	}
	_ = c.cmd.Process.Kill()
	<-c.waited
}

// SetChatEventHandlers installs the stage-D inbound event consumers
// (chat.ChatEventsSetter). Call before Connect.
func (p *Proxy) SetChatEventHandlers(h chat.ChatEventHandlers) {
	p.mu.Lock()
	p.events = h
	p.mu.Unlock()
}

func (p *Proxy) chatEvents() chat.ChatEventHandlers {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.events
}

// EditMessage / React / DeleteMessage delegate the stage-D outbound
// commands (chat.Editor / chat.Reactor / chat.Deleter). Gate on the
// matching Capabilities field, as everywhere.
func (p *Proxy) EditMessage(ctx context.Context, chatID, messageID, text string) error {
	sess, err := p.currentSession()
	if err != nil {
		return err
	}
	return sess.EditMessage(ctx, chatID, messageID, text)
}

func (p *Proxy) React(ctx context.Context, chatID, messageID, key string, remove bool) error {
	sess, err := p.currentSession()
	if err != nil {
		return err
	}
	return sess.React(ctx, chatID, messageID, key, remove)
}

func (p *Proxy) DeleteMessage(ctx context.Context, chatID, messageID string) error {
	sess, err := p.currentSession()
	if err != nil {
		return err
	}
	return sess.DeleteMessage(ctx, chatID, messageID)
}
