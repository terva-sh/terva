package build

import (
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// The card-model store: a card with no pref reads as unset (not an error), Set
// round-trips, an empty Set clears the file (back to the workspace default), and
// a directory-escaping id is refused.
func TestCardModelStoreRoundTrip(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	s := NewCardModelStore()

	// Unset is the common case: no file, no error, ok=false.
	if _, ok, err := s.Get("alice-abc123"); err != nil || ok {
		t.Fatalf("unset card should read false/nil, got ok=%v err=%v", ok, err)
	}

	if err := s.Set("alice-abc123", "openai", "gpt-5.6"); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get("alice-abc123")
	if err != nil || !ok {
		t.Fatalf("set card should read back: ok=%v err=%v", ok, err)
	}
	if got.Provider != "openai" || got.Model != "gpt-5.6" {
		t.Errorf("round trip lost the pref: %+v", got)
	}

	// A second card is independent.
	if _, ok, _ := s.Get("bob-def456"); ok {
		t.Error("bob should have no pref")
	}

	// Clearing (both fields empty) deletes the file → back to unset.
	if err := s.Set("alice-abc123", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get("alice-abc123"); ok {
		t.Error("cleared card should read as unset")
	}
	// Clearing a card that never had a pref is not an error.
	if err := s.Set("never-had-one", "", ""); err != nil {
		t.Errorf("clearing an unset card should be a no-op: %v", err)
	}

	// A model with no provider is allowed (the resolver disambiguates later).
	if err := s.Set("carol-777aaa", "", "glm-5.2"); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := s.Get("carol-777aaa"); !ok || got.Model != "glm-5.2" || got.Provider != "" {
		t.Errorf("provider-less pref not stored: ok=%v %+v", ok, got)
	}

	// A directory-escaping id is refused on every path.
	if _, _, err := s.Get("../escape"); err == nil {
		t.Error("Get should reject a traversal id")
	}
	if err := s.Set("../escape", "openai", "gpt-5.6"); err == nil {
		t.Error("Set should reject a traversal id")
	}
}
