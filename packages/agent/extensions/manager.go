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
	"sync"
	"time"

	"terva.sh/terva/packages/agent/extdriver"
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

	// projectTrusted is the Workspace Trust verdict for m.cwd. When false
	// (the SAFE DEFAULT — a fresh Manager is untrusted), searchDirs drops
	// the project-local extension roots (the cwd/<project-dir>/extensions
	// paths from ProjectDirNames) so a cloned/untrusted repo's extensions
	// are never spawned. The global root ($TERVA_HOME/extensions) is
	// always searched. Set via SetProjectTrusted BEFORE Discover. Guarded
	// by mu. See docs/plans/workspace-trust.md.
	projectTrusted bool
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
	if home := m.TervaHome(); home != "" {
		dirs = append(dirs, filepath.Join(home, "extensions"))
	}
	return dirs
}

// loadOne reads a single extension's manifest, applies the host policy
// (enabled, not load-disabled), and hands an enabled extension to the
// driver to spawn + handshake. Trust gating already happened in
// searchDirs (project roots are dropped for an untrusted cwd).
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
	return m.Driver.Load(ctx, dir, mf)
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
