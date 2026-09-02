package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// always_on_skills is a *[]string, and the pointer is the whole mechanism.
// nil means the operator has no opinion and terva pins its default. A non-nil
// pointer to an empty slice means pin nothing, which is the opt-out.
//
// A refactor to a plain []string compiles, passes every other test, and
// silently switches the shipped default back on for everybody who turned it
// off. So the distinction gets a test of its own.
func TestAlwaysOnSkillsUnsetDiffersFromEmpty(t *testing.T) {
	var unset Config
	if err := json.Unmarshal([]byte(`{}`), &unset); err != nil {
		t.Fatal(err)
	}
	if unset.AlwaysOnSkills != nil {
		t.Errorf("a config with no always_on_skills key should decode to nil, got %v", *unset.AlwaysOnSkills)
	}

	var empty Config
	if err := json.Unmarshal([]byte(`{"always_on_skills":[]}`), &empty); err != nil {
		t.Fatal(err)
	}
	if empty.AlwaysOnSkills == nil {
		t.Fatal("an explicit empty list decoded to nil, so the opt-out is indistinguishable from unset")
	}
	if len(*empty.AlwaysOnSkills) != 0 {
		t.Errorf("expected an empty list, got %v", *empty.AlwaysOnSkills)
	}
}

// The opt-out has to survive being written back. `omitempty` on a POINTER
// tests nil, so an empty list still serializes. On a plain slice it tests
// length, which would drop the key and lose the operator's choice on the next
// save. This test fails on that refactor.
func TestAlwaysOnSkillsEmptyListSurvivesARoundTrip(t *testing.T) {
	out, err := json.Marshal(Config{AlwaysOnSkills: &[]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"always_on_skills":[]`) {
		t.Fatalf("the empty list was dropped on write, so the opt-out does not persist: %s", out)
	}

	var back Config
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if back.AlwaysOnSkills == nil || len(*back.AlwaysOnSkills) != 0 {
		t.Errorf("round trip lost the opt-out: %v", back.AlwaysOnSkills)
	}
}

// An unset field must stay out of a written config, so terva does not stamp its
// current default into every operator's file and freeze it there.
func TestAlwaysOnSkillsUnsetIsNotWritten(t *testing.T) {
	out, err := json.Marshal(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "always_on_skills") {
		t.Errorf("an unset always_on_skills was written out: %s", out)
	}
}
