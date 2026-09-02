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
	for _, m := range Catalog {
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
	for _, m := range Catalog {
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
	// fourfold, and ComputeCost is the only input to the cost tracker.
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
