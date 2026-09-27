package workspace

import (
	"context"
	"strings"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modelreg"
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
//	world   — the saved World's own default (worlds.set_model), for anything
//	          happening inside that World that the card does not speak for
//	workspace — the configured default (models.set_default; project shadows global),
//	          else the boot-resolved default (launch --model + catalog fallback).
//
// The card rung sits ABOVE the world rung because it is the narrower statement:
// "this character runs on X" is about the character wherever they appear, and a
// World naming a model is setting the room's floor, not overruling a cast member
// who was given a voice on purpose. A World that wants the last word on one
// character says so per-character (worlds.set_character_model), which is a pin on
// the routing path and outranks all of this.
//
// Both the card and world rungs are resolved through the catalog
// (modelreg.FindModel) exactly like an explicit pick, so a pref naming an
// unqualified model degrades to the next rung down instead of seeding an
// unrunnable session. They do NOT degrade on a missing credential. A card or a
// World names its model on purpose, and a run that quietly moved it onto
// another provider would spend on an account nobody aimed it at. Its session
// refuses instead, and the refusal names the model.
//
// The configured default is different: it degrades when no login reaches its
// provider. A default is not a pick, but the session seeded from it resolves
// with the provider EXPLICIT, and an explicit provider gets no credential
// fallback. A pin left on a provider the user has since logged out of therefore
// failed every new session with "no credential for anthropic". Boot had already
// fallen back past the same pin, and existing sessions ran fine. The boot model
// stays as-is: it is the last resort, and boot did the credential fallback.
func (w *Workspace) effectiveDefaultModel(cardID, worldID string) (prov, model string, source ctrlproto.DefaultSource) {
	prov, model = w.provider, w.model
	if dp, dm, _ := w.defaultModel(); dp != "" && dm != "" && w.defaultReachable(dp) {
		prov, model = dp, dm
	}
	source = ctrlproto.DefaultSourceWorkspace

	if worldID = strings.TrimSpace(worldID); worldID != "" {
		if doc, err := build.NewWorldStore().Get(worldID); err == nil && strings.TrimSpace(doc.Model.Model) != "" {
			if m, e := modelreg.FindModel(doc.Model.Provider, doc.Model.Model); e == nil {
				prov, model, source = m.Provider, m.ID, ctrlproto.DefaultSourceWorld
			}
		}
	}

	if cardID = strings.TrimSpace(cardID); cardID != "" {
		if cm, ok, _ := w.cardModelStore().Get(cardID); ok {
			if m, e := modelreg.FindModel(cm.Provider, cm.Model); e == nil {
				return m.Provider, m.ID, ctrlproto.DefaultSourceCard
			}
		}
	}
	return prov, model, source
}

// defaultReachable reports whether a configured default on provider p can seed
// a session ([build.ProviderConfigured]).
//
// 🔑 A pin whose login EXPIRED counts, on purpose. Its session refuses with
// CodeNoCredential, and the host offers /login or the other provider for that
// one session. Seeding the fallback instead would bill another account with
// nothing asked (TestADefaultSessionStillRefusesRatherThanSwitchingItself). A
// login that is simply absent has no such offer to make, so it degrades.
//
// ⚠️ A presence check, never a resolve: createSeededLocked reaches this with
// w.mu held, and a resolve can refresh a token over the network.
func (w *Workspace) defaultReachable(p string) bool {
	return build.ProviderConfigured(p)
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
		m, e := modelreg.FindModel(prov, model)
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
