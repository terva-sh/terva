package provider

import "testing"

// Model.ReasoningEfforts lets a model say which reasoning_effort values it
// actually accepts. The contract has two halves, and both are load-bearing:
// an undeclared model must behave exactly as it did before the field existed,
// and a declared model must never be sent a value it has disowned.

// neotEfforts is the shape that motivated the field: a local qwen server that
// takes xhigh/medium/low, rejects high/minimal/max with an HTTP 400, and
// accepts "none" despite omitting it from its own error message.
var neotEfforts = []string{"none", "low", "medium", "xhigh"}

func compatModel(efforts []string) Model {
	return Model{
		Provider: "openai-compatible", ID: "qwen3.8-27b-abl",
		Reasoning: true, ReasoningEfforts: efforts,
	}
}

// Half one: silence means unlimited. Nobody who has not described their model
// may lose a rung to this feature.
func TestUndeclaredEffortsBehaveExactlyAsBefore(t *testing.T) {
	m := compatModel(nil)
	for _, lv := range []string{"", "minimum", "low", "medium", "high", "maximum", "max"} {
		want := OpenAIReasoningEffort(lv)
		if got := openAICompatEffort(m, lv); got != want {
			t.Errorf("level %q: got %q, want the unchanged mapping %q", lv, got, want)
		}
	}
}

// Half two, and the point of the whole exercise.
func TestDeclaredEffortsBendOntoTheDeclaredSet(t *testing.T) {
	m := compatModel(neotEfforts)
	cases := []struct{ level, want, why string }{
		{"", "none", "off must SEND none: omitting it on a server whose default is xhigh buys maximum thinking"},
		{"minimum", "low", "minimal is rejected here; the nearest declared rung upward is low"},
		{"low", "low", "declared exactly"},
		{"medium", "medium", "declared exactly"},
		{"high", "medium", "high is rejected here; the nearest declared rung is medium"},
		{"maximum", "xhigh", "maximum means the top; xhigh is declared and must be reached"},
		{"max", "xhigh", "max clamps to the highest rung the model admits to"},
	}
	for _, c := range cases {
		if got := openAICompatEffort(m, c.level); got != c.want {
			t.Errorf("level %q: got %q, want %q — %s", c.level, got, c.want, c.why)
		}
	}
}

// The regression that the conservative mappers would have caused if they ran
// before the clamp: "maximum" pre-clamped to "high", and "high" is not in the
// set, so it would land on medium while the declared xhigh went unused.
func TestDeclaredMaximumIsNotPreClampedToHigh(t *testing.T) {
	if got := openAICompatEffort(compatModel(neotEfforts), "maximum"); got == "medium" {
		t.Fatal("maximum landed on medium: the blind high-clamp ran before the declared clamp, " +
			"and the xhigh this model declares was never reached")
	}
}

// Off must not be able to turn thinking ON, and a thinking level must not be
// able to turn it OFF. Both directions, because the clamp walks both ways.
func TestClampNeverCrossesTheOffBoundary(t *testing.T) {
	// No "none" declared: off stays omitted rather than climbing to a
	// thinking rung. Such a model reasons whatever terva does, and saying so
	// by omission is honest.
	if got := openAICompatEffort(compatModel([]string{"low", "high"}), ""); got != "" {
		t.Errorf("off on a model without none = %q, want \"\" (omit), never a thinking rung", got)
	}
	// "none" is nearest to "low" here in pure arithmetic. Answering it would
	// silently disable thinking for someone who asked to think.
	if got := openAICompatEffort(compatModel([]string{"none", "xhigh"}), "low"); got != "xhigh" {
		t.Errorf("low on {none,xhigh} = %q, want xhigh; never none", got)
	}
}

// "none" is off said out loud. If Off() did not know that, the ladder would
// offer the off rung as a live thinking level.
func TestNoneCountsAsOff(t *testing.T) {
	e := ReasoningEffectFor(compatModel(neotEfforts), "")
	if e.Effort != "none" {
		t.Fatalf("Effort = %q, want none", e.Effort)
	}
	if !e.Off() {
		t.Error("Off() = false for effort \"none\": the off rung would render as a thinking level")
	}
}

// The ladder and the wire read the same function, so a declared model cannot
// be shown a rung the request would not send.
func TestLadderReflectsDeclaredEfforts(t *testing.T) {
	m := compatModel(neotEfforts)
	for _, r := range ReasoningLadderFor(m) {
		if got := openAICompatEffort(m, LadderWireValue(r.Level)); got != r.Effect.Effort {
			t.Errorf("rung %q: ladder shows %q, wire sends %q", r.Level, r.Effect.Effort, got)
		}
	}
}

// 🪤 applyUserOverrides is field-by-field, so a new Model field is silently
// discarded for any model that already exists in the catalog or live layer —
// which is nearly all of them. The operator would read their own declaration
// back from models.json and believe it while the wire ignored it.
func TestDeclaredEffortsSurviveTheUserOverrideMerge(t *testing.T) {
	base := []Model{{Provider: "openai-compatible", ID: "qwen3.8-27b-abl", Reasoning: true, Source: "catalog"}}
	over := []UserOverride{{Model: Model{
		Provider: "openai-compatible", ID: "qwen3.8-27b-abl", ReasoningEfforts: neotEfforts,
	}}}

	out := applyUserOverrides(base, over)
	if len(out) != 1 {
		t.Fatalf("got %d models, want 1", len(out))
	}
	if len(out[0].ReasoningEfforts) == 0 {
		t.Fatal("reasoningEfforts was dropped by the user-override merge: " +
			"the operator's declaration would do nothing on any catalog model")
	}
	if got := openAICompatEffort(out[0], ""); got != "none" {
		t.Errorf("off after merge = %q, want none", got)
	}
}
