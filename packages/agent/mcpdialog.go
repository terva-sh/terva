package agent

// Backs the interactive /mcp dialog (modes.mcpDialog): the configured
// server scan with live state, the two persistence surfaces it toggles
// (user disable_mcp and project disable_mcp), and a live start/stop.
// Plumbed into modes as InteractiveConfig callbacks so that package never
// imports this one — the same shape as the /extensions plumbing in
// extdialog.go. Unlike extensions there is no per-server manifest:
// "enabled" simply means the name is absent from disable_mcp.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/mcp"
	"terva.sh/terva/packages/agent/modes"
)

// resolvedDisableMCP is the resolved disable set (user ∪ restrict-only
// project) read fresh from disk — what setupMCP, the ACP MCP block, and a
// live toggle must honor. Project entries are honored even when untrusted
// (refusing to spawn is always safe), matching ResolveConfig.
func resolvedDisableMCP(cwd string, trusted bool) map[string]bool {
	eff := ResolveConfig(cwd, trusted)
	set := make(map[string]bool, len(eff.Config.DisableMCP))
	for _, n := range eff.Config.DisableMCP {
		set[n] = true
	}
	return set
}

// listMCPServers builds the /mcp dialog's row set from the configured
// servers (user, scope "global"; project, scope "project"; user wins on a
// name collision), overlays the live Manager's connection/tool state and
// any startup warnings, and resolves the disable flags. It is config-
// driven on purpose: the Manager only knows servers it actually started,
// so a disabled or failed server would vanish if we listed from the
// Manager. mgr may be nil (--no-mcp): everything shows not-connected.
func listMCPServers(cwd string, trusted bool, mgr *mcp.Manager) []modes.MCPInfo {
	user, _ := LoadConfig()
	userDisabled := map[string]bool{}
	for _, n := range user.DisableMCP {
		userDisabled[n] = true
	}
	projDisabled := map[string]bool{}
	var projServers map[string]mcp.ServerConfig
	if pc, err := LoadProjectConfig(cwd); err == nil && pc != nil {
		for _, n := range pc.DisableMCP {
			projDisabled[n] = true
		}
		if pc.MCP != nil {
			projServers = pc.MCP.Servers
		}
	}

	rows := map[string]*modes.MCPInfo{}
	add := func(name, scope, desc string, gated bool) {
		rows[name] = &modes.MCPInfo{
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
	out := make([]modes.MCPInfo, 0, len(names))
	for _, name := range names {
		r := rows[name]
		r.Effective = !r.UserDisabled && !r.ProjectDisabled && !r.ProjectGated
		if _, err := os.Stat(filepath.Join(LogsPath(), "mcp-"+name+".log")); err == nil {
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

// setUserMCPDisabled adds or removes name from the user config's
// disable_mcp list and writes config.json. The user layer is fully
// modeled by Config, so the structured round-trip (LoadConfig /
// toggleStringMember / SaveConfig) is correct and matches the other
// user-level toggles (favorites, etc.). "Enable" is just removal.
func setUserMCPDisabled(name string, disabled bool) error {
	c, err := LoadConfig()
	if err != nil {
		return err
	}
	c.DisableMCP = toggleStringMember(c.DisableMCP, name, disabled)
	return SaveConfig(c)
}

// setProjectMCPDisabled adds or removes name from the project config's
// disable_mcp list (preserving other fields) and writes it atomically.
// The project layer isn't a fully writable struct, so this uses the same
// generic-map round-trip as setProjectExtensionDisabled.
func setProjectMCPDisabled(cwd, name string, disabled bool) error {
	path := projectConfigPath(cwd)
	generic := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &generic); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	// Rebuild the list without name, then re-add it iff disabling.
	var list []string
	if arr, ok := generic["disable_mcp"].([]any); ok {
		for _, e := range arr {
			if s, ok := e.(string); ok && s != name {
				list = append(list, s)
			}
		}
	}
	if disabled {
		list = append(list, name)
	}
	if len(list) == 0 {
		delete(generic, "disable_mcp")
	} else {
		generic["disable_mcp"] = list
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(generic, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// mcpServerShouldRun reports whether the named server should be running
// under the current on-disk config: it must be a defined server (user, or
// a trusted project's) and not disabled (user ∪ project). Mirrors
// extensionShouldRun so a live toggle can't drift from what the row shows.
func mcpServerShouldRun(cwd string, trusted bool, name string) bool {
	user, _ := LoadConfig()
	merged := mergeMCPConfigs(user.MCP, trustedProjectMCP(cwd, trusted))
	if merged == nil {
		return false
	}
	if _, ok := merged.Servers[name]; !ok {
		return false // not defined here (or an untrusted project's server)
	}
	return !resolvedDisableMCP(cwd, trusted)[name]
}

// serverConfigFor resolves the live ServerConfig for one server name from
// the freshly-merged user+project sets (user wins). Returns ok=false when
// the name isn't a defined server. Used by a live enable to spawn it.
func serverConfigFor(cwd string, trusted bool, name string) (mcp.ServerConfig, bool) {
	user, _ := LoadConfig()
	merged := mergeMCPConfigs(user.MCP, trustedProjectMCP(cwd, trusted))
	if merged == nil {
		return mcp.ServerConfig{}, false
	}
	sc, ok := merged.Servers[name]
	return sc, ok
}

// applyMCPChangeLive applies a just-persisted enable/disable for ONE
// server against the live Manager, then fires onReload so the agent's tool
// registry is rebuilt to match (the rebuild re-merges adapter.Tools(), so
// a removed server's tools genuinely disappear). adapter may be nil when
// MCP was skipped for the session (--no-mcp): the config change is still
// persisted and applies next launch.
func applyMCPChangeLive(adapter *mcpToolAdapter, cwd string, trusted bool, name string, onReload func()) {
	if adapter == nil || adapter.mgr == nil {
		return
	}
	if mcpServerShouldRun(cwd, trusted, name) {
		if sc, ok := serverConfigFor(cwd, trusted, name); ok {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			_ = adapter.mgr.StartOne(ctx, name, sc, adapter.stderrFor(name))
		}
	} else {
		adapter.mgr.StopOne(name)
	}
	if onReload != nil {
		onReload()
	}
}
