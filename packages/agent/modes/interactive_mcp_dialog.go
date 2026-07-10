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

// openMCPDialog populates and shows the /mcp dialog.
func (i *Interactive) openMCPDialog() {
	if i.cfg.Carrier != nil && i.mcpDialog != nil {
		// ctrlproto mode: the server list rides the mcp surface.
		i.mcpDialog.Open(i.carrierListMCP())
		i.invalidate()
		return
	}
	if i.mcpDialog == nil || i.cfg.ListMCP == nil {
		i.setStatusErr(i18n.T("MCP management is not available in this build"))
		return
	}
	i.mcpDialog.Open(i.cfg.ListMCP())
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
	if i.cfg.Carrier != nil {
		i.applyCarrierMCPToggle(act)
		return
	}

	var err error
	switch {
	case act.ToggleGlobal:
		if i.cfg.SetMCPGlobalEnabled != nil {
			err = i.cfg.SetMCPGlobalEnabled(act.Name, act.On)
		}
	case act.ToggleProject:
		if i.cfg.SetMCPProjectEnabled != nil {
			err = i.cfg.SetMCPProjectEnabled(act.Name, act.On)
		}
	default:
		return
	}

	if err != nil {
		i.setStatusErr(err.Error())
	} else {
		if i.cfg.ApplyMCPChange != nil {
			i.cfg.ApplyMCPChange(act.Name)
		}
		scope := "globally"
		if act.ToggleProject {
			scope = "for this project"
		}
		state := "enabled"
		if !act.On {
			state = "disabled"
		}
		i.setStatusOK(act.Name + " " + state + " " + scope)
	}

	// Refresh either way so the list reflects reality (config + live
	// connection state) after the attempt.
	if i.mcpDialog != nil && i.cfg.ListMCP != nil {
		i.mcpDialog.SetItems(i.cfg.ListMCP())
	}
	i.invalidate()
}
