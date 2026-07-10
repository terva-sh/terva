package build

import (
	"testing"

	"terva.sh/terva/packages/agent/config"
)

// TestRaatiProfilesOverlay: the shipped built-ins are present by
// default, a user profile with a built-in's name replaces it wholesale,
// and raati.builtin_profiles=false hides the shipped set.
func TestRaatiProfilesOverlay(t *testing.T) {
	profs := RaatiProfiles(config.Config{})
	for _, name := range []string{"triage", "counsel", "code-review", "ethics"} {
		if _, ok := profs[name]; !ok {
			t.Errorf("built-in %q missing from default overlay", name)
		}
	}

	var uc config.Config
	uc.Raati.Profiles = map[string]config.RaatiProfileConfig{
		"triage": {Description: "my own triage", Level: &config.RaatiProfileLevel{Auto: true, Ceiling: 1}},
		"local":  {Description: "user-only"},
	}
	profs = RaatiProfiles(uc)
	if got := profs["triage"]; got.Description != "my own triage" || !got.AutoLevel || got.AutoCeiling != 1 || got.SingleRound != nil {
		t.Errorf("user override did not replace the built-in wholesale: %+v", got)
	}
	if _, ok := profs["local"]; !ok {
		t.Error("user-only profile missing")
	}
	if _, ok := profs["counsel"]; !ok {
		t.Error("untouched built-in missing alongside user profiles")
	}

	off := false
	uc.Raati.BuiltinProfiles = &off
	profs = RaatiProfiles(uc)
	if _, ok := profs["counsel"]; ok {
		t.Error("builtin_profiles=false still ships counsel")
	}
	if _, ok := profs["local"]; !ok {
		t.Error("builtin_profiles=false dropped the user's own profiles")
	}
}
