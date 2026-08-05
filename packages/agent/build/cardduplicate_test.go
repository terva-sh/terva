package build

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// Duplicate exists because copying a card the two obvious ways fails quietly:
// re-importing an export returns the original (ids are content-addressed and
// ImportBytes is idempotent), and rebuilding the card from its JSON loses the
// portrait (the avatar lives outside the card document). These tests pin both
// halves — the picture travels, and a rename that would reproduce either
// silent-no-op is refused rather than written.

const dupCardJSON = `{"spec":"chara_card_v2","spec_version":"2.0","data":{` +
	`"name":"Kobeni","description":"a nervous devil hunter","personality":"anxious",` +
	`"first_mes":"...hello?","tags":["horror"],"extensions":{"vendor":{"keep":"me"}}}}`

func TestCardDuplicateCarriesThePortrait(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	png := mkCharaPNG(dupCardJSON)
	src, err := st.ImportBytes(png)
	if err != nil {
		t.Fatal(err)
	}
	if !src.HasAvatar() {
		t.Fatal("setup: the PNG import should have retained an avatar")
	}

	dup, err := st.Duplicate(src.ID, "Kobeni (copy)")
	if err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	if !dup.HasAvatar() {
		t.Error("the copy lost its portrait — which is the whole reason this is a server verb")
	}
	// Byte-identical, not merely present: the stored avatar has already been
	// through normalizeAvatar, so the copy must inherit the picture the original
	// shows rather than a second pass over it.
	orig, err := os.ReadFile(filepath.Join(CardsDir(), src.ID, cardAvatarName))
	if err != nil {
		t.Fatal(err)
	}
	copied, err := os.ReadFile(filepath.Join(CardsDir(), dup.ID, cardAvatarName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(orig, copied) {
		t.Error("the copy's portrait differs from the original's")
	}
}

func TestCardDuplicateIsASecondCard(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	src, err := st.ImportBytes([]byte(dupCardJSON))
	if err != nil {
		t.Fatal(err)
	}
	dup, err := st.Duplicate(src.ID, "Kobeni (copy)")
	if err != nil {
		t.Fatalf("duplicate: %v", err)
	}

	if dup.ID == src.ID {
		t.Fatal("the copy landed on the original's id — it is not a copy")
	}
	if !strings.HasPrefix(dup.ID, "kobeni-copy-") {
		t.Errorf("the copy should be filed under its own name: %q", dup.ID)
	}
	if dup.Card.Name != "Kobeni (copy)" {
		t.Errorf("copy name = %q", dup.Card.Name)
	}
	// Everything but the name comes along, `extensions` included — the field an
	// editor never renders and therefore the one most likely to be dropped.
	if dup.Card.Description != "a nervous devil hunter" || dup.Card.Personality != "anxious" {
		t.Errorf("the copy lost prose fields: %+v", dup.Card)
	}
	// Marshal pretty-prints, so match on the key rather than a compact pair.
	if !strings.Contains(string(dup.Raw), `"keep"`) || !strings.Contains(string(dup.Raw), `"me"`) {
		t.Errorf("the copy dropped the original's extensions: %s", dup.Raw)
	}

	// The original is untouched, and both are in the library.
	again, err := st.Get(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Card.Name != "Kobeni" {
		t.Errorf("duplicating renamed the ORIGINAL: %q", again.Card.Name)
	}
	cards, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 2 {
		t.Fatalf("library should hold both cards, has %d", len(cards))
	}
}

// A copy starts clean. The source's earlier revisions belong to the card they
// were taken from; inheriting them would let a restore on the copy silently
// reintroduce the original's name.
func TestCardDuplicateStartsWithNoHistory(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	src, err := st.ImportBytes([]byte(dupCardJSON))
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(dupCardJSON, "anxious", "steadier", 1)
	if _, err := st.Edit(src.ID, []byte(edited)); err != nil {
		t.Fatal(err)
	}
	if v, err := st.history().List(src.ID, nil); err != nil || len(v) == 0 {
		t.Fatalf("setup: the edit should have recorded a revision: %v, %d", err, len(v))
	}

	dup, err := st.Duplicate(src.ID, "Kobeni (copy)")
	if err != nil {
		t.Fatal(err)
	}
	versions, err := st.history().List(dup.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 0 {
		t.Errorf("a fresh copy should have no earlier versions, has %d", len(versions))
	}
}

// mustRefuse duplicates and fails the test unless the store said no, returning
// the error so the caller can check WHICH refusal fired. The two refusals cover
// the same unsafe write and differ only in what they tell the author, so a test
// that accepted any error could not tell them apart.
func mustRefuse(t *testing.T, st *CardStore, id, name string) error {
	t.Helper()
	sc, err := st.Duplicate(id, name)
	if err == nil {
		t.Fatalf("duplicating %q to %q must be refused; got card %q", id, name, sc.ID)
	}
	return err
}

// The refusals. Each one guards a write that would have reported success while
// creating nothing — the exact failure this verb was added to remove.
func TestCardDuplicateRefusesANameThatIsNotACopy(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	src, err := st.ImportBytes([]byte(dupCardJSON))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("the name it already has", func(t *testing.T) {
		// Unchanged name over unchanged contents yields the SAME id, so this
		// write would land back on the original and read as a duplicate.
		//
		// The MESSAGE is asserted, not just the refusal, and that is the whole
		// point of this case: the collision check below would reject this write
		// too, so without pinning the wording the dedicated check is untestable
		// and reads as dead code. It is not — it is the difference between
		// "pick a different name" and being told some other card is in the way
		// when the other card is the one you are copying.
		err := mustRefuse(t, st, src.ID, "Kobeni")
		if !strings.Contains(err.Error(), "already has") {
			t.Errorf("a same-name copy should say the name is taken BY THIS CARD, said: %v", err)
		}
		// Whitespace is not a rename: card.Marshal trims, so " Kobeni " would
		// reach the same id by a route the caller cannot see.
		if err := mustRefuse(t, st, src.ID, "  Kobeni  "); !strings.Contains(err.Error(), "already has") {
			t.Errorf("a whitespace-only rename should refuse the same way, said: %v", err)
		}
		cards, err := st.List()
		if err != nil {
			t.Fatal(err)
		}
		if len(cards) != 1 {
			t.Fatalf("a refused duplicate must write nothing, library has %d", len(cards))
		}
	})

	t.Run("no name at all", func(t *testing.T) {
		if _, err := st.Duplicate(src.ID, "   "); err == nil {
			t.Fatal("an empty name must be refused")
		}
	})

	t.Run("a card already holding these contents", func(t *testing.T) {
		first, err := st.Duplicate(src.ID, "Nia")
		if err != nil {
			t.Fatal(err)
		}
		// Same source, same target name ⇒ same contents ⇒ same id. Writing would
		// snapshot the existing Nia into history and overwrite her: a merge
		// wearing a copy's clothes. A DIFFERENT card is in the way here, so this
		// is the other message.
		err = mustRefuse(t, st, src.ID, "Nia")
		if !strings.Contains(err.Error(), "already in your library") {
			t.Errorf("a collision should name the card in the way, said: %v", err)
		}
		if v, err := st.history().List(first.ID, nil); err != nil || len(v) != 0 {
			t.Errorf("the refused write still displaced a revision: %v, %d", err, len(v))
		}
	})
}

func TestCardDuplicateWithoutAPortrait(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	src, err := st.ImportBytes([]byte(dupCardJSON)) // JSON import: no avatar
	if err != nil {
		t.Fatal(err)
	}
	dup, err := st.Duplicate(src.ID, "Kobeni (copy)")
	if err != nil {
		t.Fatalf("a card with no portrait must still duplicate: %v", err)
	}
	if dup.HasAvatar() {
		t.Error("the copy invented a portrait the original never had")
	}
}

func TestCardDuplicateUnknownCard(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	st := NewCardStore()

	if _, err := st.Duplicate("nobody-000000000000", "Copy"); err == nil {
		t.Fatal("duplicating a card that does not exist must fail")
	}
	// An id that could escape the library is rejected before anything is read.
	if _, err := st.Duplicate("../../etc", "Copy"); err == nil {
		t.Fatal("a traversal id must be rejected")
	}
}
