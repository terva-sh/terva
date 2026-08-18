package provider

// Gemini charges thinking to the OUTPUT cap, and a current model cannot be told
// to stop thinking. So the cap a caller sends is not the answer budget it looks
// like: thoughts come out of it first, and a tight cap is spent entirely on
// them. These pin the correction.
//
// The live evidence behind the numbers is recorded on geminiThinkingReserve.
// The short version: at cap 200 on gemini-flash-latest a one-line ask spent
// 189-273 tokens thinking and came back severed or empty, and every attempt to
// switch thinking off was either rejected by the API or accepted and ignored.
//
// Model ids here are deliberately NOT in the catalog, so buildRequest takes its
// documented fallback (MaxOutput 8192, Reasoning on when the id names 2.5 or 3).
// That keeps the arithmetic below independent of catalog edits, which move.

import "testing"

func geminiWireFor(t *testing.T, model, reasoning string, reasoningSet bool, maxTokens int) *gemRequest {
	t.Helper()
	c := NewGemini("k", "https://example.invalid").(*geminiClient)
	wire, _, err := c.buildRequest(Request{
		Model:        model,
		MaxTokens:    maxTokens,
		Reasoning:    reasoning,
		ReasoningSet: reasoningSet,
		Messages:     []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if wire.GenerationConfig == nil {
		t.Fatal("no generationConfig on the wire")
	}
	return wire
}

func geminiCapOf(t *testing.T, wire *gemRequest) int {
	t.Helper()
	if wire.GenerationConfig.MaxOutputTokens == nil {
		t.Fatal("no maxOutputTokens on the wire")
	}
	return *wire.GenerationConfig.MaxOutputTokens
}

// The case the bug was reported as: reasoning explicitly OFF, a tight cap, and a
// model that thinks anyway. The cap has to clear the thinking the model is going
// to do regardless, or the answer is severed.
func TestGeminiReservesRoomForThinkingItCannotDisable(t *testing.T) {
	wire := geminiWireFor(t, "gemini-3-flash-uncatalogued", "", true, 200)

	want := geminiUnstoppableThoughtReserve + 200
	if got := geminiCapOf(t, wire); got != want {
		t.Fatalf("maxOutputTokens = %d, want %d (a %d-token reserve on top of the caller's 200)",
			got, want, geminiUnstoppableThoughtReserve)
	}
	// Off still sends no thinkingConfig, because there is no value that means
	// off: OFF and NONE are not in the enum, MINIMAL is rejected per-model, and
	// thinkingBudget 0 is accepted and ignored. The reserve is the whole fix.
	if wire.GenerationConfig.ThinkingConfig != nil {
		t.Fatalf("a thinkingConfig was sent for reasoning-off: %+v", wire.GenerationConfig.ThinkingConfig)
	}
}

// When a level IS asked for, the reserve is what that level is worth, so the
// answer survives a deeper think than the default.
func TestGeminiReservesTheAskedLevelsBudget(t *testing.T) {
	wire := geminiWireFor(t, "gemini-3-flash-uncatalogued", "low", true, 200)

	want := ReasoningBudget("low") + 200
	if got := geminiCapOf(t, wire); got != want {
		t.Fatalf("maxOutputTokens = %d, want %d (the low budget plus the caller's 200)", got, want)
	}
	if tc := wire.GenerationConfig.ThinkingConfig; tc == nil || tc.ThinkingLevel != "LOW" {
		t.Fatalf("thinkingConfig = %+v, want thinkingLevel LOW", tc)
	}
	// A deeper level must reserve more, or the reserve is a constant wearing a
	// function's clothes.
	deeper := geminiCapOf(t, geminiWireFor(t, "gemini-3-flash-uncatalogued", "high", true, 200))
	if deeper <= want {
		t.Fatalf("high reserved %d, low reserved %d — a deeper level must reserve more", deeper, want)
	}
}

// The 2.5 family takes a token budget rather than an enum, and it thinks by
// default too, so it gets the same correction.
func TestGeminiReservesOnTheBudgetFamilyToo(t *testing.T) {
	wire := geminiWireFor(t, "gemini-2.5-flash-uncatalogued", "low", true, 200)

	want := ReasoningBudget("low") + 200
	if got := geminiCapOf(t, wire); got != want {
		t.Fatalf("maxOutputTokens = %d, want %d", got, want)
	}
	tc := wire.GenerationConfig.ThinkingConfig
	if tc == nil || tc.ThinkingBudget == nil {
		t.Fatalf("thinkingConfig = %+v, want a thinkingBudget on the 2.5 path", tc)
	}
	if tc.ThinkingLevel != "" {
		t.Fatalf("the 2.5 path sent thinkingLevel %q; that knob belongs to gen 3", tc.ThinkingLevel)
	}
}

// A model that does not think spends nothing on thoughts, so its caller's cap
// means exactly what it says. Without this the reserve would quietly inflate
// every image and non-reasoning request in the tree.
func TestGeminiLeavesANonThinkingModelsCapAlone(t *testing.T) {
	wire := geminiWireFor(t, "gemini-1.0-plain-uncatalogued", "", true, 200)

	if got := geminiCapOf(t, wire); got != 200 {
		t.Fatalf("maxOutputTokens = %d, want the caller's 200 untouched", got)
	}
}

// The raise is a request to a real API, so it stays inside what the model
// accepts. The fallback model advertises 8192.
func TestGeminiClampsTheReserveToTheModelsOutputCap(t *testing.T) {
	const fallbackMaxOutput = 8192
	wire := geminiWireFor(t, "gemini-3-flash-uncatalogued", "", true, fallbackMaxOutput-10)

	if got := geminiCapOf(t, wire); got != fallbackMaxOutput {
		t.Fatalf("maxOutputTokens = %d, want it clamped to the model's %d", got, fallbackMaxOutput)
	}
}

// A caller asking for MORE than the model advertises keeps its own number. The
// raise may only ever raise: clamping such a request down to MaxOutput would be
// a behaviour change smuggled in under a bug fix, and this request worked before
// the reserve existed.
func TestGeminiNeverLowersACallersCap(t *testing.T) {
	const aboveTheModelMaximum = 10000 // the fallback model advertises 8192
	wire := geminiWireFor(t, "gemini-3-flash-uncatalogued", "", true, aboveTheModelMaximum)

	if got := geminiCapOf(t, wire); got != aboveTheModelMaximum {
		t.Fatalf("maxOutputTokens = %d, want the caller's %d left alone", got, aboveTheModelMaximum)
	}
}

// A caller that named no cap at all already gets the model's maximum, and there
// is nothing above it to raise into.
func TestGeminiLeavesAnUncappedRequestAtTheModelMaximum(t *testing.T) {
	const fallbackMaxOutput = 8192
	wire := geminiWireFor(t, "gemini-3-flash-uncatalogued", "", true, 0)

	if got := geminiCapOf(t, wire); got != fallbackMaxOutput {
		t.Fatalf("maxOutputTokens = %d, want the model maximum %d", got, fallbackMaxOutput)
	}
}
