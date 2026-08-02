package tools

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/raati"
)

// A ladder built out of thinking effort rather than model choice. This is the
// shape that was impossible before a rung could name an effort: a provider
// that ships one good model and no cheap sibling had no ladder to offer, and
// three rungs on one id resolved to three identical children.
func TestThinkingLadderOnOneModel(t *testing.T) {
	ov := SwarmTierMap{"kimi": {
		"weak":   {Model: "k3", Reasoning: "off"},
		"medium": {Model: "k3", Reasoning: "medium"},
		"strong": {Model: "k3", Reasoning: "high"},
	}}

	picks, sources := SwarmTierLadder("kimi", ov)
	for i, want := range []string{"off", "medium", "high"} {
		if picks[i].Model != "k3" || picks[i].Reasoning != want {
			t.Errorf("%s = %+v, want k3 at %q", swarmRankName[i], picks[i], want)
		}
		if sources[i] != "override" {
			t.Errorf("%s source = %q, want override", swarmRankName[i], sources[i])
		}
	}

	// The host cap must not collapse the ladder. Ranking the host model is
	// ambiguous here by construction — one id on three rungs — so ranking
	// declines and every tier resolves uncapped. Without that, a k3 host
	// would rank as "weak" (the first match) and every spawn would be
	// pinned to thinking off.
	if got := ResolveSwarmTier("kimi", "k3", "strong", ov); got.Reasoning != "high" {
		t.Errorf("strong from a k3 host = %+v, want high (the cap must not fire)", got)
	}
	if got := ResolveSwarmTier("kimi", "k3", "weak", ov); got.Reasoning != "off" {
		t.Errorf("weak from a k3 host = %+v, want off", got)
	}
}

// A rung that names ONLY an effort is a complete instruction: run the rung's
// built-in model, think this hard. Repeating the id would just invite it to
// drift away from the built-in one.
func TestReasoningOnlyRungBorrowsTheBuiltinModel(t *testing.T) {
	ov := SwarmTierMap{"anthropic": {"weak": {Reasoning: "off"}}}
	got := ResolveSwarmTier("anthropic", "claude-opus-4-1", "weak", ov)
	if !strings.Contains(got.Model, "haiku") {
		t.Errorf("model = %q, want the built-in weak rung (a haiku)", got.Model)
	}
	if got.Reasoning != "off" {
		t.Errorf("reasoning = %q, want off", got.Reasoning)
	}
	// An effort-only rung on a provider with no built-in table has no model
	// to borrow and must not resolve to a bare effort.
	none := SwarmTierMap{"openrouter": {"weak": {Reasoning: "off"}}}
	if got := ResolveSwarmTier("openrouter", "x", "weak", none); !got.IsZero() {
		t.Errorf("effort-only rung with no built-in model = %+v, want the zero pick", got)
	}
}

// Built-in rungs never name an effort. terva recognises model FAMILIES; how
// hard someone wants their sub-agents to think is not something to guess.
func TestBuiltinRungsNameNoEffort(t *testing.T) {
	for _, p := range tableProviders() {
		picks, _ := SwarmTierLadder(p, nil)
		for rank, pick := range picks {
			if pick.Reasoning != "" {
				t.Errorf("%s/%s ships an effort (%q) — the built-in table must not guess one", p, swarmRankName[rank], pick.Reasoning)
			}
		}
	}
}

// Level 1 over a thinking ladder seats three seats that differ only in how
// hard they think — and the label has to say so, because it is the only thing
// telling the seats apart on the record.
func TestLevel1SeatsAThinkingLadder(t *testing.T) {
	ov := SwarmTierMap{"kimi": {
		"weak":   {Model: "k3", Reasoning: "off"},
		"medium": {Model: "k3", Reasoning: "medium"},
		"strong": {Model: "k3", Reasoning: "high"},
	}}
	pool, err := ResolveRaatiBindings(1, "kimi", "k3", "kimi", ov, nil, 3)
	if err != nil {
		t.Fatalf("level 1 on a thinking ladder: %v", err)
	}
	// Strong→weak in seat order, same as a model ladder.
	for i, want := range []string{"high", "medium", "off"} {
		if pool[i].Model != "k3" || pool[i].Reasoning != want {
			t.Errorf("seat %d = %+v, want k3 at %q", i, pool[i], want)
		}
	}
	if !raati.SameWeights(pool) {
		t.Error("SameWeights should hold — one model")
	}
	if raati.SameEffort(pool) {
		t.Error("SameEffort should NOT hold — three efforts")
	}
	// The seats line is how a verdict's reader knows what judged them.
	label := raati.Unit{Provider: pool[0].Provider, Model: pool[0].Model, Reasoning: pool[0].Reasoning}.BindingLabel()
	if !strings.Contains(label, "high") {
		t.Errorf("binding label %q omits the effort — three seats would render identically", label)
	}

	// And a thinking ladder is enough to reach level 1 at all.
	if lvl := HighestRaatiLevel("kimi", ov, nil, 3); lvl != 1 {
		t.Errorf("highest level on a thinking ladder = %d, want 1", lvl)
	}
}
