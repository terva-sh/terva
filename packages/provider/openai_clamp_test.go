package provider

import "testing"

// withLiveModels installs a synthetic catalog overlay for the duration
// of a test and restores the previous state afterwards. It lets these
// tests pin exact ContextWindow / MaxOutput values without depending on
// the real catalog.
func withLiveModels(t *testing.T, models []Model) {
	t.Helper()
	withCatalogState(t)
	SetLiveModels(models)
}

// outputBudget pulls whichever max-output field buildRequest populated
// for a non-reasoning model (out.MaxTokens) so tests can assert the
// clamped value regardless of the reasoning path.
func outputBudget(t *testing.T, out *oaiRequest) int {
	t.Helper()
	switch {
	case out.MaxTokens != nil:
		return *out.MaxTokens
	case out.MaxCompletionTok != nil:
		return *out.MaxCompletionTok
	default:
		t.Fatalf("no output budget set on request")
		return 0
	}
}

// TestBuildRequestDoesNotClampWhenOutputFitsWindow is the regression
// guard for the original PR #24 behavior: a normal model whose MaxOutput
// comfortably fits its context window must NOT have its budget reduced.
// The earlier implementation subtracted 4096 from min(window, MaxOutput),
// which silently shrank every OpenAI/DeepSeek model by 4096 tokens.
func TestBuildRequestDoesNotClampWhenOutputFitsWindow(t *testing.T) {
	withLiveModels(t, []Model{{
		Provider:      "openai",
		ID:            "fits-fine",
		ContextWindow: 128000,
		MaxOutput:     16384,
	}})
	c := &openaiClient{name: "openai"}

	out, err := c.buildRequest(Request{
		Model:    "fits-fine",
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := outputBudget(t, out); got != 16384 {
		t.Fatalf("output budget = %d; want 16384 (must not clamp when output fits window)", got)
	}
}

// TestBuildRequestClampsLargeWindowAtMaxReserve covers the bug PR #24
// targets for a large-window model whose MaxOutput equals its (served)
// context window. Sending max_tokens == window leaves no room for input,
// so OpenRouter rejects it. The proportional reserve (window/8) is capped
// at 4096, so a 262144 window reserves the cap, not window/8.
func TestBuildRequestClampsLargeWindowAtMaxReserve(t *testing.T) {
	const window = 262144
	withLiveModels(t, []Model{{
		Provider:      "openrouter",
		ID:            "nemotron-tight",
		ContextWindow: window,
		MaxOutput:     window,
	}})
	c := &openaiClient{name: "openrouter"}

	out, err := c.buildRequest(Request{
		Model:    "nemotron-tight",
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := window - 4096 // window/8 = 32768 > 4096 cap, so reserve = 4096
	if got := outputBudget(t, out); got != want {
		t.Fatalf("output budget = %d; want %d (window - capped reserve)", got, want)
	}
}

// TestBuildRequestProportionalReserveSmallWindow verifies the reserve is
// proportional for small windows so they aren't over-penalized. gpt-4's
// 8192/8192 model reserves window/8 = 1024 (not a flat 4096, which would
// halve its budget), leaving 7168.
func TestBuildRequestProportionalReserveSmallWindow(t *testing.T) {
	const window = 8192
	withLiveModels(t, []Model{{
		Provider:      "openai",
		ID:            "gpt-4-like",
		ContextWindow: window,
		MaxOutput:     window,
	}})
	c := &openaiClient{name: "openai"}

	out, err := c.buildRequest(Request{
		Model:    "gpt-4-like",
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := window - window/8 // 8192 - 1024 = 7168
	if got := outputBudget(t, out); got != want {
		t.Fatalf("output budget = %d; want %d (window - window/8)", got, want)
	}
}

// TestBuildRequestClampFloor verifies a tiny context window never yields
// a zero or negative budget. window/8 of 16 is 2, leaving 14.
func TestBuildRequestClampFloor(t *testing.T) {
	withLiveModels(t, []Model{{
		Provider:      "openrouter",
		ID:            "tiny-window",
		ContextWindow: 16,
		MaxOutput:     16,
	}})
	c := &openaiClient{name: "openrouter"}

	out, err := c.buildRequest(Request{
		Model:    "tiny-window",
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := outputBudget(t, out); got != 14 {
		t.Fatalf("output budget = %d; want 14 (window - window/8, still positive)", got)
	}
}

// TestBuildRequestClampDoesNotInflate confirms the clamp only ever
// lowers the budget: a small explicit MaxTokens under the clamp ceiling
// passes through untouched.
func TestBuildRequestClampDoesNotInflate(t *testing.T) {
	withLiveModels(t, []Model{{
		Provider:      "openrouter",
		ID:            "roomy",
		ContextWindow: 262144,
		MaxOutput:     262144,
	}})
	c := &openaiClient{name: "openrouter"}

	out, err := c.buildRequest(Request{
		Model:     "roomy",
		MaxTokens: 8000,
		Messages:  []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := outputBudget(t, out); got != 8000 {
		t.Fatalf("output budget = %d; want 8000 (explicit request below ceiling, unchanged)", got)
	}
}
