package provider

import (
	"net/http"
	"testing"
)

// The two captures below are what a CLIProxyAPI gateway returned on
// 2026-09-14 for POST /v1/chat/completions — the OpenAI wire — with the model
// choosing which subscription answered. Copied from the wire rather than
// composed, so the tests fail if either vendor's shape moves.

// codexViaChatCompletionsHeaders: a Codex-backed model served over Chat
// Completions. x-codex-*, no x-ratelimit-* anywhere.
func codexViaChatCompletionsHeaders() http.Header {
	h := http.Header{}
	for k, v := range map[string]string{
		"X-Codex-Active-Limit":                         "premium",
		"X-Codex-Plan-Type":                            "prolite",
		"X-Codex-Credits-Balance":                      "0",
		"X-Codex-Credits-Has-Credits":                  "False",
		"X-Codex-Credits-Unlimited":                    "False",
		"X-Codex-Primary-Over-Secondary-Limit-Percent": "0",
		"X-Codex-Primary-Reset-After-Seconds":          "473673",
		"X-Codex-Primary-Reset-At":                     "1789822292",
		"X-Codex-Primary-Used-Percent":                 "17",
		"X-Codex-Primary-Window-Minutes":               "10080",
		"X-Codex-Secondary-Reset-After-Seconds":        "0",
		"X-Codex-Secondary-Reset-At":                   "",
		"X-Codex-Secondary-Used-Percent":               "0",
		"X-Codex-Secondary-Window-Minutes":             "0",
		"X-Codex-Bengalfox-Limit-Name":                 "GPT-5.3-Codex-Spark",
		"X-Codex-Bengalfox-Primary-Used-Percent":       "0",
		"X-Codex-Bengalfox-Primary-Window-Minutes":     "300",
		"X-Oai-Request-Id":                             "1415fb21-5e0b-4ca9-8f6b-f0c664d6e950",
	} {
		h.Set(k, v)
	}
	return h
}

// claudeViaChatCompletionsHeaders: a Claude-backed model over the same wire.
// The unified subscription set, translated body, still no x-ratelimit-*.
func claudeViaChatCompletionsHeaders() http.Header {
	h := http.Header{}
	for k, v := range map[string]string{
		"Anthropic-Ratelimit-Unified-5h-Reset":            "1789362000",
		"Anthropic-Ratelimit-Unified-5h-Status":           "allowed",
		"Anthropic-Ratelimit-Unified-5h-Utilization":      "0.13",
		"Anthropic-Ratelimit-Unified-7d-Reset":            "1789934400",
		"Anthropic-Ratelimit-Unified-7d-Status":           "allowed",
		"Anthropic-Ratelimit-Unified-7d-Utilization":      "0.03",
		"Anthropic-Ratelimit-Unified-Overage-Reset":       "1790812800",
		"Anthropic-Ratelimit-Unified-Overage-Status":      "allowed",
		"Anthropic-Ratelimit-Unified-Overage-Utilization": "0.0",
		"Anthropic-Ratelimit-Unified-Reset":               "1789362000",
		"Anthropic-Ratelimit-Unified-Status":              "allowed",
		"Request-Id":                                      "req_011Cf2T1KqEV26pm9dgc1vDD",
	} {
		h.Set(k, v)
	}
	return h
}

// The regression: an OpenAI-compatible endpoint fronting a Codex subscription
// forwards x-codex-* and terva read only x-ratelimit-*, so /usage said the
// endpoint reported no limits while a 17%-spent weekly window rode every turn.
func TestOpenAIClientReadsCodexHeadersForwardedByAGateway(t *testing.T) {
	c := &openaiClient{name: "cpa"}
	c.recordUsageHeaders(codexViaChatCompletionsHeaders())

	snap, ok := c.UsageSnapshot()
	if !ok {
		t.Fatal("no usage recorded from a response that carried x-codex-*")
	}
	if snap.Provider != "cpa" {
		t.Errorf("Provider = %q, want the endpoint's own id", snap.Provider)
	}
	if snap.CapturedAt.IsZero() {
		t.Error("CapturedAt not stamped")
	}
	w, found := windowByLabel(snap, "weekly")
	if !found || w.UsedPercent != 17 || w.Kind != WindowPlan {
		t.Fatalf("weekly window = %+v found=%v; want 17%% WindowPlan", w, found)
	}
	// The secondary slot is all zeros: "not in force", never an empty second bar.
	if len(snap.Windows) != 1 {
		t.Errorf("windows = %+v, want only the weekly one", snap.Windows)
	}
	if snap.Credits == nil || snap.Credits.HasCredits || snap.Credits.Unlimited {
		t.Errorf("Credits = %+v, want a present, empty balance", snap.Credits)
	}
}

// Same wire, other vendor: the model picked Claude, so the unified set arrived.
func TestOpenAIClientReadsAnthropicUnifiedHeadersForwardedByAGateway(t *testing.T) {
	c := &openaiClient{name: "cpa"}
	c.recordUsageHeaders(claudeViaChatCompletionsHeaders())

	snap, ok := c.UsageSnapshot()
	if !ok {
		t.Fatal("no usage recorded from a response that carried anthropic-ratelimit-unified-*")
	}
	five, ok5 := windowByLabel(snap, "5h")
	week, okW := windowByLabel(snap, "weekly")
	if !ok5 || !okW || five.UsedPercent != 13 || week.UsedPercent != 3 {
		t.Fatalf("windows = %+v; want 5h at 13%% and weekly at 3%%", snap.Windows)
	}
	if len(snap.Windows) != 2 {
		t.Errorf("windows = %+v, want the untouched overage left out", snap.Windows)
	}
}

// The mirror: the Anthropic-Messages client behind the same gateway, with the
// model routed to a Codex subscription.
func TestAnthropicClientReadsCodexHeadersForwardedByAGateway(t *testing.T) {
	c := NewAnthropicCompatOpts("cpa-anthropic", "k", "http://gw", AnthropicCompatOptions{}).(*anthropicClient)
	c.recordUsageHeaders(codexViaChatCompletionsHeaders())

	snap, ok := c.UsageSnapshot()
	if !ok {
		t.Fatal("no usage recorded from a response that carried x-codex-*")
	}
	if snap.Provider != "cpa-anthropic" {
		t.Errorf("Provider = %q, want the endpoint's own id", snap.Provider)
	}
	if w, found := windowByLabel(snap, "weekly"); !found || w.UsedPercent != 17 {
		t.Errorf("weekly window = %+v found=%v", w, found)
	}
}

// A response carrying both a subscription set and throughput windows keeps
// both, budget first, so the status bar's "busiest plan window" is the plan.
func TestUsageHeadersMergePlanBeforeRateLimit(t *testing.T) {
	h := codexViaChatCompletionsHeaders()
	h.Set("x-ratelimit-limit-requests", "100")
	h.Set("x-ratelimit-remaining-requests", "40")

	snap, ok := parseUsageHeaders(h, "cpa")
	if !ok || len(snap.Windows) != 2 {
		t.Fatalf("windows = %+v ok=%v, want plan + rate-limit", snap.Windows, ok)
	}
	if snap.Windows[0].Kind != WindowPlan || snap.Windows[1].Kind != WindowRateLimit {
		t.Errorf("order = %+v, want plan then rate-limit", snap.Windows)
	}
}

// A provider whose rate-limit parsing is disabled still gets the vendor sets:
// the switch is about junk x-ratelimit-* values, not about usage as such.
func TestUsageHeadersIgnoreDisabledRateLimitSpecOnly(t *testing.T) {
	rateLimitSpecs["junk-rl"] = rateLimitSpec{disabled: true}
	defer delete(rateLimitSpecs, "junk-rl")

	h := codexViaChatCompletionsHeaders()
	h.Set("x-ratelimit-limit-requests", "100")
	h.Set("x-ratelimit-remaining-requests", "40")
	snap, ok := parseUsageHeaders(h, "junk-rl")
	if !ok || len(snap.Windows) != 1 || snap.Windows[0].Kind != WindowPlan {
		t.Fatalf("windows = %+v ok=%v, want the codex window alone", snap.Windows, ok)
	}
}

func TestUsageHeadersAbsentReportsNothing(t *testing.T) {
	if snap, ok := parseUsageHeaders(http.Header{}, "cpa"); ok {
		t.Fatalf("ok=true with no headers: %+v", snap)
	}
}
