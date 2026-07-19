package build

import (
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func TestUserPersonaStore(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	s := NewUserPersonaStore()

	// An empty library lists nothing (and does not error on a missing dir).
	if list, err := s.List(); err != nil || len(list) != 0 {
		t.Fatalf("empty list: %v, %d", err, len(list))
	}

	// Save upserts by a name slug and returns the ref.
	kira, err := s.Save(UserPersona{Name: "Kira", Description: "A wary courier."})
	if err != nil {
		t.Fatal(err)
	}
	if kira.Ref != "kira" {
		t.Fatalf("ref = %q, want kira", kira.Ref)
	}
	if got, err := s.Get("kira"); err != nil || got.Name != "Kira" || got.Description != "A wary courier." {
		t.Fatalf("get: %+v, %v", got, err)
	}

	// A second persona; setting a default marks exactly one.
	if _, err := s.Save(UserPersona{Name: "Aria"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefault("aria"); err != nil {
		t.Fatal(err)
	}
	def, ok := s.Default()
	if !ok || def.Ref != "aria" {
		t.Fatalf("default = %+v ok=%v, want aria", def, ok)
	}
	if k, _ := s.Get("kira"); k.Default {
		t.Error("kira should not be default after aria was set")
	}

	// Switching the default clears the old one.
	if err := s.SetDefault("kira"); err != nil {
		t.Fatal(err)
	}
	if def, _ := s.Default(); def.Ref != "kira" {
		t.Errorf("default = %q, want kira", def.Ref)
	}
	if a, _ := s.Get("aria"); a.Default {
		t.Error("aria should no longer be default")
	}

	// List is name-sorted and complete.
	list, _ := s.List()
	if len(list) != 2 || list[0].Name != "Aria" || list[1].Name != "Kira" {
		t.Fatalf("list = %+v, want [Aria Kira]", list)
	}

	// Delete removes one; a missing delete is a no-op.
	if err := s.Delete("aria"); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.List(); len(list) != 1 {
		t.Errorf("after delete, list = %+v, want 1", list)
	}
	if err := s.Delete("nope"); err != nil {
		t.Errorf("deleting a missing persona should be a no-op, got %v", err)
	}

	// SetDefault("") clears the default.
	if err := s.SetDefault(""); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Default(); ok {
		t.Error("SetDefault(\"\") should clear the default")
	}
}
