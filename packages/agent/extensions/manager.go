// Package extensions implements the host side of terva's subprocess
// extension protocol. The Manager discovers extensions in well-known
// directories, spawns each one, completes the hello handshake, and
// routes slash commands to the right extension.
//
// Each extension is its own process, communicating with terva over its
// stdin/stdout in newline-delimited JSON. Stderr is redirected to a
// per-extension log file under $TERVA_HOME/logs/. Crashing one
// extension does not affect the others or the host.
//
// See docs/extensions.md for the user-facing reference and
// packages/agent/extproto for the wire format.
package extensions

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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/agent/procenv"
	"terva.sh/terva/packages/envcompat"
	"terva.sh/terva/packages/tui"
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
}

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

// HostHooks is the small interface the manager calls back into the
// running TUI through. Decouples extensions from interactive.go.
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
}

// Manager owns every extension subprocess for the lifetime of terva.
type Manager struct {
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

	// explicitPaths remembers ad-hoc paths passed via --ext so
	// Reload can respawn them alongside the discovered set.
	explicitPaths []string

	// onReload, if set, is invoked after a successful Reload. Used
	// by the host so it can rebuild the agent's tool registry with
	// the freshly-registered extension tools.
	onReload func()

	// contextDisabled is the set of extension names whose MODEL context
	// contributions (static + cards) the host ignores — their tools,
	// commands, and status segments still work. Set once from the
	// resolved config (user ∪ project) via SetContextDisabled; replaced
	// wholesale, never mutated, so a captured reference stays immutable.
	contextDisabled map[string]bool

	// disabledExtensions is the set of extension names that must not be
	// loaded at all (resolved user ∪ project config). Consulted in
	// loadOne, so it MUST be set via SetDisabledExtensions BEFORE
	// Discover / LoadExplicit. Guarded by mu.
	disabledExtensions map[string]bool

	// projectTrusted is the Workspace Trust verdict for m.cwd. When false
	// (the SAFE DEFAULT — a fresh Manager is untrusted), searchDirs drops
	// the project-local extension roots (the cwd/<project-dir>/extensions
	// paths from ProjectDirNames) so a cloned/untrusted repo's extensions
	// are never spawned. The global root ($TERVA_HOME/extensions) is
	// always searched. Set via SetProjectTrusted BEFORE Discover. Guarded
	// by mu. See docs/plans/workspace-trust.md.
	projectTrusted bool
}

// New constructs an empty Manager. Call Discover to populate it from
// the on-disk extension directories.
func New(tervaHome, cwd, tervaVersion, provider, model string, hooks HostHooks) *Manager {
	return &Manager{
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

// Discover scans the global and project extension dirs and starts
// every extension whose manifest is enabled. Spawns happen in
// parallel so a slow runtime (e.g. `npx tsx` cold-start, ~1.5s)
// doesn't block other extensions from starting. Returns a slice of
// errors encountered (one per extension); a single bad extension
// doesn't abort the rest.
func (m *Manager) Discover(ctx context.Context) []error {
	type loadJob struct {
		dir string
	}
	var jobs []loadJob
	seenDirs := map[string]bool{} // dedup by basename so project wins
	for _, dir := range m.searchDirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // missing directory is fine
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if seenDirs[e.Name()] {
				continue // higher-priority location already queued
			}
			seenDirs[e.Name()] = true
			jobs = append(jobs, loadJob{dir: filepath.Join(dir, e.Name())})
		}
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(jobs))
	for _, j := range jobs {
		wg.Add(1)
		go func(extDir string) {
			defer wg.Done()
			if err := m.loadOne(ctx, extDir); err != nil {
				errCh <- fmt.Errorf("%s: %w", extDir, err)
			}
		}(j.dir)
	}
	wg.Wait()
	close(errCh)

	var errs []error
	for e := range errCh {
		errs = append(errs, e)
	}
	return errs
}

// searchDirs returns the directories the discoverer walks, in
// priority order: project-local first (so a project can override
// global behavior), then global. Both project-dir spellings are
// walked, new name first (the rename's dual-read seam; dedup by
// extension basename makes .terva win over .terva for the same name).
//
// The project-local roots (every spelling from ProjectDirNames) are
// GATED on Workspace Trust: when the cwd is untrusted (the default — see
// SetProjectTrusted), they are omitted, so a cloned/untrusted repo's
// project extensions are never discovered or spawned (the headline RCE
// fix, plan Phase 1). The global root ($TERVA_HOME/extensions) — code
// the user installed themselves — is always searched.
func (m *Manager) searchDirs() []string {
	var dirs []string
	if m.cwd != "" && m.isProjectTrusted() {
		for _, dirName := range envcompat.ProjectDirNames() {
			dirs = append(dirs, filepath.Join(m.cwd, dirName, "extensions"))
		}
	}
	if m.tervaHome != "" {
		dirs = append(dirs, filepath.Join(m.tervaHome, "extensions"))
	}
	return dirs
}

// extDataDir is an extension's writable, install-independent data
// directory, under $TERVA_HOME/ext-data/<name>. Keyed by manifest name
// so it's stable no matter which discovery root the extension loaded
// from. Returns "" when there's no terva home, signalling the caller to
// fall back to the install dir.
func (m *Manager) extDataDir(name string) string {
	if m.tervaHome == "" || name == "" {
		return ""
	}
	return filepath.Join(m.tervaHome, "ext-data", name)
}

// loadOne reads a single extension's manifest and, if enabled,
// spawns its subprocess + completes the hello handshake.
func (m *Manager) loadOne(ctx context.Context, dir string) error {
	manifestPath := filepath.Join(dir, "extension.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	var mf Manifest
	if err := json.Unmarshal(raw, &mf); err != nil {
		return fmt.Errorf("parse manifest: %w", err)
	}
	if mf.Name == "" {
		return errors.New("manifest: name is required")
	}
	hasTheme := hasExtensionTheme(dir)
	if mf.Exec == "" && !hasTheme {
		return errors.New("manifest: exec is required")
	}
	if !mf.IsEnabled() {
		// Quietly skip disabled extensions; terva ext list will show them.
		return nil
	}
	if m.extensionLoadDisabled(mf.Name) {
		// Disabled by user/project config (disable_extensions): never
		// spawned, so its tools/commands/panels/context never appear.
		fmt.Fprintf(os.Stderr, "extension %q not loaded (disabled by config)\n", mf.Name)
		return nil
	}

	m.mu.RLock()
	_, dup := m.ext[mf.Name]
	m.mu.RUnlock()
	if dup {
		// Project-local copy already won; ignore the global one.
		return nil
	}

	ext := &Extension{
		Manifest:         mf,
		Dir:              dir,
		readyCh:          make(chan struct{}),
		pending:          map[string]chan extproto.CommandResponseFromExt{},
		pendingTool:      map[string]chan extproto.ToolResultFromExt{},
		pendingIntercept: map[string]chan extproto.EventInterceptResponseFromExt{},
		eventSubs:        map[string]struct{}{},
		interceptSubs:    map[string]struct{}{},
	}
	if mf.Exec != "" {
		if err := m.spawn(ctx, ext); err != nil {
			return err
		}
	} else {
		ext.readyOnce.Do(func() { close(ext.readyCh) })
	}

	m.mu.Lock()
	m.ext[mf.Name] = ext
	// Note: ext.commands and ext.tools may be empty here — they're
	// populated by the read loop as register_* frames arrive after
	// hello. Indexing happens in the read loop too. Discover()'s
	// caller can WaitForReady() before relying on the registries.
	m.mu.Unlock()
	return nil
}

// LoadExplicit loads each path as an ad-hoc extension. Used for
// `terva --ext <path>` so extension authors can iterate on a working
// copy without having to `terva ext install` after every change.
//
// Loaded BEFORE Discover so explicit paths win on name conflicts
// against installed extensions. Spawns happen in parallel like the
// regular discovery path; errors are returned per path.
func hasExtensionTheme(dir string) bool {
	for _, file := range []string{"theme.json", filepath.Join("themes", "theme.json")} {
		if _, err := os.Stat(filepath.Join(dir, file)); err == nil {
			return true
		}
	}
	return false
}

func (m *Manager) ThemeOptions() []tui.ThemeOption {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.ext))
	for name := range m.ext {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []tui.ThemeOption
	for _, name := range names {
		ext := m.ext[name]
		for _, file := range []string{"theme.json", "themes/theme.json"} {
			path := filepath.Join(ext.Dir, file)
			value := path
			if opt, ok := tui.ThemeOptionFromFile(path, value, "extension "+ext.Manifest.Name); ok {
				out = append(out, opt)
			}
		}
	}
	return out
}

func (m *Manager) LoadExplicit(ctx context.Context, paths []string) []error {
	if len(paths) == 0 {
		return nil
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(paths))
	absPaths := make([]string, 0, len(paths))
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			errCh <- fmt.Errorf("%s: %w", p, err)
			continue
		}
		absPaths = append(absPaths, abs)
		wg.Add(1)
		go func(extDir string) {
			defer wg.Done()
			if err := m.loadOne(ctx, extDir); err != nil {
				errCh <- fmt.Errorf("%s: %w", extDir, err)
			}
		}(abs)
	}
	wg.Wait()
	close(errCh)

	m.mu.Lock()
	m.explicitPaths = append(m.explicitPaths, absPaths...)
	m.mu.Unlock()

	var errs []error
	for e := range errCh {
		errs = append(errs, e)
	}
	return errs
}

// SetOnReload registers a callback fired after a successful Reload.
// Hosts use it to rebuild the agent's tool registry with freshly-
// registered extension tools.
func (m *Manager) SetOnReload(fn func()) {
	m.mu.Lock()
	m.onReload = fn
	m.mu.Unlock()
}

// ReloadStats summarises the outcome of Reload.
type ReloadStats struct {
	Stopped int     // how many old processes were torn down
	Loaded  int     // how many new processes reached spawn
	Ready   int     // how many of those signalled ready in time
	Errors  []error // non-fatal per-extension errors
}

// Reload tears down every running extension, re-reads the manifests
// from disk, respawns everyone (including the --ext paths remembered
// from LoadExplicit), waits up to grace for ready signals, and
// invokes the SetOnReload callback so the host can rebuild its tool
// registry. The manager's internal maps are cleared before the
// new load to ensure a clean slate.
//
// Safe to call concurrently with normal host operations: the lock is
// released between stop and respawn so pending InvokeTool / Invoke
// calls on the old processes get a clean error as their stdin
// closes.
func (m *Manager) Reload(ctx context.Context, grace time.Duration) ReloadStats {
	stats := ReloadStats{}

	// Snapshot and remember the explicit paths before we wipe state.
	m.mu.Lock()
	old := m.ext
	explicit := append([]string(nil), m.explicitPaths...)
	stats.Stopped = len(old)
	m.ext = map[string]*Extension{}
	m.commandIndex = map[string]*Extension{}
	m.toolIndex = map[string]*Extension{}
	m.explicitPaths = nil
	callback := m.onReload
	m.mu.Unlock()

	// Graceful stop of the old set (reuses Stop's shutdown logic,
	// but Stop re-reads m.ext which is now empty, so we replicate
	// the small shutdown loop here on the snapshot).
	oldExts := make([]*Extension, 0, len(old))
	for _, ext := range old {
		oldExts = append(oldExts, ext)
	}
	stopExtensions(oldExts, grace)

	// Fresh load. Explicit paths first (they still win on conflict).
	if errs := m.LoadExplicit(ctx, explicit); len(errs) > 0 {
		stats.Errors = append(stats.Errors, errs...)
	}
	if errs := m.Discover(ctx); len(errs) > 0 {
		stats.Errors = append(stats.Errors, errs...)
	}

	m.mu.RLock()
	stats.Loaded = len(m.ext)
	m.mu.RUnlock()

	// Wait for ready frames. Use the same 3s grace terva uses at
	// startup so the reload feels no slower than a cold boot.
	readyDeadline := time.Now().Add(grace)
	if time.Until(readyDeadline) < 3*time.Second {
		readyDeadline = time.Now().Add(3 * time.Second)
	}
	m.WaitForReady(time.Until(readyDeadline))

	m.mu.RLock()
	for _, ext := range m.ext {
		select {
		case <-ext.readyCh:
			stats.Ready++
		default:
		}
	}
	m.mu.RUnlock()

	if callback != nil {
		callback()
	}

	return stats
}

// WaitForReady blocks until every loaded extension has signalled
// ReadyFromExt, or the grace period expires for the slowest one.
//
// Waits run in parallel: total time is max(per-extension wait), not
// sum. Without this, a single slow extension (e.g. `npx tsx` cold)
// would gate every other extension's wait too and terva startup would
// scale linearly with the number of slow runtimes installed.
//
// Call after Discover and before relying on tool registrations.
func (m *Manager) WaitForReady(grace time.Duration) {
	m.mu.RLock()
	exts := make([]*Extension, 0, len(m.ext))
	for _, e := range m.ext {
		exts = append(exts, e)
	}
	m.mu.RUnlock()

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
func (m *Manager) spawn(ctx context.Context, ext *Extension) error {
	logsDir := filepath.Join(m.tervaHome, "logs")
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
	case <-time.After(helloTimeout):
		_ = cmd.Process.Kill()
		<-scanned // ReadFrame returns once the killed process's stdout closes.
		fmt.Fprintf(logFile, "[terva] extension %s failed to handshake within %s; killed and skipped\n", ext.Manifest.Name, helloTimeout)
		return fmt.Errorf("extension %s failed to send hello within %s", ext.Manifest.Name, helloTimeout)
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

	hostVersion := m.tervaVersion
	// DataDir is the extension's writable state directory, kept separate
	// from ext.Dir (the read-only install dir) so a read-only / system
	// install still works and code never mixes with data. Keyed by
	// manifest name so it's stable across install location. Falls back to
	// the install dir when there's no terva home or the dir can't be
	// created — preserving the old colocated behavior rather than failing.
	dataDir := ext.Dir
	if d := m.extDataDir(ext.Manifest.Name); d != "" {
		if err := os.MkdirAll(d, 0o755); err == nil {
			dataDir = d
		}
	}
	if err := ext.writeFrame(extproto.HelloAckFromHost{
		Type:            "hello_ack",
		ProtocolVersion: extproto.ProtocolVersion,
		ZotVersion:      hostVersion, // rename:keep — frozen wire field
		TervaVersion:    hostVersion,
		Provider:        m.provider,
		Model:           m.model,
		CWD:             m.cwd,
		ExtensionDir:    ext.Dir,
		DataDir:         dataDir,
	}); err != nil {
		// Tear down the writer goroutine we just started so it doesn't
		// leak on this failed spawn.
		ext.quitOnce.Do(func() { close(ext.quit) })
		_ = cmd.Process.Kill()
		return fmt.Errorf("send hello_ack: %w", err)
	}

	// Spin up the read loop now that the handshake is done.
	go m.readLoop(ext, reader)

	// Compatibility shim: extensions built against the phase-1 SDK
	// don't send a ready frame. Watch the read loop's frame arrival
	// rate; if nothing's arrived for readyIdleWindow we treat the
	// extension as ready so WaitForReady doesn't burn the full grace
	// on every startup. Newer extensions still trigger the explicit
	// path on their own ready frame.
	go m.assumeReadyAfterIdle(ext)

	return nil
}

// readyIdleWindow is how long the manager waits for a frame after
// hello before assuming an extension that doesn't send `ready` is
// nevertheless ready. 250ms is enough for any well-behaved native
// binary to flush its register frames; slow runtimes (npx tsx) flush
// even faster once they've started, so this rarely affects them.
const readyIdleWindow = 250 * time.Millisecond

// helloTimeout bounds how long spawn() waits for an extension's hello
// frame before giving up on it. Kept consistent with the 3s ready
// grace terva uses elsewhere (see WaitForReady / Reload) so a slow but
// legitimate runtime cold-start still has room to print hello. Past
// this the subprocess is killed and the extension is skipped without
// blocking the rest of Discover.
const helloTimeout = 3 * time.Second

func (m *Manager) assumeReadyAfterIdle(ext *Extension) {
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

// readLoop processes every frame the extension sends after hello.
// Returns when stdout closes.
func (m *Manager) readLoop(ext *Extension, reader *bufio.Reader) {
	defer func() {
		// On close, drop every command + tool this extension owned so
		// future invocations don't dangle. The subprocess is gone; we
		// won't hear back about its commands or tool calls anymore.
		m.mu.Lock()
		for name, owner := range m.commandIndex {
			if owner == ext {
				delete(m.commandIndex, name)
			}
		}
		for name, owner := range m.toolIndex {
			if owner == ext {
				delete(m.toolIndex, name)
			}
		}
		m.mu.Unlock()
		ext.readyOnce.Do(func() { close(ext.readyCh) })
		fmt.Fprintf(ext.logFile, "[terva] extension %s read loop exited at %s\n", ext.Manifest.Name, time.Now().Format(time.RFC3339))
		// Crash surfacing: if the read loop ended without the host
		// asking the extension to stop, the subprocess died on its own
		// (crash, panic, killed externally). Its tools and commands
		// were just dropped from the indexes above, so calls would now
		// fail as "unknown" with no explanation. Tell the user once,
		// and point them at the log (stderr is already tee'd there).
		if !ext.stopping.Load() && m.hooks != nil {
			m.hooks.Notify(ext.Manifest.Name, "error",
				fmt.Sprintf("extension %q exited unexpectedly; its tools and commands are unavailable (see %s)", ext.Manifest.Name, ext.LogPath))
		}
	}()

	for {
		line, tooLong, rerr := extproto.ReadFrame(reader)
		if tooLong {
			fmt.Fprintf(ext.logFile, "[terva] dropped oversized frame from extension %s (>%d bytes); skipping\n", ext.Manifest.Name, extproto.MaxFrameBytes)
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
			continue
		}
		switch frame.Type {
		case "register_command":
			var rc extproto.RegisterCommandFromExt
			if err := json.Unmarshal(line, &rc); err == nil {
				// Both ext.commands and m.commandIndex are read by
				// the public Commands() / HasCommand() helpers under
				// m.mu, so the writes have to take the same lock to
				// keep the race detector happy.
				m.mu.Lock()
				ext.commands = append(ext.commands, rc)
				if _, exists := m.commandIndex[rc.Name]; !exists {
					m.commandIndex[rc.Name] = ext
				}
				m.mu.Unlock()
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
			m.mu.Lock()
			ext.tools = append(ext.tools, rt)
			if _, exists := m.toolIndex[rt.Name]; !exists {
				m.toolIndex[rt.Name] = ext
			}
			m.mu.Unlock()
		case "ready":
			ext.readyOnce.Do(func() { close(ext.readyCh) })
		case "subscribe":
			var sub extproto.SubscribeFromExt
			if err := json.Unmarshal(line, &sub); err == nil {
				ext.mu.Lock()
				for _, ev := range sub.Events {
					ext.eventSubs[ev] = struct{}{}
				}
				for _, ev := range sub.Intercept {
					switch ev {
					case "tool_call", "turn_start", "assistant_message":
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
				m.hooks.Notify(ext.Manifest.Name, n.Level, n.Message)
			}
		case "clear_notes":
			m.hooks.ClearNotes(ext.Manifest.Name)
		case "submit_slash":
			// Spontaneous request to invoke a slash command in the
			// TUI. Refused unless the payload looks like a slash
			// command so a misbehaving extension can't sneak a model
			// prompt through this path.
			var s extproto.SubmitSlashFromExt
			if err := json.Unmarshal(line, &s); err == nil {
				text := strings.TrimSpace(s.Text)
				if strings.HasPrefix(text, "/") {
					m.hooks.SubmitSlash(text)
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
				m.hooks.OpenPanel(ext.Manifest.Name, op.Panel)
			}
		case "panel_render":
			var pr extproto.PanelRenderFromExt
			if err := json.Unmarshal(line, &pr); err == nil {
				m.hooks.UpdatePanel(ext.Manifest.Name, pr.PanelID, pr.Title, pr.Lines, pr.Footer)
			}
		case "panel_close":
			var pc extproto.PanelCloseFromExt
			if err := json.Unmarshal(line, &pc); err == nil {
				m.hooks.ClosePanel(ext.Manifest.Name, pc.PanelID)
			}
		case "register_context":
			var rc extproto.RegisterContextFromExt
			if err := json.Unmarshal(line, &rc); err == nil {
				ext.mu.Lock()
				ext.staticContext = rc.Text
				ext.mu.Unlock()
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
				if m.hooks != nil {
					m.hooks.RefreshStatus()
				}
			}
		case "shutdown_ack":
			// Caller of Stop is waiting on the process exit, not this frame.
		default:
			fmt.Fprintf(ext.logFile, "[terva] unknown frame type %q\n", frame.Type)
		}
	}
}

// Commands returns a snapshot of every (extension, command) pair
// currently registered. Used by the slash autocomplete + /help.
func (m *Manager) Commands() []CommandInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []CommandInfo
	for _, ext := range m.ext {
		for _, c := range ext.commands {
			out = append(out, CommandInfo{
				Extension:   ext.Manifest.Name,
				Name:        c.Name,
				Description: c.Description,
			})
		}
	}
	return out
}

// CommandInfo is one extension-registered slash command, surfaced to
// the rest of terva for display purposes.
type CommandInfo struct {
	Extension   string
	Name        string
	Description string
}

// ToolInfo is one extension-registered tool. Used by the agent's
// build step to materialise core.Tool wrappers.
type ToolInfo struct {
	Extension   string
	Name        string
	Description string
	Schema      json.RawMessage
	// ReadOnly carries the extension's no-side-effects declaration
	// (register_tool read_only) into the host's permission
	// classification.
	ReadOnly bool
}

// Tools returns a snapshot of every (extension, tool) pair currently
// registered. Used at agent-build time to fold extension tools into
// the runtime tool registry.
func (m *Manager) Tools() []ToolInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []ToolInfo
	for _, ext := range m.ext {
		for _, t := range ext.tools {
			out = append(out, ToolInfo{
				Extension:   ext.Manifest.Name,
				Name:        t.Name,
				Description: t.Description,
				Schema:      t.Schema,
				ReadOnly:    t.ReadOnly,
			})
		}
	}
	return out
}

// HasTool reports whether name is registered by any extension.
func (m *Manager) HasTool(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.toolIndex[name]
	return ok
}

// InvokeTool sends a tool_call to the owning extension and waits for
// the matching tool_result. Used by the core.Tool wrapper that the
// agent registers per extension-defined tool.
func (m *Manager) InvokeTool(ctx context.Context, name string, args json.RawMessage, timeout time.Duration) (extproto.ToolResultFromExt, error) {
	m.mu.RLock()
	ext, ok := m.toolIndex[name]
	m.mu.RUnlock()
	if !ok {
		return extproto.ToolResultFromExt{}, fmt.Errorf("no extension registered for tool %q", name)
	}

	// Cap the outbound args. A frame larger than the extension's read
	// limit would kill it mid-read (and InvokeTool would only find out
	// when it timed out, ~60s later). Return a normal tool error the
	// model can recover from instead, and never send the oversized frame.
	if len(args) > extproto.MaxToolCallBytes {
		return extproto.ToolResultFromExt{
			IsError: true,
			Content: []extproto.ContentBlock{{
				Type: "text",
				Text: fmt.Sprintf("tool %q arguments are %d bytes; the extension frame limit is %d bytes — send less data",
					name, len(args), extproto.MaxToolCallBytes),
			}},
		}, nil
	}

	id := newCorrelationID()
	ch := make(chan extproto.ToolResultFromExt, 1)
	ext.mu.Lock()
	ext.pendingTool[id] = ch
	ext.mu.Unlock()

	if err := ext.writeFrame(extproto.ToolCallFromHost{
		Type: "tool_call",
		ID:   id,
		Name: name,
		Args: args,
	}); err != nil {
		ext.mu.Lock()
		delete(ext.pendingTool, id)
		ext.mu.Unlock()
		return extproto.ToolResultFromExt{}, fmt.Errorf("write: %w", err)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(timeout):
		ext.mu.Lock()
		delete(ext.pendingTool, id)
		ext.mu.Unlock()
		return extproto.ToolResultFromExt{}, fmt.Errorf("timeout waiting for %s/%s", ext.Manifest.Name, name)
	case <-ctx.Done():
		ext.mu.Lock()
		delete(ext.pendingTool, id)
		ext.mu.Unlock()
		return extproto.ToolResultFromExt{}, ctx.Err()
	}
}

// HasCommand reports whether name is registered by any extension.
func (m *Manager) HasCommand(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.commandIndex[name]
	return ok
}

func (m *Manager) CommandOwner(name string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if ext, ok := m.commandIndex[name]; ok && ext != nil {
		return ext.Manifest.Name
	}
	return ""
}

// Invoke fires the named slash command's handler in the owning
// extension and waits up to timeout for the response. Returns the
// extension's CommandResponse so the caller can act on the action
// (prompt / insert / display).
func (m *Manager) Invoke(ctx context.Context, name, args string, timeout time.Duration) (extproto.CommandResponseFromExt, error) {
	m.mu.RLock()
	ext, ok := m.commandIndex[name]
	m.mu.RUnlock()
	if !ok {
		return extproto.CommandResponseFromExt{}, fmt.Errorf("no extension registered for /%s", name)
	}

	id := newCorrelationID()
	ch := make(chan extproto.CommandResponseFromExt, 1)
	ext.mu.Lock()
	ext.pending[id] = ch
	ext.mu.Unlock()

	if err := ext.writeFrame(extproto.CommandInvokedFromHost{
		Type: "command_invoked",
		ID:   id,
		Name: name,
		Args: args,
	}); err != nil {
		ext.mu.Lock()
		delete(ext.pending, id)
		ext.mu.Unlock()
		return extproto.CommandResponseFromExt{}, fmt.Errorf("write: %w", err)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(timeout):
		ext.mu.Lock()
		delete(ext.pending, id)
		ext.mu.Unlock()
		return extproto.CommandResponseFromExt{}, fmt.Errorf("timeout waiting for %s/%s", ext.Manifest.Name, name)
	case <-ctx.Done():
		ext.mu.Lock()
		delete(ext.pending, id)
		ext.mu.Unlock()
		return extproto.CommandResponseFromExt{}, ctx.Err()
	}
}

// Stop cleanly terminates every extension. Sends ShutdownFromHost,
// waits up to gracePeriod for each subprocess to exit, then SIGTERMs
// (and SIGKILLs after another second) the holdouts.
func (m *Manager) SendPanelKey(extName, panelID, key, text string) error {
	m.mu.RLock()
	ext, ok := m.ext[extName]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("no extension %q", extName)
	}
	return ext.writeFrame(extproto.PanelKeyFromHost{Type: "panel_key", PanelID: panelID, Key: key, Text: text})
}

func (m *Manager) SendPanelClose(extName, panelID string) error {
	m.mu.RLock()
	ext, ok := m.ext[extName]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("no extension %q", extName)
	}
	return ext.writeFrame(extproto.PanelCloseFromHost{Type: "panel_close", PanelID: panelID})
}

func (m *Manager) Stop(gracePeriod time.Duration) {
	m.mu.RLock()
	exts := make([]*Extension, 0, len(m.ext))
	for _, e := range m.ext {
		exts = append(exts, e)
	}
	m.mu.RUnlock()
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

// All returns every extension currently tracked, enabled or not.
// Used by `terva ext list`.
func (m *Manager) All() []*Extension {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Extension, 0, len(m.ext))
	for _, e := range m.ext {
		out = append(out, e)
	}
	return out
}

// newCorrelationID returns a short non-cryptographic id. We don't
// need uniqueness across processes, just within the lifetime of one
// extension's pending map.
// corrSeq makes correlation IDs collision-free across concurrent
// callers. The wall-clock prefix alone (microsecond resolution) was not
// unique enough: a tight burst of concurrent InvokeTool / intercept /
// command calls could mint the same ID within one microsecond, and the
// second registration would overwrite the first's reply channel in the
// pending map — so one caller received the other's response (or none)
// and hung. See the frame-integrity test in manager_test.go.
var corrSeq atomic.Uint64

// newCorrelationID returns a process-unique id for matching a host
// request to its extension response. The timestamp keeps ids readable
// in the per-extension log; the atomic counter guarantees uniqueness.
func newCorrelationID() string {
	ts := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
	return ts + "-" + strconv.FormatUint(corrSeq.Add(1), 10)
}
