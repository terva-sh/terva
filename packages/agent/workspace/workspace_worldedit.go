package workspace

import (
	"context"
	"strings"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// Saved-World CONTENT edits, sessionless (WS-1).
//
// The `world.*` family in workspace_world.go writes a SESSION's working copy of
// its World; these write the SAVED World by id, with no session anywhere. The
// split matters because until now a saved World was only writable through a
// session — `worlds.update` reached its name, description, and cover, and
// nothing reached its roster, lorebook, or coordination. That made the World
// shelf a read-only display and forced every authoring surface, including the
// world doctor, to open a scene first just to have somewhere to apply a change.
//
// The two scopes deliberately share their rules, not just their shape:
// putWorldLore, deleteWorldLore, and checkCoordination live in
// workspace_world.go and are called from both. A saved World and a session's
// copy hold the SAME book — a session seeds from the save and worlds.save
// writes back — so two upsert rules would mean promoting a book could reshape
// it.
//
// Each verb answers with the stored WorldView, so a client renders from the
// write's own result rather than re-listing and hoping it raced correctly.
// Sessions is 0 in that view, matching worlds.update and
// worlds.set_character_model: the count belongs to the listing, and a mutation
// answer that guessed at it would be the stale number a client trusted.
//
// None of these touch member sessions. A session already in this World keeps
// its working copy untouched — the same explicit, never-live sync the store
// documents (build/worldstore.go). Edits here seed the NEXT session started in
// the World.

// loadWorld resolves the id every verb in this file starts from, mapping a
// missing World onto NotFound rather than the store's bare error.
func loadWorld(id string) (*build.WorldStore, build.WorldDoc, error) {
	store := build.NewWorldStore()
	doc, err := store.Get(strings.TrimSpace(id))
	if err != nil {
		return nil, build.WorldDoc{}, ctrlproto.Errorf(ctrlproto.CodeNotFound, "%v", err)
	}
	return store, doc, nil
}

// saveWorldDoc writes the doc back and answers with its view. The library
// broadcast tells open shelves to re-read: a World edited from the studio has
// to reach a Library rendered in another tab, which is exactly what
// SurfaceUpdatedEvent("characters") already drives for card and World changes.
func (w *Workspace) saveWorldDoc(store *build.WorldStore, doc build.WorldDoc, what string) (ctrlproto.WorldView, error) {
	saved, err := store.Save(doc)
	if err != nil {
		return ctrlproto.WorldView{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "%s: %v", what, err)
	}
	w.broadcastLibraryChanged()
	return worldDocToView(saved, 0), nil
}

// WorldsLorePut adds or updates one lore entry on a saved World — the
// sessionless twin of WorldLorePut, sharing its upsert rule.
func (w *Workspace) WorldsLorePut(_ context.Context, p ctrlproto.WorldsLorePutParams) (ctrlproto.WorldView, error) {
	store, doc, err := loadWorld(p.ID)
	if err != nil {
		return ctrlproto.WorldView{}, err
	}
	entry, err := worldLoreFromWire(p.Entry)
	if err != nil {
		return ctrlproto.WorldView{}, err
	}
	// No stampScenePin here, unlike the session path: the scene-state pin is
	// dated against a message count, and a saved World has no transcript to be
	// stale against. A pin lifted into the save keeps the stamp it earned in the
	// scene it was played in.
	next, err := putWorldLore(doc.Lore, entry, p.Replace)
	if err != nil {
		return ctrlproto.WorldView{}, err
	}
	doc.Lore = next
	return w.saveWorldDoc(store, doc, "put world lore")
}

// WorldsLoreDelete removes one lore entry from a saved World.
func (w *Workspace) WorldsLoreDelete(_ context.Context, p ctrlproto.WorldsLoreDeleteParams) (ctrlproto.WorldView, error) {
	store, doc, err := loadWorld(p.ID)
	if err != nil {
		return ctrlproto.WorldView{}, err
	}
	next, err := deleteWorldLore(doc.Lore, p.Name)
	if err != nil {
		return ctrlproto.WorldView{}, err
	}
	doc.Lore = next
	return w.saveWorldDoc(store, doc, "delete world lore")
}

// WorldsSet updates a saved World's coordination mode (W3), validated against
// the SAVED roster — the roster a new session in this World would actually
// start with.
func (w *Workspace) WorldsSet(_ context.Context, p ctrlproto.WorldsSetParams) (ctrlproto.WorldView, error) {
	store, doc, err := loadWorld(p.ID)
	if err != nil {
		return ctrlproto.WorldView{}, err
	}
	mode := strings.TrimSpace(p.Coordination)
	if err := checkCoordination(mode, doc.Characters); err != nil {
		return ctrlproto.WorldView{}, err
	}
	doc.Coordination = mode
	return w.saveWorldDoc(store, doc, "set world coordination")
}

// WorldSetModel sets (or clears) the World's OWN default model — the world rung
// of the Card → World → Workspace ladder in effectiveDefaultModel, which stood
// reserved and unreachable until this verb gave it a writer.
//
// It is a different question from worlds.set_character_model, which pins ONE
// actor's voice on the routing path. This one sets the room's floor: what a
// scene started here opens on, what the World's own doctor runs on, and what a
// character with no pin of their own inherits.
//
// The pick is validated against the catalog before it is stored — unlike the
// per-character pin, which is read back by a routing path that degrades visibly
// to the session model. A default that fails to resolve degrades SILENTLY down
// the ladder, so a typo would be accepted here and simply never show up again.
func (w *Workspace) WorldSetModel(_ context.Context, p ctrlproto.WorldSetModelParams) (ctrlproto.WorldView, error) {
	store, doc, err := loadWorld(p.ID)
	if err != nil {
		return ctrlproto.WorldView{}, err
	}
	prov, model := strings.TrimSpace(p.Provider), strings.TrimSpace(p.Model)
	if prov == "" && model == "" {
		doc.Model = core.CastRoute{}
	} else {
		m, e := provider.FindModel(prov, model)
		if e != nil {
			return ctrlproto.WorldView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("unknown model %q", model))
		}
		doc.Model = core.CastRoute{Provider: m.Provider, Model: m.ID}
	}
	return w.saveWorldDoc(store, doc, "set world model")
}

// WorldsCreateCharacter imports a new card and rosters it, in one operation,
// recording that the character was born in this World.
//
// The order is deliberate: import first, then roster, then origin. A failed
// import leaves the World untouched; a failed roster leaves an unreferenced
// card, which is inert and re-importable rather than a World pointing at
// nothing. Origin goes LAST because it is the only one of the three that is
// meaningless on its own — a provenance record for a card no roster mentions.
//
// The origin record carries no ForkedFrom, and that absence is the whole
// distinction the shelf reads. A FORK is hidden there, because its original is
// still on the shelf and two near-identical cards with nothing to tell them
// apart is worse than one. A character born here has no original: hiding it
// would mean a card that exists only inside one World, unfindable, unexportable,
// and impossible to reuse anywhere else. So it stays on the shelf and is BADGED
// with the World instead.
func (w *Workspace) WorldsCreateCharacter(_ context.Context, p ctrlproto.WorldsCreateCharacterParams) (ctrlproto.WorldsCreateCharacterResult, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return ctrlproto.WorldsCreateCharacterResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("name the character to create"))
	}
	if len(p.Card) == 0 {
		return ctrlproto.WorldsCreateCharacterResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("worlds.create_character needs a card body"))
	}
	store, doc, err := loadWorld(p.ID)
	if err != nil {
		return ctrlproto.WorldsCreateCharacterResult{}, err
	}
	if _, taken := doc.Characters[name]; taken {
		return ctrlproto.WorldsCreateCharacterResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("%s is already on this World's roster", name))
	}
	stored, err := w.cardStore().ImportBytes(p.Card)
	if err != nil {
		return ctrlproto.WorldsCreateCharacterResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "import card: %v", err)
	}
	if doc.Characters == nil {
		doc.Characters = map[string]string{}
	}
	doc.Characters[name] = stored.ID
	view, err := w.saveWorldDoc(store, doc, "create world character")
	if err != nil {
		return ctrlproto.WorldsCreateCharacterResult{}, err
	}
	// Best-effort, and deliberately not fatal: the character IS on the roster
	// and playable. A missing origin record costs a badge, which is not worth
	// undoing a create over — and the derivation treats an absent record as "an
	// ordinary card", which is the safe reading.
	_ = build.NewCardOriginStore().Set(stored.ID, build.CardOrigin{World: doc.ID})
	return ctrlproto.WorldsCreateCharacterResult{World: view, CardID: stored.ID}, nil
}

// WorldsAddCharacter puts a character on a saved World's roster — the
// sessionless cast.add. Re-adding an existing name re-points it, which is how a
// swap is spelled; the model pin is keyed by NAME, so it survives a re-point
// deliberately (the pin is "who plays this part", not "which card").
func (w *Workspace) WorldsAddCharacter(_ context.Context, p ctrlproto.WorldsAddCharacterParams) (ctrlproto.WorldView, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return ctrlproto.WorldView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("name the character to add"))
	}
	ref := strings.TrimSpace(p.Ref)
	if ref == "" {
		return ctrlproto.WorldView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("%s needs a character card", name))
	}
	// Resolve before writing. A roster ref that resolves to nothing is a part
	// that cannot be cast — worlds.export already skips such an entry, and a
	// session created in the World would fail to build it. Refusing here keeps
	// the broken state from being written at all.
	if _, err := build.ResolveCardRef(ref); err != nil {
		return ctrlproto.WorldView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("no character card %q in your library", ref))
	}
	store, doc, err := loadWorld(p.ID)
	if err != nil {
		return ctrlproto.WorldView{}, err
	}
	if doc.Characters == nil {
		doc.Characters = map[string]string{}
	}
	doc.Characters[name] = ref
	return w.saveWorldDoc(store, doc, "add world character")
}

// WorldsRemoveCharacter takes a character off the roster. The model pin goes
// with them: pins are keyed by roster name, so a left-behind pin would silently
// re-apply to whoever next took that name.
func (w *Workspace) WorldsRemoveCharacter(_ context.Context, p ctrlproto.WorldsRemoveCharacterParams) (ctrlproto.WorldView, error) {
	store, doc, err := loadWorld(p.ID)
	if err != nil {
		return ctrlproto.WorldView{}, err
	}
	name := strings.TrimSpace(p.Name)
	ref, ok := doc.Characters[name]
	if !ok {
		return ctrlproto.WorldView{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("%s is not on this World's roster", name))
	}
	delete(doc.Characters, name)
	delete(doc.CharacterModels, name)
	// A coordination mode focused on the character just removed would name
	// someone no longer castable, and the next session in this World would
	// route every turn to a part that does not exist. Falling back to auto is
	// the only mode that is always valid.
	if doc.Coordination == coordinationFocus+name {
		doc.Coordination = ""
	}
	if len(doc.Characters) == 0 {
		doc.Characters = nil
	}
	// The fork's provenance goes with the roster slot that justified it: a
	// variant is "this World's take on X", so once the World stops casting it
	// the record is no longer true. Dropping it here means the card reappears in
	// the library as an ordinary card rather than as a hidden orphan.
	if origin, ok, _ := build.NewCardOriginStore().Get(ref); ok && origin.World == doc.ID {
		_ = build.NewCardOriginStore().Delete(ref)
	}
	return w.saveWorldDoc(store, doc, "remove world character")
}

// WorldsEditCharacter edits a roster character's card without the change
// escaping this World.
//
// The hazard it removes: a roster holds a plain card ref, and CardStore.Edit
// rewrites a card IN PLACE — the content hash is minted at import and never
// re-derived — so one library card is shared by every World, every session, and
// the shelf. An edit accepted inside one World through cards.edit rewrites the
// character every other World is still playing, silently and immediately.
//
// So the card is FORKED and the roster re-pointed. The original is never opened
// for writing. Content-addressing carries the whole scheme: an edit that changes
// the bytes hashes to a new id, and an edit that changes nothing hashes to the
// same one and is a no-op, so a speculative apply cannot litter the library with
// a twin.
//
// p.AlsoLibrary opts into the old behaviour for the case the fork exists to
// protect against — a fix that belongs to the character everywhere rather than
// to this World's take on them. It is off by default because the surface driving
// this is a doctor proposing edits to characters the author may be playing
// elsewhere, and the safe reading has to be the one you get by not deciding.
func (w *Workspace) WorldsEditCharacter(_ context.Context, p ctrlproto.WorldsEditCharacterParams) (ctrlproto.WorldsEditCharacterResult, error) {
	if len(p.Card) == 0 {
		return ctrlproto.WorldsEditCharacterResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("worlds.edit_character needs a card body"))
	}
	store, doc, err := loadWorld(p.ID)
	if err != nil {
		return ctrlproto.WorldsEditCharacterResult{}, err
	}
	name := strings.TrimSpace(p.Character)
	ref, ok := doc.Characters[name]
	if !ok {
		return ctrlproto.WorldsEditCharacterResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%s", i18n.T("%s is not on this World's roster", name))
	}

	cards := w.cardStore()
	// The library write goes FIRST when it was asked for, so that a refusal
	// (a malformed document, a card that has since been deleted) fails before
	// anything is forked and the World is left exactly as it was.
	if p.AlsoLibrary {
		if _, err := cards.Edit(ref, p.Card); err != nil {
			return ctrlproto.WorldsEditCharacterResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "edit card: %v", err)
		}
	}
	forked, err := cards.Fork(ref, p.Card)
	if err != nil {
		return ctrlproto.WorldsEditCharacterResult{}, ctrlproto.Errorf(ctrlproto.CodeBadRequest, "fork card: %v", err)
	}

	res := ctrlproto.WorldsEditCharacterResult{CardID: forked.ID, Forked: forked.ID != ref}
	if !res.Forked {
		// Nothing changed — with AlsoLibrary the library card was rewritten and
		// the fork resolved back to it, without it the document matched what was
		// already stored. Either way the roster still points at the right card
		// and there is nothing to record.
		w.broadcastLibraryChanged()
		res.World = worldDocToView(doc, 0)
		return res, nil
	}

	doc.Characters[name] = forked.ID
	// Provenance is what keeps the fork legible afterwards: without it the
	// variant is an anonymous near-duplicate on the shelf, indistinguishable
	// from a card the author made on purpose. ForkedFrom points at what this was
	// forked from, which may itself be a fork — re-editing a variant walks the
	// chain rather than flattening it, so "where did this come from" stays
	// answerable one hop at a time.
	if err := build.NewCardOriginStore().Set(forked.ID, build.CardOrigin{World: doc.ID, ForkedFrom: ref}); err != nil {
		return ctrlproto.WorldsEditCharacterResult{}, ctrlproto.Errorf(ctrlproto.CodeInternal, "record card origin: %v", err)
	}
	view, err := w.saveWorldDoc(store, doc, "edit world character")
	if err != nil {
		return ctrlproto.WorldsEditCharacterResult{}, err
	}
	res.World = view
	return res, nil
}
