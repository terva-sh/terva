// Package modelreg holds terva's model registry: the one catalog every agent
// and client terva builds reads through (decision 0021,
// docs/plans/model-catalog.md). The wire keeps no catalog state of its own
// for terva; it is handed this registry.
//
// The functions keep the names the wire's package functions had, so a call
// site moved by re-qualifying it. It is a leaf: it imports only the wire, so
// every terva package can reach it.
package modelreg

import "terva.sh/terva/packages/provider"

var reg = provider.NewRegistry()

// Registry returns terva's registry, to pass to core.Agent.SetCatalog and
// provider.WithCatalog.
func Registry() *provider.Registry { return reg }

// FindModel returns a model by id, optionally constrained by provider.
func FindModel(providerID, id string) (provider.Model, error) {
	return reg.FindModel(providerID, id)
}

// Active returns the merged catalog.
func Active() []provider.Model { return reg.Active() }

// ModelsForProvider returns every model for the given provider.
func ModelsForProvider(providerID string) []provider.Model {
	return reg.ModelsForProvider(providerID)
}

// ContextGauge is the denominator every user-facing context reading uses. See
// provider.Registry.ContextGauge.
func ContextGauge(providerID, id string) int { return reg.ContextGauge(providerID, id) }

// CatalogRevision increments whenever the catalog is recomputed.
func CatalogRevision() uint64 { return reg.Revision() }

// SetLiveModels replaces the live layer.
func SetLiveModels(live []provider.Model) { reg.SetLiveModels(live) }

// RegisterExtraModel upserts one model into the extra layer.
func RegisterExtraModel(m provider.Model) { reg.RegisterExtraModel(m) }

// SetUserOverrides replaces the user layer with models.json overrides.
func SetUserOverrides(overrides []provider.UserOverride) { reg.SetUserOverrides(overrides) }

// SetUserModels is the []Model form of SetUserOverrides.
func SetUserModels(models []provider.Model) { reg.SetUserModels(models) }

// ResetCatalogLayers clears every overlay layer. For tests.
func ResetCatalogLayers() { reg.Reset() }
