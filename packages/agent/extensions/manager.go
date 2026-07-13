// Package extensions implements the host integration layer of terva's
// subprocess extension protocol. It discovers extensions in well-known
// directories, gates them on Workspace Trust and config, and hands each
// one to the embedded extdriver.Driver — the authoritative wire — which
// spawns the subprocess, completes the hello handshake, and routes
// slash commands / tool calls. On top of the wire, this package adds
// the agent-registry tool wrapper (tool.go) and theme-option discovery.
//
// Each extension is its own process, communicating with terva over its
// stdin/stdout in newline-delimited JSON. Stderr is redirected to a
// per-extension log file under $TERVA_HOME/logs/. Crashing one
// extension does not affect the others or the host.
//
// See docs/extensions.md for the user-facing reference,
// packages/agent/extproto for the wire format, and
// packages/agent/extdriver for the wire driver this layer embeds.
package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/extdriver"
	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/envcompat"
)

// Re-exported driver types so existing callers (cli.go, rpc, acp,
// swarm) and the package's own tests keep referring to them as
// extensions.* after the wire moved to extdriver. These are aliases,
// not new types, so an extdriver.Extension IS an extensions.Extension.
type (
	HostHooks   = extdriver.HostHooks
	Manifest    = extdriver.Manifest
	Extension   = extdriver.Extension
	ToolInfo    = extdriver.ToolInfo
	CommandInfo = extdriver.CommandInfo
	ContextItem = extdriver.ContextItem
)

// Manager owns every extension for the lifetime of terva. It embeds the
// wire Driver (so InvokeTool / EmitEvent / Tools / Stop / WaitForReady /
// the context aggregation and the rest of the wire surface are promoted
// unchanged to callers) and adds discovery, Workspace Trust gating,
// config-driven load-disable, theme options, and reload orchestration.
type Manager struct {
	*extdriver.Driver

	cwd string

	// mu guards the host-integration policy state below, kept separate
	// from the Driver's own lock.
	mu sync.RWMutex

	// explicitPaths remembers ad-hoc paths passed via --ext so
	// Reload can respawn them alongside the discovered set.
	explicitPaths []string

	// onReload, if set, is invoked after a successful Reload. Used
	// by the host so it can rebuild the agent's tool registry with
	// the freshly-registered extension tools.
	onReload func()

	// disabledExtensions is the set of extension names that must not be
	// loaded at all (resolved user ∪ project config). Consulted in
	// loadOne, so it MUST be set via SetDisabledExtensions BEFORE
	// Discover / LoadExplicit. Guarded by mu.
	disabledExtensions map[string]bool

	// allowedExtensions is the per-run allowlist (--extensions): nil =
	// no allowlist, otherwise only DISCOVERED extensions named here
	// load; explicit --ext paths bypass it and disabledExtensions
	// still subtracts. Set via SetAllowedExtensions BEFORE Discover.
	// Guarded by mu.
	allowedExtensions map[string]bool

	// projectTrusted is the Workspace Trust verdict for m.cwd. When false
	// (the SAFE DEFAULT — a fresh Manager is untrusted), searchDirs drops
	// the project-local extension roots (the cwd/<project-dir>/extensions
	// paths from ProjectDirNames) so a cloned/untrusted repo's extensions
	// are never spawned. The global root ($TERVA_HOME/extensions) is
	// always searched. Set via SetProjectTrusted BEFORE Discover. Guarded
	// by mu. See docs/plans/workspace-trust.md.
	projectTrusted bool

	// adoptRoot + adoptNames re-admit specific GLOBAL extensions into a
	// project-scoped agent. adoptRoot is the user's global extensions dir;
	// adoptNames is the set the project's adopt_extensions selects. Discover
	// loads those names from adoptRoot — but only when projectTrusted (adopted
	// extensions are project-declared, so they obey the same trust gate) and
	// only for names the project itself didn't already provide. Set via
	// SetAdopt BEFORE Discover. Guarded by mu.
	adoptRoot  string
	adoptNames map[string]bool

	// lastAnnounced is the identity of the session most recently passed to
	// a session_start emit. The host bookends it with session_end when a
	// DIFFERENT session is announced (a switch / close) or at shutdown, so
	// session_start and session_end stay paired. Opaque strings — no core
	// dependency (the deps test forbids it). Guarded by mu.
	lastAnnounced SessionIdentity
}

// SessionIdentity is the core-free snapshot the Manager remembers so it
// can bookend a session_start with a matching session_end. ProjectID is
// precomputed by the caller (it needs core.ProjectKey) so the manager can
// build the event without importing core.
type SessionIdentity struct {
	ID        string
	Path      string
	Title     string
	CWD       string
	ProjectID string
}

// SwapAnnouncedSession records next as the newly-announced session and
// returns the previously-announced one when it was a different, non-empty
// session — the one the host should now bookend with session_end before
// announcing next. A repeat of the same id (e.g. a /cd that re-announces
// the same session with a new cwd) returns changed=false.
func (m *Manager) SwapAnnouncedSession(next SessionIdentity) (prev SessionIdentity, changed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev = m.lastAnnounced
	changed = prev.ID != "" && prev.ID != next.ID
	m.lastAnnounced = next
	return prev, changed
}

// TakeAnnouncedSession returns the last-announced session and clears it,
// so a shutdown path emits its final session_end exactly once (a later
// call, or a session already ended by a switch, returns ok=false).
func (m *Manager) TakeAnnouncedSession() (prev SessionIdentity, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev = m.lastAnnounced
	m.lastAnnounced = SessionIdentity{}
	return prev, prev.ID != ""
}

// EmitSessionEnd fires a session_end lifecycle event for an ending
// session — the bookend to session_start. Best-effort: a hard kill skips
// it, so an extension must still persist incrementally and treat this as
// a flush point, not a durability guarantee. A no-op for an empty id.
func (m *Manager) EmitSessionEnd(s SessionIdentity) {
	if s.ID == "" {
		return
	}
	m.EmitEvent(extproto.EventFromHost{
		Event:        extproto.EventSessionEnd,
		SessionID:    s.ID,
		SessionPath:  s.Path,
		SessionTitle: s.Title,
		CWD:          s.CWD,
		ProjectID:    s.ProjectID,
	})
}

// Stop emits a final session_end for the active session (the bookend to
// session_start) and then tears down every extension. Overrides the
// promoted Driver.Stop so every shutdown path gets the event without
// touching each call site; the session_end is enqueued before the
// shutdown frame on the same FIFO outbox, so a healthy extension sees it
// before exiting.
func (m *Manager) Stop(grace time.Duration) {
	if s, ok := m.TakeAnnouncedSession(); ok {
		m.EmitSessionEnd(s)
	}
	m.Driver.Stop(grace)
}

// New constructs an empty Manager wrapping a fresh wire Driver. Call
// Discover to populate it from the on-disk extension directories.
func New(tervaHome, cwd, tervaVersion, provider, model string, hooks HostHooks) *Manager {
	return &Manager{
		Driver: extdriver.New(tervaHome, cwd, tervaVersion, provider, model, hooks),
		cwd:    cwd,
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

	// Project-scoped adoption: pull specific extensions from the user's GLOBAL
	// install into this project. Trust-gated (adoptTargets returns nothing when
	// untrusted) and lower priority than the project's own — a name the project
	// already provides wins, and a name not installed globally is skipped
	// (gracefully absent rather than an error).
	if adoptRoot, adoptNames := m.adoptTargets(); adoptRoot != "" {
		for _, name := range adoptNames {
			// Defense in depth: SetAdopt already drops non-plain names, but
			// re-check before the Join so a traversal name can never escape
			// adoptRoot even if a future caller populates adoptNames directly.
			if seenDirs[name] || !isPlainExtensionName(name) {
				continue
			}
			dir := filepath.Join(adoptRoot, name)
			if st, err := os.Stat(dir); err != nil || !st.IsDir() {
				continue
			}
			seenDirs[name] = true
			jobs = append(jobs, loadJob{dir: dir})
		}
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(jobs))
	for _, j := range jobs {
		wg.Add(1)
		go func(extDir string) {
			defer wg.Done()
			if err := m.loadOne(ctx, extDir, false); err != nil {
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
	if home := m.TervaHome(); home != "" {
		dirs = append(dirs, filepath.Join(home, "extensions"))
	}
	return dirs
}

// reservedExtensionName rejects identifiers that would collide with the tool
// capability-group namespace (lazy tool visibility, retro H2·b). An extension
// tool's group IS its extension name (core.ToolGroup via the Extension()
// accessor), so an extension named "core" would land in the always-visible
// built-in group — bypassing lazy hiding — and an "mcp:"-prefixed name would
// share an activation group and /context attribution with a real MCP server.
// No authority is at stake (permissions are per-tool), but visibility and
// attribution must stay unambiguous. The literals mirror core.CoreToolGroup
// and the MCP group prefix; this package deliberately does not import core
// (TestExtensionsDependencyBoundary), so a lockstep test pins them instead.
func reservedExtensionName(name string) string {
	if name == "core" {
		return "it names the always-visible built-in tool group"
	}
	if strings.HasPrefix(name, "mcp:") {
		return `the "mcp:" prefix names MCP server tool groups`
	}
	return ""
}

// isPlainExtensionName reports whether name is a single, safe path element to
// join under the global extensions root. adopt_extensions comes from the
// project's (untrusted) config, so a crafted "../../evil" or "/abs/path" must
// never resolve outside adoptRoot when filepath.Join'd in Discover. Accepts
// only a bare directory name: non-empty, not "."/"..", no separators, not
// absolute.
func isPlainExtensionName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if filepath.IsAbs(name) || strings.ContainsAny(name, `/\`) {
		return false
	}
	return filepath.Base(name) == name
}

// loadOne reads a single extension's manifest, applies the host policy
// (enabled, not load-disabled, allowlisted unless explicitly pathed),
// and hands an enabled extension to the driver to spawn + handshake.
// Trust gating already happened in searchDirs (project roots are
// dropped for an untrusted cwd). explicit marks a --ext path load,
// which bypasses the --extensions allowlist: pointing at a directory
// is already explicit consent.
func (m *Manager) loadOne(ctx context.Context, dir string, explicit bool) error {
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
	if !isPlainExtensionName(mf.Name) {
		// The manifest name becomes a map key and is joined into the host's
		// ext-data/<name> and ext-<name>.log paths (extdriver). A value with
		// path separators, "."/"..", or an absolute path could make those
		// host-created paths escape their intended directory, so reject it
		// with the same plain-element rule adopt-list names must satisfy.
		return fmt.Errorf("manifest: name %q must be a plain identifier (no path separators, %q, %q, or absolute paths)", mf.Name, ".", "..")
	}
	if why := reservedExtensionName(mf.Name); why != "" {
		return fmt.Errorf("manifest: name %q is reserved: %s", mf.Name, why)
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
	if !explicit && !m.extensionAllowlisted(mf.Name) {
		// Outside this run's --extensions allowlist: never spawned. One
		// line per skip so a scoped bot's startup log doubles as an
		// audit of what was kept out.
		fmt.Fprintf(os.Stderr, "extension %q not loaded (not in the --extensions allowlist)\n", mf.Name)
		return nil
	}
	return m.Driver.Load(ctx, dir, mf)
}

// ApplyOne surgically brings extension `name` into the desired running
// state WITHOUT touching any other extension: it loads the one (when want
// and a matching, enabled, not-config-disabled manifest exists) or stops
// it gracefully and silently. It then fires onReload so the host rebuilds
// its tool registry. Used by the /extensions dialog so a single toggle
// doesn't tear down and respawn the whole set (which both spends time and,
// via overlapping reloads, can orphan instances into spurious "exited
// unexpectedly" notices). Callers should SetDisabledExtensions with the
// fresh config first so loadOne honors a just-changed disable.
func (m *Manager) ApplyOne(ctx context.Context, name string, want bool, grace time.Duration) {
	if want {
		for _, root := range m.searchDirs() {
			dir := filepath.Join(root, name)
			if _, err := os.Stat(filepath.Join(dir, "extension.json")); err != nil {
				continue
			}
			_ = m.loadOne(ctx, dir, false) // idempotent (Driver.Load skips a dup) and policy-checked
			break                          // first match wins, mirroring searchDirs precedence
		}
		// Let a freshly spawned extension register before the host rebuilds
		// its tool registry below.
		m.Driver.WaitForReady(grace)
	} else {
		m.Driver.StopByName(name, grace)
	}

	m.mu.RLock()
	cb := m.onReload
	m.mu.RUnlock()
	if cb != nil {
		cb()
	}
}

// RestartExtension stops (or reaps, when it already crashed) extension
// `name` and respawns it from its installed manifest — the crash-
// recovery primitive behind chat/extconn's restart budget for connector
// extensions, whose Receive respawns a dead dual-role process instead
// of ending the bot loop. Safe for the agent's tool registry: extension
// tools dispatch BY NAME through the driver's live index at call time
// (exttool → InvokeTool), so a respawn of the same binary re-binds the
// existing wrappers with no registry surgery; onReload still fires (via
// ApplyOne) so an interactive host also picks up any registration
// drift. Returns an error when the extension isn't installed in a
// search dir anymore or never comes back ready.
func (m *Manager) RestartExtension(ctx context.Context, name string) error {
	// A crashed extension stays in the loaded set (only its tools and
	// commands were dropped), and Load dup-skips a claimed name — so the
	// stop is what makes the respawn possible. Deliberate teardown: no
	// "exited unexpectedly" notice for a process we're about to revive.
	m.Driver.StopByName(name, 2*time.Second)
	m.ApplyOne(ctx, name, true, 3*time.Second)
	ext, ok := m.Driver.ExtensionByName(name)
	if !ok {
		return fmt.Errorf("extension %q did not respawn (uninstalled, disabled, or failed to load — see its log)", name)
	}
	if !ext.Ready() {
		return fmt.Errorf("extension %q respawned but never signalled ready (see its log)", name)
	}
	return nil
}

// LoadExplicit loads each path as an ad-hoc extension. Used for
// `terva --ext <path>` so extension authors can iterate on a working
// copy without having to `terva ext install` after every change.
//
// Loaded BEFORE Discover so explicit paths win on name conflicts
// against installed extensions. Spawns happen in parallel like the
// regular discovery path; errors are returned per path.
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
			if err := m.loadOne(ctx, extDir, true); err != nil {
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

// hasExtensionTheme reports whether dir ships a theme file, so a
// theme-only extension (no exec) is allowed to load.
func hasExtensionTheme(dir string) bool {
	for _, file := range []string{"theme.json", filepath.Join("themes", "theme.json")} {
		if _, err := os.Stat(filepath.Join(dir, file)); err == nil {
			return true
		}
	}
	return false
}

// Theme-option discovery moved up to the tui-aware host
// (agent.extensionThemeOptions): it iterates Extensions() — the neutral
// per-extension dir + name the driver already exposes — and builds
// tui.ThemeOption there, so neither this package nor the wire driver
// depends on tui. See docs/plans/extdriver-extraction.md phase 3.

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
// registry. The driver's internal maps are cleared (via Reset) before
// the new load to ensure a clean slate.
//
// Safe to call concurrently with normal host operations: Reset stops
// the old set after clearing the maps, so pending InvokeTool / Invoke
// calls on the old processes get a clean error as their stdin closes.
func (m *Manager) Reload(ctx context.Context, grace time.Duration) ReloadStats {
	stats := ReloadStats{}

	// Snapshot and remember the explicit paths before we wipe state.
	m.mu.Lock()
	explicit := append([]string(nil), m.explicitPaths...)
	m.explicitPaths = nil
	callback := m.onReload
	m.mu.Unlock()

	// Clear the wire state and gracefully stop the old set.
	stats.Stopped = m.Driver.Reset(grace)

	// Fresh load. Explicit paths first (they still win on conflict).
	if errs := m.LoadExplicit(ctx, explicit); len(errs) > 0 {
		stats.Errors = append(stats.Errors, errs...)
	}
	if errs := m.Discover(ctx); len(errs) > 0 {
		stats.Errors = append(stats.Errors, errs...)
	}

	stats.Loaded = m.Driver.Count()

	// Wait for ready frames. Use the same 3s grace terva uses at
	// startup so the reload feels no slower than a cold boot.
	readyDeadline := time.Now().Add(grace)
	if time.Until(readyDeadline) < 3*time.Second {
		readyDeadline = time.Now().Add(3 * time.Second)
	}
	m.Driver.WaitForReady(time.Until(readyDeadline))

	stats.Ready = m.Driver.ReadyCount()

	if callback != nil {
		callback()
	}

	return stats
}
