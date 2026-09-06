package provider

import "testing"

// The capability tri-states moved into the ModelParam registry, and their
// merge moved with them: it now reads the capability map instead of
// UserOverride.ReasoningSet. These pin the three answers a tri-state has to
// keep apart, because two of them are the same bool and only the third state
// says which.

func paramByKey(t *testing.T, key string) ModelParam {
	t.Helper()
	for _, p := range ModelParams() {
		if p.Key == key {
			return p
		}
	}
	t.Fatalf("no ModelParam declares %q", key)
	return ModelParam{}
}

// The bug this whole change came from. A local endpoint's model is discovered
// through /v1/models, which says nothing about thinking, so it arrives with
// Reasoning false. The operator says otherwise in models.json, and that has to
// reach the merged row: ReasoningLadderFor returns nil while it does not, so
// the picker offers seven rungs that all read "this model takes no thinking
// setting" and buildRequest sends no reasoning_effort at all.
func TestReasoningOverrideReachesTheMergedRow(t *testing.T) {
	base := []Model{{Provider: "neot", ID: "qwen3.8-27b-abl", Source: "live"}}
	if ReasoningLadderFor(base[0]) != nil {
		t.Fatal("precondition: the discovered row should carry no ladder")
	}

	out := applyUserOverrides(base, []UserOverride{{
		Model:        Model{Provider: "neot", ID: "qwen3.8-27b-abl", Reasoning: true},
		ReasoningSet: true,
	}})

	if !out[0].Reasoning {
		t.Error("Model.Reasoning is false after the merge: buildRequest gates the " +
			"whole reasoning_effort block on it, so the request would carry nothing")
	}
	if !out[0].Has(CapReasoning) {
		t.Error("Has(CapReasoning) is false after the merge: every capability " +
			"reader would still call this model incapable of thinking")
	}
	if ReasoningLadderFor(out[0]) == nil {
		t.Error("the merged row still has no ladder, so both pickers would say " +
			"this model takes no thinking setting")
	}
}

// "off" is the operator overruling what terva believes, not the absence of an
// opinion. It has to beat a catalog row that says the model reasons.
func TestReasoningOverrideCanTurnACapableModelOff(t *testing.T) {
	base := []Model{{Provider: "openai-compatible", ID: "m", Reasoning: true}}
	off := false
	out := applyUserOverrides(base, []UserOverride{{
		Model:        Model{Provider: "openai-compatible", ID: "m", Reasoning: off},
		ReasoningSet: true,
	}})
	if out[0].Reasoning || out[0].Has(CapReasoning) {
		t.Error("an explicit off did not survive the merge")
	}
}

// 🪤 The third state. An entry that does not mention reasoning must leave the
// underlying layer's answer alone, and the value it carries in that case is
// the zero value — indistinguishable from a deliberate off without the set
// signal. Merging it would switch thinking off for every model an operator has
// ever pinned a context window on.
func TestAnEntryThatSaysNothingAboutReasoningLeavesItAlone(t *testing.T) {
	base := []Model{{Provider: "openai-compatible", ID: "m", Reasoning: true}}
	out := applyUserOverrides(base, []UserOverride{{
		Model: Model{Provider: "openai-compatible", ID: "m", ContextWindow: 4096},
	}})
	if !out[0].Reasoning {
		t.Error("an unrelated override turned thinking off")
	}
	if out[0].ContextWindow != 4096 {
		t.Errorf("ContextWindow = %d, want the override applied", out[0].ContextWindow)
	}
}

// image-input travels as a capability key rather than a top-level field, so it
// exercises the other spelling of the same tri-state.
func TestImageInputOverrideMergesThroughItsCapabilityKey(t *testing.T) {
	base := []Model{{Provider: "openai-compatible", ID: "m"}}
	out := applyUserOverrides(base, []UserOverride{{
		Model: Model{
			Provider: "openai-compatible", ID: "m",
			Caps: map[Capability]bool{CapImageInput: true},
		},
	}})
	if !out[0].Has(CapImageInput) {
		t.Error("the image-input capability did not survive the merge")
	}
}

// The editor reads a value back through the same registry entry that wrote it,
// so a round trip that loses the value shows up as a form that forgets what
// the operator just saved.
func TestTriStateRoundTripsThroughTheRegistry(t *testing.T) {
	reasoning := paramByKey(t, "reasoning")
	image := paramByKey(t, "imageInput")

	for _, tc := range []struct{ set, want string }{
		{"on", "on"}, {"off", "off"}, {"", ""}, {"inherit", ""},
	} {
		var um UserModel
		if err := reasoning.SetOverride(&um, tc.set); err != nil {
			t.Fatalf("reasoning SetOverride(%q): %v", tc.set, err)
		}
		if got := reasoning.Override(um); got != tc.want {
			t.Errorf("reasoning %q round-tripped to %q, want %q", tc.set, got, tc.want)
		}

		um = UserModel{}
		if err := image.SetOverride(&um, tc.set); err != nil {
			t.Fatalf("imageInput SetOverride(%q): %v", tc.set, err)
		}
		if got := image.Override(um); got != tc.want {
			t.Errorf("imageInput %q round-tripped to %q, want %q", tc.set, got, tc.want)
		}
	}

	var um UserModel
	if err := reasoning.SetOverride(&um, "yes please"); err == nil {
		t.Error("a value that is neither on nor off was accepted")
	}
}

// Clearing an image-input override must remove the key rather than write
// false, or "inherit" would persist as a claim that the model has no vision.
func TestClearingACapabilityRemovesTheKey(t *testing.T) {
	image := paramByKey(t, "imageInput")
	um := UserModel{Capabilities: map[string]bool{"image-input": true}}
	if err := image.SetOverride(&um, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := um.Capabilities["image-input"]; ok {
		t.Error("the key survived a clear, so inherit was written as an assertion")
	}
}

// The efforts row is a list, and a list typed into a box arrives with commas,
// spaces, capitals and repeats.
func TestAcceptedEffortsParseAndRoundTrip(t *testing.T) {
	efforts := paramByKey(t, "reasoningEfforts")
	var um UserModel
	if err := efforts.SetOverride(&um, "None, LOW  medium,,xhigh, low"); err != nil {
		t.Fatal(err)
	}
	want := []string{"none", "low", "medium", "xhigh"}
	if len(um.ReasoningEfforts) != len(want) {
		t.Fatalf("parsed %v, want %v", um.ReasoningEfforts, want)
	}
	for i, v := range want {
		if um.ReasoningEfforts[i] != v {
			t.Errorf("efforts[%d] = %q, want %q", i, um.ReasoningEfforts[i], v)
		}
	}
	if got := efforts.Override(um); got != "none, low, medium, xhigh" {
		t.Errorf("Override = %q", got)
	}
}

// The row is read by openAICompatEffort and nothing else, so it must not
// appear on a wire that cannot act on it. An empty Options is the contract
// both frontends already use to omit a row.
func TestAcceptedEffortsRowOnlyAppearsOnTheCompatWire(t *testing.T) {
	efforts := paramByKey(t, "reasoningEfforts")
	if got := efforts.Options(Model{Provider: "openai-compatible"}); len(got) == 0 {
		t.Error("no options on the compat wire, so the row would never render")
	}
	for _, p := range []string{"anthropic", "google", "openai-codex", "amazon-bedrock"} {
		if got := efforts.Options(Model{Provider: p}); len(got) != 0 {
			t.Errorf("%s offers %v, but only the compat wire reads declared efforts", p, got)
		}
	}
}

// FreeValues is what keeps clampEffortToDeclared's pass-through reachable: it
// leaves an effort it does not recognize alone, on the reasoning that an
// unknown value is the server's own word. A closed picker would make that
// reachable only by hand-editing the file.
func TestAcceptedEffortsAcceptsAValueOffTheScale(t *testing.T) {
	efforts := paramByKey(t, "reasoningEfforts")
	if !efforts.FreeValues {
		t.Fatal("the efforts row is a closed set, so a server's own effort name is unreachable")
	}
	var um UserModel
	if err := efforts.SetOverride(&um, "none, ludicrous"); err != nil {
		t.Fatal(err)
	}
	if len(um.ReasoningEfforts) != 2 || um.ReasoningEfforts[1] != "ludicrous" {
		t.Errorf("parsed %v, want the server's own word kept", um.ReasoningEfforts)
	}
}
