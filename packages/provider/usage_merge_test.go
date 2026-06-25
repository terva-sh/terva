package provider

import (
	"testing"
	"time"
)

// WindowPlan must stay the zero value so windows that predate the Kind field
// (e.g. Codex's) keep their meaning with no change.
func TestWindowPlanIsZeroValue(t *testing.T) {
	if WindowPlan != 0 {
		t.Fatalf("WindowPlan must be the zero value, got %d", WindowPlan)
	}
	var w UsageWindow
	if w.Kind != WindowPlan {
		t.Errorf("zero UsageWindow.Kind = %d, want WindowPlan", w.Kind)
	}
}

func TestMergeUsage(t *testing.T) {
	t1 := time.Unix(1000, 0)
	t2 := time.Unix(2000, 0)

	if _, ok := mergeUsage(UsageSnapshot{}, false, UsageSnapshot{}, false); ok {
		t.Error("merging two empty snapshots should be ok=false")
	}

	a := UsageSnapshot{Provider: "openrouter", Credits: &Credits{Balance: 5}, CapturedAt: t2}
	if got, ok := mergeUsage(a, true, UsageSnapshot{}, false); !ok || got.Credits == nil || got.Provider != "openrouter" {
		t.Errorf("a-only merge = %+v ok=%v", got, ok)
	}
	if got, ok := mergeUsage(UsageSnapshot{}, false, a, true); !ok || got.Credits == nil {
		t.Errorf("b-only merge = %+v ok=%v", got, ok)
	}

	// Both populated: credits + provider from a, newest CapturedAt, windows
	// unioned and ordered plan-before-rate-limit regardless of input order.
	b := UsageSnapshot{
		Windows: []UsageWindow{
			{Label: "requests", Kind: WindowRateLimit},
			{Label: "weekly", Kind: WindowPlan},
		},
		CapturedAt: t1,
	}
	got, ok := mergeUsage(a, true, b, true)
	if !ok {
		t.Fatal("merge of two populated snapshots should be ok")
	}
	if got.Credits == nil || got.Credits.Balance != 5 {
		t.Errorf("credits should come from a: %+v", got.Credits)
	}
	if got.Provider != "openrouter" {
		t.Errorf("provider = %q, want openrouter", got.Provider)
	}
	if !got.CapturedAt.Equal(t2) {
		t.Errorf("CapturedAt = %v, want newest %v", got.CapturedAt, t2)
	}
	if len(got.Windows) != 2 || got.Windows[0].Label != "weekly" || got.Windows[1].Label != "requests" {
		t.Errorf("windows should be unioned and plan-first, got %+v", got.Windows)
	}
}
