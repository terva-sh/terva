package modes

// Hooks the /extensions dialog to the host callbacks: open with the
// current installed set, apply a global/project toggle via the injected
// persisters, reload live, then refresh the list in place so the new
// state (including a failed respawn) shows immediately.

// openExtensionsDialog populates and shows the /extensions dialog.
func (i *Interactive) openExtensionsDialog() {
	if i.extensionsDialog == nil || i.cfg.ListExtensions == nil {
		i.setStatusErr("extension management is not available in this build")
		return
	}
	i.extensionsDialog.Open(i.cfg.ListExtensions())
	i.invalidate()
}

// applyExtensionToggle persists a global (manifest) or project (config)
// enable/disable, reloads extensions live, and refreshes the dialog. The
// whole flow runs on the UI goroutine: extension reload for a single
// toggle is fast, and keeping it inline avoids racing the renderer on the
// dialog's item slice.
func (i *Interactive) applyExtensionToggle(act extensionsAction) {
	var err error
	switch {
	case act.ToggleGlobal:
		if i.cfg.SetExtensionGlobalEnabled != nil {
			err = i.cfg.SetExtensionGlobalEnabled(act.Name, act.On)
		}
	case act.ToggleProject:
		if i.cfg.SetExtensionProjectEnabled != nil {
			err = i.cfg.SetExtensionProjectEnabled(act.Name, act.On)
		}
	default:
		return
	}

	if err != nil {
		i.setStatusErr(err.Error())
	} else {
		if i.cfg.ApplyExtensionChange != nil {
			i.cfg.ApplyExtensionChange(act.Name)
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
	// running state) after the attempt.
	if i.extensionsDialog != nil && i.cfg.ListExtensions != nil {
		i.extensionsDialog.SetItems(i.cfg.ListExtensions())
	}
	i.invalidate()
}
