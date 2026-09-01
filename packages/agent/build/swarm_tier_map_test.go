package build

import (
	"slices"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/tools"
)

// config.TierConfig has a field per rung, so a name has to be mapped back to a
// field somewhere. When that mapping is spelled out at a call site it goes
// stale in SILENCE: SwarmTierMap listed weak/medium/strong inline, so every
// `cheap` override a user wrote was discarded before it reached the resolver —
// while the built-in cheap pick still resolved underneath and made both the CLI
// table and the TUI ladder look entirely correct.
//
// The ladder is the authority. If a rung is added to it and not to Rungs(),
// this fails rather than the override quietly evaporating.
func TestTierConfigCoversEveryLadderRung(t *testing.T) {
	var got []string
	for name := range (config.TierConfig{}).Rungs() {
		got = append(got, name)
	}
	want := slices.Clone(tools.SwarmTierNames())
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("TierConfig.Rungs() covers %v, the ladder is %v", got, want)
	}
}

// The end-to-end shape of the bug: a provider whose ONLY pinned rung is the
// cost tier. Nothing about weak/medium/strong is involved, so a conversion that
// drops cheap drops the provider entirely and returns a nil map.
func TestACheapOnlyOverrideSurvivesTheConversion(t *testing.T) {
	in := map[string]config.TierConfig{
		"ollama": {Cheap: config.TierRung{Model: "llama-3.2-1b", Reasoning: "minimum"}},
	}
	out := SwarmTierMap(in)
	if out == nil {
		t.Fatal("a cheap-only override produced a nil map — the whole provider was dropped")
	}
	pick, ok := out["ollama"]["cheap"]
	if !ok {
		t.Fatalf("cheap rung missing from the converted map: %#v", out["ollama"])
	}
	if pick.Model != "llama-3.2-1b" || pick.Reasoning != "minimum" {
		t.Errorf("cheap pick = %#v, want the pinned model and effort", pick)
	}
}

// Every rung round-trips, not just the one that was broken. A mapping keyed by
// name is exactly as easy to get wrong in the other direction — pointing two
// names at one field would satisfy a per-rung check done one rung at a time.
func TestEveryRungRoundTripsToItsOwnField(t *testing.T) {
	var tc config.TierConfig
	tc.Weak = config.TierRung{Model: "w"}
	tc.Medium = config.TierRung{Model: "m"}
	tc.Strong = config.TierRung{Model: "s"}
	tc.Cheap = config.TierRung{Model: "c"}

	out := SwarmTierMap(map[string]config.TierConfig{"p": tc})
	got := map[string]string{}
	for rung, pick := range out["p"] {
		got[rung] = pick.Model
	}
	want := map[string]string{"weak": "w", "medium": "m", "strong": "s", "cheap": "c"}
	if len(got) != len(want) {
		t.Fatalf("converted %d rungs, want %d: %v", len(got), len(want), got)
	}
	for rung, model := range want {
		if got[rung] != model {
			t.Errorf("rung %q -> model %q, want %q", rung, got[rung], model)
		}
	}
}
