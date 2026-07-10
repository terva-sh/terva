package build

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/extdriver"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/mcp"
)

// Live extension/MCP reconfiguration: applying a config or enable/disable change
// to the running managers. They need the live extensions.Manager and the MCP
// adapter, so they sit with agent construction, not with the config schema.

// ResolveExtensionConfig produces the values to deliver to one extension:
// for every field its manifest declares, the user's saved value if present
// (config.json extensions.<name>.<key>), else the field's default. It is
// schema-driven — only declared keys are delivered, so a stale saved key
// (the field was removed) is kept on disk but dropped from delivery.
// Returns nil when the extension declares no schema or nothing resolves.
// Matches extdriver.ConfigResolver so it wires straight into the driver.
func ResolveExtensionConfig(name string, schema []extdriver.ConfigField) map[string]json.RawMessage {
	if len(schema) == 0 {
		return nil
	}
	var stored map[string]json.RawMessage
	if c, err := config.LoadConfig(); err == nil {
		stored = c.Extensions[name]
	}
	out := map[string]json.RawMessage{}
	for _, f := range schema {
		if v, ok := stored[f.Key]; ok && len(v) > 0 {
			out[f.Key] = v
			continue
		}
		if f.Default != nil {
			if b, err := json.Marshal(f.Default); err == nil {
				out[f.Key] = b
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ApplyExtensionConfigLive delivers the just-saved config to a running
// extension as a config_update event. No-op when the extension is stopped
// or didn't subscribe — its new values still arrive in hello_ack on the
// next spawn.
func ApplyExtensionConfigLive(mgr *extensions.Manager, cwd, name string) {
	if mgr == nil {
		return
	}
	mgr.PushConfigUpdate(name, ResolveExtensionConfig(name, ExtensionConfigSchema(cwd, name)))
}

// ApplyExtensionChangeLive applies a just-persisted enable/disable for ONE
// extension: refresh the disabled set, then surgically start or stop only
// that extension (graceful + silent). Every other running extension is
// left untouched — so a single toggle no longer reloads the whole set or
// spams "exited unexpectedly" for its neighbours.
func ApplyExtensionChangeLive(mgr *extensions.Manager, cwd string, trusted bool, name string) {
	if mgr == nil {
		return
	}
	mgr.SetDisabledExtensions(config.UnionDisabledExtensions(cwd))
	want := ExtensionShouldRun(cwd, trusted, name)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	mgr.ApplyOne(ctx, name, want, 2*time.Second)
}

// ApplyMCPChangeLive applies a just-persisted enable/disable for ONE
// server against the live Manager, then fires onReload so the agent's tool
// registry is rebuilt to match (the rebuild re-merges adapter.Tools(), so
// a removed server's tools genuinely disappear). adapter may be nil when
// MCP was skipped for the session (--no-mcp): the config change is still
// persisted and applies next launch.
func ApplyMCPChangeLive(adapter *MCPToolAdapter, cwd string, trusted bool, name string, onReload func()) {
	if adapter == nil || adapter.Mgr == nil {
		return
	}
	// The run's --mcp allowlist gates live enables too: the dialog still
	// persists the config change, but an excluded server won't spawn
	// until a run without the allowlist (mirrors how the extension
	// manager's load policy treats --extensions).
	if config.MCPServerShouldRun(cwd, trusted, name) && adapter.AllowsThisRun(name) {
		if sc, ok := config.ServerConfigFor(cwd, trusted, name); ok {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			_ = adapter.Mgr.StartOne(ctx, name, sc, adapter.StderrFor(name))
		}
	} else {
		adapter.Mgr.StopOne(name)
	}
	if onReload != nil {
		onReload()
	}
}

// ExtensionConfigSchema reads one installed extension's declared config
// schema from its manifest, without spawning it (so the dialog works for a
// stopped or disabled extension). Returns nil when not found or none.
func ExtensionConfigSchema(cwd, name string) []extdriver.ConfigField {
	dir, err := config.FindExtensionDirIn(cwd, name)
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, "extension.json"))
	if err != nil {
		return nil
	}
	var m struct {
		Config []extdriver.ConfigField `json:"config"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m.Config
}

// ExtensionShouldRun reports whether the named extension should be loaded
// under the current on-disk config (manifest enabled, not user/project
// disabled, project trust satisfied). Derived from the same scan the
// dialog lists, so it can't drift from what the row shows.
func ExtensionShouldRun(cwd string, trusted bool, name string) bool {
	for _, info := range ListInstalledExtensions(cwd, trusted, nil) {
		if info.Name == name {
			return info.Effective
		}
	}
	return false
}

// ListInstalledExtensions scans the global and project extension dirs and
// returns each installed extension with enable/disable + live state.
// cwd scopes the project dir + project config; trusted gates whether
// project extensions can load; mgr (may be nil) supplies ground-truth
// running state and provide-counts from the live driver.
func ListInstalledExtensions(cwd string, trusted bool, mgr *extensions.Manager) []extensions.Info {
	userDisabled := map[string]bool{}
	if c, err := config.LoadConfig(); err == nil {
		for _, n := range c.DisableExtensions {
			userDisabled[n] = true
		}
	}
	projDisabled := map[string]bool{}
	if pc, err := config.LoadProjectConfig(cwd); err == nil && pc != nil {
		for _, n := range pc.DisableExtensions {
			projDisabled[n] = true
		}
	}

	// Ground truth from the live manager (embedded extdriver.Driver):
	// which names are ready, and how many commands/tools each registered.
	ready := map[string]bool{}
	cmdCount := map[string]int{}
	toolCount := map[string]int{}
	if mgr != nil {
		for _, e := range mgr.All() {
			ready[e.Manifest.Name] = e.Ready()
		}
		for _, c := range mgr.Commands() {
			cmdCount[c.Extension]++
		}
		for _, t := range mgr.Tools() {
			toolCount[t.Extension]++
		}
	}

	type scoped struct{ scope, dir string }
	roots := []scoped{{"global", filepath.Join(config.TervaHome(), "extensions")}}
	if cwd != "" {
		roots = append(roots, scoped{"project", filepath.Join(cwd, ".terva", "extensions")})
	}

	var out []extensions.Info
	seen := map[string]bool{}
	for _, r := range roots {
		entries, err := os.ReadDir(r.dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(r.dir, e.Name(), "extension.json"))
			if err != nil {
				continue
			}
			var m struct {
				Name        string            `json:"name"`
				Version     string            `json:"version"`
				Language    string            `json:"language"`
				Description string            `json:"description"`
				Enabled     *bool             `json:"enabled"`
				Config      []json.RawMessage `json:"config"`
			}
			if err := json.Unmarshal(raw, &m); err != nil || m.Name == "" {
				continue
			}
			globalEnabled := m.Enabled == nil || *m.Enabled
			projectGated := r.scope == "project" && !trusted
			info := extensions.Info{
				Name:               m.Name,
				Version:            m.Version,
				Language:           m.Language,
				Description:        m.Description,
				Scope:              r.scope,
				GlobalEnabled:      globalEnabled,
				ProjectDisabled:    projDisabled[m.Name],
				UserConfigDisabled: userDisabled[m.Name],
				ProjectGated:       projectGated,
				Effective:          globalEnabled && !projDisabled[m.Name] && !userDisabled[m.Name] && !projectGated,
				Running:            ready[m.Name],
				Commands:           cmdCount[m.Name],
				Tools:              toolCount[m.Name],
				HasConfig:          len(m.Config) > 0,
			}
			if _, err := os.Stat(filepath.Join(config.TervaHome(), "logs", "ext-"+m.Name+".log")); err == nil {
				info.HasLog = true
			}
			// Only when it SHOULD be running but isn't (crashed) — not for a
			// deliberately-disabled extension, whose state label says so.
			if info.Effective && !info.Running && info.HasLog {
				info.LastLog = lastLogReason("ext", m.Name)
			}
			seen[m.Name] = true
			out = append(out, info)
		}
	}

	// Explicit --ext loads live outside the install roots, so the scan above
	// never emits them. Surface any extension the manager is actually running
	// that the roots didn't account for, so a `terva --ext .` dev extension
	// shows up in /extensions alongside the installed ones.
	if mgr != nil {
		live := make([]sessionExt, 0)
		for _, e := range mgr.All() {
			live = append(live, sessionExt{
				Name:        e.Manifest.Name,
				Version:     e.Manifest.Version,
				Language:    e.Manifest.Language,
				Description: e.Manifest.Description,
				LogPath:     e.LogPath,
				Ready:       e.Ready(),
			})
		}
		out = appendUnrootedExtensions(out, seen, live, cmdCount, toolCount)
	}
	return out
}

// ListMCPServers builds the /mcp dialog's row set from the configured
// servers (user, scope "global"; project, scope "project"; user wins on a
// name collision), overlays the live Manager's connection/tool state and
// any startup warnings, and resolves the disable flags. It is config-
// driven on purpose: the Manager only knows servers it actually started,
// so a disabled or failed server would vanish if we listed from the
// Manager. mgr may be nil (--no-mcp): everything shows not-connected.
func ListMCPServers(cwd string, trusted bool, mgr *mcp.Manager) []mcp.Info {
	user, _ := config.LoadConfig()
	userDisabled := map[string]bool{}
	for _, n := range user.DisableMCP {
		userDisabled[n] = true
	}
	projDisabled := map[string]bool{}
	var projServers map[string]mcp.ServerConfig
	if pc, err := config.LoadProjectConfig(cwd); err == nil && pc != nil {
		for _, n := range pc.DisableMCP {
			projDisabled[n] = true
		}
		if pc.MCP != nil {
			projServers = pc.MCP.Servers
		}
	}

	rows := map[string]*mcp.Info{}
	add := func(name, scope, desc string, gated bool) {
		rows[name] = &mcp.Info{
			Name:            name,
			Scope:           scope,
			Description:     desc,
			UserDisabled:    userDisabled[name],
			ProjectDisabled: projDisabled[name],
			ProjectGated:    gated,
		}
	}
	// Project servers are read straight from the project config (NOT
	// trustedProjectMCP) so an untrusted workspace's servers still appear
	// as "off (untrusted)" rather than silently vanishing. The user pass
	// runs second so a same-named user server wins (scope "global").
	for name, sc := range projServers {
		add(name, "project", describeServer(sc), !trusted)
	}
	if user.MCP != nil {
		for name, sc := range user.MCP.Servers {
			add(name, "global", describeServer(sc), false)
		}
	}

	if mgr != nil {
		for _, st := range mgr.Status() {
			if r, ok := rows[st.Name]; ok {
				r.Connected = true
				r.Tools = st.ToolCount
				if st.Info != "" {
					r.Description = st.Info
				}
			}
		}
		for _, w := range mgr.Warnings() {
			if name, msg, ok := splitServerWarning(w); ok {
				if r, ok := rows[name]; ok && r.StartupError == "" {
					r.StartupError = msg
				}
			}
		}
	}

	names := make([]string, 0, len(rows))
	for n := range rows {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]mcp.Info, 0, len(names))
	for _, name := range names {
		r := rows[name]
		r.Effective = !r.UserDisabled && !r.ProjectDisabled && !r.ProjectGated
		if _, err := os.Stat(filepath.Join(config.LogsPath(), "mcp-"+name+".log")); err == nil {
			r.HasLog = true
		}
		// When the server should run but isn't connected and the manager
		// recorded no warning, fall back to the log tail for the reason.
		if r.Effective && !r.Connected && r.StartupError == "" && r.HasLog {
			r.StartupError = lastLogReason("mcp", name)
		}
		out = append(out, *r)
	}
	return out
}

// sessionExt is the minimal view of a live extension that
// appendUnrootedExtensions needs — decoupled from extdriver.Extension (whose
// readiness lives behind unexported state) so the dedup logic is unit-testable.
type sessionExt struct {
	Name, Version, Language, Description, LogPath string
	Ready                                         bool
}

// appendUnrootedExtensions adds a "session"-scoped row for each live extension
// whose name the install-root scan didn't already emit (tracked in seen). These
// are the explicit --ext loads: loaded by path for this run only, not installed,
// so they carry no persistent enable/disable state to toggle.
func appendUnrootedExtensions(out []extensions.Info, seen map[string]bool, live []sessionExt, cmdCount, toolCount map[string]int) []extensions.Info {
	for _, e := range live {
		if e.Name == "" || seen[e.Name] {
			continue
		}
		seen[e.Name] = true
		info := extensions.Info{
			Name:          e.Name,
			Version:       e.Version,
			Language:      e.Language,
			Description:   e.Description,
			Scope:         "session",
			GlobalEnabled: true, // loaded explicitly for this run
			Effective:     e.Ready,
			Running:       e.Ready,
			Commands:      cmdCount[e.Name],
			Tools:         toolCount[e.Name],
		}
		if e.LogPath != "" {
			if _, err := os.Stat(e.LogPath); err == nil {
				info.HasLog = true
			}
		}
		out = append(out, info)
	}
	return out
}

// describeServer is the one-line server description for the detail row:
// the launch command (and args), used until the server reports its own
// name at initialize.
func describeServer(sc mcp.ServerConfig) string {
	if sc.Command == "" {
		return ""
	}
	if len(sc.Args) > 0 {
		return sc.Command + " " + strings.Join(sc.Args, " ")
	}
	return sc.Command
}

// splitServerWarning parses a "mcp server <name>: <msg>" warning back into
// its server name and message. ok is false for any other warning shape.
func splitServerWarning(w string) (name, msg string, ok bool) {
	const pre = "mcp server "
	if !strings.HasPrefix(w, pre) {
		return "", "", false
	}
	name, msg, ok = strings.Cut(w[len(pre):], ": ")
	if !ok {
		return "", "", false
	}
	return name, msg, true
}

// lastLogReason is the one-line "why it's off" hint for a row: the last
// line that isn't one of terva's own "[terva] …" host annotations (i.e.
// the extension/server's own stderr — usually the actual error), falling
// back to the last non-empty line. Capped so a giant line can't blow up
// the row. Empty when there's no log.
func lastLogReason(kind, name string) string {
	lines := logTailLines(logPathFor(kind, name), 50)
	var fallback string
	for i := len(lines) - 1; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if t == "" {
			continue
		}
		if fallback == "" {
			fallback = t
		}
		if !strings.HasPrefix(t, "[terva]") {
			return capLine(t)
		}
	}
	return capLine(fallback)
}
