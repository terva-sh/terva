package provider

import "testing"

// There were two context-window semantics in the tree, and the surfaces that
// used them contradicted each other in front of the user.
//
// Agent.ContextUsage, the compaction keep-tail budget and ShouldAutoCompact all
// divide by EffectiveContextWindow. Nine gauge sites read the raw ContextWindow:
// the TUI status bar, the script-mode payload, the chat-bridge /status line, the
// web session card, the usage surface and the context inspector. tools/status.go
// stated the contract out loud — "this percentage matches the status-bar gauge
// and the auto-compaction threshold" — and it did not match.
//
// On gpt-5.6-luna (ContextWindow 1,050,000, DesiredContextWindow 272,000)
// auto-compaction fires at 217,600 tokens while every gauge read 21% full. The
// user watched the conversation compact at a fifth of a bar with no surface
// showing the number that triggered it.
//
// These tests use a SYNTHETIC model rather than naming a catalog row: a fixture
// keyed to gpt-5.6-luna would become a scheduled skip the day that row is
// retired, and the invariant is about the accessor, not about any one model.

const gaugeTestProvider = "context-gauge-test"

// withSyntheticModels installs models for the duration of a test and restores
// the user layer afterwards. SetUserModels(nil) clears it, which is the state
// every other test expects.
func withSyntheticModels(t *testing.T, models ...Model) {
	t.Helper()
	SetUserModels(models)
	t.Cleanup(func() { SetUserModels(nil) })
}

func TestContextGaugeReportsTheEffectiveWindowNotTheHardCeiling(t *testing.T) {
	withSyntheticModels(t, Model{
		Provider: gaugeTestProvider, ID: "surcharged",
		ContextWindow: 1050000, DesiredContextWindow: 272000,
	})

	if got, want := ContextGauge(gaugeTestProvider, "surcharged"), 272000; got != want {
		t.Errorf("ContextGauge = %d, want the effective window %d — a gauge reading against the "+
			"hard ceiling shows 21%% at the moment auto-compaction fires", got, want)
	}
	// And the hard ceiling is still reachable, because the maxTok clamp and
	// every surface reporting the model's SPEC needs it.
	m, err := FindModel(gaugeTestProvider, "surcharged")
	if err != nil {
		t.Fatal(err)
	}
	if m.ContextWindow != 1050000 {
		t.Errorf("the hard ceiling was lost: %d", m.ContextWindow)
	}
}

// The pair that must agree: whatever a gauge divides by, and whatever
// auto-compaction divides by. Stated as one assertion so the two cannot drift
// apart again without something failing.
func TestTheGaugeDenominatorEqualsTheAutoCompactionDenominator(t *testing.T) {
	cases := []Model{
		{Provider: gaugeTestProvider, ID: "surcharged", ContextWindow: 1050000, DesiredContextWindow: 272000},
		{Provider: gaugeTestProvider, ID: "plain", ContextWindow: 200000},
		// A desired window ABOVE the max is clamped down; the gauge must follow
		// the clamp, not the raw desire.
		{Provider: gaugeTestProvider, ID: "over-desired", ContextWindow: 100000, DesiredContextWindow: 400000},
		// An unknown max with a desired window is honoured as-is.
		{Provider: gaugeTestProvider, ID: "unknown-max", DesiredContextWindow: 64000},
	}
	withSyntheticModels(t, cases...)

	for _, want := range cases {
		m, err := FindModel(gaugeTestProvider, want.ID)
		if err != nil {
			t.Fatalf("%s: %v", want.ID, err)
		}
		if got := ContextGauge(gaugeTestProvider, want.ID); got != m.EffectiveContextWindow() {
			t.Errorf("%s: gauge denominator %d != auto-compaction denominator %d",
				want.ID, got, m.EffectiveContextWindow())
		}
	}
}

// An unknown model yields 0, which every caller already renders as "no gauge".
// Returning the hard ceiling, or panicking, would each be worse in its own way.
func TestContextGaugeIsZeroForAnUnknownModel(t *testing.T) {
	if got := ContextGauge(gaugeTestProvider, "no-such-model"); got != 0 {
		t.Errorf("ContextGauge(unknown) = %d, want 0", got)
	}
}

// Every model the product actually ships must agree too. This is the
// non-vacuous half: the catalog has thousands of rows, so a ContextGauge that
// stopped consulting EffectiveContextWindow fails here even if the synthetic
// cases above were somehow satisfied.
func TestEveryCatalogModelsGaugeIsItsEffectiveWindow(t *testing.T) {
	models := Active()
	if len(models) < 100 {
		t.Fatalf("the active catalog holds %d models; this scan is not exercising anything", len(models))
	}
	var checked, differing int
	for _, m := range models {
		got := ContextGauge(m.Provider, m.ID)
		if got != m.EffectiveContextWindow() {
			t.Errorf("%s/%s: gauge %d != effective window %d", m.Provider, m.ID, got, m.EffectiveContextWindow())
		}
		checked++
		if m.EffectiveContextWindow() != m.ContextWindow {
			differing++
		}
	}
	// Reported, not asserted: whether any SHIPPED model currently sets a desired
	// window is a catalog fact that may legitimately change, and failing the
	// build over it would be a test asserting a product decision. The synthetic
	// cases above are what keep this suite honest when the count is zero.
	t.Logf("checked %d catalog models; %d have an effective window below their hard ceiling", checked, differing)
}
