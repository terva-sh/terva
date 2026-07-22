package build

import (
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// The group store: Save mints a stable id once, updates mutate in place (id +
// Created preserved), members are deduplicated, Delete removes, List sorts.
func TestGroupStoreRoundTrip(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	s := NewCardGroupStore()

	if _, err := s.Save(Group{}); err == nil {
		t.Error("a group needs a name")
	}

	doc, err := s.Save(Group{Name: "WIP", Color: "#c80", Members: []string{"a", "b", "a", "", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if doc.ID == "" || doc.Created.IsZero() {
		t.Fatalf("save should mint id + Created: %+v", doc)
	}
	if len(doc.Members) != 2 || doc.Members[0] != "a" || doc.Members[1] != "b" {
		t.Errorf("members not deduped in order: %v", doc.Members)
	}

	got, err := s.Get(doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "WIP" || got.Color != "#c80" {
		t.Fatalf("round-trip lost state: %+v", got)
	}

	// An update keeps the id and Created — a group mutates in place.
	got.Name = "Working"
	got.Members = []string{"a", "c"}
	updated, err := s.Save(got)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != doc.ID {
		t.Errorf("update minted a new id: %q vs %q", updated.ID, doc.ID)
	}
	if !updated.Created.Equal(doc.Created) {
		t.Errorf("update lost Created: %v vs %v", updated.Created, doc.Created)
	}
	if updated.Updated.Before(doc.Updated) {
		t.Error("update should stamp a later Updated")
	}

	// A second group, listed sorted by name.
	if _, err := s.Save(Group{Name: "Aardvark"}); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "Aardvark" || list[1].Name != "Working" {
		t.Fatalf("list not sorted by name: %+v", list)
	}

	if err := s.Delete(doc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(doc.ID); err == nil {
		t.Error("deleted group should be gone")
	}
	if err := s.Delete(doc.ID); err == nil {
		t.Error("deleting a missing group should error")
	}
}

// The two namespaces are independent directories: a card group and a session
// group with the same name never collide.
func TestGroupStoreNamespacesAreIndependent(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	cards := NewCardGroupStore()
	sessions := NewSessionGroupStore()

	if _, err := cards.Save(Group{Name: "Ready", Members: []string{"card-1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Save(Group{Name: "Ready", Members: []string{"sess-1"}}); err != nil {
		t.Fatal(err)
	}
	cl, err := cards.List()
	if err != nil || len(cl) != 1 || cl[0].Members[0] != "card-1" {
		t.Fatalf("card groups leaked: %+v (%v)", cl, err)
	}
	sl, err := sessions.List()
	if err != nil || len(sl) != 1 || sl[0].Members[0] != "sess-1" {
		t.Fatalf("session groups leaked: %+v (%v)", sl, err)
	}
}
