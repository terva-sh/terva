package modes

import (
	"testing"

	"terva.sh/terva/packages/provider"
)

// The /model picker's ":token" query words filter by capability while
// the remaining words keep fuzzy-matching ids. Unrecognized tokens
// (including ones still being typed) have no effect.
func TestModelDialogCapabilityFilter(t *testing.T) {
	d := newModelDialog()
	d.all = sortedModels([]provider.Model{
		{Provider: "p", ID: "seer", Reasoning: false},
		{Provider: "p", ID: "blind", Reasoning: true,
			Caps: map[provider.Capability]bool{provider.CapImageInput: false}},
		{Provider: "p", ID: "painter",
			Caps: map[provider.Capability]bool{provider.CapImageOutput: true}},
	})
	d.active = true

	ids := func() []string {
		var out []string
		for _, m := range d.view {
			out = append(out, m.ID)
		}
		return out
	}
	set := func(q string) {
		d.query = q
		d.refilter()
	}

	set(":img")
	if got := ids(); len(got) != 2 || got[0] != "painter" || got[1] != "seer" {
		t.Errorf(":img view = %v, want [painter seer] (blind excluded)", got)
	}

	set(":reasoning")
	if got := ids(); len(got) != 1 || got[0] != "blind" {
		t.Errorf(":reasoning view = %v, want [blind]", got)
	}

	set(":imggen")
	if got := ids(); len(got) != 1 || got[0] != "painter" {
		t.Errorf(":imggen view = %v, want [painter]", got)
	}

	// Capability token + text needle compose.
	set(":img seer")
	if got := ids(); len(got) != 1 || got[0] != "seer" {
		t.Errorf(":img seer view = %v, want [seer]", got)
	}

	// A token mid-typing (unrecognized) must not hide anything.
	set(":i")
	if got := ids(); len(got) != 3 {
		t.Errorf(":i view = %v, want all 3 (unrecognized token ignored)", got)
	}
}
