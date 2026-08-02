package provider

import (
	"net/http"
	"testing"
	"time"
)

// liveHeaders is the exact set a real OAuth /v1/messages response returned,
// values and all. Copied from the capture rather than composed, so this test
// fails if Anthropic's shape moves — an invented fixture only ever proves the
// parser agrees with whoever wrote it.
func liveHeaders() http.Header {
	h := http.Header{}
	for k, v := range map[string]string{
		"Anthropic-Ratelimit-Unified-5h-Reset":             "1785657600",
		"Anthropic-Ratelimit-Unified-5h-Status":            "allowed",
		"Anthropic-Ratelimit-Unified-5h-Utilization":       "0.06",
		"Anthropic-Ratelimit-Unified-7d-Reset":             "1785700800",
		"Anthropic-Ratelimit-Unified-7d-Status":            "allowed",
		"Anthropic-Ratelimit-Unified-7d-Utilization":       "0.54",
		"Anthropic-Ratelimit-Unified-Overage-Reset":        "1785644400",
		"Anthropic-Ratelimit-Unified-Overage-Status":       "allowed",
		"Anthropic-Ratelimit-Unified-Overage-Utilization":  "0.0",
		"Anthropic-Ratelimit-Unified-Representative-Claim": "five_hour",
		"Anthropic-Ratelimit-Unified-Fallback-Percentage":  "0.5",
		"Anthropic-Ratelimit-Unified-Reset":                "1785657600",
		"Anthropic-Ratelimit-Unified-Status":               "allowed",
	} {
		h.Set(k, v)
	}
	return h
}

func windowByLabel(snap UsageSnapshot, label string) (UsageWindow, bool) {
	for _, w := range snap.Windows {
		if w.Label == label {
			return w, true
		}
	}
	return UsageWindow{}, false
}

// The header is a FRACTION and UsedPercent is 0..100. Getting this backwards
// renders a week that is more than half spent as half of one percent — a
// number a user would act on, in the wrong direction.
func TestAnthropicUtilizationIsAFractionNotAPercent(t *testing.T) {
	snap, ok := parseAnthropicUsageHeaders(liveHeaders())
	if !ok {
		t.Fatal("live headers parsed as no usage — the whole complaint, unfixed")
	}
	five, ok := windowByLabel(snap, "5h")
	if !ok {
		t.Fatalf("no 5h window in %+v", snap.Windows)
	}
	if five.UsedPercent != 6 {
		t.Errorf("5h UsedPercent = %v, want 6 (from 0.06)", five.UsedPercent)
	}
	week, ok := windowByLabel(snap, "weekly")
	if !ok {
		t.Fatalf("no weekly window in %+v", snap.Windows)
	}
	if week.UsedPercent != 54 {
		t.Errorf("weekly UsedPercent = %v, want 54 (from 0.54)", week.UsedPercent)
	}
}

func TestAnthropicResetIsAUnixTimestamp(t *testing.T) {
	snap, _ := parseAnthropicUsageHeaders(liveHeaders())
	five, _ := windowByLabel(snap, "5h")
	if want := time.Unix(1785657600, 0); !five.ResetsAt.Equal(want) {
		t.Errorf("5h ResetsAt = %v, want %v", five.ResetsAt, want)
	}
}

// An overage window reported at 0% is "you have no overage", not a budget you
// are near the start of. Drawn beside two real windows it reads as the latter.
func TestAnUntouchedOverageWindowIsNotDrawn(t *testing.T) {
	snap, _ := parseAnthropicUsageHeaders(liveHeaders())
	if _, ok := windowByLabel(snap, "overage"); ok {
		t.Errorf("an allowed overage at 0.0 was reported as a window: %+v", snap.Windows)
	}
	if len(snap.Windows) != 2 {
		t.Errorf("got %d windows, want 2 (5h and weekly): %+v", len(snap.Windows), snap.Windows)
	}
}

// ...but one carrying something must show, or the one number the user most
// needs is the one terva hides.
func TestAnOverageInUseIsDrawn(t *testing.T) {
	h := liveHeaders()
	h.Set("Anthropic-Ratelimit-Unified-Overage-Utilization", "0.25")
	snap, _ := parseAnthropicUsageHeaders(h)
	w, ok := windowByLabel(snap, "overage")
	if !ok {
		t.Fatalf("an overage at 25%% was dropped: %+v", snap.Windows)
	}
	if w.UsedPercent != 25 {
		t.Errorf("overage UsedPercent = %v, want 25", w.UsedPercent)
	}
}

// A window that is no longer "allowed" shows even at 0%: an unexpected state is
// exactly what a usage view exists to surface, and filtering it out for looking
// empty hides the one thing worth reading.
func TestANotAllowedOverageIsDrawnEvenAtZero(t *testing.T) {
	h := liveHeaders()
	h.Set("Anthropic-Ratelimit-Unified-Overage-Status", "exceeded")
	snap, _ := parseAnthropicUsageHeaders(h)
	if _, ok := windowByLabel(snap, "overage"); !ok {
		t.Errorf("an exceeded overage was filtered out for reading 0%%: %+v", snap.Windows)
	}
}

// An API-key account gets no unified headers at all. It must report NOTHING —
// windows reading zero would be a confident claim about a subscription that
// does not exist.
func TestNoHeadersReportsNoUsageRatherThanZeros(t *testing.T) {
	snap, ok := parseAnthropicUsageHeaders(http.Header{})
	if ok {
		t.Errorf("bare headers reported usage: %+v", snap)
	}
	if len(snap.Windows) != 0 {
		t.Errorf("windows invented from nothing: %+v", snap.Windows)
	}
}

// "Reported but unparseable" must read as unknown, not as a confident 0%.
func TestAnUnparseableUtilizationIsUnknownNotZero(t *testing.T) {
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "n/a")
	h.Set("Anthropic-Ratelimit-Unified-5h-Reset", "1785657600")
	snap, ok := parseAnthropicUsageHeaders(h)
	if !ok {
		t.Fatal("a window with a reset but a bad percentage was dropped entirely")
	}
	if snap.Windows[0].UsedPercent >= 0 {
		t.Errorf("UsedPercent = %v; want negative (unknown)", snap.Windows[0].UsedPercent)
	}
}

// The client half: a successful response must fill the snapshot, and the
// provider name must be the CLIENT's — this same client drives kimi-coding and
// other Anthropic-Messages third parties, and stamping "anthropic" on their
// windows would attribute one subscription's usage to another.
func TestTheClientRecordsWindowsUnderItsOwnProviderName(t *testing.T) {
	c := NewAnthropicOAuthSource(StaticCredential("t"), "").(*anthropicClient)
	c.name = "kimi-coding"
	c.recordUsageHeaders(liveHeaders())

	snap, ok := c.UsageSnapshot()
	if !ok {
		t.Fatal("client reported no usage after a response that carried it")
	}
	if snap.Provider != "kimi-coding" {
		t.Errorf("Provider = %q, want the client's own name", snap.Provider)
	}
}

// The regression that started this: before the wiring, /usage said Anthropic
// reports nothing. The client must satisfy UsageReporter at all.
func TestTheAnthropicClientIsAUsageReporter(t *testing.T) {
	var c Client = NewAnthropicOAuthSource(StaticCredential("t"), "")
	if _, ok := clientAs[UsageReporter](c); !ok {
		t.Fatal("the anthropic client implements no UsageReporter — /usage will keep saying it has none")
	}
}
