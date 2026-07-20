package build

import (
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// The saved-World store: Save mints a stable id once, updates mutate in place
// (id + Created preserved), Delete removes, List sorts, and the lore's
// audience/learned axes round-trip — the whole point of saving.
func TestWorldStoreRoundTrip(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	s := NewWorldStore()

	if _, err := s.Save(WorldDoc{}); err == nil {
		t.Error("a World needs a name")
	}
	doc, err := s.Save(WorldDoc{
		Name:         "Lowtown",
		Characters:   map[string]string{"Elira": "elira-1"},
		Lore:         []core.WorldLoreEntry{{Name: "secret", Constant: true, Content: "y", Audience: []string{"Elira"}, Learned: map[string]string{"Rook": "2026-07-19T00:00:00Z"}}},
		Coordination: "focus:Elira",
	})
	if err != nil {
		t.Fatal(err)
	}
	if doc.ID == "" || doc.Created.IsZero() {
		t.Fatalf("save should mint id + Created: %+v", doc)
	}

	got, err := s.Get(doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Lowtown" || got.Coordination != "focus:Elira" ||
		got.Lore[0].Learned["Rook"] == "" || got.Lore[0].Audience[0] != "Elira" {
		t.Fatalf("round-trip lost state: %+v", got)
	}

	// An update keeps the id and Created — a World mutates in place.
	got.Lore = append(got.Lore, core.WorldLoreEntry{Name: "new", Constant: true, Content: "z"})
	updated, err := s.Save(got)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != doc.ID || !updated.Created.Equal(doc.Created) {
		t.Errorf("update must preserve identity: %+v vs %+v", updated, doc)
	}
	if reread, _ := s.Get(doc.ID); len(reread.Lore) != 2 {
		t.Errorf("update should persist, got %d entries", len(reread.Lore))
	}

	if list, _ := s.List(); len(list) != 1 || list[0].ID != doc.ID {
		t.Errorf("list = %+v", list)
	}
	if err := s.Delete(doc.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(doc.ID); err == nil {
		t.Error("double delete should report missing")
	}
	if list, _ := s.List(); len(list) != 0 {
		t.Errorf("list after delete = %+v", list)
	}
}

// Cover images (W5b): stored beside world.json, path-validated like Get so
// the result is always safe to serve, absent reads as "".
func TestWorldStoreCover(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	s := NewWorldStore()
	doc, err := s.Save(WorldDoc{Name: "Lowtown"})
	if err != nil {
		t.Fatal(err)
	}
	if p := s.CoverPath(doc.ID); p != "" {
		t.Errorf("no cover yet, got %q", p)
	}
	if err := s.SetCover("no-such-world", []byte("png")); err == nil {
		t.Error("SetCover on a missing World should fail")
	}
	if err := s.SetCover(doc.ID, []byte("png-bytes")); err != nil {
		t.Fatal(err)
	}
	if p := s.CoverPath(doc.ID); p == "" {
		t.Error("cover should resolve after SetCover")
	}
	if p := s.CoverPath("../" + doc.ID); p != "" {
		t.Errorf("an escaping id must not resolve, got %q", p)
	}
	if err := s.RemoveCover(doc.ID); err != nil {
		t.Fatal(err)
	}
	if p := s.CoverPath(doc.ID); p != "" {
		t.Errorf("cover should be gone, got %q", p)
	}
	if err := s.RemoveCover(doc.ID); err != nil {
		t.Errorf("removing an absent cover is a no-op, got %v", err)
	}
}
