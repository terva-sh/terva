package provider

import (
	"math"
	"strings"
	"testing"
)

// haikuish has Haiku 5.5's shape: one step at 100K, every rate 5x above it.
var haikuish = Model{
	ID: "haikuish", PriceInput: 0.1, PriceOutput: 0.5,
	PriceCacheRead: 0.01, PriceCacheWrite: 0.125,
	ContextSurchargeAt: 100000,
	LongContextTiers: []PriceTier{
		{Above: 100000, Input: 0.5, Output: 2.5, CacheRead: 0.05, CacheWrite: 0.625},
	},
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

// The line itself is billed at the base sheet, and one token past it moves the
// WHOLE response, output included, to the long sheet. A tier that billed only
// the tokens past the line would understate by nearly the full step.
func TestLongContextTierOneTokenEitherSideOfTheLine(t *testing.T) {
	at := Usage{InputTokens: 100000, OutputTokens: 1000}
	if got, want := computeCost(haikuish, at), (100000*0.1+1000*0.5)/1e6; !near(got, want) {
		t.Errorf("at the threshold: cost = %v, want %v (the base sheet; the tier starts above the line)", got, want)
	}
	over := Usage{InputTokens: 100001, OutputTokens: 1000}
	if got, want := computeCost(haikuish, over), (100001*0.5+1000*2.5)/1e6; !near(got, want) {
		t.Errorf("one token over: cost = %v, want %v (every token, output too, at the long sheet)", got, want)
	}
}

// Anthropic counts cache reads and writes toward the 100K, so a turn that is
// almost all cache read still crosses the line. Counting InputTokens alone
// would keep a long cached session on the base sheet forever.
func TestLongContextTierCountsCachedPrompt(t *testing.T) {
	u := Usage{InputTokens: 1000, CacheReadTokens: 90000, CacheWriteTokens: 9001, OutputTokens: 500}
	want := (1000*0.5 + 90000*0.05 + 9001*0.625 + 500*2.5) / 1e6
	if got := computeCost(haikuish, u); !near(got, want) {
		t.Errorf("cost = %v, want %v: a 100,001-token prompt that is mostly cache is still over the line", got, want)
	}
}

// Several steps: the response pays the highest one it passed, whatever order
// the tiers arrive in.
func TestLongContextTiersPickTheHighestPassed(t *testing.T) {
	m := Model{
		PriceInput: 1, PriceOutput: 5,
		LongContextTiers: []PriceTier{
			{Above: 128000, Input: 3, Output: 15},
			{Above: 32000, Input: 1.8, Output: 9},
			{Above: 256000, Input: 6, Output: 60},
		},
	}
	for _, tc := range []struct {
		prompt  int
		in, out float64
	}{
		{32000, 1, 5},
		{32001, 1.8, 9},
		{128001, 3, 15},
		{256001, 6, 60},
	} {
		u := Usage{InputTokens: tc.prompt, OutputTokens: 100}
		want := (float64(tc.prompt)*tc.in + 100*tc.out) / 1e6
		if got := computeCost(m, u); !near(got, want) {
			t.Errorf("prompt %d: cost = %v, want %v (input %v, output %v)", tc.prompt, got, want, tc.in, tc.out)
		}
	}
}

// models.dev leaves cache_write off most tiers. Above the line those tokens
// pay the base write rate, not nothing.
func TestLongContextTierZeroRateFallsBackToBase(t *testing.T) {
	m := Model{
		PriceInput: 3, PriceOutput: 15, PriceCacheRead: 0.3, PriceCacheWrite: 3.75,
		LongContextTiers: []PriceTier{{Above: 200000, Input: 6, Output: 22.5, CacheRead: 0.6}},
	}
	u := Usage{InputTokens: 100000, CacheWriteTokens: 100001}
	want := (100000*6 + 100001*3.75) / 1e6
	if got := computeCost(m, u); !near(got, want) {
		t.Errorf("cost = %v, want %v: an unset tier rate bills at the base rate", got, want)
	}
}

// A rate the higher step leaves out inherits from the step below, not from
// the base sheet. Falling back to the base would bill the cache cheaper past
// 128K than past 32K, which no vendor does and the catalog guard rejects.
func TestLongContextTierZeroRateInheritsFromTheStepBelow(t *testing.T) {
	m := Model{
		PriceInput: 1, PriceOutput: 5, PriceCacheRead: 0.2,
		LongContextTiers: []PriceTier{
			{Above: 128000, Input: 3, Output: 15},
			{Above: 32000, Input: 1.8, Output: 9, CacheRead: 0.36},
		},
	}
	u := Usage{InputTokens: 100000, CacheReadTokens: 28001}
	want := (100000*3 + 28001*0.36) / 1e6
	if got := computeCost(m, u); !near(got, want) {
		t.Errorf("cost = %v, want %v: cache reads past 128K pay the 32K step's 0.36", got, want)
	}
}

// The savings counterfactual has to come off the same sheet as the bill. A
// base-rate counterfactual against a 5x bill reports a heavy loss on a turn
// where the cache saved most of the price.
func TestCacheSavingsUsesTheSameTierAsTheBill(t *testing.T) {
	u := Usage{InputTokens: 2000, CacheReadTokens: 180000, OutputTokens: 500}
	uncached := 182000 * 0.5 / 1e6
	billed := (2000*0.5 + 180000*0.05) / 1e6
	got := cacheSavings(haikuish, u)
	if !near(got, uncached-billed) {
		t.Errorf("cacheSavings = %v, want %v (both sides at the long sheet)", got, uncached-billed)
	}
	if got <= 0 {
		t.Errorf("cacheSavings = %v: a 99%%-cached turn must report a saving", got)
	}

	applied := u
	ApplyCost(haikuish, &applied)
	if !near(applied.CostUSD, computeCost(haikuish, u)) || !near(applied.CacheSavedUSD, got) {
		t.Errorf("ApplyCost = cost %v / saved %v, want %v / %v",
			applied.CostUSD, applied.CacheSavedUSD, computeCost(haikuish, u), got)
	}
}

// A model with no tiers prices exactly as it did before tiers existed.
func TestNoLongContextTiersIsTheFlatSheet(t *testing.T) {
	flat := haikuish
	flat.LongContextTiers = nil
	u := Usage{InputTokens: 900000, CacheReadTokens: 50000, OutputTokens: 2000}
	want := (900000*0.1 + 50000*0.01 + 2000*0.5) / 1e6
	if got := computeCost(flat, u); !near(got, want) {
		t.Errorf("cost = %v, want %v", got, want)
	}
}

// Every row that declares tiers must state them in a shape pricedFor and the
// working-window hint agree on. ContextSurchargeAt is what the model editor
// offers as the cost-safe window, so a hint above the first step tells the
// user a window is safe when it is already billing the higher sheet.
func TestLongContextTiersAgreeWithSurcharge(t *testing.T) {
	seen := 0
	for _, m := range catalog {
		key := m.Provider + "/" + m.ID
		if len(m.LongContextTiers) == 0 {
			if m.ContextSurchargeAt > 0 {
				t.Errorf("%s declares ContextSurchargeAt %d but no LongContextTiers, so the surcharge is never billed",
					key, m.ContextSurchargeAt)
			}
			continue
		}
		seen++
		if m.ContextSurchargeAt != m.LongContextTiers[0].Above {
			t.Errorf("%s ContextSurchargeAt = %d, want %d (the first tier's threshold)",
				key, m.ContextSurchargeAt, m.LongContextTiers[0].Above)
		}
		prev := PriceTier{Input: m.PriceInput, Output: m.PriceOutput, CacheRead: m.PriceCacheRead, CacheWrite: m.PriceCacheWrite}
		for i, tier := range m.LongContextTiers {
			if tier.Above <= prev.Above {
				t.Errorf("%s tier %d: Above %d is not above the previous step (%d); list them in ascending order",
					key, i, tier.Above, prev.Above)
			}
			// A step that lowers a rate is a transcription error. Zero is
			// "inherit", so it is skipped.
			for _, r := range []struct {
				name      string
				got, base float64
			}{
				{"Input", tier.Input, prev.Input},
				{"Output", tier.Output, prev.Output},
				{"CacheRead", tier.CacheRead, prev.CacheRead},
				{"CacheWrite", tier.CacheWrite, prev.CacheWrite},
			} {
				if r.got > 0 && r.got < r.base {
					t.Errorf("%s tier %d: %s %v is below the step under it (%v)", key, i, r.name, r.got, r.base)
				}
			}
			prev = PriceTier{
				Above:      tier.Above,
				Input:      rateOr(tier.Input, prev.Input),
				Output:     rateOr(tier.Output, prev.Output),
				CacheRead:  rateOr(tier.CacheRead, prev.CacheRead),
				CacheWrite: rateOr(tier.CacheWrite, prev.CacheWrite),
			}
		}
	}
	if seen == 0 {
		t.Fatal("no catalog row declares LongContextTiers, so this guard tests nothing")
	}
}

// The hand-curated sheets, as published. OpenAI's figures are from
// developers.openai.com/api/docs/pricing and Anthropic's from the Haiku 5.5
// pricing section of platform.claude.com, both read on 2026-10-10.
func TestCuratedLongContextSheets(t *testing.T) {
	openai := map[string]PriceTier{
		"gpt-6-astra":   {Above: 272000, Input: 20, Output: 75, CacheRead: 2, CacheWrite: 25},
		"gpt-6.1-sol":   {Above: 272000, Input: 4, Output: 15, CacheRead: 0.2, CacheWrite: 5},
		"gpt-6-sol":     {Above: 272000, Input: 4, Output: 15, CacheRead: 0.4, CacheWrite: 5},
		"gpt-6-luna":    {Above: 272000, Input: 0.2, Output: 0.75, CacheRead: 0.02, CacheWrite: 0.25},
		"gpt-5.6-sol":   {Above: 272000, Input: 8, Output: 30, CacheRead: 0.8, CacheWrite: 10},
		"gpt-5.6-terra": {Above: 272000, Input: 4, Output: 18, CacheRead: 0.4, CacheWrite: 5},
		"gpt-5.6-luna":  {Above: 272000, Input: 0.4, Output: 1.8, CacheRead: 0.04, CacheWrite: 0.5},
		"gpt-5.5":       {Above: 272000, Input: 10, Output: 45, CacheRead: 1},
		"gpt-5.4":       {Above: 272000, Input: 5, Output: 22.5, CacheRead: 0.5},
	}
	cases := map[string]PriceTier{
		"anthropic/claude-haiku-5-5":             {Above: 100000, Input: 0.5, Output: 2.5, CacheRead: 0.05, CacheWrite: 0.625},
		"openrouter/anthropic/claude-sonnet-4.5": {Above: 200000, Input: 6, Output: 22.5, CacheRead: 0.6, CacheWrite: 7.5},
	}
	for id, sheet := range openai {
		cases["openai-codex/"+id] = sheet
	}
	for _, id := range []string{"gpt-6-astra", "gpt-6.1-sol", "gpt-6-sol", "gpt-6-luna"} {
		cases["openai-responses/"+id] = openai[id]
	}
	for _, id := range []string{"gpt-5.4", "gpt-5.5"} {
		cases["openai/"+id] = openai[id]
	}
	for key, want := range cases {
		prov, id, _ := strings.Cut(key, "/")
		m, err := testReg.FindModel(prov, id)
		if err != nil {
			t.Errorf("%s: %v", key, err)
			continue
		}
		if len(m.LongContextTiers) != 1 || m.LongContextTiers[0] != want {
			t.Errorf("%s LongContextTiers = %+v, want [%+v]", key, m.LongContextTiers, want)
		}
	}
}

// End to end on a real row: one token past Haiku 5.5's line costs five times
// the token under it, cache and output included.
func TestHaiku55BillsFiveTimesPastTheLine(t *testing.T) {
	m, err := testReg.FindModel("anthropic", "claude-haiku-5-5")
	if err != nil {
		t.Fatal(err)
	}
	under := Usage{InputTokens: 10000, CacheReadTokens: 80000, CacheWriteTokens: 10000, OutputTokens: 4000}
	over := under
	over.CacheReadTokens++
	ratio := computeCost(m, over) / computeCost(m, under)
	if math.Abs(ratio-5) > 1e-4 {
		t.Errorf("cost ratio one token past 100,000 = %.5f, want 5", ratio)
	}
}
