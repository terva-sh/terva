package build

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/card"
	"terva.sh/terva/packages/agent/slug"
	"terva.sh/terva/packages/testsupport"
)

// Fork is the copy-on-write half of Duplicate: it exists so an edit made in one
// place cannot rewrite a card other places are still using. The property that
// matters is therefore not "a new card appeared" but "the ORIGINAL is
// byte-for-byte what it was", which every case here checks directly.

func forkFixture(t *testing.T) *CardStore {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	return NewCardStore()
}

func cardBytes(t *testing.T, s *CardStore, id string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.dir, id, cardJSONName))
	if err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	return raw
}

func TestForkLeavesTheOriginalUntouched(t *testing.T) {
	s := forkFixture(t)
	src, err := s.ImportBytes([]byte(`{"name":"Kobeni","personality":"anxious","first_mes":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	before := cardBytes(t, s, src.ID)

	forked, err := s.Fork(src.ID, []byte(`{"name":"Kobeni","personality":"anxious, but sharper when cornered","first_mes":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if forked.ID == src.ID {
		t.Fatal("a changed card must fork to a new id")
	}
	// THE guarantee. Not "the original still exists" — the original's bytes are
	// unchanged, which is what every other World and session reading that ref
	// depends on.
	if got := cardBytes(t, s, src.ID); string(got) != string(before) {
		t.Fatalf("fork rewrote the original:\n before %s\n after  %s", before, got)
	}
	if forked.Card.Personality != "anxious, but sharper when cornered" {
		t.Errorf("the fork did not take the edit: %q", forked.Card.Personality)
	}
	// The name is deliberately shared: Kobeni in one World and Kobeni in another
	// are both Kobeni. Only the contents diverge — which is exactly why Fork
	// cannot reuse Duplicate, whose whole contract is a NEW name.
	if forked.Card.Name != "Kobeni" {
		t.Errorf("a fork keeps the character's name, got %q", forked.Card.Name)
	}
}

func TestForkOfAnUnchangedCardIsANoOp(t *testing.T) {
	s := forkFixture(t)
	src, err := s.ImportBytes([]byte(`{"name":"Kobeni","personality":"anxious","first_mes":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw := cardBytes(t, s, src.ID)

	// Forking the card's own current document must return the card itself. A
	// caller that forks speculatively — a doctor applying a proposal that turned
	// out to change nothing — would otherwise litter the library with a twin.
	same, err := s.Fork(src.ID, raw)
	if err != nil {
		t.Fatal(err)
	}
	if same.ID != src.ID {
		t.Fatalf("an unchanged fork minted %q, want the original %q", same.ID, src.ID)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("a no-op fork added a card: %d in the library", len(list))
	}
	// And it must not burn a revision slot recording a change that did not
	// happen — retention is 10, and idle saves would push out the as-imported
	// revision that matters most.
	vs, err := s.history().List(src.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 0 {
		t.Errorf("a no-op fork recorded %d revision(s)", len(vs))
	}
}

// The no-op case again, on a card that has ALREADY BEEN EDITED — which is where
// the first implementation was wrong and the fresh-import case above could not
// see it.
//
// A card's id is minted at import and never re-derived, so one edit is enough to
// make id != hash(contents). An implementation that decides "did anything
// change?" by re-deriving the id therefore calls an identical document a change,
// and forks a perfect twin. Every card the world doctor has touched once is in
// this state, so this is the common path, not a corner.
func TestForkOfAnEditedCardIsStillANoOp(t *testing.T) {
	s := forkFixture(t)
	src, err := s.ImportBytes([]byte(`{"name":"Kobeni","personality":"anxious","first_mes":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Edit(src.ID, []byte(`{"name":"Kobeni","personality":"sharper","first_mes":"hi"}`)); err != nil {
		t.Fatal(err)
	}
	// The id has not moved, but it no longer hashes its own contents — the
	// premise the bug depended on. Asserted so this test cannot quietly become
	// a duplicate of the fresh-import case if Edit ever starts re-deriving ids.
	edited, err := s.Get(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	editedRaw, err := card.Marshal(edited.Card)
	if err != nil {
		t.Fatal(err)
	}
	if slug.ID(edited.Card.Name, editedRaw) == src.ID {
		t.Fatal("the fixture needs a card whose id no longer matches its contents")
	}

	same, err := s.Fork(src.ID, cardBytes(t, s, src.ID))
	if err != nil {
		t.Fatal(err)
	}
	if same.ID != src.ID {
		t.Fatalf("forking an edited card's own document minted %q, want %q", same.ID, src.ID)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("a no-op fork of an edited card twinned it: %d cards", len(list))
	}
}

func TestForkCarriesThePortrait(t *testing.T) {
	s := forkFixture(t)
	src, err := s.ImportBytes([]byte(`{"name":"Kobeni","personality":"anxious","first_mes":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	// Stand in a portrait the way an avatar-bearing import would have left one.
	avatar := []byte("not-really-a-png-but-bytes")
	if err := os.WriteFile(filepath.Join(s.dir, src.ID, cardAvatarName), avatar, 0o644); err != nil {
		t.Fatal(err)
	}

	forked, err := s.Fork(src.ID, []byte(`{"name":"Kobeni","personality":"sharper","first_mes":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(s.dir, forked.ID, cardAvatarName))
	if err != nil {
		t.Fatalf("the fork lost its portrait: %v", err)
	}
	// Copied from the STORED avatar, not re-derived: the stored one has already
	// been normalized, so a fork inherits the picture the original actually
	// shows rather than a second downscale of it.
	if string(got) != string(avatar) {
		t.Errorf("the fork's portrait is not the original's bytes")
	}
}

func TestForkRefusals(t *testing.T) {
	s := forkFixture(t)
	src, err := s.ImportBytes([]byte(`{"name":"Kobeni","first_mes":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Fork("no-such-card", []byte(`{"name":"X"}`)); err == nil {
		t.Error("forking a card that is not there should be refused")
	}
	if _, err := s.Fork(src.ID, []byte(`{"not":"a card"`)); err == nil {
		t.Error("a malformed document should be refused")
	}
	// A card needs a name; card.ParseJSON is the shared validation and Fork must
	// not route around it.
	if _, err := s.Fork(src.ID, []byte(`{"first_mes":"nameless"}`)); err == nil {
		t.Error("a card with no name should be refused")
	}
}

// A fork whose contents land on a card that ALREADY exists returns that card.
// Content-addressing means it is byte-for-byte what the fork would have written,
// so nothing is displaced and the caller still gets the card it asked for.
func TestForkOntoAnExistingCardReturnsIt(t *testing.T) {
	s := forkFixture(t)
	a, err := s.ImportBytes([]byte(`{"name":"Kobeni","personality":"anxious","first_mes":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.ImportBytes([]byte(`{"name":"Kobeni","personality":"sharper","first_mes":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatal("the fixture needs two distinct cards")
	}
	bBefore := cardBytes(t, s, b.ID)

	got, err := s.Fork(a.ID, cardBytes(t, s, b.ID))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != b.ID {
		t.Fatalf("forking onto existing contents returned %q, want %q", got.ID, b.ID)
	}
	if string(cardBytes(t, s, b.ID)) != string(bBefore) {
		t.Error("the existing card was rewritten")
	}
	if vs, _ := s.history().List(b.ID, nil); len(vs) != 0 {
		t.Errorf("the existing card took a revision it should not have: %d", len(vs))
	}
}
