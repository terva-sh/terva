package external

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"terva.sh/terva/packages/agent/chat"
	"terva.sh/terva/packages/agent/connproto"
	"terva.sh/terva/packages/agent/procenv"
	"terva.sh/terva/packages/provider"
)

// Tunables. Fields on the Proxy (not consts) so tests can shrink them.
const (
	defaultHelloTimeout   = 3 * time.Second  // same grace as extensions
	defaultConnectTimeout = 30 * time.Second // service dial + auth round-trip
	defaultSendTimeout    = 30 * time.Second // per send/send_image/send_file result
	defaultRestartMax     = 3                // crashes tolerated per window
	defaultRestartWindow  = 60 * time.Second
	defaultRestartDelay   = time.Second // base backoff, doubles per attempt
)

// inboundBuffer bounds how many normalized messages can queue between
// the read loop and Receive. The read loop must never block on it —
// it also delivers send results — so overflow drops with a warn.
const inboundBuffer = 256

// Proxy adapts one external connector executable to chat.Connector.
// It spawns `<exec> <args...> run`, completes the hello handshake
// (with negotiated protocol versioning), and translates between
// connproto frames and the chat contract. Crashes are loud: the
// child is restarted with backoff and every attempt is surfaced via
// Warn; past RestartMax crashes in RestartWindow, Receive returns an
// error — permanently broken, no silent deregistration.
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

	mu             sync.Mutex
	child          *child
	caps           chat.Capabilities
	identity       chat.Identity
	pending        map[string]chan connproto.ResultFromConn
	connectPending chan connectOutcome

	inbound   chan chat.Message
	childExit chan error
	restarts  []time.Time
}

type connectOutcome struct {
	identity chat.Identity
	err      error
}

// child is one spawned connector process and its pipes.
type child struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdinMu sync.Mutex // serializes frame writes, the extension pattern
	logFile *os.File
	// waited closes once cmd.Wait returned. Wait is called exactly
	// once, by the goroutine the read loop starts when stdout closes
	// (calling it earlier would close the pipe under the scanner).
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
		pending:        map[string]chan connproto.ResultFromConn{},
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

// spawnAndConnect launches the child, runs hello/hello_ack with
// version negotiation, starts the read loop, and completes the
// connect round-trip. On any failure the process is killed and the
// error explains itself at /connect time.
func (p *Proxy) spawnAndConnect(ctx context.Context) error {
	logPath := p.logPath()
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open connector log: %w", err)
	}
	fmt.Fprintf(logFile, "\n[terva] starting connector %s/%s at %s\n",
		p.manifest.Name, p.manifest.Version, time.Now().Format(time.RFC3339))

	if err := os.MkdirAll(p.dataDir(), 0o755); err != nil {
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

	abort := func(err error) error {
		_ = cmd.Process.Kill()
		go func() { _ = cmd.Wait(); close(c.waited); logFile.Close() }()
		return err
	}

	// Hello handshake with a deadline, the extension pattern: a child
	// that never prints hello must not hang /connect with no
	// diagnostic. The blocking Scan races a timer; killing the child
	// closes stdout, so the scan goroutine always finishes.
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	scanned := make(chan bool, 1)
	go func() { scanned <- scanner.Scan() }()
	var ok bool
	select {
	case ok = <-scanned:
	case <-time.After(p.helloTimeout):
		return abort(fmt.Errorf("connector %q sent no hello within %s (see %s)", p.manifest.Name, p.helloTimeout, logPath))
	case <-ctx.Done():
		return abort(ctx.Err())
	}
	if !ok {
		return abort(fmt.Errorf("connector %q exited before hello: %v (see %s)", p.manifest.Name, scanner.Err(), logPath))
	}
	var hello connproto.HelloFromConn
	if err := json.Unmarshal(scanner.Bytes(), &hello); err != nil {
		return abort(fmt.Errorf("connector %q: malformed hello: %v", p.manifest.Name, err))
	}
	if hello.Type != "hello" {
		return abort(fmt.Errorf("connector %q: first frame must be hello, got %q", p.manifest.Name, hello.Type))
	}
	if hello.Name != "" && hello.Name != p.manifest.Name {
		// Trust the manifest (it names the state dirs); just leave a trace.
		fmt.Fprintf(logFile, "[terva] hello name %q != manifest name %q; using the manifest's\n", hello.Name, p.manifest.Name)
	}
	// Negotiated versioning, not announce-only: refuse a mismatch
	// with a clear error instead of failing on a weird frame later.
	if hello.ProtocolMin > connproto.ProtocolVersion || hello.ProtocolMax < connproto.ProtocolVersion {
		return abort(fmt.Errorf("connector %q speaks protocol %d..%d; this terva speaks %d — upgrade %s",
			p.manifest.Name, hello.ProtocolMin, hello.ProtocolMax, connproto.ProtocolVersion,
			map[bool]string{true: "terva", false: "the connector"}[hello.ProtocolMin > connproto.ProtocolVersion]))
	}

	hostVersion := getTervaVersion()
	if err := c.writeFrame(connproto.HelloAckFromHost{
		Type:         "hello_ack",
		Protocol:     connproto.ProtocolVersion,
		ZotVersion:   hostVersion, // rename:keep — frozen wire field
		TervaVersion: hostVersion,
		DataDir:      p.dataDir(),
	}); err != nil {
		return abort(fmt.Errorf("send hello_ack: %w", err))
	}

	connectCh := make(chan connectOutcome, 1)
	p.mu.Lock()
	p.child = c
	p.caps = chat.Capabilities{
		MaxTextLen:    hello.Capabilities.MaxTextLen,
		TypingRefresh: time.Duration(hello.Capabilities.TypingRefreshMS) * time.Millisecond,
		SendsImages:   hello.Capabilities.SendsImages,
		SendsFiles:    hello.Capabilities.SendsFiles,
	}
	p.connectPending = connectCh
	p.mu.Unlock()

	go p.readLoop(c, scanner)

	if err := c.writeFrame(connproto.ConnectFromHost{Type: "connect"}); err != nil {
		p.shutdownChild()
		return fmt.Errorf("send connect: %w", err)
	}
	select {
	case out := <-connectCh:
		if out.err != nil {
			p.shutdownChild()
			return fmt.Errorf("connector %q: connect: %w", p.manifest.Name, out.err)
		}
		p.mu.Lock()
		p.identity = out.identity
		p.connectPending = nil
		p.mu.Unlock()
		return nil
	case <-time.After(p.connectTimeout):
		p.shutdownChild()
		return fmt.Errorf("connector %q: no connected/connect_error within %s (see %s)", p.manifest.Name, p.connectTimeout, logPath)
	case <-ctx.Done():
		p.shutdownChild()
		return ctx.Err()
	}
}

// readLoop processes every frame after hello until stdout closes,
// then reaps the process and reports the exit on childExit.
func (p *Proxy) readLoop(c *child, scanner *bufio.Scanner) {
	for scanner.Scan() {
		line := scanner.Bytes()
		var frame connproto.Frame
		if err := json.Unmarshal(line, &frame); err != nil {
			fmt.Fprintf(c.logFile, "[terva] malformed frame from connector: %v\n", err)
			continue
		}
		switch frame.Type {
		case "connected":
			var conn connproto.ConnectedFromConn
			if err := json.Unmarshal(line, &conn); err == nil {
				p.deliverConnect(connectOutcome{identity: chat.Identity{ID: conn.ID, Username: conn.Username}})
			}
		case "connect_error":
			var ce connproto.ConnectErrorFromConn
			if err := json.Unmarshal(line, &ce); err == nil {
				p.deliverConnect(connectOutcome{err: errors.New(ce.Error)})
			}
		case "message":
			var msg connproto.MessageFromConn
			if err := json.Unmarshal(line, &msg); err != nil {
				fmt.Fprintf(c.logFile, "[terva] bad message frame: %v\n", err)
				continue
			}
			m := chat.Message{
				ChatID:   msg.ChatID,
				UserID:   msg.UserID,
				Username: msg.Username,
				ReplyTo:  msg.ReplyTo,
				Text:     msg.Text,
				Images:   p.ingestAttachments(msg.Attachments),
			}
			select {
			case p.inbound <- m:
			default:
				p.warnf("connector %q: inbound queue full; dropping a message", p.manifest.Name)
			}
		case "result":
			var res connproto.ResultFromConn
			if err := json.Unmarshal(line, &res); err == nil {
				p.mu.Lock()
				ch, ok := p.pending[res.ID]
				if ok {
					delete(p.pending, res.ID)
				}
				p.mu.Unlock()
				if ok {
					ch <- res // buffered(1); never blocks
				}
			}
		case "warn":
			var w connproto.WarnFromConn
			if err := json.Unmarshal(line, &w); err == nil {
				p.warnf("connector %q: %s", p.manifest.Name, w.Message)
			}
		case "hello":
			// Duplicate hello after handshake; harmless, note it.
			fmt.Fprintf(c.logFile, "[terva] unexpected extra hello frame\n")
		default:
			fmt.Fprintf(c.logFile, "[terva] unknown frame type %q\n", frame.Type)
		}
	}

	scanErr := scanner.Err()
	fmt.Fprintf(c.logFile, "[terva] connector read loop ended at %s (err=%v)\n", time.Now().Format(time.RFC3339), scanErr)

	// Reap. Wait must come after all pipe reads (it closes them).
	go func() { _ = c.cmd.Wait(); close(c.waited); c.logFile.Close() }()

	// Only an organic exit of the CURRENT child counts as a crash.
	// shutdownChild detaches the child before stdin closes, so
	// deliberate shutdowns and failed-connect cleanups don't burn
	// restart budget.
	p.mu.Lock()
	wasCurrent := p.child == c
	if wasCurrent {
		p.child = nil
	}
	p.mu.Unlock()

	// Unblock a connect waiter; its child just died.
	p.deliverConnect(connectOutcome{err: errors.New("connector process exited during connect")})

	if !wasCurrent {
		return
	}
	exitErr := scanErr
	if exitErr == nil {
		exitErr = errors.New("stdout closed")
	}
	select {
	case p.childExit <- exitErr:
	default:
	}
}

// deliverConnect resolves the in-flight connect round-trip, if any.
func (p *Proxy) deliverConnect(out connectOutcome) {
	p.mu.Lock()
	ch := p.connectPending
	p.connectPending = nil
	p.mu.Unlock()
	if ch != nil {
		ch <- out // buffered(1)
	}
}

// ingestAttachments turns by-path attachments into in-memory image
// blocks: read, then delete. Paths must resolve inside the child's
// data dir — a connector must not be able to point the host at
// arbitrary files (and certainly not get them deleted).
func (p *Proxy) ingestAttachments(atts []connproto.Attachment) []provider.ImageBlock {
	if len(atts) == 0 {
		return nil
	}
	dataReal, err := filepath.EvalSymlinks(p.dataDir())
	if err != nil {
		p.warnf("connector %q: resolve data dir: %v", p.manifest.Name, err)
		return nil
	}
	var images []provider.ImageBlock
	for _, a := range atts {
		real, err := filepath.EvalSymlinks(a.Path)
		if err != nil {
			p.warnf("connector %q: attachment %s: %v", p.manifest.Name, a.Path, err)
			continue
		}
		rel, err := filepath.Rel(dataReal, real)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			p.warnf("connector %q: attachment outside data dir ignored: %s", p.manifest.Name, a.Path)
			continue
		}
		data, err := os.ReadFile(real)
		if err != nil {
			p.warnf("connector %q: read attachment: %v", p.manifest.Name, err)
			continue
		}
		_ = os.Remove(real)
		images = append(images, provider.ImageBlock{MimeType: a.MimeType, Data: data})
	}
	return images
}

func (p *Proxy) Send(ctx context.Context, out chat.Outgoing) error {
	id := newFrameID()
	return p.roundTrip(ctx, id, connproto.SendFromHost{
		Type: "send", ID: id, ChatID: out.ChatID, ReplyTo: out.ReplyTo, Text: out.Text,
	})
}

func (p *Proxy) SendImage(ctx context.Context, chatID, path, caption string) error {
	id := newFrameID()
	return p.roundTrip(ctx, id, connproto.SendImageFromHost{
		Type: "send_image", ID: id, ChatID: chatID, Path: path, Caption: caption,
	})
}

func (p *Proxy) SendFile(ctx context.Context, chatID, path, caption string) error {
	id := newFrameID()
	return p.roundTrip(ctx, id, connproto.SendFileFromHost{
		Type: "send_file", ID: id, ChatID: chatID, Path: path, Caption: caption,
	})
}

func (p *Proxy) Typing(ctx context.Context, chatID string) error {
	return p.writeFrame(connproto.TypingFromHost{Type: "typing", ChatID: chatID})
}

// roundTrip writes one command frame and waits for its result. The
// loop sends replies under context.Background(), so the send timeout
// is what protects against a wedged child.
func (p *Proxy) roundTrip(ctx context.Context, id string, frame any) error {
	ch := make(chan connproto.ResultFromConn, 1)
	p.mu.Lock()
	p.pending[id] = ch
	p.mu.Unlock()
	cleanup := func() {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
	}
	if err := p.writeFrame(frame); err != nil {
		cleanup()
		return err
	}
	select {
	case res := <-ch:
		if res.Error != "" {
			return errors.New(res.Error)
		}
		return nil
	case <-time.After(p.sendTimeout):
		cleanup()
		return fmt.Errorf("connector %q: no result within %s", p.manifest.Name, p.sendTimeout)
	case <-ctx.Done():
		cleanup()
		return ctx.Err()
	}
}

// writeFrame sends one frame to the current child, if any.
func (p *Proxy) writeFrame(v any) error {
	p.mu.Lock()
	c := p.child
	p.mu.Unlock()
	if c == nil {
		return fmt.Errorf("connector %q: process not running", p.manifest.Name)
	}
	return c.writeFrame(v)
}

func (c *child) writeFrame(v any) error {
	frame, err := connproto.Encode(v)
	if err != nil {
		return err
	}
	c.stdinMu.Lock()
	defer c.stdinMu.Unlock()
	if c.stdin == nil {
		return errors.New("connector stdin closed")
	}
	_, err = c.stdin.Write(frame)
	return err
}

// shutdownChild gracefully stops the current child: shutdown frame,
// stdin close, then SIGTERM/SIGKILL on a deadline. Safe to call when
// no child is running, and safe against a concurrent read-loop exit
// (the reaping goroutine owns cmd.Wait; we only watch c.waited).
func (p *Proxy) shutdownChild() {
	p.mu.Lock()
	c := p.child
	p.child = nil
	p.mu.Unlock()
	if c == nil {
		return
	}
	_ = c.writeFrame(connproto.ShutdownFromHost{Type: "shutdown"})
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

// frameSeq + newFrameID mint process-unique correlation ids, the
// extension manager's scheme: timestamp for log readability, counter
// for uniqueness within a burst.
var frameSeq atomic.Uint64

func newFrameID() string {
	ts := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	return ts + "-" + strconv.FormatUint(frameSeq.Add(1), 10)
}
