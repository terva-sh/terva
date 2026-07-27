package modes

import (
	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/i18n"
)

// Hooks the /extensions dialog to the host callbacks: open with the
// current installed set, apply a global/project toggle via the injected
// persisters, reload live, then refresh the list in place so the new
// state (including a failed respawn) shows immediately.

// openExtensionsDialog populates and shows the /extensions dialog. The
// inventory rides the carrier's extensions surface — the only source since the
// direct driver's removal took the host-callback path's last caller with it.
func (i *Interactive) openExtensionsDialog() {
	if i.extensionsDialog == nil {
		i.setStatusErr(i18n.T("extension management is not available in this build"))
		return
	}
	i.extensionsDialog.Open(i.carrierListExtensions())
	i.invalidate()
}

// applyExtensionToggle persists a global (manifest) or project (config)
// enable/disable, reloads extensions live, and refreshes the dialog. The
// whole flow runs on the UI goroutine: extension reload for a single
// toggle is fast, and keeping it inline avoids racing the renderer on the
// dialog's item slice.
func (i *Interactive) applyExtensionToggle(act dialogs.ExtensionsAction) {
	// A session extension is loaded by path via --ext for this run only; it
	// lives outside the install roots, so there's no manifest or project
	// config to flip. Explain that instead of failing with a "not found".
	if act.Scope == "session" {
		i.setStatusOK(act.Name + ": loaded via --ext for this run only — no persistent enable/disable")
		return
	}
	i.applyCarrierExtensionToggle(act)
}
