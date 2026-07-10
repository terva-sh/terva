package tools

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/raati"
)

func TestResolveRaatiBindingsLevel0(t *testing.T) {
	got, err := ResolveRaatiBindings(0, "ollama", "qwen3:8b", nil, nil, 3)
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
	tiers := SwarmTierMap{"ollama": {"weak": "qwen3:4b", "medium": "qwen3:14b", "strong": "qwen3:32b"}}
	got, err := ResolveRaatiBindings(1, "ollama", "qwen3:14b", tiers, nil, 3)
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
	tiers := SwarmTierMap{"someprov": {"weak": "mini"}} // no medium/strong
	_, err := ResolveRaatiBindings(1, "someprov", "big", tiers, nil, 3)
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
	got, err := ResolveRaatiBindings(2, "host", "hostmodel", nil, seats, 3)
	if err != nil {
		t.Fatalf("level 2: %v", err)
	}
	if got[1].Provider != "openai-codex" || got[2].Model != "qwen3:32b" {
		t.Errorf("seats = %+v", got)
	}

	if _, err := ResolveRaatiBindings(2, "h", "m", nil, seats[:2], 3); err == nil {
		t.Errorf("wrong seat count must be rejected")
	}
	bad := append([]raati.Binding(nil), seats...)
	bad[1].Model = ""
	if _, err := ResolveRaatiBindings(2, "h", "m", nil, bad, 3); err == nil {
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
	ladder := SwarmTierMap{"ollama": {"weak": "w", "medium": "m", "strong": "s"}}
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
	partial := SwarmTierMap{"ollama": {"weak": "w", "strong": "s"}}
	if got := HighestRaatiLevel("ollama", partial, nil, seats); got != 0 {
		t.Errorf("partial ladder = %d, want 0", got)
	}
}

func TestRefuseCorrelatedGate(t *testing.T) {
	// Only an AUTO-resolved correlated gate refuses.
	if err := RefuseCorrelatedGate("code-review", raati.ClassGate, 0, true); err == nil || !strings.Contains(err.Error(), "correlated panel cannot hold a gate") {
		t.Errorf("auto gate at 0 = %v", err)
	}
	if err := RefuseCorrelatedGate("code-review", raati.ClassGate, 1, true); err != nil {
		t.Errorf("auto gate at 1 = %v", err)
	}
	// Explicit level 0 is the trust root's deliberate call.
	if err := RefuseCorrelatedGate("x", raati.ClassGate, 0, false); err != nil {
		t.Errorf("explicit gate at 0 = %v", err)
	}
	// Veto proceeds at 0 (majority-based; the usual disclosure applies).
	if err := RefuseCorrelatedGate("ethics", raati.ClassVeto, 0, true); err != nil {
		t.Errorf("auto veto at 0 = %v", err)
	}
}
