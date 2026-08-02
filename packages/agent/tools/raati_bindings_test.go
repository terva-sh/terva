package tools

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/raati"
)

func TestResolveRaatiBindingsLevel0(t *testing.T) {
	got, err := ResolveRaatiBindings(0, "ollama", "qwen3:8b", "ollama", nil, nil, 3)
	if err != nil {
		t.Fatalf("level 0: %v", err)
	}
	for i, b := range got {
		if b.Provider != "ollama" || b.Model != "qwen3:8b" {
			t.Errorf("seat %d = %+v, want the host binding", i, b)
		}
	}
}

func TestResolveRaatiBindingsLevel1Ladder(t *testing.T) {
	tiers := SwarmTierMap{"ollama": {"weak": {Model: "qwen3:4b"}, "medium": {Model: "qwen3:14b"}, "strong": {Model: "qwen3:32b"}}}
	got, err := ResolveRaatiBindings(1, "ollama", "qwen3:14b", "ollama", tiers, nil, 3)
	if err != nil {
		t.Fatalf("level 1: %v", err)
	}
	// Strong→weak in seat order, UNCAPPED by the host model (level 1 is
	// an explicit request for the ladder, unlike delegation tiers).
	want := []string{"qwen3:32b", "qwen3:14b", "qwen3:4b"}
	for i, b := range got {
		if b.Provider != "ollama" || b.Model != want[i] {
			t.Errorf("seat %d = %+v, want ollama/%s", i, b, want[i])
		}
	}
}

func TestResolveRaatiBindingsLevel1NeedsFullLadder(t *testing.T) {
	tiers := SwarmTierMap{"someprov": {"weak": {Model: "mini"}}} // no medium/strong
	_, err := ResolveRaatiBindings(1, "someprov", "big", "someprov", tiers, nil, 3)
	if err == nil || !strings.Contains(err.Error(), "swarm_tiers") {
		t.Fatalf("err = %v, want an actionable ladder error", err)
	}
}

func TestResolveRaatiBindingsLevel2(t *testing.T) {
	seats := []raati.Binding{
		{Provider: "anthropic", Model: "claude-opus-4-8"},
		{Provider: "openai-codex", Model: "gpt-5.5"},
		{Provider: "ollama", Model: "qwen3:32b"},
	}
	got, err := ResolveRaatiBindings(2, "host", "hostmodel", "host", nil, seats, 3)
	if err != nil {
		t.Fatalf("level 2: %v", err)
	}
	if got[1].Provider != "openai-codex" || got[2].Model != "qwen3:32b" {
		t.Errorf("seats = %+v", got)
	}

	if _, err := ResolveRaatiBindings(2, "h", "m", "h", nil, seats[:2], 3); err == nil {
		t.Errorf("wrong seat count must be rejected")
	}
	bad := append([]raati.Binding(nil), seats...)
	bad[1].Model = ""
	if _, err := ResolveRaatiBindings(2, "h", "m", "h", nil, bad, 3); err == nil {
		t.Errorf("half-empty binding must be rejected")
	}
}

func TestRaatiLevelName(t *testing.T) {
	for level, want := range map[int]string{0: "kaiku", 1: "kuoro", 2: "käräjät"} {
		if got := RaatiLevelName(level); got != want {
			t.Errorf("RaatiLevelName(%d) = %q, want %q", level, got, want)
		}
	}
}

func TestHighestRaatiLevel(t *testing.T) {
	seats := 3
	l2 := []raati.Binding{{Provider: "a", Model: "1"}, {Provider: "b", Model: "2"}, {Provider: "c", Model: "3"}}
	ladder := SwarmTierMap{"ollama": {"weak": {Model: "w"}, "medium": {Model: "m"}, "strong": {Model: "s"}}}
	if got := HighestRaatiLevel("ollama", ladder, l2, seats); got != 2 {
		t.Errorf("level2 configured = %d, want 2", got)
	}
	if got := HighestRaatiLevel("ollama", ladder, nil, seats); got != 1 {
		t.Errorf("ladder only = %d, want 1", got)
	}
	// A hollow level2 (wrong count / missing model) doesn't count.
	if got := HighestRaatiLevel("ollama", ladder, l2[:2], seats); got != 1 {
		t.Errorf("short level2 = %d, want 1", got)
	}
	if got := HighestRaatiLevel("other", ladder, nil, seats); got != 0 {
		t.Errorf("nothing configured = %d, want 0", got)
	}
	// A partial ladder doesn't count either.
	partial := SwarmTierMap{"ollama": {"weak": {Model: "w"}, "strong": {Model: "s"}}}
	if got := HighestRaatiLevel("ollama", partial, nil, seats); got != 0 {
		t.Errorf("partial ladder = %d, want 0", got)
	}
}

// The gate honesty rule reads the resolved SEATS. Three shapes matter, and
// the level number can no longer tell them apart: identical seats, one model
// deliberately spanning thinking efforts, and genuinely different weights.
func TestRefuseCorrelatedGate(t *testing.T) {
	same := func(reasoning ...string) []raati.Binding {
		out := make([]raati.Binding, 0, len(reasoning))
		for _, r := range reasoning {
			out = append(out, raati.Binding{Provider: "p", Model: "m", Reasoning: r})
		}
		return out
	}
	correlated := same("", "", "")
	thinking := same("off", "medium", "high")
	decorrelated := []raati.Binding{
		{Provider: "a", Model: "1"}, {Provider: "b", Model: "2"}, {Provider: "c", Model: "3"},
	}

	// Only an AUTO-resolved correlated gate refuses.
	if err := RefuseCorrelatedGate("code-review", raati.ClassGate, correlated, true); err == nil || !strings.Contains(err.Error(), "correlated panel cannot hold a gate") {
		t.Errorf("auto gate on identical seats = %v", err)
	}
	if err := RefuseCorrelatedGate("code-review", raati.ClassGate, decorrelated, true); err != nil {
		t.Errorf("auto gate on a cross-provider panel = %v", err)
	}
	// Explicit is the trust root's deliberate call.
	if err := RefuseCorrelatedGate("x", raati.ClassGate, correlated, false); err != nil {
		t.Errorf("explicit correlated gate = %v", err)
	}
	// Veto proceeds (majority-based; the usual disclosure applies).
	if err := RefuseCorrelatedGate("ethics", raati.ClassVeto, correlated, true); err != nil {
		t.Errorf("auto veto on identical seats = %v", err)
	}

	// One model at three efforts: a real advisory panel, still not three
	// independent judges — and it must say WHICH problem it is, because the
	// fix ("give this provider a cross-model ladder") is a different fix.
	err := RefuseCorrelatedGate("code-review", raati.ClassGate, thinking, true)
	if err == nil {
		t.Fatal("auto gate on a one-model thinking ladder was allowed")
	}
	if !strings.Contains(err.Error(), "thinking levels") {
		t.Errorf("thinking-ladder gate refused with the wrong reason: %v", err)
	}
	// …and it is fine for everything that is not a gate. That is the whole
	// point of allowing the shape at all.
	if err := RefuseCorrelatedGate("counsel", raati.ClassAdvisory, thinking, true); err != nil {
		t.Errorf("auto advisory on a thinking ladder = %v", err)
	}
}

// The spare-host knob: with an alternative full ladder configured, the
// level-1 ladder moves off the session's provider account — so panel
// traffic stops competing for the cache the session's own prompts live
// behind. A measured convening on the host account evicted the session's
// 200K cached prefix.
func TestSpareHostLadderMovesOffTheHostAccount(t *testing.T) {
	full := map[string]TierPick{"weak": {Model: "w"}, "medium": {Model: "m"}, "strong": {Model: "s"}}
	tiers := SwarmTierMap{"hostprov": full, "otherprov": full}

	if got := SpareHostLadder("hostprov", false, tiers, nil); got != "hostprov" {
		t.Errorf("spare off: ladder = %q, want the host's own", got)
	}
	if got := SpareHostLadder("hostprov", true, tiers, nil); got != "otherprov" {
		t.Errorf("spare on: ladder = %q, want otherprov", got)
	}

	// The resolved level-1 seats follow the spared ladder; level 0 does
	// NOT move — its documented meaning is "this session's model".
	pool, err := ResolveRaatiBindings(1, "hostprov", "hostmodel", "otherprov", tiers, nil, 3)
	if err != nil {
		t.Fatalf("level 1 on the spared ladder: %v", err)
	}
	for i, b := range pool {
		if b.Provider != "otherprov" {
			t.Errorf("seat %d = %+v, want otherprov", i, b)
		}
	}
	l0, err := ResolveRaatiBindings(0, "hostprov", "hostmodel", "otherprov", tiers, nil, 3)
	if err != nil {
		t.Fatalf("level 0: %v", err)
	}
	for i, b := range l0 {
		if b.Provider != "hostprov" {
			t.Errorf("level 0 seat %d = %+v — sparing must not move an explicit level 0 off the host", i, b)
		}
	}
}

// Sparing degrades, it never refuses: no alternative full ladder means the
// host's own ladder, exactly as if the knob were off.
func TestSpareHostLadderFallsBackToTheHost(t *testing.T) {
	full := map[string]TierPick{"weak": {Model: "w"}, "medium": {Model: "m"}, "strong": {Model: "s"}}
	partial := map[string]TierPick{"weak": {Model: "w"}}
	tiers := SwarmTierMap{"hostprov": full, "otherprov": partial}
	if got := SpareHostLadder("hostprov", true, tiers, nil); got != "hostprov" {
		t.Errorf("ladder = %q, want the host fallback when no alternative is full", got)
	}
}

// A raati.level2 seat's provider counts as a candidate even without a
// swarm_tiers override — those are the providers the user demonstrably
// configured credentials for. Determinism: candidates resolve in sorted
// order, so two eligible alternatives always pick the same one.
func TestSpareHostLadderCandidates(t *testing.T) {
	full := map[string]TierPick{"weak": {Model: "w"}, "medium": {Model: "m"}, "strong": {Model: "s"}}
	tiers := SwarmTierMap{"hostprov": full, "bbb": full, "aaa": full}
	if got := SpareHostLadder("hostprov", true, tiers, nil); got != "aaa" {
		t.Errorf("ladder = %q, want the sorted-first alternative aaa", got)
	}
	// level2-only provider: candidate iff its ladder resolves fully
	// (built-ins may supply one even without an override entry).
	seats := []raati.Binding{{Provider: "aaa", Model: "x"}}
	only := SwarmTierMap{"hostprov": full, "aaa": full}
	if got := SpareHostLadder("hostprov", true, only, seats); got != "aaa" {
		t.Errorf("ladder = %q, want the level2 seat's provider", got)
	}
}
