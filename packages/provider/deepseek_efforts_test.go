package provider

import "testing"

func TestDirectDeepSeekReasoningEfforts(t *testing.T) {
	for _, id := range []string{"deepseek-v4-pro", "deepseek-v4-flash"} {
		m, err := Builtin().FindModel("deepseek", id)
		if err != nil {
			t.Fatal(err)
		}
		client := newOpenAICompat("deepseek", "synthetic", "https://example.test", "").(*openaiClient)
		for _, tc := range []struct{ level, want string }{
			{"", "none"}, {"low", "low"}, {"high", "high"}, {"maximum", "high"}, {"max", "max"},
		} {
			body, err := client.buildRequest(Request{Model: id, Reasoning: tc.level, ReasoningSet: true})
			if err != nil {
				t.Fatal(err)
			}
			if body.ReasoningEffort != tc.want {
				t.Errorf("%s %q: wire=%q want=%q", id, tc.level, body.ReasoningEffort, tc.want)
			}
		}
		sawMax := false
		for _, rung := range ReasoningLadderFor(m) {
			body, err := client.buildRequest(Request{Model: id, Reasoning: LadderWireValue(rung.Level), ReasoningSet: true})
			if err != nil {
				t.Fatal(err)
			}
			if body.ReasoningEffort != rung.Effect.Effort {
				t.Fatalf("ladder and wire disagree for %s: %q vs %q", id, body.ReasoningEffort, rung.Effect.Effort)
			}
			sawMax = sawMax || rung.Effect.Effort == "max"
		}
		if !sawMax {
			t.Fatal("picker has no max rung")
		}
	}
}

func TestCompatibleDeepSeekKeepsOwnEffortLimits(t *testing.T) {
	client := newOpenAICompat("openai-compatible", "synthetic", "https://example.test", "").(*openaiClient)
	body, err := client.buildRequest(Request{Model: "deepseek-v4-pro", Reasoning: "max", ReasoningSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if body.ReasoningEffort != "high" {
		t.Fatalf("gateway borrowed direct effort enum: %q", body.ReasoningEffort)
	}
	m := Model{Provider: "openai-compatible", ID: "deepseek-v4-pro", Reasoning: true}
	if got := openAICompatEffort(m, "max"); got != "high" {
		t.Fatalf("undeclared gateway max=%q", got)
	}
	m.ReasoningEfforts = []string{"none", "low", "high"}
	if got := openAICompatEffort(m, "max"); got != "high" {
		t.Fatalf("declared gateway max=%q", got)
	}
	cat := NewRegistry()
	cat.RegisterExtraModel(m)
	WithCatalog(client, cat)
	for _, rung := range ReasoningLadderFor(m) {
		body, err := client.buildRequest(Request{Model: m.ID, Reasoning: LadderWireValue(rung.Level), ReasoningSet: true})
		if err != nil {
			t.Fatal(err)
		}
		if body.ReasoningEffort != rung.Effect.Effort {
			t.Fatalf("gateway ladder %s: wire=%q, effect=%q", rung.Level, body.ReasoningEffort, rung.Effect.Effort)
		}
	}
}
