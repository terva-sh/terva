package provider

import (
	"net/http"
	"testing"
	"time"
)

func TestRateLimitSpecFor(t *testing.T) {
	// Unknown provider rides the standard spec (default-on).
	if s, ok := rateLimitSpecFor("groq"); !ok ||
		s.requestsLimit != "x-ratelimit-limit-requests" || s.resetFormat != resetDuration {
		t.Errorf("groq should use the standard spec: %+v ok=%v", s, ok)
	}
	// Cerebras override: window-suffixed names + seconds reset.
	if s, ok := rateLimitSpecFor("cerebras"); !ok ||
		s.tokensRemaining != "x-ratelimit-remaining-tokens-minute" || s.resetFormat != resetSeconds {
		t.Errorf("cerebras override missing: %+v ok=%v", s, ok)
	}
}

func TestParseRateLimitHeadersStandard(t *testing.T) {
	spec, _ := rateLimitSpecFor("groq")
	h := http.Header{}
	h.Set("x-ratelimit-limit-requests", "1000")
	h.Set("x-ratelimit-remaining-requests", "750")
	h.Set("x-ratelimit-reset-requests", "2m59.56s")
	h.Set("x-ratelimit-limit-tokens", "6000")
	h.Set("x-ratelimit-remaining-tokens", "4800")
	h.Set("x-ratelimit-reset-tokens", "7.66s")

	snap, ok := parseRateLimitHeaders(h, spec)
	if !ok || len(snap.Windows) != 2 {
		t.Fatalf("want 2 windows, got %+v ok=%v", snap.Windows, ok)
	}
	req := snap.Windows[0]
	if req.Label != "requests" || req.Kind != WindowRateLimit {
		t.Errorf("requests window = %+v", req)
	}
	if req.UsedPercent < 24.9 || req.UsedPercent > 25.1 { // (1000-750)/1000
		t.Errorf("requests used%% = %v, want 25", req.UsedPercent)
	}
	if req.ResetsAt.IsZero() {
		t.Error("requests reset should parse from the duration string")
	}
}

func TestParseRateLimitHeadersCerebrasSeconds(t *testing.T) {
	spec, _ := rateLimitSpecFor("cerebras")
	h := http.Header{}
	// Only the tokens window present → one window, seconds reset.
	h.Set("x-ratelimit-limit-tokens-minute", "100000")
	h.Set("x-ratelimit-remaining-tokens-minute", "60000")
	h.Set("x-ratelimit-reset-tokens-minute", "12.5")

	snap, ok := parseRateLimitHeaders(h, spec)
	if !ok || len(snap.Windows) != 1 {
		t.Fatalf("want 1 window (tokens only), got %+v ok=%v", snap.Windows, ok)
	}
	w := snap.Windows[0]
	if w.Label != "tokens/min" || w.UsedPercent < 39.9 || w.UsedPercent > 40.1 { // 40% used
		t.Errorf("tokens window = %+v", w)
	}
	if w.ResetsAt.IsZero() {
		t.Error("seconds reset should parse")
	}
}

func TestParseRateLimitHeadersAbsent(t *testing.T) {
	spec, _ := rateLimitSpecFor("groq")
	if _, ok := parseRateLimitHeaders(http.Header{}, spec); ok {
		t.Error("no headers should yield ok=false")
	}
}

func TestParseResetValue(t *testing.T) {
	if d, ok := parseResetValue("2m59.56s", resetDuration); !ok || d < 2*time.Minute {
		t.Errorf("duration parse = %v ok=%v", d, ok)
	}
	if d, ok := parseResetValue("12.5", resetSeconds); !ok || d != time.Duration(12.5*float64(time.Second)) {
		t.Errorf("seconds parse = %v ok=%v", d, ok)
	}
	if _, ok := parseResetValue("garbage", resetDuration); ok {
		t.Error("garbage should not parse")
	}
	if _, ok := parseResetValue("", resetSeconds); ok {
		t.Error("empty should not parse")
	}
}

// A disabled spec turns the provider off at the lookup layer.
func TestRateLimitDisabled(t *testing.T) {
	rateLimitSpecs["__test_disabled"] = rateLimitSpec{disabled: true}
	defer delete(rateLimitSpecs, "__test_disabled")
	if _, ok := rateLimitSpecFor("__test_disabled"); ok {
		t.Error("a disabled provider should return ok=false")
	}
}

// End-to-end on openaiClient: recording headers populates UsageSnapshot, and
// ClientUsage surfaces it via the UsageReporter contract.
func TestOpenAIClientRecordsRateLimit(t *testing.T) {
	c := &openaiClient{name: "groq"}
	if _, ok := c.UsageSnapshot(); ok {
		t.Error("no usage before any response")
	}
	h := http.Header{}
	h.Set("x-ratelimit-limit-requests", "100")
	h.Set("x-ratelimit-remaining-requests", "40")
	h.Set("x-ratelimit-reset-requests", "30s")
	c.recordRateLimitHeaders(h)

	snap, ok := c.UsageSnapshot()
	if !ok || snap.Provider != "groq" || len(snap.Windows) != 1 || snap.Windows[0].UsedPercent != 60 {
		t.Fatalf("UsageSnapshot = %+v ok=%v (want 1 window, 60%%)", snap, ok)
	}
	if snap.Windows[0].Kind != WindowRateLimit {
		t.Errorf("window Kind = %d, want WindowRateLimit", snap.Windows[0].Kind)
	}
	if s, ok := ClientUsage(c); !ok || len(s.Windows) != 1 {
		t.Errorf("ClientUsage should surface rate-limit windows: %+v ok=%v", s, ok)
	}
}
