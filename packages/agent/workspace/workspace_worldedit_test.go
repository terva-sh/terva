package workspace

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// The sessionless saved-World content verbs (WS-1). Every case here runs with
// NO session anywhere — that is the property under test, not an incidental
// setup choice: before these verbs, a saved World's roster, lorebook, and
// coordination could only be written by opening a scene in it.

func worldEditFixture(t *testing.T) (*Workspace, context.Context) {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w, context.Background()
}

// savedWorld puts a World in the library directly. There is deliberately no
// sessionless create verb — worlds.save promotes a session's World — so a test
// of the EDIT verbs seeds the store rather than inventing a creation path.
func savedWorld(t *testing.T, doc build.WorldDoc) build.WorldDoc {
	t.Helper()
	saved, err := build.NewWorldStore().Save(doc)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func loreNames(v ctrlproto.WorldView) []string {
	out := make([]string, 0, len(v.Lore))
	for _, e := range v.Lore {
		out = append(out, e.Name)
	}
	return out
}

func TestWorldsLorePutAndDelete(t *testing.T) {
	w, ctx := worldEditFixture(t)
	world := savedWorld(t, build.WorldDoc{Name: "Bellhaven", Lore: []core.WorldLoreEntry{
		{Name: "Curfew", Keys: []string{"curfew"}, Content: "The bells ring at dusk.", Learned: map[string]string{"Kobeni": "2026-01-01T00:00:00Z"}},
		{Name: "Docks", Keys: []string{"docks"}, Content: "Tar and rope."},
	}})

	// An update lands IN PLACE — an edit must not shuffle the book.
	v, err := w.WorldsLorePut(ctx, ctrlproto.WorldsLorePutParams{ID: world.ID, Entry: ctrlproto.WorldLoreEntry{
		Name: "Curfew", Keys: []string{"curfew", "bells"}, Content: "The bells ring at dusk, and the gates close.",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := loreNames(v); len(got) != 2 || got[0] != "Curfew" || got[1] != "Docks" {
		t.Fatalf("an update reordered the book: %v", got)
	}
	// The learned-when ledger is provenance, not content: a user edit keeps it.
	if len(v.Lore[0].Learned) != 1 {
		t.Errorf("the learned ledger did not survive an edit: %+v", v.Lore[0].Learned)
	}

	// A rename edits in place rather than appending a second entry.
	v, err = w.WorldsLorePut(ctx, ctrlproto.WorldsLorePutParams{ID: world.ID, Replace: "Docks", Entry: ctrlproto.WorldLoreEntry{
		Name: "The docks", Keys: []string{"docks"}, Content: "Tar and rope.",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := loreNames(v); len(got) != 2 || got[1] != "The docks" {
		t.Fatalf("rename did not edit in place: %v", got)
	}

	// A rename ONTO an existing name would leave two entries answering to one
	// name, and every later verb keys on names.
	if _, err := w.WorldsLorePut(ctx, ctrlproto.WorldsLorePutParams{ID: world.ID, Replace: "The docks", Entry: ctrlproto.WorldLoreEntry{
		Name: "Curfew", Keys: []string{"x"}, Content: "collides",
	}}); err == nil {
		t.Error("a rename onto an existing entry's name should be refused")
	}

	// The wire validation is shared with the session path: content is required,
	// and keys are required unless the entry is always-on.
	if _, err := w.WorldsLorePut(ctx, ctrlproto.WorldsLorePutParams{ID: world.ID, Entry: ctrlproto.WorldLoreEntry{Name: "Empty"}}); err == nil {
		t.Error("an entry with no content should be refused")
	}
	if _, err := w.WorldsLorePut(ctx, ctrlproto.WorldsLorePutParams{ID: world.ID, Entry: ctrlproto.WorldLoreEntry{Name: "Unfireable", Content: "never activates"}}); err == nil {
		t.Error("an entry with neither keys nor constant should be refused")
	}

	v, err = w.WorldsLoreDelete(ctx, ctrlproto.WorldsLoreDeleteParams{ID: world.ID, Name: "Curfew"})
	if err != nil {
		t.Fatal(err)
	}
	if got := loreNames(v); len(got) != 1 || got[0] != "The docks" {
		t.Fatalf("after delete: %v", got)
	}
	// A delete that matched nothing is a no-op, and reporting success for it
	// would let a client believe it removed something.
	if _, err := w.WorldsLoreDelete(ctx, ctrlproto.WorldsLoreDeleteParams{ID: world.ID, Name: "Curfew"}); err == nil {
		t.Error("deleting an absent entry should be refused")
	}

	// The write reached DISK, not just the returned view.
	reread, err := build.NewWorldStore().Get(world.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reread.Lore) != 1 || reread.Lore[0].Name != "The docks" {
		t.Fatalf("the store holds %+v", reread.Lore)
	}
}

func TestWorldsSetCoordination(t *testing.T) {
	w, ctx := worldEditFixture(t)
	world := savedWorld(t, build.WorldDoc{Name: "Bellhaven", Characters: map[string]string{"Kobeni": "kobeni-abc"}})

	for _, mode := range []string{"", CoordinationOff, "focus:Kobeni"} {
		if _, err := w.WorldsSet(ctx, ctrlproto.WorldsSetParams{ID: world.ID, Coordination: mode}); err != nil {
			t.Errorf("mode %q: %v", mode, err)
		}
	}
	// Focus is validated against the SAVED roster — the roster a new session in
	// this World would actually start with.
	if _, err := w.WorldsSet(ctx, ctrlproto.WorldsSetParams{ID: world.ID, Coordination: "focus:Nobody"}); err == nil {
		t.Error("focusing someone off the roster should be refused")
	}
	if _, err := w.WorldsSet(ctx, ctrlproto.WorldsSetParams{ID: world.ID, Coordination: "sideways"}); err == nil {
		t.Error("an unknown coordination mode should be refused")
	}
}

func TestWorldsRosterAddAndRemove(t *testing.T) {
	w, ctx := worldEditFixture(t)
	world := savedWorld(t, build.WorldDoc{Name: "Bellhaven"})
	card, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Kobeni","first_mes":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}

	v, err := w.WorldsAddCharacter(ctx, ctrlproto.WorldsAddCharacterParams{ID: world.ID, Name: "Kobeni", Ref: card.ID})
	if err != nil {
		t.Fatal(err)
	}
	if v.Characters["Kobeni"] != card.ID {
		t.Fatalf("roster is %+v", v.Characters)
	}

	if _, err := w.WorldsAddCharacter(ctx, ctrlproto.WorldsAddCharacterParams{ID: world.ID, Name: "  ", Ref: card.ID}); err == nil {
		t.Error("a blank name should be refused")
	}
	if _, err := w.WorldsAddCharacter(ctx, ctrlproto.WorldsAddCharacterParams{ID: world.ID, Name: "Ghost"}); err == nil {
		t.Error("a character with no card should be refused")
	}
	// A roster ref that resolves to nothing is a part that cannot be cast. It is
	// refused at the write rather than at some later session build, so the
	// broken state is never stored.
	if _, err := w.WorldsAddCharacter(ctx, ctrlproto.WorldsAddCharacterParams{ID: world.ID, Name: "Ghost", Ref: "not-a-real-card"}); err == nil {
		t.Error("a ref that resolves to no card should be refused")
	}
	if reread, _ := build.NewWorldStore().Get(world.ID); len(reread.Characters) != 1 {
		t.Fatalf("a refused add still wrote: %+v", reread.Characters)
	}

	// The model pin keys by roster NAME — it is "who plays this part", so it
	// survives re-pointing the part at a different card.
	if _, err := w.WorldSetCharacterModel(ctx, ctrlproto.WorldSetCharacterModelParams{ID: world.ID, Character: "Kobeni", Provider: "openai", Model: "gpt-5"}); err != nil {
		t.Fatal(err)
	}
	other, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Kobeni","first_mes":"a different take"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if other.ID == card.ID {
		t.Fatal("the fixture needs two distinct cards")
	}
	v, err = w.WorldsAddCharacter(ctx, ctrlproto.WorldsAddCharacterParams{ID: world.ID, Name: "Kobeni", Ref: other.ID})
	if err != nil {
		t.Fatal(err)
	}
	if v.Characters["Kobeni"] != other.ID {
		t.Errorf("re-adding a name should re-point it: %+v", v.Characters)
	}
	if v.CharacterModels["Kobeni"].Model != "gpt-5" {
		t.Errorf("the model pin should survive a re-point: %+v", v.CharacterModels)
	}

	// Focus on the character about to be removed: the removal has to clear it,
	// or the next session in this World routes every turn to a part that is
	// no longer castable.
	if _, err := w.WorldsSet(ctx, ctrlproto.WorldsSetParams{ID: world.ID, Coordination: "focus:Kobeni"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.WorldsRemoveCharacter(ctx, ctrlproto.WorldsRemoveCharacterParams{ID: world.ID, Name: "Nobody"}); err == nil {
		t.Error("removing someone off the roster should be refused")
	}
	v, err = w.WorldsRemoveCharacter(ctx, ctrlproto.WorldsRemoveCharacterParams{ID: world.ID, Name: "Kobeni"})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Characters) != 0 {
		t.Errorf("roster still holds %+v", v.Characters)
	}
	// The pin goes with them: pins key by name, so a left-behind one would
	// silently re-apply to whoever next took that name.
	if len(v.CharacterModels) != 0 {
		t.Errorf("the model pin outlived the character: %+v", v.CharacterModels)
	}
	reread, err := build.NewWorldStore().Get(world.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reread.Coordination != "" {
		t.Errorf("focus on a removed character should fall back to auto, got %q", reread.Coordination)
	}
}

// The propagation guarantee, end to end: an edit accepted inside one World must
// not reach the same character in another. This is the reason WS-3 exists, and
// it is asserted against the LIBRARY CARD'S BYTES rather than against ids —
// an implementation that repointed the roster but still rewrote the card would
// pass an id check and lose the whole point.
func TestWorldsEditCharacterDoesNotEscapeTheWorld(t *testing.T) {
	w, ctx := worldEditFixture(t)
	card, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Kobeni","personality":"anxious","first_mes":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	// TWO Worlds casting the SAME library card — the situation the fork protects.
	here := savedWorld(t, build.WorldDoc{Name: "Bellhaven", Characters: map[string]string{"Kobeni": card.ID}})
	elsewhere := savedWorld(t, build.WorldDoc{Name: "Lowtown", Characters: map[string]string{"Kobeni": card.ID}})

	before, err := w.CardsGet(ctx, ctrlproto.CardGetParams{ID: card.ID})
	if err != nil {
		t.Fatal(err)
	}

	res, err := w.WorldsEditCharacter(ctx, ctrlproto.WorldsEditCharacterParams{
		ID: here.ID, Character: "Kobeni",
		Card: []byte(`{"name":"Kobeni","personality":"sharper when cornered","first_mes":"hi"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Forked || res.CardID == card.ID {
		t.Fatalf("an edit inside a World must fork, got %+v", res)
	}
	if res.World.Characters["Kobeni"] != res.CardID {
		t.Errorf("the roster was not repointed at the fork: %+v", res.World.Characters)
	}

	// The library card is untouched...
	after, err := w.CardsGet(ctx, ctrlproto.CardGetParams{ID: card.ID})
	if err != nil {
		t.Fatal(err)
	}
	if string(after.Raw) != string(before.Raw) {
		t.Fatalf("the library card was rewritten:\n before %s\n after  %s", before.Raw, after.Raw)
	}
	// ...and so is the other World, which is what "does not escape" means.
	other, err := build.NewWorldStore().Get(elsewhere.ID)
	if err != nil {
		t.Fatal(err)
	}
	if other.Characters["Kobeni"] != card.ID {
		t.Errorf("the other World's roster moved: %+v", other.Characters)
	}

	// The fork is marked as this World's variant, so the library can tell it
	// apart from a card the author made on purpose.
	origin, ok, err := build.NewCardOriginStore().Get(res.CardID)
	if err != nil || !ok {
		t.Fatalf("the fork recorded no origin (ok=%v, err=%v)", ok, err)
	}
	if origin.World != here.ID || origin.ForkedFrom != card.ID {
		t.Errorf("origin is %+v", origin)
	}
}

// also_library is the explicit opt-out: the same call, asked to reach everyone.
func TestWorldsEditCharacterAlsoLibraryWritesThrough(t *testing.T) {
	w, ctx := worldEditFixture(t)
	card, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Kobeni","personality":"anxious","first_mes":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	world := savedWorld(t, build.WorldDoc{Name: "Bellhaven", Characters: map[string]string{"Kobeni": card.ID}})

	if _, err := w.WorldsEditCharacter(ctx, ctrlproto.WorldsEditCharacterParams{
		ID: world.ID, Character: "Kobeni", AlsoLibrary: true,
		Card: []byte(`{"name":"Kobeni","personality":"sharper when cornered","first_mes":"hi"}`),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := w.CardsGet(ctx, ctrlproto.CardGetParams{ID: card.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got.Raw), "sharper when cornered") {
		t.Fatalf("also_library did not reach the library card: %s", got.Raw)
	}
	// Having rewritten the library card, the fork resolves back to it — so this
	// must NOT also leave a variant behind.
	if len(mustOrigins(t)) != 0 {
		t.Errorf("also_library left a variant: %+v", mustOrigins(t))
	}
}

// An edit that changes nothing must not fork. A doctor applying a proposal that
// turned out to be a no-op would otherwise twin the card every time.
func TestWorldsEditCharacterNoOpDoesNotFork(t *testing.T) {
	w, ctx := worldEditFixture(t)
	card, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Kobeni","personality":"anxious","first_mes":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	world := savedWorld(t, build.WorldDoc{Name: "Bellhaven", Characters: map[string]string{"Kobeni": card.ID}})
	cur, err := w.CardsGet(ctx, ctrlproto.CardGetParams{ID: card.ID})
	if err != nil {
		t.Fatal(err)
	}

	res, err := w.WorldsEditCharacter(ctx, ctrlproto.WorldsEditCharacterParams{ID: world.ID, Character: "Kobeni", Card: cur.Raw})
	if err != nil {
		t.Fatal(err)
	}
	if res.Forked || res.CardID != card.ID {
		t.Fatalf("a no-op edit forked: %+v", res)
	}
	cards, err := w.CardsList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards.Cards) != 1 {
		t.Errorf("a no-op edit added a card: %d in the library", len(cards.Cards))
	}
}

// A variant is hidden from the shelf only while its World still claims it, so
// the two ways a World can stop claiming it both un-hide the card — with no
// cleanup pass anywhere.
func TestWorldVariantMarkingIsDerived(t *testing.T) {
	w, ctx := worldEditFixture(t)
	card, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Kobeni","personality":"anxious","first_mes":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	world := savedWorld(t, build.WorldDoc{Name: "Bellhaven", Characters: map[string]string{"Kobeni": card.ID}})
	res, err := w.WorldsEditCharacter(ctx, ctrlproto.WorldsEditCharacterParams{
		ID: world.ID, Character: "Kobeni",
		Card: []byte(`{"name":"Kobeni","personality":"sharper","first_mes":"hi"}`),
	})
	if err != nil {
		t.Fatal(err)
	}

	variantOf := func(id string) string {
		t.Helper()
		list, err := w.CardsList(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range list.Cards {
			if c.ID == id {
				return c.VariantOf
			}
		}
		t.Fatalf("card %q is not in the library", id)
		return ""
	}
	if got := variantOf(res.CardID); got != world.ID {
		t.Fatalf("the fork is not marked as %q's variant, got %q", world.ID, got)
	}
	if got := variantOf(card.ID); got != "" {
		t.Errorf("the original was marked a variant of %q", got)
	}

	// Editing the SAME character again forks from the variant, leaving the first
	// fork with an origin record naming a World that no longer casts it. Without
	// the "still rosters it" half of the rule that intermediate card would stay
	// hidden from the shelf forever — an orphan nothing points at and nothing
	// can reach. Successive doctor edits are the common path, so this is where
	// invisible clutter would accumulate.
	second, err := w.WorldsEditCharacter(ctx, ctrlproto.WorldsEditCharacterParams{
		ID: world.ID, Character: "Kobeni",
		Card: []byte(`{"name":"Kobeni","personality":"sharper, and done apologising","first_mes":"hi"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.CardID == res.CardID {
		t.Fatal("the second edit should have forked again")
	}
	if got := variantOf(second.CardID); got != world.ID {
		t.Errorf("the newest fork is not the World's variant, got %q", got)
	}
	if got := variantOf(res.CardID); got != "" {
		t.Errorf("the superseded fork is still hidden as a variant of %q", got)
	}

	// Deleting the World releases its variants — nothing runs a cleanup, the
	// answer just changes because the derivation's premise is gone.
	if err := w.WorldDelete(ctx, ctrlproto.WorldDeleteParams{ID: world.ID}); err != nil {
		t.Fatal(err)
	}
	if got := variantOf(second.CardID); got != "" {
		t.Errorf("deleting the World left its variant hidden, still %q", got)
	}
}

// Taking a character off the roster releases their variant too — the other way
// a World can stop claiming a card.
func TestWorldVariantReleasedOnRemove(t *testing.T) {
	w, ctx := worldEditFixture(t)
	card, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Kobeni","personality":"anxious","first_mes":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	world := savedWorld(t, build.WorldDoc{Name: "Bellhaven", Characters: map[string]string{"Kobeni": card.ID}})
	res, err := w.WorldsEditCharacter(ctx, ctrlproto.WorldsEditCharacterParams{
		ID: world.ID, Character: "Kobeni",
		Card: []byte(`{"name":"Kobeni","personality":"sharper","first_mes":"hi"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WorldsRemoveCharacter(ctx, ctrlproto.WorldsRemoveCharacterParams{ID: world.ID, Name: "Kobeni"}); err != nil {
		t.Fatal(err)
	}
	list, err := w.CardsList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list.Cards {
		if c.ID == res.CardID && c.VariantOf != "" {
			t.Errorf("a dropped character's variant is still hidden as %q's", c.VariantOf)
		}
	}
	// And its origin record is gone, not merely out-voted by the derivation:
	// the record claims something no longer true.
	if _, ok, _ := build.NewCardOriginStore().Get(res.CardID); ok {
		t.Error("the origin record outlived the roster slot that justified it")
	}
}

func mustOrigins(t *testing.T) map[string]build.CardOrigin {
	t.Helper()
	all, err := build.NewCardOriginStore().All()
	if err != nil {
		t.Fatal(err)
	}
	return all
}

// Every verb in the family resolves its World first, so an unknown id is a
// not-found rather than a panic or a silently-created World.
func TestWorldsEditVerbsRejectUnknownWorld(t *testing.T) {
	w, ctx := worldEditFixture(t)
	calls := map[string]func() error{
		"worlds.lore.put": func() error {
			_, err := w.WorldsLorePut(ctx, ctrlproto.WorldsLorePutParams{ID: "no-such-world", Entry: ctrlproto.WorldLoreEntry{Name: "A", Constant: true, Content: "c"}})
			return err
		},
		"worlds.lore.delete": func() error {
			_, err := w.WorldsLoreDelete(ctx, ctrlproto.WorldsLoreDeleteParams{ID: "no-such-world", Name: "A"})
			return err
		},
		"worlds.set": func() error {
			_, err := w.WorldsSet(ctx, ctrlproto.WorldsSetParams{ID: "no-such-world"})
			return err
		},
		"worlds.add_character": func() error {
			_, err := w.WorldsAddCharacter(ctx, ctrlproto.WorldsAddCharacterParams{ID: "no-such-world", Name: "A", Ref: "r"})
			return err
		},
		"worlds.remove_character": func() error {
			_, err := w.WorldsRemoveCharacter(ctx, ctrlproto.WorldsRemoveCharacterParams{ID: "no-such-world", Name: "A"})
			return err
		},
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Errorf("%s accepted an unknown World id", name)
		}
	}
}

// worlds.set_model writes the World rung of the default-model ladder. The
// catalog check is the load-bearing part: effectiveDefaultModel degrades
// SILENTLY past a default it cannot resolve, so a typo accepted here would look
// saved in the picker and never once decide a session.
func TestWorldsSetModel(t *testing.T) {
	w, ctx := worldEditFixture(t)
	world := savedWorld(t, build.WorldDoc{Name: "Bellhaven"})

	v, err := w.WorldSetModel(ctx, ctrlproto.WorldSetModelParams{ID: world.ID, Provider: "openai", Model: "gpt-5.5"})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if v.Model.Provider != "openai" || v.Model.Model != "gpt-5.5" {
		t.Errorf("view model = %s/%s, want openai/gpt-5.5", v.Model.Provider, v.Model.Model)
	}
	// The view is what the client re-renders from, but disk is what the next
	// session reads — assert both, or a verb that answered correctly and wrote
	// nothing would pass.
	if doc, err := build.NewWorldStore().Get(world.ID); err != nil || doc.Model.Model != "gpt-5.5" {
		t.Errorf("stored model = %q (err %v), want gpt-5.5", doc.Model.Model, err)
	}

	// Both fields empty clears it — the picker's Default row sends exactly this,
	// and it must mean "inherit again", not "store two empty strings".
	v, err = w.WorldSetModel(ctx, ctrlproto.WorldSetModelParams{ID: world.ID})
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if v.Model.Model != "" {
		t.Errorf("cleared model = %q, want empty", v.Model.Model)
	}

	if _, err := w.WorldSetModel(ctx, ctrlproto.WorldSetModelParams{ID: world.ID, Model: "no-such-model-xyz"}); err == nil {
		t.Error("a model the catalog does not hold must be refused, not stored to fail silently later")
	}
	if _, err := w.WorldSetModel(ctx, ctrlproto.WorldSetModelParams{ID: "no-such-world", Model: "gpt-5"}); err == nil {
		t.Error("an unknown World must be refused")
	}
}

// A character the World INVENTED is not a variant, and the difference decides
// whether the shelf shows it. A fork has an original to fall back to; this has
// none, so hiding it would put a card beyond reach entirely.
func TestWorldsCreateCharacterIsBornNotForked(t *testing.T) {
	w, ctx := worldEditFixture(t)
	world := savedWorld(t, build.WorldDoc{Name: "Bellhaven"})

	res, err := w.WorldsCreateCharacter(ctx, ctrlproto.WorldsCreateCharacterParams{
		ID: world.ID, Name: "Kira", Card: []byte(`{"name":"Kira","description":"a rival chandler","first_mes":"hm."}`),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if res.CardID == "" || res.World.Characters["Kira"] != res.CardID {
		t.Fatalf("the new card must be on the roster: %+v", res)
	}
	// One operation: the card exists, the roster points at it, and the origin
	// records the World — all three, or the verb was not worth adding.
	if _, err := build.NewCardStore().Get(res.CardID); err != nil {
		t.Errorf("the card should be in the library: %v", err)
	}
	if doc, _ := build.NewWorldStore().Get(world.ID); doc.Characters["Kira"] != res.CardID {
		t.Error("the roster write did not reach disk")
	}
	origin, ok, _ := build.NewCardOriginStore().Get(res.CardID)
	if !ok || origin.World != world.ID {
		t.Fatalf("origin = %+v (ok=%v), want the World", origin, ok)
	}
	// THE distinction: no ForkedFrom. That absence is what keeps the card on the
	// shelf instead of hidden as somebody's near-duplicate.
	if origin.ForkedFrom != "" {
		t.Errorf("a character born in a World was forked from nothing, got %q", origin.ForkedFrom)
	}

	// And the derivation reads it that way: owned by the World, not a variant.
	list, err := w.CardsList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, c := range list.Cards {
		if c.ID != res.CardID {
			continue
		}
		found = true
		if c.WorldOf != world.ID {
			t.Errorf("world_of = %q, want %q — the badge has nothing to read", c.WorldOf, world.ID)
		}
		if c.VariantOf != "" {
			t.Errorf("variant_of = %q — a born character is not a copy, and this hides it from the shelf", c.VariantOf)
		}
	}
	if !found {
		t.Error("a character born in a World must still be IN the library listing")
	}
}

// The refusals, all of which have to land before anything is written — a
// half-applied create is the failure this verb exists to remove.
func TestWorldsCreateCharacterRefusals(t *testing.T) {
	w, ctx := worldEditFixture(t)
	world := savedWorld(t, build.WorldDoc{Name: "Bellhaven", Characters: map[string]string{"Kira": "kira-existing0"}})
	body := []byte(`{"name":"Kira","first_mes":"hm."}`)

	for _, tc := range []struct {
		name string
		p    ctrlproto.WorldsCreateCharacterParams
	}{
		{"no name", ctrlproto.WorldsCreateCharacterParams{ID: world.ID, Card: body}},
		{"no card", ctrlproto.WorldsCreateCharacterParams{ID: world.ID, Name: "Rook"}},
		{"unknown world", ctrlproto.WorldsCreateCharacterParams{ID: "no-such-world", Name: "Rook", Card: body}},
		{"name already on the roster", ctrlproto.WorldsCreateCharacterParams{ID: world.ID, Name: "Kira", Card: body}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := w.WorldsCreateCharacter(ctx, tc.p); err == nil {
				t.Error("must be refused")
			}
		})
	}
	// The roster is exactly as it was — no refusal wrote anything.
	doc, _ := build.NewWorldStore().Get(world.ID)
	if len(doc.Characters) != 1 || doc.Characters["Kira"] != "kira-existing0" {
		t.Errorf("a refusal changed the roster: %+v", doc.Characters)
	}
}

// A card BORROWED into a World claims no provenance. Inventing a character and
// casting one you already had are different acts, and only one of them makes
// the World the character's home.
func TestWorldsAddCharacterClaimsNoProvenance(t *testing.T) {
	w, ctx := worldEditFixture(t)
	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Rook","first_mes":"hi"}`)})
	if err != nil {
		t.Fatal(err)
	}
	world := savedWorld(t, build.WorldDoc{Name: "Bellhaven"})
	if _, err := w.WorldsAddCharacter(ctx, ctrlproto.WorldsAddCharacterParams{ID: world.ID, Name: "Rook", Ref: imported.ID}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := build.NewCardOriginStore().Get(imported.ID); ok {
		t.Error("borrowing an existing card into a World must not claim it was born there")
	}
	list, _ := w.CardsList(ctx)
	for _, c := range list.Cards {
		if c.ID == imported.ID && c.WorldOf != "" {
			t.Errorf("a borrowed card got a World badge: world_of=%q", c.WorldOf)
		}
	}
}
