package provider

import (
	"fmt"
	"sync"
)

// ModelCatalog is what the engine and the clients read about models: one
// lookup. A host passes it in (Agent.SetCatalog, WithCatalog), so two engines
// in one process can see different models. The wire holds no catalog of its
// own. Listing, revision and mutation belong to whoever owns the catalog;
// Registry is the implementation terva uses, and Builtin is the one a host
// that passes nothing gets.
type ModelCatalog interface {
	// FindModel returns a Model by id, optionally constrained by provider. An
	// empty provider matches the first model with that id.
	FindModel(provider, id string) (Model, error)
}

var (
	_ ModelCatalog = (*Registry)(nil)
	_ ModelCatalog = staticCatalog{}
)

// Builtin returns the compiled-in catalog alone. It holds no state: the list
// it reads is filled at init and no setter reaches it.
func Builtin() ModelCatalog { return staticCatalog{} }

type staticCatalog struct{}

func (staticCatalog) FindModel(provider, id string) (Model, error) {
	return findIn(Catalog, provider, id)
}

// catalogOr returns c, or Builtin when c is nil: a client its host gave no
// catalog knows the compiled-in models and nothing else.
func catalogOr(c ModelCatalog) ModelCatalog {
	if c == nil {
		return Builtin()
	}
	return c
}

// catalogRef is the catalog a client reads model metadata through. Each
// concrete client embeds one, and WithCatalog sets it.
type catalogRef struct{ cat ModelCatalog }

func (r *catalogRef) setCatalog(c ModelCatalog) { r.cat = c }

// models is the catalog to read: the one set, or Builtin.
func (r *catalogRef) models() ModelCatalog { return catalogOr(r.cat) }

type catalogHolder interface{ setCatalog(ModelCatalog) }

// WithCatalog makes c read model metadata (output limits, capabilities,
// reasoning, prices) through cat, reaching the concrete client through any
// wrapper layers the way WithHTTPClient does. Call it before the client's
// first request. A nil cat restores Builtin.
func WithCatalog(c Client, cat ModelCatalog) Client {
	for cur := c; cur != nil; {
		if h, ok := cur.(catalogHolder); ok {
			h.setCatalog(cat)
		}
		u, ok := cur.(unwrapper)
		if !ok {
			break
		}
		cur = u.Unwrap()
	}
	return c
}

func findIn(models []Model, provider, id string) (Model, error) {
	for _, m := range models {
		if m.ID == id && (provider == "" || m.Provider == provider) {
			return m, nil
		}
	}
	return Model{}, fmt.Errorf("unknown model %q (provider=%q)", id, provider)
}

// Registry is a model catalog built from four declarative layers, lowest to
// highest precedence:
//
//	builtin — the baked-in Catalog (extended from init()s)
//	live    — /v1/models discovery or its disk cache (SetLiveModels)
//	extra   — individually registered models, e.g. the
//	          openai-compatible endpoint's listing (RegisterExtraModel)
//	user    — $TERVA_HOME/models.json overrides (SetUserOverrides)
//
// Precedence is data, not call ordering: each setter replaces only its own
// layer and the merge recomputes. The zero value is not usable; build one with
// NewRegistry. It is safe for concurrent use.
type Registry struct {
	mu    sync.RWMutex
	live  []Model
	extra []Model
	user  []UserOverride
	// merged is the cached layer merge, recomputed on every layer write
	// (writes are rare; reads are hot). nil means no layer has ever been set
	// and Active serves the baked-in Catalog.
	merged []Model
	// rev bumps on every remerge so live UIs (the /model picker) can detect
	// when background /v1/models discovery has grown the catalog and re-read
	// it, instead of holding a stale snapshot.
	rev uint64
}

// NewRegistry returns a Registry over the built-in catalog, with no layers.
func NewRegistry() *Registry { return &Registry{} }

// remergeLocked recomputes the merged catalog. Callers hold r.mu.
func (r *Registry) remergeLocked() {
	out := MergeCatalog(r.live)
	out = upsertModels(out, r.extra)
	out = applyUserOverrides(out, r.user)
	r.merged = out
	r.rev++
}

// Revision returns a counter that increments whenever the catalog is
// recomputed (a layer write — e.g. live discovery completing). A long-lived
// view can poll it to know when to re-read Active.
func (r *Registry) Revision() uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.rev
}

// SetLiveModels replaces the "live" layer. Typically called after a
// successful /v1/models discovery or on load from the on-disk cache.
func (r *Registry) SetLiveModels(live []Model) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.live = append([]Model(nil), live...)
	r.remergeLocked()
}

// RegisterExtraModel upserts a single model into the "extra" layer, replacing
// any layer entry with the same provider/id. Used for models that are neither
// in the baked-in catalog nor discovered via the standard refresh — currently
// the openai-compatible endpoint's models. Entries persist across
// SetLiveModels calls.
func (r *Registry) RegisterExtraModel(m Model) {
	r.mu.Lock()
	defer r.mu.Unlock()
	replaced := false
	for i, e := range r.extra {
		if e.Provider == m.Provider && e.ID == m.ID {
			r.extra[i] = m
			replaced = true
			break
		}
	}
	if !replaced {
		r.extra = append(r.extra, m)
	}
	r.remergeLocked()
}

// SetUserOverrides replaces the "user" layer with the given models.json
// overrides. User entries take precedence over every other layer; nil clears
// the layer.
func (r *Registry) SetUserOverrides(overrides []UserOverride) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.user = append([]UserOverride(nil), overrides...)
	r.remergeLocked()
}

// SetUserModels is the []Model convenience form of SetUserOverrides for
// callers that build models programmatically (mostly tests): every field
// including Reasoning is treated as explicitly set. nil clears the user layer.
//
// A non-empty DisplayName counts as explicitly set for the same reason, so a
// caller that spells one out gets it — the merge asks DisplayNameSet, which
// the JSON loader derives from the raw entry and a hand-built Model has no
// other way to assert.
func (r *Registry) SetUserModels(models []Model) {
	r.SetUserOverrides(userModelOverrides(models))
}

// Reset clears every overlay layer (live, extra, user), returning Active to
// the baked-in Catalog. Intended for tests that need a pristine catalog
// regardless of what earlier tests installed.
func (r *Registry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.live, r.extra, r.user, r.merged = nil, nil, nil, nil
}

// Active returns the current merged catalog.
//
// When no layer has ever been set it returns the fully-assembled static
// Catalog. Reading Catalog at call time (rather than capturing it into a
// package-level var initializer) is load-bearing: the extended catalog in
// catalog_builtin.go / extra_models.go is appended from init() functions,
// which run AFTER package-level var initializers. Snapshotting Catalog at
// var-init time would freeze the picker to the curated seed list and drop
// every extra provider (openrouter, groq, xai, ...). The same applies to
// remergeLocked — it only ever runs from a setter call, well after init.
func (r *Registry) Active() []Model {
	r.mu.RLock()
	defer r.mu.RUnlock()
	src := r.merged
	if src == nil {
		src = Catalog
	}
	out := make([]Model, len(src))
	copy(out, src)
	return out
}

// FindModel implements ModelCatalog against the merged catalog.
func (r *Registry) FindModel(provider, id string) (Model, error) {
	return findIn(r.Active(), provider, id)
}

// ContextGauge is the denominator EVERY user-facing context reading must use:
// the model's EFFECTIVE window, resolved from the catalog. 0 when the model is
// unknown, which callers render as "no gauge" rather than as a division by
// zero.
//
// There were two context-window semantics in the tree and they disagreed.
// Agent.ContextUsage, the auto-compaction keep-tail budget and the compaction
// policy all divide by EffectiveContextWindow; nine gauge sites read the raw
// ContextWindow instead — the TUI status bar, the script-mode payload, the
// chat-bridge /status line, the web session card, the usage surface and the
// context inspector. tools/status.go stated the contract out loud ("this
// percentage matches the status-bar gauge and the auto-compaction threshold")
// and it did not match.
//
// On a model with a DesiredContextWindow the gap is not cosmetic. gpt-5.6-luna
// ships ContextWindow 1,050,000 against DesiredContextWindow 272,000, so
// auto-compaction fires at 217,600 tokens while every gauge read 21% full: the
// user watched their conversation compact at a fifth of a bar, with no surface
// anywhere showing the number that triggered it. Any operator who sets
// desiredContextWindow in models.json to dodge a context surcharge reproduces
// it on any model.
//
// The hard ceiling keeps using Model.ContextWindow — the maxTok clamp and
// every surface that reports the model's SPEC (`--list-models`, models.list,
// the rpc and sdk model rows). Two meanings, two names, and the name says
// which.
func (r *Registry) ContextGauge(provider, id string) int {
	m, err := r.FindModel(provider, id)
	if err != nil {
		return 0
	}
	return m.EffectiveContextWindow()
}

// ModelsForProvider returns all models for the given provider, from the
// merged catalog.
func (r *Registry) ModelsForProvider(provider string) []Model {
	var out []Model
	for _, m := range r.Active() {
		if m.Provider == provider {
			out = append(out, m)
		}
	}
	return out
}
