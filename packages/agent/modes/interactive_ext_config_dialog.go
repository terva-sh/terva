package modes

import (
	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/i18n"
)

// Hooks the per-extension config form to the host callbacks: open with the
// extension's schema + saved values, then on save type+persist the values
// and push them to the running extension, refreshing the /extensions list
// underneath (a newly-configured extension may now run).

// openExtConfigDialog populates and shows the config form for one extension.
func (i *Interactive) openExtConfigDialog(name string) {
	if i.extConfigDialog == nil {
		i.setStatusErr(i18n.T("extension config is not available in this build"))
		return
	}
	// Prefer the wire: the daemon owns the manifests and config.json, and on an
	// attached terminal the local disk is a different machine's.
	var fields []dialogs.ConfigField
	switch {
	case i.cfg.Carrier != nil:
		fields = i.carrierExtConfigFields(name)
	case i.cfg.ExtensionConfigFields != nil:
		fields = i.cfg.ExtensionConfigFields(name)
	default:
		i.setStatusErr(i18n.T("extension config is not available in this build"))
		return
	}
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
func (i *Interactive) applyExtConfig(act dialogs.ExtConfigAction) {
	if !act.Save {
		return
	}
	// One round trip on the carrier path: the daemon persists AND applies, so
	// there is no window where the values are on disk but not in the running
	// extension. The local path keeps its two steps — it is the same process,
	// so there is no window to close.
	switch {
	case i.cfg.Carrier != nil:
		if err := i.applyCarrierExtConfig(act.Name, act.Values); err != nil {
			i.setStatusErr(err.Error())
		} else {
			i.setStatusOK(act.Name + " config saved")
		}
	case i.cfg.SetExtensionConfig != nil:
		if err := i.cfg.SetExtensionConfig(act.Name, act.Values); err != nil {
			i.setStatusErr(err.Error())
		} else {
			if i.cfg.ApplyExtensionConfig != nil {
				i.cfg.ApplyExtensionConfig(act.Name)
			}
			i.setStatusOK(act.Name + " config saved")
		}
	default:
		i.setStatusErr(i18n.T("extension config is not available in this build"))
		return
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
