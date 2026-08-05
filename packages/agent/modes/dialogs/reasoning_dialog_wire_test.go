package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

func mustModel(t *testing.T, provider_, id string) provider.Model {
	t.Helper()
	m, err := provider.FindModel(provider_, id)
	if err != nil {
		t.Fatalf("FindModel(%q, %q): %v", provider_, id, err)
	}
	return m
}

func detailFor(t *testing.T, d *ReasoningDialog, rung string) string {
	t.Helper()
	for _, r := range d.rows {
		if r.label == rung {
			return r.detail
		}
	}
	t.Fatalf("no row labelled %q", rung)
	return ""
}

// The bug this replaces: the dialog printed a ladder-wide token budget for
// every model, so a Codex/Responses model — which takes an effort enum and no
// budget at all — was told "~8k tokens of thinking" for a request that carried
// no such number. Nothing in the suite could see it, because the suite never
// looked at what the row said for that model.
func TestEffortWireModelsAreNeverDescribedInTokens(t *testing.T) {
	for _, tc := range []struct{ provider_, id string }{
		{"openai-codex", "gpt-5.6-sol"},
		{"google", "gemini-3-pro-preview"},
		{"groq", "deepseek-r1-distill-llama-70b"},
	} {
		d := NewReasoningDialog()
		d.Open("", "", mustModel(t, tc.provider_, tc.id))
		for _, r := range d.rows {
			if strings.Contains(r.detail, "tokens of thinking") {
				t.Errorf("%s row %q promises a token budget, but this model takes an effort enum: %q",
					tc.id, r.label, r.detail)
			}
		}
	}
}

// The other half, which keeps the fix above from being "delete the budget
// wording": a model that genuinely takes a budget must still show one, and it
// must be the CLAMPED value the builder sends rather than the ladder constant.
func TestBudgetWireModelsStillShowTheirClampedBudget(t *testing.T) {
	m := mustModel(t, "anthropic", "claude-opus-4-1-20250805")
	d := NewReasoningDialog()
	d.Open("", "", m)

	medium := detailFor(t, d, "medium")
	if !strings.Contains(medium, "tokens of thinking") {
		t.Errorf("a budget-wire model lost its budget wording: %q", medium)
	}

	// maximum's ladder value is 32768, but this model's output cap forces the
	// builder lower. The row must show what is sent, not what the ladder says.
	got := detailFor(t, d, "maximum")
	if strings.Contains(got, "32k") {
		t.Errorf("maximum row shows the unclamped ladder budget: %q", got)
	}
	want := provider.ReasoningEffectFor(m, "maximum")
	if want.Budget == 0 {
		t.Fatal("fixture no longer sends a budget — pick another budget-wire model")
	}
}

// A rung that lands on the same wire value as another says so, and names the
// rung a user would recognize rather than whichever came first in the ladder.
func TestCollapsedRungsNameTheCanonicalRung(t *testing.T) {
	d := NewReasoningDialog()
	d.Open("", "", mustModel(t, "groq", "deepseek-r1-distill-llama-70b"))

	// minimum and low both send effort "low"; "low" is the one named.
	if got := detailFor(t, d, "minimum"); !strings.Contains(got, "same as low") {
		t.Errorf("minimum does not point at low: %q", got)
	}
	// ...and the canonical rung itself carries no annotation, or the dialog
	// would claim low is the same as itself.
	if got := detailFor(t, d, "low"); strings.Contains(got, "same as") {
		t.Errorf("the canonical rung is annotated as a duplicate: %q", got)
	}
	// The top collapses onto high, which is the rung that actually differs.
	for _, rung := range []string{"maximum", "max"} {
		if got := detailFor(t, d, rung); !strings.Contains(got, "same as high") {
			t.Errorf("%s does not point at high: %q", rung, got)
		}
	}
}

// A model whose top rung IS native must not be annotated as a duplicate — the
// two notes are mutually exclusive, and running them together was how the max
// row could have said both.
func TestNativeMaxIsNotAlsoCalledADuplicate(t *testing.T) {
	d := NewReasoningDialog()
	d.Open("", "", mustModel(t, "openai-codex", "gpt-5.6-sol"))
	got := detailFor(t, d, "max")
	if !strings.Contains(got, "native") {
		t.Errorf("gpt-5.6 max row lost its native note: %q", got)
	}
	if strings.Contains(got, "same as") {
		t.Errorf("gpt-5.6 max row is both native and a duplicate: %q", got)
	}
}
