package workspace

import (
	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/mcp"
	"terva.sh/terva/packages/i18n"
)

// The MCP management surface: the workspace's configured Model Context Protocol
// servers with live status, and an enable/disable toggle. Servers are
// workspace-global (one manager for the daemon), so a toggle rebuilds EVERY live
// session's tool set, not just the acting one.

// mcpView builds the MCP pane from the config-driven server list overlaid with
// live manager status (mirrors the TUI's /mcp dialog via listMCPServers).
func (s *wsSession) mcpView() *ctrlproto.MCPView {
	v := &ctrlproto.MCPView{}
	if s.ws == nil || s.ws.mcpAdapter == nil {
		return v
	}
	for _, m := range build.ListMCPServers(s.cwd, s.trusted.Load(), s.ws.mcpAdapter.Mgr) {
		v.Servers = append(v.Servers, ctrlproto.MCPServerInfo{
			Name:            m.Name,
			Scope:           m.Scope,
			Description:     m.Description,
			Status:          mcpStatus(m),
			Enabled:         m.Effective,
			Tools:           m.Tools,
			Note:            m.StartupError,
			UserDisabled:    m.UserDisabled,
			ProjectDisabled: m.ProjectDisabled,
			Connected:       m.Connected,
		})
	}
	return v
}

// mcpStatus collapses the MCPInfo flags into one badge value.
func mcpStatus(m mcp.Info) string {
	switch {
	case m.UserDisabled || m.ProjectDisabled:
		return "disabled"
	case m.ProjectGated:
		return "gated"
	case m.StartupError != "" && !m.Connected:
		return "failed"
	case m.Connected:
		return "running"
	default:
		return "stopped" // enabled but not connected
	}
}

// mcpAction toggles an MCP server on/off: it persists the choice (to the user or
// project disable list, per the server's scope), starts/stops the shared server
// live, and rebuilds every session's tools (the servers are workspace-global).
func (s *wsSession) mcpAction(action string, args map[string]string) error {
	if s.ws == nil || s.ws.mcpAdapter == nil {
		return ctrlproto.Errorf(ctrlproto.CodeUnsupported, "%s", i18n.T("MCP is not enabled"))
	}
	name := args["name"]
	if name == "" {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("mcp: missing name"))
	}
	switch action {
	case "toggle":
		on := args["enabled"] == "true"
		var err error
		if args["scope"] == "project" {
			err = config.SetProjectMCPDisabled(s.cwd, name, !on)
		} else {
			err = config.SetUserMCPDisabled(name, !on)
		}
		if err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "toggle %s: %v", name, err)
		}
		// Serialize toggles (StartOne/StopOne want a single caller); the live
		// apply rebuilds every session's tools since the manager is shared.
		s.ws.mcpMu.Lock()
		build.ApplyMCPChangeLive(s.ws.mcpAdapter, s.cwd, s.trusted.Load(), name,
			func() { s.ws.rebuildAllSessions("mcp-toggle") })
		s.ws.mcpMu.Unlock()
		s.ws.BroadcastAll(ctrlproto.SurfaceUpdatedEvent("mcp"))
		return nil
	default:
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("unknown mcp action %q", action))
	}
}

// rebuildAllSessions refreshes every live session's model-facing view — used
// after a live MCP toggle (the MCP manager is workspace-global, so one toggle
// changes the shared server set for all sessions) and an auto-swarm toggle
// (workspace-global config read at rebuild time). reason labels the
// prompt_rebuilt notice each changed session broadcasts.
func (w *Workspace) rebuildAllSessions(reason string) {
	w.mu.Lock()
	sess := make([]*wsSession, 0, len(w.sessions))
	for _, s := range w.sessions {
		sess = append(sess, s)
	}
	w.mu.Unlock()
	for _, s := range sess {
		s.rebuildTools(reason)
	}
}
