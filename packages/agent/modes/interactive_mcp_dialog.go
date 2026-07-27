package modes

import (
	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/i18n"
)

// Hooks the /mcp dialog to the host callbacks: open with the current
// configured-server set, apply a global/project toggle via the injected
// persisters, start/stop the server live, then refresh the list in place
// so the new state (including a failed respawn) shows immediately. Mirrors
// interactive_extensions_dialog.go.

// openMCPDialog populates and shows the /mcp dialog. The server list rides the
// carrier's mcp surface — the only source since the direct driver's removal
// took the host-callback path's last caller with it.
func (i *Interactive) openMCPDialog() {
	if i.mcpDialog == nil {
		i.setStatusErr(i18n.T("MCP management is not available in this build"))
		return
	}
	i.mcpDialog.Open(i.carrierListMCP())
	i.invalidate()
}

// applyMCPToggle persists a global (user config) or project (project
// config) enable/disable, starts or stops the server live, and refreshes
// the dialog. Runs on the UI goroutine: a single server start/stop is
// quick, and keeping it inline avoids racing the renderer on the item
// slice.
func (i *Interactive) applyMCPToggle(act dialogs.MCPAction) {
	// A project-defined server has no user-scope toggle: it isn't in the
	// user config to disable there. Explain instead of writing a no-op.
	if act.ToggleGlobal && act.Scope == "project" {
		i.setStatusOK(act.Name + ": project-defined server — toggle it for this project with p")
		return
	}
	i.applyCarrierMCPToggle(act)
}
