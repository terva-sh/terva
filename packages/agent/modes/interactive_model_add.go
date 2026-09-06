package modes

// Creating a model the catalog does not ship: the clone form opened with ctrl+n
// from the picker, and the commit behind it.
//
// Unlike applyModelEdit next door, which writes models.json directly, this
// commits through the carrier's ctrlproto.ModelParamsController. That is where
// the guards live, and two of them cannot be checked here at all: whether the id
// already resolves, and whether the provider is one this machine can reach. A
// second copy of that reasoning in the TUI is the failure build.LoggedInProviders
// exists to prevent, where three copies of one predicate drifted and only the
// TUI's learned about named endpoints.

import (
	"context"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// modelParamsController returns the carrier's models.json surface, or nil when
// this carrier does not serve one — a replay session, for instance. The add
// form is not opened in that case, rather than opened and then refused on save.
func (i *Interactive) modelParamsController() ctrlproto.ModelParamsController {
	if i.cfg.Carrier == nil {
		return nil
	}
	c, ok := i.cfg.Carrier.(ctrlproto.ModelParamsController)
	if !ok {
		return nil
	}
	return c
}

// carrierCtx is the context for a control call, falling back to Background
// before Run has bound one.
func (i *Interactive) carrierCtx() context.Context {
	if i.runCtx != nil {
		return i.runCtx
	}
	return context.Background()
}

// openModelAdd opens the create form, cloned from the picked model.
//
// prov/modelID name the CLONE SOURCE. The form copies its settings and asks for
// the one thing a clone cannot supply, which is the new id.
func (i *Interactive) openModelAdd(prov, modelID string) {
	if i.modelEditDialog == nil {
		return
	}
	if i.modelParamsController() == nil {
		i.setStatusErr(i18n.T("this session cannot add models"))
		return
	}
	src, err := provider.FindModel(prov, modelID)
	if err != nil {
		i.setStatusErr(err.Error())
		return
	}
	var providers []string
	if i.cfg.LoggedInProviders != nil {
		providers = i.cfg.LoggedInProviders()
	}
	i.modelEditDialog.OpenAdd(src, providers, i.cfg.Reasoning)
	i.invalidate()
}

// applyModelAdd commits the new model and reopens the picker with it under the
// cursor.
func (i *Interactive) applyModelAdd(prov, modelID string, entry provider.UserModel) {
	c := i.modelParamsController()
	if c == nil {
		i.setStatusErr(i18n.T("this session cannot add models"))
		return
	}

	// Back to the wire's whole-form map. The dialog assembled this UserModel
	// through the registry's SetOverride, and Override is its inverse, so this
	// is the same duality the form's seed relies on rather than a second
	// spelling of every field.
	values := make(map[string]string, len(provider.ModelParams()))
	for _, p := range provider.ModelParams() {
		values[p.Key] = p.Override(entry)
	}

	if err := c.ModelAdd(i.carrierCtx(), ctrlproto.ModelAddParams{
		Provider: prov,
		Model:    modelID,
		Values:   values,
	}); err != nil {
		i.setStatusErr(err.Error())
		return
	}

	// An in-process carrier has already reloaded the override layer, so this is
	// redundant there. It is not redundant for a carrier in another process,
	// which wrote a models.json this one has not read.
	i.reapplyUserModels()
	i.setStatusOK(i18n.T("added %s", prov+"/"+modelID))
	i.reopenModelPickerAt(prov, modelID)
	i.invalidate()
}

// reopenModelPickerAt reopens the picker scoped to the new model.
//
// The add does not switch to it. That matches the verb the edit path already
// uses: a save re-resolves the sessions sitting on a model, it never moves the
// user onto one. Leaving the cursor there makes enter the whole cost for
// someone who added a model in order to use it.
func (i *Interactive) reopenModelPickerAt(prov, modelID string) {
	if i.modelDialog == nil {
		return
	}
	var loggedIn []string
	if i.cfg.LoggedInProviders != nil {
		loggedIn = i.cfg.LoggedInProviders()
	}
	var favs []string
	if i.cfg.FavoriteModels != nil {
		favs = i.cfg.FavoriteModels()
	}
	var hidden []string
	if i.cfg.HiddenModels != nil {
		hidden = i.cfg.HiddenModels()
	}
	i.modelDialog.OpenAt(i.cfg.Model, loggedIn, favs, hidden, prov, modelID)
}
