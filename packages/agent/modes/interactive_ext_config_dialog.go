package modes

import "terva.sh/terva/packages/i18n"

// Hooks the per-extension config form to the host callbacks: open with the
// extension's schema + saved values, then on save type+persist the values
// and push them to the running extension, refreshing the /extensions list
// underneath (a newly-configured extension may now run).

// openExtConfigDialog populates and shows the config form for one extension.
func (i *Interactive) openExtConfigDialog(name string) {
	if i.extConfigDialog == nil || i.cfg.ExtensionConfigFields == nil {
		i.setStatusErr(i18n.T("extension config is not available in this build"))
		return
	}
	fields := i.cfg.ExtensionConfigFields(name)
	if len(fields) == 0 {
		// This dialog only opens from the /extensions list's config action,
		// which is gated on the manifest declaring a schema. An empty form
		// therefore means the schema was seen when the list was built but the
		// install dir can't be resolved or its manifest can't be read now — a
		// real failure, not "nothing to configure". Surface it instead of a
		// reassuring OK. (A genuinely schema-less extension is handled in the
		// list itself, where HasConfig is false.)
		i.setStatusErr(name + ": couldn't load its configuration (the installed files may be missing or unreadable)")
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
		i.setStatusErr(i18n.T("extension config is not available in this build"))
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
	// so refresh the /extensions list underneath — from the surface on the
	// carrier path (same source the toggle refresh uses), locally otherwise.
	if i.extensionsDialog != nil && i.extensionsDialog.Active() {
		switch {
		case i.cfg.Carrier != nil:
			i.extensionsDialog.SetItems(i.carrierListExtensions())
		case i.cfg.ListExtensions != nil:
			i.extensionsDialog.SetItems(i.cfg.ListExtensions())
		}
	}
	i.invalidate()
}
