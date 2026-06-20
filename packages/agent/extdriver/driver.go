package extdriver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/agent/procenv"
)

// HostHooks is the small interface the driver calls back into the
// running TUI through. Decouples the wire from the interactive host:
// spontaneous frames (notify / submit / display / panels / status) are
// routed here, and a conformance run supplies a no-op implementation.
type HostHooks interface {
	// Notify pushes an ext-originated status message into the chat.
	// Level is one of "info", "warn", "error", "success".
	Notify(extName, level, message string)

	// Submit feeds text as if the user had typed and pressed enter,
	// running it through the agent loop.
	Submit(text string)

	// SubmitSlash runs text as a slash command in the TUI as if the
	// user had typed it (text must start with '/'). Unlike Submit it
	// does NOT run text through the model. Wired to the spontaneous
	// submit_slash frame from extensions; ignored when the host is
	// not interactive.
	SubmitSlash(text string)

	// Insert places text at the cursor in the editor.
	Insert(text string)

	// Display appends a one-shot styled note to the chat without
	// invoking the model and without writing to the transcript.
	Display(extName, text string)

	// ClearNotes removes any notes previously pushed by extName via
	// Notify/Display so transient status lines do not stack forever.
	ClearNotes(extName string)

	OpenPanel(extName string, spec extproto.PanelSpec)
	UpdatePanel(extName, panelID, title string, lines []string, footer string)
	ClosePanel(extName, panelID string)

	// RefreshStatus asks the host to redraw after an extension changed
	// its status-bar segment (status_segment), so the update shows even
	// when nothing else triggers a frame. No-op outside interactive mode.
	RefreshStatus()

	// RefreshContext asks the host to rebuild the cached system prompt
	// after an extension replaced its static context block
	// (refresh_context, protocol 3), so the new block takes effect on the
	// next turn. No-op where the prompt is fixed for the run
	// (print/json/rpc build it once); the live interactive/ACP session
	// re-folds it.
	RefreshContext()
}

// Driver owns every extension subprocess's wire for the lifetime of
// terva: spawning, the hello handshake, the read/write loops, frame
// correlation, and event fan-out. The host integration layer
// (extensions.Manager) embeds a *Driver and adds discovery, trust, and
// the agent-registry shell on top.
type Driver struct {
	tervaHome    string
	cwd          string
	tervaVersion string
	provider     string
	model        string
	hooks        HostHooks

	mu  sync.RWMutex
	ext map[string]*Extension // keyed by manifest name

	// commandIndex maps a slash-command name (without the leading /)
	// to the extension that registered it. First-come-first-served:
	// later registrations of the same command are dropped with a
	// warning.
	commandIndex map[string]*Extension

	// toolIndex maps an extension-defined tool name to its owning
	// extension. Same first-come-first-served rule as commandIndex.
	toolIndex map[string]*Extension

	// contextDisabled is the set of extension names whose MODEL context
	// contributions (static + cards) the host ignores — their tools,
	// commands, and status segments still work. Set once from the
	// resolved config (user ∪ project) via SetContextDisabled; replaced
	// wholesale, never mutated, so a captured reference stays immutable.
	contextDisabled map[string]bool

	// onMalformedFrame, if set, observes every stdout line the read loop
	// can't dispatch (not valid JSON, oversized, or an unrecognised
	// type). Optional and additive: the live host leaves it nil and the
	// frames are logged as ever; a conformance harness sets it to make
	// the stdout-purity check a first-class assertion. Guarded by mu.
	onMalformedFrame func(extName, raw, reason string)

	// hostToolDispatch, if set, runs a host tool on behalf of an
	// extension's host_tool_call (protocol 3). The driver is
	// dependency-light and owns neither the tool registry nor the
	// permission gate, so the agent layer injects this; nil means host
	// tool calls are unsupported and every host_tool_call is answered
	// with an error. Guarded by mu.
	hostToolDispatch HostToolDispatcher

	// sessionReader, if set, serves an extension's list_sessions /
	// read_session (protocol 3). The driver does not own session
	// storage, so the agent layer injects this; nil means session reads
	// are unsupported (an empty list / not-found). Guarded by mu.
	sessionReader SessionReader
}

// SessionReader gives an extension read-only, project-scoped access to
// past session transcripts (protocol 3 list_sessions / read_session).
// Injected by the host because the dependency-light driver does not own
// session storage. extName is the calling extension (the host may scope
// what it returns); ListSessions honors a project_id filter, ReadSession
// returns found=false for an unknown or out-of-project id.
type SessionReader interface {
	ListSessions(extName, projectID string) []extproto.SessionInfo
	ReadSession(extName, sessionID string) (msgs []extproto.SessionMessage, found bool)
}

// HostToolDispatcher runs a host tool for an extension's host_tool_call
// and returns the result content. Injected by the host because the
// dependency-light driver cannot import the tool registry or permission
// policy. extName is the calling extension (for trust / authority
// decisions); silent is the extension's hint to not surface the call in
// the UI/transcript. Implementations must run the tool under the host's
// normal permission gate — the extension gets reach, not authority — and
// should bound their own runtime (the driver passes a background
// context).
type HostToolDispatcher func(ctx context.Context, extName, toolName string, args json.RawMessage, silent bool) (content []extproto.ContentBlock, isError bool)

// New constructs an empty Driver. The host integration layer calls
// Load for each discovered extension to populate it.
func New(tervaHome, cwd, tervaVersion, provider, model string, hooks HostHooks) *Driver {
	return &Driver{
		tervaHome:    tervaHome,
		cwd:          cwd,
		tervaVersion: tervaVersion,
		provider:     provider,
		model:        model,
		hooks:        hooks,
		ext:          map[string]*Extension{},
		commandIndex: map[string]*Extension{},
		toolIndex:    map[string]*Extension{},
	}
}

// Load registers a single extension whose manifest the host has already
// read, validated, and cleared against policy (enabled / not
// load-disabled / trust). If the manifest declares an exec, Load spawns
// the subprocess and completes the hello handshake; a theme-only
// extension (no exec) is registered as immediately ready. A name that is
// already loaded is a no-op (the higher-priority discovery location, or
// the explicit --ext path, already won).
func (d *Driver) Load(ctx context.Context, dir string, mf Manifest) error {
	ext := newExtension(mf, dir)

	// Claim the name atomically BEFORE spawning. The dup-check and the
	// insert must happen under the same lock: the old split (RLock check,
	// spawn, then Lock insert) let two concurrent loads of the same name
	// both pass the check and both spawn — the second overwrote the map
	// and orphaned the first subprocess (left running, untracked, so its
	// eventual exit was mis-reported as "exited unexpectedly"). A
	// claimed-but-not-yet-spawned entry simply reads as not-ready until
	// spawn fills in ext.cmd/commands/tools below.
	d.mu.Lock()
	if _, dup := d.ext[mf.Name]; dup {
		d.mu.Unlock()
		// Project-local / explicit copy already won; ignore this one.
		return nil
	}
	d.ext[mf.Name] = ext
	d.mu.Unlock()

	if mf.Exec == "" {
		ext.readyOnce.Do(func() { close(ext.readyCh) })
		return nil
	}

	if err := d.spawn(ctx, ext); err != nil {
		// spawn started no read loop, so nothing else will clear the
		// claim — roll it back (only if still ours; a concurrent
		// Reset/Stop may already have removed or replaced it).
		d.mu.Lock()
		if d.ext[mf.Name] == ext {
			delete(d.ext, mf.Name)
		}
		d.mu.Unlock()
		return err
	}

	// A concurrent Reset/Stop could have dropped our claim while we were
	// spawning (it saw a nil cmd and skipped the teardown). If so, the
	// process we just started is an orphan — stop it cleanly rather than
	// leak it.
	d.mu.RLock()
	stillOurs := d.ext[mf.Name] == ext
	d.mu.RUnlock()
	if !stillOurs {
		stopExtensions([]*Extension{ext}, time.Second)
	}
	return nil
}

// TervaHome returns the configured $TERVA_HOME root, so the host
// integration layer can derive the global extensions / logs dirs
// without duplicating the value.
func (d *Driver) TervaHome() string { return d.tervaHome }

// SetOnMalformedFrame registers an optional observer for stdout frames
// the read loop can't dispatch: a line that isn't valid JSON, a frame
// over the size cap, or a frame with an unrecognised type. It is
// additive — the per-extension log still records the frame, so the live
// host's behaviour is unchanged when no observer is set. A conformance
// harness sets it to turn the stdout-purity check (every line must be a
// well-formed typed frame) from a log-scrape into a real assertion. raw
// is the offending line (empty for an oversized frame, whose bytes are
// intentionally discarded); reason says why it was rejected. Set before
// loading extensions.
func (d *Driver) SetOnMalformedFrame(fn func(extName, raw, reason string)) {
	d.mu.Lock()
	d.onMalformedFrame = fn
	d.mu.Unlock()
}

// SetHostToolDispatcher installs the handler that runs host tools for
// extension host_tool_call frames (protocol 3). nil (the default) leaves
// host tool calls unsupported. Set before loading extensions.
func (d *Driver) SetHostToolDispatcher(fn HostToolDispatcher) {
	d.mu.Lock()
	d.hostToolDispatch = fn
	d.mu.Unlock()
}

// SetSessionReader installs the reader that serves extension
// list_sessions / read_session frames (protocol 3). nil (the default)
// leaves session reads unsupported. Set before loading extensions.
func (d *Driver) SetSessionReader(r SessionReader) {
	d.mu.Lock()
	d.sessionReader = r
	d.mu.Unlock()
}

// reportMalformed invokes the optional malformed-frame observer if one
// is registered. Called from the read loop in addition to the
// per-extension log.
func (d *Driver) reportMalformed(extName, raw, reason string) {
	d.mu.RLock()
	fn := d.onMalformedFrame
	d.mu.RUnlock()
	if fn != nil {
		fn(extName, raw, reason)
	}
}

// extDataDir is an extension's writable, install-independent data
// directory, under $TERVA_HOME/ext-data/<name>. Keyed by manifest name
// so it's stable no matter which discovery root the extension loaded
// from. Returns "" when there's no terva home, signalling the caller to
// fall back to the install dir.
func (d *Driver) extDataDir(name string) string {
	if d.tervaHome == "" || name == "" {
		return ""
	}
	return filepath.Join(d.tervaHome, "ext-data", name)
}

// Reset tears down every currently loaded extension and clears the
// driver's maps, returning how many processes were stopped. The host's
// Reload uses it to wipe the slate before respawning: clearing the maps
// before the graceful stop means pending InvokeTool / Invoke calls on
// the old processes get a clean error as their stdin closes.
func (d *Driver) Reset(gracePeriod time.Duration) int {
	d.mu.Lock()
	old := d.ext
	d.ext = map[string]*Extension{}
	d.commandIndex = map[string]*Extension{}
	d.toolIndex = map[string]*Extension{}
	d.mu.Unlock()

	oldExts := make([]*Extension, 0, len(old))
	for _, ext := range old {
		oldExts = append(oldExts, ext)
	}
	stopExtensions(oldExts, gracePeriod)
	return len(oldExts)
}

// StopByName gracefully stops the single loaded extension `name` and
// drops its commands/tools from the indexes, reporting whether it was
// running. The teardown is marked deliberate (via stopExtensions), so
// the read loop's exit is NOT surfaced to the user as a crash — this is
// what lets a host toggle one extension off without the "exited
// unexpectedly" notice. No-op (false) when `name` isn't loaded.
func (d *Driver) StopByName(name string, gracePeriod time.Duration) bool {
	d.mu.Lock()
	ext, ok := d.ext[name]
	if ok {
		delete(d.ext, name)
		for n, owner := range d.commandIndex {
			if owner == ext {
				delete(d.commandIndex, n)
			}
		}
		for n, owner := range d.toolIndex {
			if owner == ext {
				delete(d.toolIndex, n)
			}
		}
	}
	d.mu.Unlock()
	if !ok {
		return false
	}
	stopExtensions([]*Extension{ext}, gracePeriod)
	return true
}

// Count returns how many extensions are currently loaded.
func (d *Driver) Count() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.ext)
}

// ReadyCount returns how many loaded extensions have signalled ready.
func (d *Driver) ReadyCount() int {
	d.mu.RLock()
	exts := make([]*Extension, 0, len(d.ext))
	for _, e := range d.ext {
		exts = append(exts, e)
	}
	d.mu.RUnlock()
	n := 0
	for _, ext := range exts {
		if ext.Ready() {
			n++
		}
	}
	return n
}

// Extensions returns a snapshot of every loaded extension, sorted by
// manifest name so the host integration layer (theme options, status
// line) iterates deterministically.
func (d *Driver) Extensions() []*Extension {
	d.mu.RLock()
	exts := make([]*Extension, 0, len(d.ext))
	for _, e := range d.ext {
		exts = append(exts, e)
	}
	d.mu.RUnlock()
	sort.Slice(exts, func(i, j int) bool { return exts[i].Manifest.Name < exts[j].Manifest.Name })
	return exts
}

// All returns every extension currently tracked, enabled or not.
// Used by `terva ext list`.
func (d *Driver) All() []*Extension {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]*Extension, 0, len(d.ext))
	for _, e := range d.ext {
		out = append(out, e)
	}
	return out
}

// WaitForReady blocks until every loaded extension has signalled
// ReadyFromExt, or the grace period expires for the slowest one.
//
// Waits run in parallel: total time is max(per-extension wait), not
// sum. Without this, a single slow extension (e.g. `npx tsx` cold)
// would gate every other extension's wait too and terva startup would
// scale linearly with the number of slow runtimes installed.
//
// Call after loading and before relying on tool registrations.
func (d *Driver) WaitForReady(grace time.Duration) {
	d.mu.RLock()
	exts := make([]*Extension, 0, len(d.ext))
	for _, e := range d.ext {
		exts = append(exts, e)
	}
	d.mu.RUnlock()

	deadline := time.After(grace)
	var wg sync.WaitGroup
	for _, ext := range exts {
		wg.Add(1)
		go func(ext *Extension) {
			defer wg.Done()
			select {
			case <-ext.readyCh:
			case <-deadline:
				fmt.Fprintf(ext.logFile, "[terva] timed out waiting for ready frame; proceeding\n")
				ext.readyOnce.Do(func() { close(ext.readyCh) })
			}
		}(ext)
	}
	wg.Wait()
}

// spawn launches the subprocess, hooks up pipes, logs stderr, and
// runs the synchronous portion of the hello handshake. Asynchronous
// frames are processed in a goroutine started here.
func (d *Driver) spawn(ctx context.Context, ext *Extension) error {
	logsDir := filepath.Join(d.tervaHome, "logs")
	_ = os.MkdirAll(logsDir, 0o755)
	logPath := filepath.Join(logsDir, "ext-"+ext.Manifest.Name+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	ext.LogPath = logPath
	ext.logFile = logFile
	fmt.Fprintf(logFile, "\n[terva] starting %s/%s at %s\n", ext.Manifest.Name, ext.Manifest.Version, time.Now().Format(time.RFC3339))

	// Exec resolution rules:
	//   - absolute path:                 used as-is.
	//   - starts with "." (./ or ../):  resolved relative to ext.Dir.
	//   - bare name (no path separator): looked up via $PATH so
	//                                    "node", "npx", "python3",
	//                                    "tsx" etc. work without
	//                                    forcing absolute paths.
	//   - other relative form (foo/bar): resolved relative to ext.Dir.
	execPath := ext.Manifest.Exec
	switch {
	case filepath.IsAbs(execPath):
		// keep
	case strings.HasPrefix(execPath, "."+string(filepath.Separator)) ||
		strings.HasPrefix(execPath, ".."+string(filepath.Separator)) ||
		execPath == "." || execPath == "..":
		execPath = filepath.Join(ext.Dir, execPath)
	case strings.ContainsRune(execPath, filepath.Separator):
		execPath = filepath.Join(ext.Dir, execPath)
	default:
		// bare name: leave as-is for exec.LookPath via exec.Command.
	}
	cmd := exec.CommandContext(ctx, execPath, ext.Manifest.Args...)
	cmd.Dir = ext.Dir
	// Loader/interpreter injection vars must not cross the trust
	// boundary into extension processes (see procenv).
	cmd.Env = procenv.Inherited()
	cmd.Stderr = logFile

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn: %w", err)
	}
	ext.cmd = cmd
	ext.stdin = stdin
	ext.stdout = stdout

	// Hello handshake. Read the extension's HelloFromExt with a deadline
	// so a binary that never prints hello (a daemon, a REPL, a typo'd
	// path that opens and waits) can't brick terva startup: Discover()
	// blocks on every spawn returning, so an unbounded Scan() here would
	// hang the whole launch with no diagnostic.
	//
	// The blocking scanner.Scan() runs on its own goroutine; we race it
	// against a timer. On timeout we kill the subprocess, which unblocks
	// the goroutine's Scan() (stdout closes) so it can return and the
	// channel send is consumed — no leaked reader goroutine.
	reader := bufio.NewReaderSize(stdout, 64*1024)
	type helloRead struct {
		line    []byte
		tooLong bool
		err     error
	}
	scanned := make(chan helloRead, 1)
	go func() {
		line, tooLong, err := extproto.ReadFrame(reader)
		scanned <- helloRead{line, tooLong, err}
	}()

	var got helloRead
	select {
	case got = <-scanned:
	case <-time.After(HelloTimeout):
		_ = cmd.Process.Kill()
		<-scanned // ReadFrame returns once the killed process's stdout closes.
		fmt.Fprintf(logFile, "[terva] extension %s failed to handshake within %s; killed and skipped\n", ext.Manifest.Name, HelloTimeout)
		return fmt.Errorf("extension %s failed to send hello within %s", ext.Manifest.Name, HelloTimeout)
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-scanned
		return ctx.Err()
	}
	if got.tooLong {
		_ = cmd.Process.Kill()
		return fmt.Errorf("extension %s hello frame exceeded %d bytes", ext.Manifest.Name, extproto.MaxFrameBytes)
	}
	if got.err != nil && len(got.line) == 0 {
		return fmt.Errorf("extension exited before hello: %w", got.err)
	}
	var hello extproto.HelloFromExt
	if err := json.Unmarshal(got.line, &hello); err != nil {
		return fmt.Errorf("parse hello: %w", err)
	}
	if hello.Type != "hello" || hello.Name == "" {
		return fmt.Errorf("first frame must be hello (got %q)", hello.Type)
	}
	// Min-protocol negotiation: an extension that needs wire features
	// this host doesn't speak declares the floor in its hello. Refuse
	// it cleanly here rather than letting it run against a wire it
	// half-understands. This is what lets a terva-only extension fail
	// with a clear message on an upstream (pre-fork) or older terva
	// of misbehaving silently.
	if hello.MinProtocol > extproto.ProtocolVersion {
		// scanned was already consumed by the success select above, so
		// don't receive from it again; just kill the subprocess.
		_ = cmd.Process.Kill()
		fmt.Fprintf(logFile, "[terva] extension %s requires protocol >= %d; host speaks %d; skipped\n",
			ext.Manifest.Name, hello.MinProtocol, extproto.ProtocolVersion)
		return fmt.Errorf("extension %s requires protocol version %d but this host speaks %d; upgrade terva",
			ext.Manifest.Name, hello.MinProtocol, extproto.ProtocolVersion)
	}
	// Trust the manifest's name; ignore mismatch from the hello.
	ext.helloAck = true

	// Start the ordered writer before the first host->extension write
	// (hello_ack). From here on every frame to this extension goes
	// through the outbox so delivery order matches enqueue order.
	ext.outbox = make(chan []byte, outboxCapacity)
	ext.quit = make(chan struct{})
	ext.writerDone = make(chan struct{})
	go ext.writeLoop()

	hostVersion := d.tervaVersion
	// DataDir is the extension's writable state directory, kept separate
	// from ext.Dir (the read-only install dir) so a read-only / system
	// install still works and code never mixes with data. Keyed by
	// manifest name so it's stable across install location. Falls back to
	// the install dir when there's no terva home or the dir can't be
	// created — preserving the old colocated behavior rather than failing.
	dataDir := ext.Dir
	if dd := d.extDataDir(ext.Manifest.Name); dd != "" {
		if err := os.MkdirAll(dd, 0o755); err == nil {
			dataDir = dd
		}
	}
	if err := ext.writeFrame(extproto.HelloAckFromHost{
		Type:            "hello_ack",
		ProtocolVersion: extproto.ProtocolVersion,
		ZotVersion:      hostVersion, // rename:keep — frozen wire field
		TervaVersion:    hostVersion,
		Provider:        d.provider,
		Model:           d.model,
		CWD:             d.cwd,
		ExtensionDir:    ext.Dir,
		DataDir:         dataDir,
		SupportedEvents: extproto.KnownEvents,
	}); err != nil {
		// Tear down the writer goroutine we just started so it doesn't
		// leak on this failed spawn.
		ext.quitOnce.Do(func() { close(ext.quit) })
		_ = cmd.Process.Kill()
		return fmt.Errorf("send hello_ack: %w", err)
	}

	// Spin up the read loop now that the handshake is done.
	go d.readLoop(ext, reader)

	// Compatibility shim: extensions built against the phase-1 SDK
	// don't send a ready frame. Watch the read loop's frame arrival
	// rate; if nothing's arrived for readyIdleWindow we treat the
	// extension as ready so WaitForReady doesn't burn the full grace
	// on every startup. Newer extensions still trigger the explicit
	// path on their own ready frame.
	go d.assumeReadyAfterIdle(ext)

	return nil
}

// readyIdleWindow is how long the manager waits for a frame after
// hello before assuming an extension that doesn't send `ready` is
// nevertheless ready. 250ms is enough for any well-behaved native
// binary to flush its register frames; slow runtimes (npx tsx) flush
// even faster once they've started, so this rarely affects them.
const readyIdleWindow = 250 * time.Millisecond

// HelloTimeout bounds how long spawn() waits for an extension's hello
// frame before giving up on it. Kept consistent with the 3s ready
// grace terva uses elsewhere (see WaitForReady / Reload) so a slow but
// legitimate runtime cold-start still has room to print hello. Past
// this the subprocess is killed and the extension is skipped without
// blocking the rest of Discover.
const HelloTimeout = 3 * time.Second

func (d *Driver) assumeReadyAfterIdle(ext *Extension) {
	ext.mu.Lock()
	last := ext.lastFrameTime
	ext.mu.Unlock()
	for {
		select {
		case <-ext.readyCh:
			return
		case <-time.After(readyIdleWindow):
		}
		ext.mu.Lock()
		current := ext.lastFrameTime
		ext.mu.Unlock()
		if current.Equal(last) {
			// No new frame in the idle window. Treat as ready.
			ext.readyOnce.Do(func() {
				fmt.Fprintf(ext.logFile, "[terva] no ready frame; auto-readying after idle (legacy SDK?)\n")
				close(ext.readyCh)
			})
			return
		}
		last = current
	}
}

// Per-extension registration caps. Extensions run as trusted local code,
// so these are defense-in-depth against a buggy (e.g. looping) extension
// plus prompt/memory hygiene — not a security boundary. Generous: a real
// extension registers a handful of each.
const (
	maxExtTools     = 64
	maxExtCommands  = 64
	maxExtEventSubs = 64 // bounds UNKNOWN event names; known events are always kept
)

// registerTool records a tool registration under the cap, indexing the
// name on first registration. Drops (and logs) past maxExtTools so a
// runaway extension can't bloat the model's tool schema or the registry.
func (d *Driver) registerTool(ext *Extension, rt extproto.RegisterToolFromExt) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(ext.tools) >= maxExtTools {
		fmt.Fprintf(ext.logFile, "[terva] dropping tool %q: extension at the %d-tool cap\n", rt.Name, maxExtTools)
		return
	}
	ext.tools = append(ext.tools, rt)
	if _, exists := d.toolIndex[rt.Name]; !exists {
		d.toolIndex[rt.Name] = ext
	}
}

// registerCommand records a command registration under the cap.
func (d *Driver) registerCommand(ext *Extension, rc extproto.RegisterCommandFromExt) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(ext.commands) >= maxExtCommands {
		fmt.Fprintf(ext.logFile, "[terva] dropping command %q: extension at the %d-command cap\n", rc.Name, maxExtCommands)
		return
	}
	ext.commands = append(ext.commands, rc)
	if _, exists := d.commandIndex[rc.Name]; !exists {
		d.commandIndex[rc.Name] = ext
	}
}

// subscribeEvents records event subscriptions under maxExtEventSubs,
// dropping UNKNOWN names first so a runaway extension's garbage can't
// crowd out real subscriptions. Known events (extproto.IsKnownEvent)
// are always kept; an unknown name — including a newer event this host
// can't yet emit — is kept only while under the cap, never rejected
// outright, so optimistic opt-in still degrades gracefully. Caller must
// NOT hold e.mu.
func (e *Extension) subscribeEvents(names []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	unknown := 0
	for ev := range e.eventSubs {
		if !extproto.IsKnownEvent(ev) {
			unknown++
		}
	}
	for _, ev := range names {
		if _, ok := e.eventSubs[ev]; ok {
			continue
		}
		if !extproto.IsKnownEvent(ev) {
			if unknown >= maxExtEventSubs {
				fmt.Fprintf(e.logFile, "[terva] dropping unknown event subscription %q: at the %d-subscription cap\n", ev, maxExtEventSubs)
				continue
			}
			unknown++
		}
		e.eventSubs[ev] = struct{}{}
	}
}

// readLoop processes every frame the extension sends after hello.
// Returns when stdout closes.
func (d *Driver) readLoop(ext *Extension, reader *bufio.Reader) {
	defer func() {
		// On close, drop every command + tool this extension owned so
		// future invocations don't dangle. The subprocess is gone; we
		// won't hear back about its commands or tool calls anymore.
		d.mu.Lock()
		for name, owner := range d.commandIndex {
			if owner == ext {
				delete(d.commandIndex, name)
			}
		}
		for name, owner := range d.toolIndex {
			if owner == ext {
				delete(d.toolIndex, name)
			}
		}
		d.mu.Unlock()
		ext.readyOnce.Do(func() { close(ext.readyCh) })
		fmt.Fprintf(ext.logFile, "[terva] extension %s read loop exited at %s\n", ext.Manifest.Name, time.Now().Format(time.RFC3339))
		// Crash surfacing: if the read loop ended without the host
		// asking the extension to stop, the subprocess died on its own
		// (crash, panic, killed externally). Its tools and commands
		// were just dropped from the indexes above, so calls would now
		// fail as "unknown" with no explanation. Tell the user once,
		// and point them at the log (stderr is already tee'd there).
		if !ext.stopping.Load() && d.hooks != nil {
			d.hooks.Notify(ext.Manifest.Name, "error",
				fmt.Sprintf("extension %q exited unexpectedly; its tools and commands are unavailable (see %s)", ext.Manifest.Name, ext.LogPath))
		}
	}()

	for {
		line, tooLong, rerr := extproto.ReadFrame(reader)
		if tooLong {
			fmt.Fprintf(ext.logFile, "[terva] dropped oversized frame from extension %s (>%d bytes); skipping\n", ext.Manifest.Name, extproto.MaxFrameBytes)
			d.reportMalformed(ext.Manifest.Name, "", fmt.Sprintf("oversized frame (>%d bytes)", extproto.MaxFrameBytes))
			continue
		}
		if rerr != nil {
			// EOF or read error: the extension's stream ended. Exit the
			// loop so the deferred teardown runs (crash surfacing fires
			// unless the host asked it to stop).
			break
		}
		ext.mu.Lock()
		ext.lastFrameTime = time.Now()
		ext.mu.Unlock()
		var frame extproto.Frame
		if err := json.Unmarshal(line, &frame); err != nil {
			fmt.Fprintf(ext.logFile, "[terva] malformed json from extension: %v\n", err)
			d.reportMalformed(ext.Manifest.Name, string(line), fmt.Sprintf("invalid json: %v", err))
			continue
		}
		switch frame.Type {
		case "register_command":
			var rc extproto.RegisterCommandFromExt
			if err := json.Unmarshal(line, &rc); err == nil {
				d.registerCommand(ext, rc)
			}
		case "register_tool":
			var rt extproto.RegisterToolFromExt
			if err := json.Unmarshal(line, &rt); err != nil {
				fmt.Fprintf(ext.logFile, "[terva] bad register_tool frame: %v\n", err)
				continue
			}
			// Validate the schema parses as JSON. If not, refuse to
			// register — a broken schema confuses the model.
			if len(rt.Schema) > 0 {
				var tmp any
				if err := json.Unmarshal(rt.Schema, &tmp); err != nil {
					fmt.Fprintf(ext.logFile, "[terva] tool %q: schema is not valid json (%v); skipped\n", rt.Name, err)
					continue
				}
			}
			d.registerTool(ext, rt)
		case "ready":
			ext.readyOnce.Do(func() { close(ext.readyCh) })
		case "subscribe":
			var sub extproto.SubscribeFromExt
			if err := json.Unmarshal(line, &sub); err == nil {
				ext.subscribeEvents(sub.Events)
				ext.mu.Lock()
				for _, ev := range sub.Intercept {
					switch ev {
					case "tool_call", "turn_start", "user_message", "assistant_message":
						ext.interceptSubs[ev] = struct{}{}
					}
				}
				ext.mu.Unlock()
			}
		case "event_intercept_response":
			var er extproto.EventInterceptResponseFromExt
			if err := json.Unmarshal(line, &er); err == nil {
				ext.mu.Lock()
				ch, ok := ext.pendingIntercept[er.ID]
				if ok {
					delete(ext.pendingIntercept, er.ID)
				}
				ext.mu.Unlock()
				if ok {
					select {
					case ch <- er:
					default:
					}
				}
			}
		case "tool_result":
			var tr extproto.ToolResultFromExt
			if err := json.Unmarshal(line, &tr); err == nil {
				ext.mu.Lock()
				ch, ok := ext.pendingTool[tr.ID]
				if ok {
					delete(ext.pendingTool, tr.ID)
				}
				ext.mu.Unlock()
				if ok {
					select {
					case ch <- tr:
					default:
					}
				}
			}
		case "notify":
			var n extproto.NotifyFromExt
			if err := json.Unmarshal(line, &n); err == nil {
				d.hooks.Notify(ext.Manifest.Name, n.Level, n.Message)
			}
		case "clear_notes":
			d.hooks.ClearNotes(ext.Manifest.Name)
		case "submit_slash":
			// Spontaneous request to invoke a slash command in the
			// TUI. Refused unless the payload looks like a slash
			// command so a misbehaving extension can't sneak a model
			// prompt through this path.
			var s extproto.SubmitSlashFromExt
			if err := json.Unmarshal(line, &s); err == nil {
				text := strings.TrimSpace(s.Text)
				if strings.HasPrefix(text, "/") {
					d.hooks.SubmitSlash(text)
				} else {
					fmt.Fprintf(ext.logFile, "[terva] submit_slash refused (not a slash command): %q\n", s.Text)
				}
			}
		case "command_response":
			var cr extproto.CommandResponseFromExt
			if err := json.Unmarshal(line, &cr); err == nil {
				ext.mu.Lock()
				ch, ok := ext.pending[cr.ID]
				if ok {
					delete(ext.pending, cr.ID)
				}
				ext.mu.Unlock()
				if ok {
					select {
					case ch <- cr:
					default:
					}
				}
			}
		case "open_panel":
			var op extproto.OpenPanelFromExt
			if err := json.Unmarshal(line, &op); err == nil {
				d.hooks.OpenPanel(ext.Manifest.Name, op.Panel)
			}
		case "panel_render":
			var pr extproto.PanelRenderFromExt
			if err := json.Unmarshal(line, &pr); err == nil {
				d.hooks.UpdatePanel(ext.Manifest.Name, pr.PanelID, pr.Title, pr.Lines, pr.Footer)
			}
		case "panel_close":
			var pc extproto.PanelCloseFromExt
			if err := json.Unmarshal(line, &pc); err == nil {
				d.hooks.ClosePanel(ext.Manifest.Name, pc.PanelID)
			}
		case "register_context":
			var rc extproto.RegisterContextFromExt
			if err := json.Unmarshal(line, &rc); err == nil {
				ext.mu.Lock()
				ext.staticContext = rc.Text
				ext.mu.Unlock()
			}
		case "refresh_context":
			// register_context you can send mid-session (protocol 3):
			// swap the static block and ask the host to rebuild the
			// cached system prompt so it takes effect next turn.
			var rc extproto.RefreshContextFromExt
			if err := json.Unmarshal(line, &rc); err == nil {
				ext.mu.Lock()
				changed := ext.staticContext != rc.Text
				ext.staticContext = rc.Text
				ext.mu.Unlock()
				if changed {
					d.hooks.RefreshContext()
				}
			}
		case "host_tool_call":
			// The extension asks the host to run one of its tools
			// (protocol 3). Dispatch off the read-loop goroutine so a
			// blocking approval prompt doesn't stall reading; the reply
			// is correlated by the extension's own id.
			var htc extproto.HostToolCallFromExt
			if err := json.Unmarshal(line, &htc); err == nil {
				d.handleHostToolCall(ext, htc)
			}
		case "list_sessions":
			var ls extproto.ListSessionsFromExt
			if err := json.Unmarshal(line, &ls); err == nil {
				d.handleListSessions(ext, ls)
			}
		case "read_session":
			var rs extproto.ReadSessionFromExt
			if err := json.Unmarshal(line, &rs); err == nil {
				d.handleReadSession(ext, rs)
			}
		case "context_card":
			var cc extproto.ContextCardFromExt
			if err := json.Unmarshal(line, &cc); err == nil && cc.ID != "" {
				ext.mu.Lock()
				if ext.contextCards == nil {
					ext.contextCards = map[string]contextCard{}
				}
				ext.contextCards[cc.ID] = contextCard{label: cc.Label, text: cc.Text, priority: cc.Priority, blocking: cc.Blocking}
				ext.mu.Unlock()
			}
		case "context_card_clear":
			var cc extproto.ContextCardClearFromExt
			if err := json.Unmarshal(line, &cc); err == nil {
				ext.mu.Lock()
				delete(ext.contextCards, cc.ID)
				ext.mu.Unlock()
			}
		case "status_segment":
			var ss extproto.StatusSegmentFromExt
			if err := json.Unmarshal(line, &ss); err == nil && ss.ID != "" {
				ext.mu.Lock()
				if ext.statusSegments == nil {
					ext.statusSegments = map[string]string{}
				}
				if ss.Text == "" {
					delete(ext.statusSegments, ss.ID)
				} else {
					ext.statusSegments[ss.ID] = ss.Text
				}
				ext.mu.Unlock()
				if d.hooks != nil {
					d.hooks.RefreshStatus()
				}
			}
		case "shutdown_ack":
			// Caller of Stop is waiting on the process exit, not this frame.
		default:
			fmt.Fprintf(ext.logFile, "[terva] unknown frame type %q\n", frame.Type)
			d.reportMalformed(ext.Manifest.Name, string(line), fmt.Sprintf("unknown frame type %q", frame.Type))
		}
	}
}

// SendPanelKey forwards a key/text event to an extension's open panel.
func (d *Driver) SendPanelKey(extName, panelID, key, text string) error {
	d.mu.RLock()
	ext, ok := d.ext[extName]
	d.mu.RUnlock()
	if !ok {
		return fmt.Errorf("no extension %q", extName)
	}
	return ext.writeFrame(extproto.PanelKeyFromHost{Type: "panel_key", PanelID: panelID, Key: key, Text: text})
}

// SendPanelClose tells an extension the user closed one of its panels.
func (d *Driver) SendPanelClose(extName, panelID string) error {
	d.mu.RLock()
	ext, ok := d.ext[extName]
	d.mu.RUnlock()
	if !ok {
		return fmt.Errorf("no extension %q", extName)
	}
	return ext.writeFrame(extproto.PanelCloseFromHost{Type: "panel_close", PanelID: panelID})
}

// Stop cleanly terminates every extension. Sends ShutdownFromHost,
// waits up to gracePeriod for each subprocess to exit, then SIGTERMs
// (and SIGKILLs after another second) the holdouts.
func (d *Driver) Stop(gracePeriod time.Duration) {
	d.mu.RLock()
	exts := make([]*Extension, 0, len(d.ext))
	for _, e := range d.ext {
		exts = append(exts, e)
	}
	d.mu.RUnlock()
	stopExtensions(exts, gracePeriod)
}

// writerFlushGrace bounds how long stopExtensions waits for an
// extension's writer goroutine to flush its queued frames (including
// the shutdown frame) before the pipe is closed. A healthy extension
// drains instantly; only a wedged one hits the timeout.
const writerFlushGrace = 200 * time.Millisecond

func stopExtensions(exts []*Extension, gracePeriod time.Duration) {
	for _, ext := range exts {
		// Mark this as a deliberate teardown so the read loop's exit
		// isn't reported to the user as a crash.
		ext.stopping.Store(true)
		if ext.outbox == nil {
			continue
		}
		// Enqueue the shutdown frame (best-effort, non-blocking so a
		// wedged writer can't hang teardown), then signal the writer to
		// flush everything queued (shutdown included) and exit. Waiting
		// on writerDone before closing the pipe is what lets the
		// shutdown frame actually reach the extension instead of being
		// cut off by the Close; a full outbox just falls back to the EOF
		// the Close delivers.
		if frame, err := extproto.Encode(extproto.ShutdownFromHost{Type: "shutdown"}); err == nil {
			_ = ext.enqueueFrame(frame, false)
		}
		ext.quitOnce.Do(func() { close(ext.quit) })
		select {
		case <-ext.writerDone:
		case <-time.After(writerFlushGrace):
		}
		// Close under stdinMu so we don't yank the pipe out from under
		// the writer's in-flight Write.
		ext.stdinMu.Lock()
		if ext.stdin != nil {
			_ = ext.stdin.Close()
		}
		ext.stdinMu.Unlock()
	}

	deadline := time.Now().Add(gracePeriod)
	for _, ext := range exts {
		if ext.cmd == nil {
			if ext.logFile != nil {
				_ = ext.logFile.Close()
			}
			continue
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			remaining = 100 * time.Millisecond
		}
		done := make(chan struct{})
		go func() { _ = ext.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(remaining):
			_ = ext.cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(time.Second):
				_ = ext.cmd.Process.Kill()
				<-done
			}
		}
		if ext.logFile != nil {
			_ = ext.logFile.Close()
		}
	}
}
