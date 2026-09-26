package core

import "terva.sh/terva/packages/provider"

// catalogBox lets the catalog ride an atomic.Pointer: an interface value
// cannot, and the lookups happen both under a.mu (refreshMaxTokensLocked) and
// outside it (the tool-image mirror), so a mutex here would need two paths.
type catalogBox struct{ c provider.ModelCatalog }

// SetCatalog sets the model catalog the agent reads output limits, context
// windows and capabilities from. The host passes it in (decision 0021), so two
// agents in one process can see different models. nil restores Builtin.
//
// The output budget is re-derived from the new catalog when it has the model,
// as SetModel does. A catalog without the model leaves the budget as it was,
// also as SetModel does: a working budget beats zero, and the client clamps to
// its own catalog's limit at send time. A host that sets its own budget
// (MaxTokens) sets it after this.
//
// The agent's client reads its own catalog, which provider.WithCatalog sets.
// A host that means one catalog passes it to both.
func (a *Agent) SetCatalog(c provider.ModelCatalog) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.catalog.Store(&catalogBox{c: c})
	a.refreshMaxTokensLocked()
}

// Catalog returns the catalog set with SetCatalog, or provider.Builtin() when
// none is: an agent its host gave no catalog knows the compiled-in models and
// nothing else.
func (a *Agent) Catalog() provider.ModelCatalog {
	if b := a.catalog.Load(); b != nil && b.c != nil {
		return b.c
	}
	return provider.Builtin()
}
