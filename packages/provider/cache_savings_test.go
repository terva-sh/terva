package provider

import (
	"math"
	"testing"
)

// sonnetish is the shape every Anthropic-priced model has: reads at a tenth of
// input, writes at 1.25x. The two multipliers are the whole story — a cache
// pays for itself after one re-read and costs money if it never gets one.
var sonnetish = Model{
	ID: "test-model", PriceInput: 3, PriceOutput: 15,
	PriceCacheRead: 0.3, PriceCacheWrite: 3.75,
}

func TestCacheSavingsIsTheCounterfactualNotTheDiscount(t *testing.T) {
	// A steady-state turn: almost everything read from cache.
	u := Usage{InputTokens: 2_000, CacheReadTokens: 180_000, OutputTokens: 500}
	// Uncached: 182k * $3/M = $0.546. Billed: 2k*$3/M + 180k*$0.30/M = $0.060.
	got := CacheSavings(sonnetish, u)
	want := 0.546 - 0.060
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("CacheSavings = %.6f; want %.6f", got, want)
	}
}

// The number has to be able to go negative, or it cannot report the failure it
// exists to report: a prefix that keeps getting invalidated, rewritten, and
// never read back. That turn is 25% MORE expensive than not caching at all, and
// a metric floored at zero would call it break-even.
func TestAWriteThatIsNeverReadCostsMoreThanNoCache(t *testing.T) {
	u := Usage{InputTokens: 1_000, CacheWriteTokens: 100_000}
	got := CacheSavings(sonnetish, u)
	if got >= 0 {
		t.Fatalf("CacheSavings = %.6f; want negative — a cold write is a premium, not a saving", got)
	}
	// 101k uncached = $0.303; billed 1k*$3/M + 100k*$3.75/M = $0.378.
	if want := 0.303 - 0.378; math.Abs(got-want) > 1e-9 {
		t.Errorf("CacheSavings = %.6f; want %.6f", got, want)
	}
}

// A local model prices everything at zero. Reporting the full prompt as "saved"
// there would be the loudest number on the panel and entirely fictional.
func TestAModelWithNoCachePricingSavesNothingRatherThanEverything(t *testing.T) {
	local := Model{ID: "ollama-ish", PriceInput: 0, PriceOutput: 0}
	u := Usage{InputTokens: 1_000, CacheReadTokens: 50_000}
	if got := CacheSavings(local, u); got != 0 {
		t.Errorf("CacheSavings on an unpriced model = %v; want 0", got)
	}
}

// Sum-of-parts, not parts-of-sum: the session total must survive a model switch,
// which is exactly what recomputing it later from one price sheet cannot do.
func TestSavingsAddAcrossModels(t *testing.T) {
	cheap := Model{PriceInput: 1, PriceCacheRead: 0.1, PriceCacheWrite: 1.25}
	dear := Model{PriceInput: 15, PriceCacheRead: 1.5, PriceCacheWrite: 18.75}

	a := Usage{InputTokens: 1_000, CacheReadTokens: 100_000}
	ApplyCost(cheap, &a)
	b := Usage{InputTokens: 1_000, CacheReadTokens: 100_000}
	ApplyCost(dear, &b)

	total := a.Add(b)
	want := CacheSavings(cheap, a) + CacheSavings(dear, b)
	if math.Abs(total.CacheSavedUSD-want) > 1e-9 {
		t.Errorf("summed savings = %.6f; want %.6f", total.CacheSavedUSD, want)
	}
	// And the expensive model's savings must dominate, which is the point of
	// pricing each response where it happened.
	if b.CacheSavedUSD <= a.CacheSavedUSD*10-1e-9 {
		t.Errorf("identical tokens on a 15x model saved %.6f vs %.6f; want ~15x",
			b.CacheSavedUSD, a.CacheSavedUSD)
	}
}

func TestPromptTokensCountsCacheOnBothSides(t *testing.T) {
	u := Usage{InputTokens: 10, CacheReadTokens: 20, CacheWriteTokens: 30, OutputTokens: 999}
	if got := u.PromptTokens(); got != 60 {
		t.Errorf("PromptTokens = %d; want 60 (output must not count)", got)
	}
}

// "No requests yet" and "every request missed" both have a zero hit rate and
// mean opposite things. The ok flag is how a panel tells them apart; a caller
// that ignores it draws an empty bar for a session that has not started.
func TestHitRateDistinguishesNoDataFromAllMisses(t *testing.T) {
	if _, ok := (Usage{}).CacheHitRate(); ok {
		t.Error("empty usage reported a hit rate; want ok=false")
	}
	rate, ok := Usage{InputTokens: 5_000}.CacheHitRate()
	if !ok {
		t.Fatal("a real request with no cache reads reported no data; want ok=true, rate=0")
	}
	if rate != 0 {
		t.Errorf("all-miss rate = %v; want 0", rate)
	}
	rate, ok = Usage{InputTokens: 1_000, CacheReadTokens: 3_000}.CacheHitRate()
	if !ok || math.Abs(rate-0.75) > 1e-9 {
		t.Errorf("CacheHitRate = %v, ok=%v; want 0.75, true", rate, ok)
	}
}
