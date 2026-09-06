package modes

// Model config editing: open the editor for a picked model, persist its
// overrides to models.json, re-apply the user catalog layer live, and
// re-resolve the active model when it's the one that changed.

import (
	"terva.sh/terva/packages/i18n"
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
			i.setStatusErr(i18n.T("read models.json: %s", err))
			return
		}
	}
	i.modelEditDialog.Open(m, existing, has, i.cfg.Reasoning)
	i.invalidate()
}

// applyModelEdit writes the edited entry to models.json, re-applies the
// override layer so the change is live, and re-resolves the active model
// when it was the one edited.
func (i *Interactive) applyModelEdit(prov, modelID string, entry provider.UserModel) {
	path := i.cfg.UserModelsPath
	if path == "" {
		i.setStatusErr(i18n.T("models.json path not configured"))
		return
	}
	if err := provider.UpsertUserModel(path, prov, entry); err != nil {
		i.setStatusErr(i18n.T("save models.json: %s", err))
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
		i.setStatusErr(i18n.T("models.json path not configured"))
		return
	}
	// Read BEFORE the removal. A synthetic model exists only because of the
	// entry, so once RemoveUserModel and reapplyUserModels have run there is
	// nothing left to ask: FindModel fails and the answer defaults to the wrong
	// half of the message below.
	synthetic := false
	if m, ferr := provider.FindModel(prov, modelID); ferr == nil {
		synthetic = m.Synthetic
	}
	removed, err := provider.RemoveUserModel(path, prov, modelID)
	if err != nil {
		i.setStatusErr(i18n.T("update models.json: %s", err))
		return
	}
	i.reapplyUserModels()

	// Deleting the model this session is ON is the one case that must not
	// re-resolve. The model has just left the catalog, so switchModel's
	// FindModel fails and it returns "unknown model" without touching the
	// session. That error then reached the status line and was wiped a moment
	// later by setStatusOK, which clears statusErr: the user was told the
	// delete succeeded and never told the swap had failed.
	//
	// Skipping it is not papering over the failure. There is nothing to
	// re-resolve TO, so the swap could only ever fail here. What the user needs
	// instead is the truth, which the message below carries: the row is gone,
	// and this session keeps using the model until they pick another. The
	// session is not broken by that. The agent still holds the client and model
	// record it was built with, so turns keep working.
	orphaned := removed && synthetic && i.isActiveModel(prov, modelID)
	if !orphaned {
		i.refreshActiveModel(prov, modelID)
	}
	switch {
	case orphaned:
		i.setStatusOK("deleted " + prov + "/" + modelID + ", but this session keeps using it until you switch with /model")
	case removed && synthetic:
		i.setStatusOK("deleted " + prov + "/" + modelID + ", it existed only in models.json")
	case removed:
		i.setStatusOK("reset " + prov + "/" + modelID + " to defaults")
	default:
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
	if i.isActiveModel(prov, modelID) {
		i.applyModelSelection(prov, modelID)
	}
}

// isActiveModel reports whether prov/modelID is the model this session is
// running on. One definition, because applyModelReset asks the same question
// to decide whether a delete has orphaned the session, and two spellings of
// "is this the active model" would be two chances to disagree.
func (i *Interactive) isActiveModel(prov, modelID string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.cfg.Provider == prov && i.cfg.Model == modelID
}
