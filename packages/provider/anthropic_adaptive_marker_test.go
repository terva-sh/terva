package provider

import (
	"strings"
	"testing"
)

// usesAdaptiveThinking had no test of its own. That is how its substring
// table stayed pinned to the Opus 4.7/4.8 pair while the whole Fable family
// shipped adaptive-only. The cost of getting this wrong is not cosmetic:
// buildRequest sends an explicit thinking budget AND a temperature, and an
// adaptive model rejects both, so the very first request 400s.
//
// The table matters most for ids that arrive without a catalog row — a
// Bedrock region prefix, a proxy that renames the model, or live discovery,
// which fills in no capability flags at all.
func TestAdaptiveThinkingSubstringFallback(t *testing.T) {
	adaptive := []string{
		"claude-fable-5",
		"claude-fable-5-1",
		"us.anthropic.claude-fable-5-1",
		"eu.anthropic.claude-fable-5",
		"anthropic/claude-fable-5.1",
		"CLAUDE-FABLE-5-1",
		"claude-opus-4-7",
		"claude-opus-4.8",
		// Covered by the "opus-5" marker rather than one of its own. The
		// entry is here because nothing else states that the marker reaches
		// a two-digit minor, and a future narrowing to "claude-opus-5" alone
		// would still pass every other case in this list.
		"claude-opus-5-5",
		"us.anthropic.claude-opus-5-5",
		"claude-haiku-5-5",
		"anthropic.claude-haiku-5-5",
	}
	for _, id := range adaptive {
		if !usesAdaptiveThinking(Model{ID: id}) {
			t.Errorf("usesAdaptiveThinking(%q) = false, want true: this model "+
				"rejects thinking budgets and sampling params, so buildRequest "+
				"must send the adaptive shape", id)
		}
	}

	// A budget-thinking model that takes the adaptive path loses its
	// configured thinking budget, so the negatives carry real weight.
	budget := []string{
		"claude-sonnet-4-5",
		"claude-opus-4-5",
		"claude-opus-4-6",
		"claude-haiku-4-5",
		// Haiku 3.5's ids put the version first, so "5-haiku" sits in them.
		// The "haiku-5" marker must not reach across that.
		"claude-3-5-haiku-20241022",
		"claude-3-5-haiku-latest",
		"claude-3-opus-20240229",
	}
	for _, id := range budget {
		if usesAdaptiveThinking(Model{ID: id}) {
			t.Errorf("usesAdaptiveThinking(%q) = true, want false: a budget-thinking "+
				"model silently loses its thinking budget on the adaptive path", id)
		}
	}
}

// The catalog flag is authoritative and the substring table is the fallback.
// Keep them in agreement, so that neither one alone carries the family.
func TestFableCatalogRowsDeclareAdaptiveThinking(t *testing.T) {
	seen := 0
	for _, m := range catalog {
		if !strings.Contains(strings.ToLower(m.ID), "fable") {
			continue
		}
		seen++
		if !m.AdaptiveThinking {
			t.Errorf("catalog row %s/%s is a Fable model but does not set "+
				"AdaptiveThinking; it will be sent a thinking budget it rejects",
				m.Provider, m.ID)
		}
	}
	if seen == 0 {
		t.Fatal("no Fable rows in the catalog, so this guard now tests nothing")
	}
}

// Fable 5.1 went live on the public API on 2026-09-01. Two things about the
// row are easy to get wrong by copying a neighbouring Opus row, and both are
// silent in normal use.
func TestFable51CatalogRow(t *testing.T) {
	var got Model
	found := false
	for _, m := range catalog {
		if m.Provider == "anthropic" && m.ID == "claude-fable-5-1" {
			got, found = m, true
			break
		}
	}
	if !found {
		t.Fatal("no anthropic/claude-fable-5-1 row in the catalog")
	}

	// Speculative rows are skipped by swarm tier resolution, so a released
	// model marked speculative quietly drops out of the tier ladder.
	if got.Speculative {
		t.Error("claude-fable-5-1 is marked Speculative, but it is live on the " +
			"public API; swarm tier resolution skips speculative rows")
	}
	if !got.AdaptiveThinking {
		t.Error("claude-fable-5-1 must set AdaptiveThinking: thinking is always " +
			"on for this model and it rejects an explicit budget")
	}

	// Cache reads on Fable 5.1 are 0.025x base input, not the 0.1x that every
	// other Claude model charges. A "correction" to 1 overstates cache cost
	// fourfold, and computeCost is the only input to the cost tracker.
	if got.PriceCacheRead != 0.25 {
		t.Errorf("claude-fable-5-1 PriceCacheRead = %v, want 0.25 (0.025x base "+
			"input, the Fable 5.1 exception)", got.PriceCacheRead)
	}
	if got.PriceInput != 10 || got.PriceOutput != 50 || got.PriceCacheWrite != 12.5 {
		t.Errorf("claude-fable-5-1 pricing = in %v / out %v / cache-write %v, "+
			"want 10 / 50 / 12.5", got.PriceInput, got.PriceOutput, got.PriceCacheWrite)
	}
	if got.ContextWindow != 1000000 || got.MaxOutput != 128000 {
		t.Errorf("claude-fable-5-1 limits = %d ctx / %d out, want 1000000 / 128000",
			got.ContextWindow, got.MaxOutput)
	}
}

// Opus 5.5 is the anthropic provider's default model, which raises the cost of
// every mistake in its row from "one model is wrong" to "the out-of-the-box
// session is wrong". The checks below are the ones nothing else would catch.
func TestOpus55CatalogRow(t *testing.T) {
	var got Model
	found := false
	for _, m := range catalog {
		if m.Provider == "anthropic" && m.ID == "claude-opus-5-5" {
			got, found = m, true
			break
		}
	}
	if !found {
		t.Fatal("no anthropic/claude-opus-5-5 row in the catalog")
	}

	// Speculative rows are skipped by swarm tier resolution AND would leave
	// the provider default pointing at a row the picker hides.
	if got.Speculative {
		t.Error("claude-opus-5-5 is marked Speculative, but it is live on the " +
			"public API and is the anthropic default model")
	}

	// Stricter than the rest of the family: Opus 5.5 rejects an explicit
	// budget AND thinking:{type:"disabled"}, at every effort level. Without
	// this flag buildRequest sends a budget and a temperature, and the first
	// request 400s.
	if !got.AdaptiveThinking {
		t.Error("claude-opus-5-5 must set AdaptiveThinking: thinking cannot be " +
			"disabled or budgeted on this model")
	}

	// $0.20 is 0.05x the $4 base — half the standard multiplier, and not the
	// 0.025x Fable/Mythos figure. anthropicCacheRateExceptions carries the
	// matching entry; both have to move together or the guard fails.
	if got.PriceCacheRead != 0.2 {
		t.Errorf("claude-opus-5-5 PriceCacheRead = %v, want 0.2 (0.05x base "+
			"input, the Opus 5.5 exception)", got.PriceCacheRead)
	}
	if got.PriceInput != 4 || got.PriceOutput != 20 || got.PriceCacheWrite != 5 {
		t.Errorf("claude-opus-5-5 pricing = in %v / out %v / cache-write %v, "+
			"want 4 / 20 / 5", got.PriceInput, got.PriceOutput, got.PriceCacheWrite)
	}
	if got.ContextWindow != 1000000 || got.MaxOutput != 128000 {
		t.Errorf("claude-opus-5-5 limits = %d ctx / %d out, want 1000000 / 128000",
			got.ContextWindow, got.MaxOutput)
	}

	// The speculative claude-opus-5 row is a separate model that three tests
	// resolve by exact id. Adding 5.5 must not have replaced it.
	if _, err := testReg.FindModel("anthropic", "claude-opus-5"); err != nil {
		t.Errorf("claude-opus-5 no longer resolves (%v); 5.5 lands beside it, "+
			"not instead of it", err)
	}
}

// Haiku 5.5 charges 5x on every rate once a prompt passes 100,000 tokens, so
// its row ships a 100K working window over the real 1M ceiling. The window
// is the part a later edit is most likely to "tidy" away, and nothing else
// would notice: sessions would simply compact at 1M and bill at the high tier.
func TestHaiku55CatalogRow(t *testing.T) {
	got, err := testReg.FindModel("anthropic", "claude-haiku-5-5")
	if err != nil {
		t.Fatalf("no anthropic/claude-haiku-5-5 row: %v", err)
	}
	if got.Speculative {
		t.Error("claude-haiku-5-5 is marked Speculative, but it is live on the public API")
	}
	if !got.AdaptiveThinking {
		t.Error("claude-haiku-5-5 must set AdaptiveThinking: it rejects an " +
			"explicit thinking budget and non-default sampling params")
	}

	// The ceiling stays honest; the working window and the surcharge line
	// carry the 100K.
	if got.ContextWindow != 1000000 || got.MaxOutput != 128000 {
		t.Errorf("claude-haiku-5-5 limits = %d ctx / %d out, want 1000000 / 128000",
			got.ContextWindow, got.MaxOutput)
	}
	if got.DesiredContextWindow != 100000 || got.ContextSurchargeAt != 100000 {
		t.Errorf("claude-haiku-5-5 desired / surcharge = %d / %d, want 100000 / 100000",
			got.DesiredContextWindow, got.ContextSurchargeAt)
	}
	if w := got.EffectiveContextWindow(); w != 100000 {
		t.Errorf("claude-haiku-5-5 EffectiveContextWindow = %d, want 100000, "+
			"so auto-compaction stays under the long-context surcharge", w)
	}

	// The low tier, which is what a session under the working window pays.
	if got.PriceInput != 0.1 || got.PriceOutput != 0.5 ||
		got.PriceCacheRead != 0.01 || got.PriceCacheWrite != 0.125 {
		t.Errorf("claude-haiku-5-5 pricing = in %v / out %v / cache-read %v / cache-write %v, "+
			"want 0.1 / 0.5 / 0.01 / 0.125", got.PriceInput, got.PriceOutput,
			got.PriceCacheRead, got.PriceCacheWrite)
	}

	// Haiku 4.5 lands beside it and keeps the budget-thinking path.
	h45, err := testReg.FindModel("anthropic", "claude-haiku-4-5")
	if err != nil {
		t.Fatalf("claude-haiku-4-5 no longer resolves: %v", err)
	}
	if usesAdaptiveThinking(h45) {
		t.Error("claude-haiku-4-5 now takes the adaptive path and loses its thinking budget")
	}
}
