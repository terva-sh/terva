package provider

import "testing"

// ResolveReasoning is the whole chain in one place:
//
//	--reasoning / session > models.json per-model > global config > CATALOG default > off
//
// The case that matters most is the middle pair, because they are the SAME
// FIELD on opposite sides of the global and every display surface in the tree
// used to collapse them. An operator's per-model value beats the global; a
// catalog default yields to it.
func TestResolveReasoningWalksTheWholeChain(t *testing.T) {
	operator := Model{DefaultReasoning: "low", DefaultReasoningSet: true}
	catalog := Model{DefaultReasoning: "high"} // e.g. the k3 rows
	plain := Model{}

	cases := []struct {
		name     string
		session  string
		model    Model
		global   string
		wantLvl  string
		wantFrom ReasoningSource
	}{
		{"a session override beats everything", "medium", operator, "high", "medium", ReasoningFromSession},
		{"an operator's per-model level beats the global", "", operator, "high", "low", ReasoningFromModelOperator},
		{"a catalog default yields to the global", "", catalog, "low", "low", ReasoningFromGlobal},
		{"a catalog default applies when nothing else is set", "", catalog, "", "high", ReasoningFromModelCatalog},
		{"nothing set anywhere runs the chain out", "", plain, "", "", ReasoningFromNothing},
		{"the global applies on a model with no default at all", "", plain, "medium", "medium", ReasoningFromGlobal},

		// "off" is a CHOICE, not an absence. It normalizes to "" but must still
		// win its rung — otherwise turning thinking off falls straight through
		// to whatever the layer below wants.
		{"an explicit session off beats a per-model level", "off", operator, "high", "", ReasoningFromSession},
		{"an explicit global off beats a catalog default", "", catalog, "none", "", ReasoningFromGlobal},

		// A set-signal with no value is not a choice; it must not swallow the
		// rungs below it.
		{"a set flag with an empty value falls through", "", Model{DefaultReasoningSet: true}, "medium", "medium", ReasoningFromGlobal},

		// The returned level is normalized, so a surface can print it.
		{"aliases normalize on the way out", "hi", plain, "", "high", ReasoningFromSession},
		{"a per-model alias normalizes too", "", Model{DefaultReasoning: "min", DefaultReasoningSet: true}, "", "minimum", ReasoningFromModelOperator},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lvl, from := ResolveReasoning(tc.session, tc.model, tc.global)
			if lvl != tc.wantLvl {
				t.Errorf("level = %q, want %q", lvl, tc.wantLvl)
			}
			if from != tc.wantFrom {
				t.Errorf("source = %d, want %d", from, tc.wantFrom)
			}
		})
	}
}

// The specific inversion every surface shipped: reading DefaultReasoning
// without DefaultReasoningSet.
//
// Pinned as its own case because it is invisible in the level alone — both
// models below answer "the model's value" when the global is empty, and only
// the presence of a global tells them apart. A surface that got this wrong
// reported an operator's deliberate per-model choice as "the global setting",
// naming a value that was not deciding anything.
func TestTheTwoModelSourcesSitOnOppositeSidesOfTheGlobal(t *testing.T) {
	const global = "medium"
	operator := Model{DefaultReasoning: "low", DefaultReasoningSet: true}
	catalog := Model{DefaultReasoning: "low"} // identical field, no set-signal

	if lvl, from := ResolveReasoning("", operator, global); lvl != "low" || from != ReasoningFromModelOperator {
		t.Errorf("operator per-model = (%q, %d); it must beat the global", lvl, from)
	}
	if lvl, from := ResolveReasoning("", catalog, global); lvl != global || from != ReasoningFromGlobal {
		t.Errorf("catalog default = (%q, %d); it must yield to the global — otherwise a global "+
			"level is unreachable on every row that ships one", lvl, from)
	}
}
