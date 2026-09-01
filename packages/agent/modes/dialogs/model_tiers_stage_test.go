package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

func tierView() ctrlproto.ModelTiersView {
	return ctrlproto.ModelTiersView{
		Provider: "google",
		Rungs: []ctrlproto.ModelTierRung{
			{Rung: "weak", Model: "gemini-3.1-flash-lite", Label: "Gemini 3.1 Flash Lite", Source: "built-in"},
			{Rung: "medium", Model: "gemini-3.7-flash", Label: "Gemini 3.7 Flash", Source: "built-in"},
			{Rung: "strong", Model: "gemini-3.1-pro-preview", Pinned: "gemini-3.1-pro-preview", Reasoning: "high", Source: "override"},
		},
	}
}

func tierDialog(v ctrlproto.ModelTiersView) *ModelDialog {
	d := NewModelDialog()
	d.active = true
	d.ShowTiers(v)
	return d
}

// The stage shows what a rung RESOLVES to and where that came from. Both halves
// are load-bearing: google's medium and strong rungs once resolved to
// image-generation models while config held nothing at all, so a screen that
// listed overrides would have been blank on the day it was wrong.
func TestTierStageShowsResolvedModelsAndTheirSource(t *testing.T) {
	d := tierDialog(tierView())
	out := strings.Join(d.Render(tui.Dark, 90), "\n")
	for _, want := range []string{
		"tiers · google",
		"weak", "gemini-3.1-flash-lite", "built-in",
		"strong", "gemini-3.1-pro-preview", "override", "thinking high",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered ladder is missing %q:\n%s", want, out)
		}
	}
}

// A rung that resolves to nothing must SAY that the sub-agent falls back to the
// host model. A bare dash reads as "off"; the truth is that asking for a cheap
// tier silently runs the host model, at host cost.
func TestTierStageNamesTheHostFallback(t *testing.T) {
	d := tierDialog(ctrlproto.ModelTiersView{
		Provider: "ollama",
		Rungs:    []ctrlproto.ModelTierRung{{Rung: "weak"}, {Rung: "medium"}, {Rung: "strong"}},
	})
	out := strings.Join(d.Render(tui.Dark, 90), "\n")
	if !strings.Contains(out, "falls back to the host model") {
		t.Errorf("an unresolved rung must name the fallback:\n%s", out)
	}
}

// Cycling a rung's thinking level echoes the PINNED model, not the resolved
// one. Sending the resolved id would silently freeze a rung that had been
// tracking its family rule — the rung would stop following the catalog the
// moment someone touched its level.
func TestTierThinkingCycleEchoesThePinNotTheResolvedModel(t *testing.T) {
	d := tierDialog(tierView())
	d.tierCursor = 0 // weak: built-in, so Pinned is empty

	act := d.HandleKey(tui.Key{Kind: tui.KeyCtrlT})
	if !act.TierSet || act.Rung != "weak" {
		t.Fatalf("ctrl+t = %+v, want a TierSet on weak", act)
	}
	if act.Model != "" {
		t.Errorf("Model = %q, want empty — echoing the resolved id would freeze a built-in rung", act.Model)
	}

	d.tierCursor = 2 // strong: pinned
	act = d.HandleKey(tui.Key{Kind: tui.KeyCtrlT})
	if act.Model != "gemini-3.1-pro-preview" {
		t.Errorf("Model = %q, want the pin carried back", act.Model)
	}
}

// Enter opens the model list FOR the rung, and picking there pins the rung
// rather than switching the session's model — which is what that key does every
// other time it is pressed in this dialog.
func TestTierEnterPicksAModelForTheRungNotTheSession(t *testing.T) {
	d := tierDialog(tierView())
	d.tierCursor = 1

	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if d.stage != stageModel || d.tierRung != "medium" {
		t.Fatalf("stage=%v tierRung=%q, want the model list opened for medium", d.stage, d.tierRung)
	}

	// Seed the list and confirm a pick.
	d.p.setCatalog([]provider.Model{{Provider: "google", ID: "gemini-3.7-flash"}}, "", 14)
	act := d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if act.Select {
		t.Error("picking for a rung must not switch the session's model")
	}
	if !act.TierSet || act.Rung != "medium" || act.Model != "gemini-3.7-flash" {
		t.Errorf("pick = %+v, want medium pinned to gemini-3.7-flash", act)
	}
	if d.stage != stageTiers || d.tierRung != "" {
		t.Errorf("stage=%v tierRung=%q, want a return to the ladder", d.stage, d.tierRung)
	}
}

// Esc backs out to the ladder, not to the provider list: dropping two levels
// would lose the place the pick was started from.
func TestTierEscFromAModelPickReturnsToTheLadder(t *testing.T) {
	d := tierDialog(tierView())
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if d.stage != stageModel {
		t.Fatal("did not reach the model list")
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if d.stage != stageTiers || d.tierRung != "" {
		t.Errorf("stage=%v tierRung=%q, want the ladder back", d.stage, d.tierRung)
	}
}

// Esc from the ladder returns to the provider list rather than closing: a
// glance at one provider's tiers should not cost the whole dialog.
func TestTierEscReturnsToTheProviderList(t *testing.T) {
	d := tierDialog(tierView())
	act := d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if act.Close || !d.Active() {
		t.Fatalf("esc closed the dialog: %+v", act)
	}
	if d.stage != stageProvider {
		t.Errorf("stage = %v, want the provider list", d.stage)
	}
}

// 'r' resets the highlighted rung.
func TestTierResetActsOnTheHighlightedRung(t *testing.T) {
	d := tierDialog(tierView())
	d.tierCursor = 2
	act := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'r'})
	if !act.TierReset || act.Rung != "strong" || act.Provider != "google" {
		t.Fatalf("'r' = %+v, want a reset of google/strong", act)
	}
}
