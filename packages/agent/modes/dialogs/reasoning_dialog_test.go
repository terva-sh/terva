package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

func gpt56() provider.Model {
	// Reasoning must be set: the dialog only ever opens on a reasoning-capable
	// model, and a fixture without it exercises the "no thinking setting" path
	// instead of the ladder.
	return provider.Model{Provider: "openai-codex", ID: "gpt-5.6-sol", ContextWindow: 400000, Reasoning: true}
}

func plainModel() provider.Model {
	return provider.Model{Provider: "openai", ID: "gpt-5.5", ContextWindow: 400000, Reasoning: true}
}

// Enter both picks and closes, so a caller that read the selection back after
// the key would get nothing. HandleKey returns the level for that reason, and
// this is the assertion that keeps it that way.
func TestEnterReturnsTheLevelItSelected(t *testing.T) {
	d := NewReasoningDialog()
	d.Open("", "medium", gpt56())

	// Row 0 is "inherit"; step to "off", then "minimum".
	d.HandleKey(tui.Key{Kind: tui.KeyDown})
	d.HandleKey(tui.Key{Kind: tui.KeyDown})
	lv, chose := d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if !chose {
		t.Fatal("enter did not report a choice")
	}
	if lv != "minimum" {
		t.Errorf("chose %q, want minimum", lv)
	}
	if d.Active() {
		t.Error("enter should close the dialog")
	}
}

// The inherit row is a real choice — "" — and must be reported as chosen, or
// clearing an override would be indistinguishable from pressing esc.
func TestInheritIsAChoiceNotAnAbsentOne(t *testing.T) {
	d := NewReasoningDialog()
	d.Open("high", "medium", gpt56())

	// Opening on an override puts the cursor on that row; go back to the top.
	for range 10 {
		d.HandleKey(tui.Key{Kind: tui.KeyUp})
	}
	lv, chose := d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if !chose {
		t.Fatal("the inherit row did not report a choice")
	}
	if lv != "" {
		t.Errorf("inherit returned %q, want the empty level", lv)
	}
}

func TestEscChoosesNothing(t *testing.T) {
	d := NewReasoningDialog()
	d.Open("", "medium", gpt56())
	if _, chose := d.HandleKey(tui.Key{Kind: tui.KeyEsc}); chose {
		t.Error("esc must not count as a choice")
	}
	if d.Active() {
		t.Error("esc should close the dialog")
	}
}

// The cursor opens on the level in force, so the dialog answers "what am I on?"
// before the user touches anything.
func TestOpensOnTheActiveLevel(t *testing.T) {
	d := NewReasoningDialog()
	d.Open("maximum", "medium", gpt56())
	lv, chose := d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if !chose || lv != "maximum" {
		t.Errorf("opened on %q, want the active maximum", lv)
	}
}

// The whole reason the picker exists rather than a bare command: "max" means
// something different per model, and the row has to say which.
func TestMaxRowSaysWhetherItIsNativeHere(t *testing.T) {
	native := NewReasoningDialog()
	native.Open("", "", gpt56())
	nativeRow := rowContaining(t, native, "max ")
	if !strings.Contains(nativeRow, "native") {
		t.Errorf("gpt-5.6 max row does not say it is native:\n%q", nativeRow)
	}

	clamped := NewReasoningDialog()
	clamped.Open("", "", plainModel())
	clampedRow := rowContaining(t, clamped, "max ")
	// The row now names the rung it collapses onto ("same as maximum on this
	// model") rather than saying "clamped" — strictly more information, and the
	// assertion still fails if the row goes silent about it.
	if !strings.Contains(clampedRow, "same as") {
		t.Errorf("a non-max model's max row does not say which rung it collapses onto:\n%q", clampedRow)
	}
	if strings.Contains(clampedRow, "native") {
		t.Errorf("a non-max model's max row claims to be native:\n%q", clampedRow)
	}
	// Teeth: the two models must actually render differently, or both
	// assertions could be satisfied by one string containing both words.
	if nativeRow == clampedRow {
		t.Error("the max row is identical for both models — the distinction is not being drawn")
	}
}

// With no global set, "inherit" must not claim to follow a global that isn't
// there; the model's own default is what would actually apply.
func TestInheritRowNamesWhatWouldActuallyApply(t *testing.T) {
	withGlobal := NewReasoningDialog()
	withGlobal.Open("", "medium", gpt56())
	if row := rowContaining(t, withGlobal, "inherit"); !strings.Contains(row, "medium") {
		t.Errorf("inherit row does not name the global in force:\n%q", row)
	}

	m := gpt56()
	m.DefaultReasoning = "high"
	noGlobal := NewReasoningDialog()
	noGlobal.Open("", "", m)
	row := rowContaining(t, noGlobal, "inherit")
	if !strings.Contains(row, "model") || !strings.Contains(row, "high") {
		t.Errorf("with no global, inherit should name the model's default:\n%q", row)
	}
}

// rowContaining returns the single rendered row holding want. Scoped to one row
// rather than the whole pane: a Contains over everything matches chrome, and
// cannot see a row that truncated.
func rowContaining(t *testing.T, d *ReasoningDialog, want string) string {
	t.Helper()
	d.MaxRows = 12
	out := d.Render(tui.Theme{}, 90)
	for _, l := range out {
		if strings.Contains(l, want) {
			return l
		}
	}
	t.Fatalf("no row contains %q:\n%s", want, strings.Join(out, "\n"))
	return ""
}
