package modes

// Hooks the per-extension config form to the host callbacks: open with the
// extension's schema + saved values, then on save type+persist the values
// and push them to the running extension, refreshing the /extensions list
// underneath (a newly-configured extension may now run).

// openExtConfigDialog populates and shows the config form for one extension.
func (i *Interactive) openExtConfigDialog(name string) {
	if i.extConfigDialog == nil || i.cfg.ExtensionConfigFields == nil {
		i.setStatusErr("extension config is not available in this build")
		return
	}
	fields := i.cfg.ExtensionConfigFields(name)
	if len(fields) == 0 {
		i.setStatusOK(name + " has no configurable settings")
		return
	}
	i.extConfigDialog.Open(name, fields)
	i.invalidate()
}

// applyExtConfig persists the form's values and pushes them live.
func (i *Interactive) applyExtConfig(act extConfigAction) {
	if !act.Save {
		return
	}
	if i.cfg.SetExtensionConfig == nil {
		i.setStatusErr("extension config is not available in this build")
		return
	}
	if err := i.cfg.SetExtensionConfig(act.Name, act.Values); err != nil {
		i.setStatusErr(err.Error())
	} else {
		if i.cfg.ApplyExtensionConfig != nil {
			i.cfg.ApplyExtensionConfig(act.Name)
		}
		i.setStatusOK(act.Name + " config saved")
	}
	// The extension may have just become runnable (a required value filled),
	// so refresh the /extensions list underneath.
	if i.extensionsDialog != nil && i.extensionsDialog.Active() && i.cfg.ListExtensions != nil {
		i.extensionsDialog.SetItems(i.cfg.ListExtensions())
	}
	i.invalidate()
}
