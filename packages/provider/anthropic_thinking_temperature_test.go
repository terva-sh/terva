package provider

import "testing"

// 🪤 Anthropic refuses a temperature beside enabled thinking: "`temperature` may
// only be set to 1 when thinking is enabled" (http 400). Nothing asserted this
// before, and `terva ticket groom` shipped unable to complete a single call
// because of it. The tier pick handed it a reasoning effort, and it also asked
// for temperature 0 so that two runs over one pool stay comparable.
//
// The guard that already existed covers adaptive-thinking models, which reject
// every sampling param outright. A non-adaptive model with an explicit budget is
// the shape that got through, and no test described it.
func TestBuildRequest_ThinkingDropsTemperature(t *testing.T) {
	withCatalogState(t)
	ResetCatalogLayers() // resolve the model from the baked-in catalog

	// claude-haiku-4-5 is the model that produced the 400. It is the built-in
	// weak rung for anthropic, at thinking high, which is exactly what the tier
	// pick handed groom.
	c := NewAnthropic("x", "").(*anthropicClient)
	var zero float32

	on, err := c.buildRequest(Request{
		Model:        "claude-haiku-4-5",
		Reasoning:    "high",
		ReasoningSet: true,
		Temperature:  &zero,
		Messages:     []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The control. A builder that stopped emitting thinking at all would pass
	// the temperature assertion below while proving nothing, so establish that
	// this request really does carry the conflict under test.
	if on.Thinking == nil {
		t.Fatal("claude-haiku-4-5 at high effort must emit a thinking block, or this test cannot see the conflict it exists to catch")
	}
	if on.Temperature != nil {
		t.Errorf("thinking is enabled, so temperature must not ship: got %v, which earns http 400", *on.Temperature)
	}

	// The converse. With reasoning explicitly off there is no thinking block,
	// the temperature is legal, and it must survive. This half is what makes
	// the assertion above a test of the rule, rather than a test that passes
	// because the builder drops temperature unconditionally.
	off, err := c.buildRequest(Request{
		Model:        "claude-haiku-4-5",
		Reasoning:    "",
		ReasoningSet: true,
		Temperature:  &zero,
		Messages:     []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if off.Thinking != nil {
		t.Fatalf("explicit off must suppress thinking, got %+v", off.Thinking)
	}
	if off.Temperature == nil {
		t.Error("no thinking block, so the caller's temperature must reach the wire")
	} else if *off.Temperature != 0 {
		t.Errorf("temperature = %v, want the 0 the caller asked for", *off.Temperature)
	}
}
