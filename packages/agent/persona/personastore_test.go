package persona

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// TestMarshalPersonaRoundTrips — Parse(Marshal(p)) == p, so the
// library can write a persona and read it back unchanged.
func TestMarshalPersonaRoundTrips(t *testing.T) {
	p := Persona{
		Name:              "Aria",
		Pronunciation:     "AR-ee-ah",
		Specialty:         "storytelling",
		Summary:           "a wandering bard",
		Emoji:             "🎵",
		AccentColor:       "#aa33cc",
		RecommendedSkills: []string{"lore"},
		GoodFor:           []string{"roleplay"},
		AvoidFor:          []string{"code"},
		Immersive:         true,
		Introduction:      "You meet Aria at the crossroads.",
		Charter:           "You are Aria, a wandering bard who speaks in verse.",
	}
	raw, err := Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(string(raw), "test")
	if err != nil {
		t.Fatalf("marshal output does not re-parse: %v\n%s", err, raw)
	}
	// Source is set from the parse arg, not the marshaled bytes.
	got.Source = ""
	want := p
	want.Source = ""
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestMarshalPersonaRequiresName(t *testing.T) {
	if _, err := Marshal(Persona{Charter: "x"}); err == nil {
		t.Error("marshaling a nameless persona should error")
	}
}

func TestWritePersonaAndLookup(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))

	if _, exists := UserPath("Custom One"); exists {
		t.Fatal("no user file should exist before a write")
	}

	dest, err := Write(Persona{Name: "Custom One", Summary: "mine", Charter: "You are Custom."})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(dest, "custom-one.md") {
		t.Errorf("unexpected persona path: %q", dest)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("persona file not written: %v", err)
	}

	got, ok := Lookup("Custom One")
	if !ok {
		t.Fatal("written persona not found by Lookup")
	}
	if got.Summary != "mine" || got.Builtin() {
		t.Errorf("looked-up persona wrong: %+v", got)
	}
	if _, exists := UserPath("Custom One"); !exists {
		t.Error("UserPath should now report the file exists")
	}
}
