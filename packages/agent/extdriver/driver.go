package extdriver

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
	"sort"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/agent/procenv"
	"terva.sh/terva/packages/privfs"
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
	UpdatePanel(extName, panelID, title string, lines []string, footer string, widgets []extproto.Widget)
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

	// RefreshTools asks the host to rebuild the tool registry after an
	// extension withdrew or restored some of its own tools
	// (set_withdrawn_tools, protocol 4), so the change to what the model
	// sees takes effect on the next turn. Like RefreshContext, a no-op
	// where the registry is fixed for the run; the live interactive session
	// rebuilds and swaps the running agent's tools.
	RefreshTools()
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

	// failed remembers extensions that were claimed and then dropped because
	// they could not be started — overwhelmingly a missed hello deadline. Load
	// rolls the claim back so the name stays free for a retry, which also means
	// the extension vanishes from every map the host reports on: `terva ext
	// doctor` could only say "not loaded" and leave the reason sitting in a log
	// file nobody opens. Keyed by manifest name; cleared when the same name
	// loads successfully, and by Reset. Guarded by mu.
	failed map[string]LoadFailure

	// helloTimeout bounds the wait for each extension's hello frame. Starts at
	// DefaultHelloTimeout; SetHelloTimeout overrides it from config. Guarded by
	// mu, so a host that sets it late cannot race a concurrent spawn.
	helloTimeout time.Duration

	// sessionReader, if set, serves an extension's list_sessions /
	// read_session (protocol 3). The driver does not own session
	// storage, so the agent layer injects this; nil means session reads
	// are unsupported (an empty list / not-found). Guarded by mu.
	sessionReader SessionReader

	// configResolver, if set, resolves an extension's host-supplied config
	// (manifest defaults overlaid with the user's saved values) at spawn
	// time, for the hello_ack Config field. The driver is dependency-light
	// and does not read config.json, so the agent layer injects this; nil
	// means extensions receive no host config. Guarded by mu.
	configResolver ConfigResolver
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
		failed:       map[string]LoadFailure{},
		helloTimeout: DefaultHelloTimeout,
	}
}

// LoadFailure records an extension that was found and tried but could not be
// started, so the host can report WHY instead of only that it isn't there.
type LoadFailure struct {
	Name    string
	Dir     string
	LogPath string
	Reason  string
}

// recordFailure remembers why an extension could not be started. Called on
// Load's rollback path, where the name claim is about to be released.
func (d *Driver) recordFailure(mf Manifest, dir, logPath, reason string) {
	d.mu.Lock()
	if d.failed == nil {
		d.failed = map[string]LoadFailure{}
	}
	d.failed[mf.Name] = LoadFailure{Name: mf.Name, Dir: dir, LogPath: logPath, Reason: reason}
	d.mu.Unlock()
}

// Failures returns the extensions that could not be started this run, sorted
// by name. Read-only reporting for `terva ext doctor` and the host's
// extension surfaces.
func (d *Driver) Failures() []LoadFailure {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]LoadFailure, 0, len(d.failed))
	for _, f := range d.failed {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SetHelloTimeout overrides how long spawn waits for an extension's hello
// frame. Call before Load / Discover; a value <= 0 restores the default.
//
// The knob exists because the deadline is really two questions wearing one
// number: "is this process alive and speaking the protocol?" (seconds) and
// "how long may it take to become that process?" (a launcher that compiles
// its extension first: minutes). An extension cannot ask for more time,
// because the thing that would ask is the artifact still being built — so
// where prebuilding isn't possible, the operator has to say it instead.
func (d *Driver) SetHelloTimeout(v time.Duration) {
	if v <= 0 {
		v = DefaultHelloTimeout
	}
	d.mu.Lock()
	d.helloTimeout = v
	d.mu.Unlock()
}

// HelloTimeout reports the current hello-frame deadline.
func (d *Driver) HelloTimeout() time.Duration {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.helloTimeout <= 0 {
		return DefaultHelloTimeout // zero-value Driver (tests construct these)
	}
	return d.helloTimeout
}

// Load registers a single extension whose manifest the host has already
// read, validated, and cleared against policy (enabled / not
// load-disabled / trust). If the manifest declares an exec, Load spawns
// the subprocess and completes the hello handshake; a theme-only
// extension (no exec) is registered as immediately ready. A name that is
// already loaded is a no-op (the higher-priority discovery location, or
// the explicit --ext path, already won).
func (d *Driver) Load(ctx context.Context, dir string, mf Manifest) error {
	// Defense in depth: the manifest name becomes a map key and is joined into
	// ext-data/<name> and ext-<name>.log host paths below, so a value with
	// path separators, "."/"..", or an absolute path could escape its intended
	// directory. The manager validates this too, but Load is the lower-level
	// API any caller reaches, so guard it here as well.
	if !isSafeManifestName(mf.Name) {
		return fmt.Errorf("extension manifest name %q is not a safe path element", mf.Name)
	}
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
	delete(d.failed, mf.Name) // a retry that gets this far supersedes the old verdict
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
		// Rolling the claim back is what keeps the name retryable, and also
		// what would otherwise erase every trace of this extension from the
		// host's view. Keep the verdict.
		if !errors.Is(err, context.Canceled) {
			d.recordFailure(mf, dir, ext.LogPath, err.Error())
		}
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

// isSafeManifestName reports whether name is a single, safe path element.
// The manifest name is joined into ext-data/<name> and ext-<name>.log host
// paths, so a value with path separators, "."/"..", or an absolute path could
// make host-created paths escape their intended directory. Mirrors the
// adopt-list rule in the extensions package.
func isSafeManifestName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if filepath.IsAbs(name) || strings.ContainsAny(name, `/\`) {
		return false
	}
	return filepath.Base(name) == name
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
	d.failed = map[string]LoadFailure{} // a reload re-derives every verdict from scratch
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

// ExtensionByName returns the loaded extension `name`, if any. Note a
// crashed extension stays in the loaded set (its tools and commands are
// dropped, the entry is not) until StopByName reaps it — check Ready()
// or the indexes for liveness-sensitive callers.
func (d *Driver) ExtensionByName(name string) (*Extension, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	ext, ok := d.ext[name]
	return ext, ok
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
				ext.mu.Lock()
				ext.readyTimedOut = true
				ext.diagnostics = append(ext.diagnostics, "timed out waiting for ready frame")
				ext.mu.Unlock()
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
	_ = privfs.MkdirAll(logsDir)
	logPath := filepath.Join(logsDir, "ext-"+ext.Manifest.Name+".log")
	logFile, err := privfs.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY)
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
	// Isolate into its own process group (kitty-cwd fix). Paired with the
	// group-targeted teardown below so background children are reaped.
	isolateExtensionProcess(cmd)

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
	// The blocking read runs on its own goroutine; we race it against a
	// timer. On timeout we kill the subprocess, which unblocks the
	// goroutine's read (stdout closes) so it can return and the channel
	// send is consumed — no leaked reader goroutine.
	//
	// A launcher may send any number of `bootstrap` frames first (see
	// bootstrapFrame): the deadline then measures SILENCE rather than total
	// elapsed time, which is what lets a launcher that compiles before it
	// can exec the protocol speaker stay legible instead of looking hung.
	reader := bufio.NewReaderSize(stdout, 64*1024)
	type helloRead struct {
		line    []byte
		tooLong bool
		err     error
	}
	readOne := func() <-chan helloRead {
		ch := make(chan helloRead, 1)
		go func() {
			line, tooLong, err := extproto.ReadFrame(reader)
			ch <- helloRead{line, tooLong, err}
		}()
		return ch
	}

	helloTimeout := d.HelloTimeout()
	hardDeadline := time.Now().Add(maxBootstrapWait)
	var got helloRead
	for {
		scanned := readOne()
		select {
		case got = <-scanned:
		case <-time.After(helloTimeout):
			killExtensionGroup(cmd.Process)
			<-scanned // ReadFrame returns once the killed process's stdout closes.
			fmt.Fprintf(logFile, "[terva] extension %s failed to handshake within %s; killed and skipped\n", ext.Manifest.Name, helloTimeout)
			return fmt.Errorf("extension %s failed to send hello within %s", ext.Manifest.Name, helloTimeout)
		case <-ctx.Done():
			killExtensionGroup(cmd.Process)
			<-scanned
			return ctx.Err()
		}
		if got.tooLong || got.err != nil {
			break // handled by the shared error paths below
		}
		msg, ok := bootstrapFrame(got.line)
		if !ok {
			break // the real hello (or something that will fail to parse as one)
		}
		// Still building. Extend the silence budget and tell the user, so a
		// two-minute cold compile reads as progress instead of a hang. The
		// hard deadline is what keeps a launcher that only ever reports
		// progress from holding the slot forever.
		if time.Now().After(hardDeadline) {
			killExtensionGroup(cmd.Process)
			fmt.Fprintf(logFile, "[terva] extension %s still bootstrapping after %s; killed and skipped\n", ext.Manifest.Name, maxBootstrapWait)
			return fmt.Errorf("extension %s was still bootstrapping after %s", ext.Manifest.Name, maxBootstrapWait)
		}
		d.noteBootstrap(ext, msg, logFile)
	}
	if got.tooLong {
		killExtensionGroup(cmd.Process)
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
		killExtensionGroup(cmd.Process)
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
		if err := privfs.MkdirAll(dd); err == nil {
			dataDir = dd
		}
	}
	ext.mu.Lock()
	ext.dataDir = dataDir // kept for chat-attachment containment (chatconn.go)
	ext.mu.Unlock()
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
		Config:          d.resolvedConfigFor(ext.Manifest.Name, ext.Manifest.Config),
	}); err != nil {
		// Tear down the writer goroutine we just started so it doesn't
		// leak on this failed spawn.
		ext.quitOnce.Do(func() { close(ext.quit) })
		killExtensionGroup(cmd.Process)
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

// DefaultHelloTimeout bounds how long spawn() waits for an extension's hello
// frame before giving up on it. Past this the subprocess is killed and the
// extension is skipped for the run, without blocking the rest of Discover.
//
// It used to be 3s, matched to the ready grace, because the whole wait sat on
// terva's startup path and every second of it was a second the user spent
// looking at nothing. The long-lived hosts now start extensions in the
// background and take the wait at the first turn (extensions.StartAsync), so
// the budget costs nothing on the common path and can be generous enough for
// the case that kept losing: a launcher that builds its extension before
// exec'ing it. A cold Go build of a small extension measured ~5.5s — inside
// 10s, outside 3s.
//
// Not a licence to make it huge. The single-shot modes (-p, --json) still pay
// it up front, and it is the only thing standing between terva and a hung
// subprocess. Operators who need more (a cold Rust build) should raise it
// deliberately via extension_hello_timeout, or better, prebuild.
const DefaultHelloTimeout = 10 * time.Second

// maxBootstrapWait is the absolute ceiling on the handshake, however much
// progress an extension reports. The hello timeout answers "is this process
// alive?"; bootstrap frames answer "how long may becoming that process take?"
// — and the second question has no principled small answer, so it gets a
// large, blunt one instead. A launcher that emits progress forever holds its
// slot until here and no further.
const maxBootstrapWait = 10 * time.Minute

// bootstrapFrame reports whether line is a pre-hello progress frame, and its
// message. A launcher that has to build its extension before it can exec the
// thing that speaks the protocol emits these while it works:
//
//	{"type":"bootstrap","message":"compiling"}
//
// Deliberately a frame the LAUNCHER can print with one `printf` — the SDK
// isn't running yet, and can't be, which is the whole difficulty. Unknown to
// older hosts, which see an unparseable hello and skip the extension exactly
// as they do today, so this is additive with no protocol bump.
func bootstrapFrame(line []byte) (string, bool) {
	var f extproto.BootstrapFromExt
	if err := json.Unmarshal(line, &f); err != nil || f.Type != extproto.TypeBootstrap {
		return "", false
	}
	return f.Message, true
}

// noteBootstrap records a bootstrap report where a human will find it: the
// extension's log, its diagnostics (so `terva ext doctor` can say "building"
// rather than "not loaded"), and the host's notify hook. Without the last one
// a slow build is invisible until it either finishes or is killed — the
// silence this whole mechanism exists to break.
func (d *Driver) noteBootstrap(ext *Extension, msg string, logFile io.Writer) {
	if msg == "" {
		msg = "starting"
	}
	fmt.Fprintf(logFile, "[terva] extension %s bootstrapping: %s\n", ext.Manifest.Name, msg)
	ext.mu.Lock()
	ext.bootstrapping = msg
	ext.diagnostics = append(ext.diagnostics, "bootstrap: "+msg)
	ext.mu.Unlock()
	if d.hooks != nil {
		d.hooks.Notify(ext.Manifest.Name, "info", msg)
	}
}

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
				ext.mu.Lock()
				ext.autoReady = true
				ext.diagnostics = append(ext.diagnostics, "no ready frame; auto-ready after idle")
				ext.mu.Unlock()
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
	// maxEssentialTools bounds how many tools ONE extension may mark
	// essential (register_tool essential). An essential tool stays
	// advertised every turn even under lazy visibility, so an unbounded set
	// would let one extension defeat the context economy lazy mode exists
	// for. Excess essential tools load deferred instead (and are logged).
	maxEssentialTools = 3
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
	// Cap load-bearing (essential) tools per extension: honor the first
	// maxEssentialTools, then downgrade the rest to ordinary deferred tools
	// so an extension can't pin its whole surface always-visible under lazy
	// mode. Counted over the already-stored (honored) set, so the cap is on
	// what we actually advertise. Logged once here, at registration.
	if rt.Essential {
		honored := 0
		for _, t := range ext.tools {
			if t.Essential {
				honored++
			}
		}
		if honored >= maxEssentialTools {
			fmt.Fprintf(ext.logFile, "[terva] tool %q asked to be essential but the extension is at the %d-essential cap; loading it deferred instead\n", rt.Name, maxEssentialTools)
			rt.Essential = false
		}
	}
	ext.tools = append(ext.tools, rt)
	if _, exists := d.toolIndex[rt.Name]; !exists {
		d.toolIndex[rt.Name] = ext
	} else {
		ext.recordDiagnostic(fmt.Sprintf("tool %s conflicts with another extension registering the same name", rt.Name))
	}
}

// resolveWithdrawn turns a set_withdrawn_tools snapshot into the concrete
// set of THIS extension's tool names to hide, intersected with its own
// registered tools so it can never hide a built-in or another extension's
// tool. All:true expands to every registered name. An empty result is
// returned as nil so restore-all is a clean zero value. Caller holds d.mu
// (it reads ext.tools).
func resolveWithdrawn(ext *Extension, sw extproto.SetWithdrawnToolsFromExt) map[string]bool {
	if sw.All {
		if len(ext.tools) == 0 {
			return nil
		}
		out := make(map[string]bool, len(ext.tools))
		for _, t := range ext.tools {
			out[t.Name] = true
		}
		return out
	}
	if len(sw.Tools) == 0 {
		return nil
	}
	owned := make(map[string]bool, len(ext.tools))
	for _, t := range ext.tools {
		owned[t.Name] = true
	}
	var out map[string]bool
	for _, n := range sw.Tools {
		if owned[n] {
			if out == nil {
				out = map[string]bool{}
			}
			out[n] = true
		}
	}
	return out
}

// sameSet reports whether two name sets are equal, treating nil and an
// empty map as equal. The cache-safety no-op guard: a redundant re-assert
// of the same withdrawn set must not fire RefreshTools.
func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
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
	} else {
		ext.recordDiagnostic(fmt.Sprintf("command /%s conflicts with another extension registering the same name", rc.Name))
	}
}

// RegisteredCommandDiagnostic describes one command an extension registered
// and whether it owns the active dispatch slot.
type RegisteredCommandDiagnostic struct {
	Name        string
	Description string
	Active      bool
}

// RegisteredToolDiagnostic describes one tool an extension registered and
// whether it owns the active dispatch slot.
type RegisteredToolDiagnostic struct {
	Name        string
	Description string
	Active      bool
}

// ExtensionDiagnostic is a read-only snapshot of one loaded extension's
// state for `terva ext doctor`.
type ExtensionDiagnostic struct {
	Name          string
	Version       string
	Dir           string
	LogPath       string
	ThemeOnly     bool
	Ready         bool
	AutoReady     bool
	ReadyTimedOut bool
	// Bootstrapping is the last pre-hello progress message, when the extension
	// reported one. Non-empty on an extension still building, and on one that
	// built and then failed anyway.
	Bootstrapping string
	// FailedReason is why the extension could not be started at all. When set,
	// the entry describes a load that was rolled back, so it carries no
	// commands or tools — only the verdict, which would otherwise exist
	// nowhere the user looks.
	FailedReason string
	Commands     []RegisteredCommandDiagnostic
	Tools        []RegisteredToolDiagnostic
	Messages     []string
}

// Diagnostics returns a snapshot of loaded-extension state for diagnostic
// commands (terva ext doctor). Read-only; it never changes driver behavior.
func (d *Driver) Diagnostics() []ExtensionDiagnostic {
	d.mu.RLock()
	defer d.mu.RUnlock()
	names := make([]string, 0, len(d.ext))
	for name := range d.ext {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]ExtensionDiagnostic, 0, len(names))
	for _, name := range names {
		ext := d.ext[name]
		ext.mu.Lock()
		msgs := append([]string(nil), ext.diagnostics...)
		autoReady := ext.autoReady
		readyTimedOut := ext.readyTimedOut
		bootstrapping := ext.bootstrapping
		ext.mu.Unlock()

		diag := ExtensionDiagnostic{
			Name:          ext.Manifest.Name,
			Version:       ext.Manifest.Version,
			Dir:           ext.Dir,
			LogPath:       ext.LogPath,
			ThemeOnly:     ext.Manifest.Exec == "",
			AutoReady:     autoReady,
			ReadyTimedOut: readyTimedOut,
			Bootstrapping: bootstrapping,
			Messages:      msgs,
		}
		select {
		case <-ext.readyCh:
			diag.Ready = true
		default:
		}
		// ext.commands/ext.tools are guarded by d.mu (held here), so reading
		// them without ext.mu is safe.
		for _, c := range ext.commands {
			diag.Commands = append(diag.Commands, RegisteredCommandDiagnostic{
				Name:        c.Name,
				Description: c.Description,
				Active:      d.commandIndex[c.Name] == ext,
			})
		}
		for _, t := range ext.tools {
			diag.Tools = append(diag.Tools, RegisteredToolDiagnostic{
				Name:        t.Name,
				Description: t.Description,
				Active:      d.toolIndex[t.Name] == ext,
			})
		}
		out = append(out, diag)
	}
	// Extensions that never got off the ground. Load rolls their claim back, so
	// they are in none of the maps above — and a doctor run that omits them can
	// only report the ABSENCE of an extension the user can plainly see
	// installed, with the reason left in a log file. Report the verdict.
	failedNames := make([]string, 0, len(d.failed))
	for name := range d.failed {
		if _, live := d.ext[name]; !live {
			failedNames = append(failedNames, name)
		}
	}
	sort.Strings(failedNames)
	for _, name := range failedNames {
		f := d.failed[name]
		out = append(out, ExtensionDiagnostic{
			Name:         f.Name,
			Dir:          f.Dir,
			LogPath:      f.LogPath,
			FailedReason: f.Reason,
		})
	}
	return out
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
		// End any live chat-tunnel session so a chat consumer's
		// Receive learns the process died instead of waiting forever.
		ext.closeChatOnExit()
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
		case "submit":
			// Spontaneous request to queue a plain model prompt. Unlike
			// submit_slash there is no slash guard: the host's SubmitOrQueue
			// treats it exactly like typed input (busy-aware). Empty text is
			// dropped.
			var s extproto.SubmitFromExt
			if err := json.Unmarshal(line, &s); err == nil {
				if text := strings.TrimSpace(s.Text); text != "" {
					d.hooks.Submit(text)
				}
			}
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
				d.hooks.UpdatePanel(ext.Manifest.Name, pr.PanelID, pr.Title, pr.Lines, pr.Footer, pr.Widgets)
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
		case "set_withdrawn_tools":
			// The tool analog of refresh_context (protocol 4): a wholesale
			// snapshot of this extension's own tools to hide from the model.
			// Unlike staticContext (ext.mu), tools/withdrawnTools are guarded
			// by d.mu — resolveWithdrawn reads ext.tools — so resolve, diff,
			// and store under d.mu, then fire the hook lock-free.
			var sw extproto.SetWithdrawnToolsFromExt
			if err := json.Unmarshal(line, &sw); err == nil {
				d.mu.Lock()
				next := resolveWithdrawn(ext, sw)
				changed := !sameSet(ext.withdrawnTools, next)
				if changed {
					ext.withdrawnTools = next
				}
				d.mu.Unlock()
				if changed {
					d.hooks.RefreshTools()
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
		case "register_connector":
			// The connector role (protocol 5): gated on the manifest's
			// Connector consent flag inside registerConnector.
			d.registerConnector(ext)
		case "chat":
			// One opaque connector-protocol frame for the live tunnel
			// session. The driver only routes it; chat/connhost parses.
			var cf extproto.ChatFrame
			if err := json.Unmarshal(line, &cf); err == nil {
				ext.deliverChatFrame(cf.ID, cf.Frame)
			}
		case "chat_down":
			// The extension's connector engine exited — with a reason
			// (its receive stream died permanently) or without (orderly
			// teardown after chat_close). The process and its tools live
			// on; connproto's analog is process exit + restart budget.
			var cd extproto.ChatDownFromExt
			if err := json.Unmarshal(line, &cd); err == nil {
				ext.deliverChatDown(cd.ID, cd.Error)
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
			terminateExtensionGroup(ext.cmd.Process)
			select {
			case <-done:
			case <-time.After(time.Second):
				killExtensionGroup(ext.cmd.Process)
				<-done
			}
		}
		if ext.logFile != nil {
			_ = ext.logFile.Close()
		}
	}
}
