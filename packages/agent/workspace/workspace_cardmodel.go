package workspace

import (
	"context"
	"strings"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider"
)

// Per-card default model on the wire. Like card groups, the store is global
// ($TERVA_HOME/card-models) and the Workspace is a thin adapter over it — filing
// a preferred model against a card never touches the card, so there is no trust
// gate beyond the workspace's own auth.
//
// The point of this file is effectiveDefaultModel: the ONE authority for "what
// model is default here?", walking Card → World → Workspace. Both the wire
// resolver (ModelDefaultFor) and the session seed (createSeededLocked) route
// through it, so a card's default propagates identically to the card doctor, the
// session it starts, and any picker's fallback row — rather than each surface
// re-deriving the default its own way.
var _ ctrlproto.CardModelController = (*Workspace)(nil)

func (w *Workspace) cardModelStore() *build.CardModelStore { return build.NewCardModelStore() }

// effectiveDefaultModel resolves the provider+model a fresh choice defaults to
// for the given context. Precedence, highest first:
//
//	card    — the card's stored pref (cardmodel.set), if it resolves to a model
//	          this workspace can run
//	world   — RESERVED: no world-level default model exists yet (WorldDoc carries
//	          only per-character pins), so worldID changes nothing today. Wired so
//	          the day one is added it slots in here with no caller change.
//	workspace — the configured default (models.set_default; project shadows global),
//	          else the boot-resolved default (launch --model + catalog fallback).
//
// The card rung is resolved through the catalog (provider.FindModel) exactly like
// an explicit pick, so a pref naming an unqualified or now-uncredentialed model
// degrades to the workspace floor instead of seeding an unrunnable session. The
// workspace rung is trusted as-is, matching createSeededLocked's original base.
func (w *Workspace) effectiveDefaultModel(cardID, worldID string) (prov, model string, source ctrlproto.DefaultSource) {
	prov, model = w.provider, w.model
	if dp, dm, _ := w.defaultModel(); dp != "" && dm != "" {
		prov, model = dp, dm
	}
	source = ctrlproto.DefaultSourceWorkspace

	_ = worldID // reserved rung; see the doc comment.

	if cardID = strings.TrimSpace(cardID); cardID != "" {
		if cm, ok, _ := w.cardModelStore().Get(cardID); ok {
			if m, e := provider.FindModel(cm.Provider, cm.Model); e == nil {
				return m.Provider, m.ID, ctrlproto.DefaultSourceCard
			}
		}
	}
	return prov, model, source
}

// ModelDefaultFor is the wire face of effectiveDefaultModel — the single default
// authority a card-context picker consults so its fallback row shows the real
// inherited model and names the rung (source) it came from.
func (w *Workspace) ModelDefaultFor(_ context.Context, p ctrlproto.DefaultForParams) (ctrlproto.DefaultForResult, error) {
	prov, model, source := w.effectiveDefaultModel(p.Card, p.World)
	return ctrlproto.DefaultForResult{Provider: prov, Model: model, Source: source}, nil
}

// CardModelSet writes a card's default model, or clears it (both fields empty).
// A non-empty pref is resolved against the catalog first, so a card never files a
// model the workspace can't run, and an unqualified id is stored already
// disambiguated to the provider the seed will read back.
func (w *Workspace) CardModelSet(_ context.Context, p ctrlproto.CardModelSetParams) error {
	card := strings.TrimSpace(p.Card)
	if card == "" {
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "a card model needs a card")
	}
	prov, model := strings.TrimSpace(p.Provider), strings.TrimSpace(p.Model)
	if prov != "" || model != "" {
		m, e := provider.FindModel(prov, model)
		if e != nil {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "unknown model %q: %v", model, e)
		}
		prov, model = m.Provider, m.ID
	}
	if err := w.cardModelStore().Set(card, prov, model); err != nil {
		return ctrlproto.Errorf(ctrlproto.CodeInternal, "set card model: %v", err)
	}
	w.broadcastLibraryChanged()
	return nil
}
