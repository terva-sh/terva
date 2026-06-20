package modes

// Model config editing: open the editor for a picked model, persist its
// overrides to models.json, re-apply the user catalog layer live, and
// re-resolve the active model when it's the one that changed.

import (
	"terva.sh/terva/packages/provider"
)

// openModelEdit resolves the picked model and any existing models.json
// override, then opens the edit overlay. Surfaces a status error rather
// than opening when the model can't be resolved.
func (i *Interactive) openModelEdit(prov, modelID string) {
	if i.modelEditDialog == nil {
		return
	}
	m, err := provider.FindModel(prov, modelID)
	if err != nil {
		i.setStatusErr(err.Error())
		return
	}
	var existing provider.UserModel
	has := false
	if path := i.cfg.UserModelsPath; path != "" {
		if um, ok, err := provider.FindUserModel(path, prov, modelID); err == nil {
			existing, has = um, ok
		} else {
			i.setStatusErr("read models.json: " + err.Error())
			return
		}
	}
	i.modelEditDialog.Open(m, existing, has)
	i.invalidate()
}

// applyModelEdit writes the edited entry to models.json, re-applies the
// override layer so the change is live, and re-resolves the active model
// when it was the one edited.
func (i *Interactive) applyModelEdit(prov, modelID string, entry provider.UserModel) {
	path := i.cfg.UserModelsPath
	if path == "" {
		i.setStatusErr("models.json path not configured")
		return
	}
	if err := provider.UpsertUserModel(path, prov, entry); err != nil {
		i.setStatusErr("save models.json: " + err.Error())
		return
	}
	i.reapplyUserModels()
	i.refreshActiveModel(prov, modelID)
	i.setStatusOK("saved settings for " + prov + "/" + modelID)
	i.invalidate()
}

// applyModelReset removes the model's models.json entry (after the
// editor's confirmation), re-applies the override layer, and re-resolves
// the active model when it was the one reset.
func (i *Interactive) applyModelReset(prov, modelID string) {
	path := i.cfg.UserModelsPath
	if path == "" {
		i.setStatusErr("models.json path not configured")
		return
	}
	removed, err := provider.RemoveUserModel(path, prov, modelID)
	if err != nil {
		i.setStatusErr("update models.json: " + err.Error())
		return
	}
	i.reapplyUserModels()
	i.refreshActiveModel(prov, modelID)
	if removed {
		i.setStatusOK("reset " + prov + "/" + modelID + " to defaults")
	} else {
		i.setStatusOK("no custom settings for " + prov + "/" + modelID)
	}
	i.invalidate()
}

// reapplyUserModels reloads models.json and replaces the provider
// package's user override layer, so an edit/reset is live without a
// restart. Warnings are intentionally dropped here: the editor only
// writes well-formed entries, and printing to stderr mid-TUI would
// corrupt the screen.
func (i *Interactive) reapplyUserModels() {
	overrides, _ := provider.LoadUserModelsWithWarnings(i.cfg.UserModelsPath)
	provider.SetUserOverrides(overrides)
}

// refreshActiveModel re-resolves the running agent's model when the
// edited/reset model is the active one, so a new base URL takes effect
// immediately (a different endpoint forces a rebuild) and context-window
// / max-output changes are picked up on the next resolve. No-op when a
// different model is active.
func (i *Interactive) refreshActiveModel(prov, modelID string) {
	i.mu.Lock()
	active := i.cfg.Provider == prov && i.cfg.Model == modelID
	i.mu.Unlock()
	if active {
		i.applyModelSelection(prov, modelID)
	}
}
