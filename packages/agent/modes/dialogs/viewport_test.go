package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/tui"
)

func vpTheme() tui.Theme { return tui.Theme{Muted: 8, Accent: 4} }

func body(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "line"
	}
	return out
}

// press feeds keys and re-fits between them, which is what a real dialog does:
// a key lands, then a render fits and paints.
func press(v *Viewport, total, rows int, kinds ...tui.KeyKind) {
	v.Fit(total, rows)
	for _, k := range kinds {
		v.HandleKey(tui.Key{Kind: k})
		v.Fit(total, rows)
	}
}

// End must leave a FULL pane with the last line at its bottom.
//
// This is the bug the shared viewport exists to make impossible. Three dialogs
// clamped at len(body)-1, so scrolling to the end left one line alone in an
// otherwise blank pane and looked like the content had been lost.
func TestBottomLeavesAFullPane(t *testing.T) {
	var v Viewport
	press(&v, 100, 14, tui.KeyEnd)

	if got := v.Offset(); got != 86 {
		t.Errorf("End offset = %d, want 86 (100 - 14)", got)
	}
	start, end := v.Window()
	if end-start != 14 {
		t.Errorf("End left %d visible rows, want a full pane of 14", end-start)
	}
	if end != 100 {
		t.Errorf("End stopped at line %d, want the last line (100) on screen", end)
	}
}

// Scrolling down one line at a time must stop where End stops. A Down that
// could walk past Max would reintroduce the empty pane one keypress at a time.
func TestDownStopsWhereEndStops(t *testing.T) {
	var v Viewport
	kinds := make([]tui.KeyKind, 200)
	for i := range kinds {
		kinds[i] = tui.KeyDown
	}
	press(&v, 100, 14, kinds...)

	if got := v.Offset(); got != 86 {
		t.Errorf("200 Downs over a 100-line body landed at %d, want 86", got)
	}
}

// A page is one screenful. Every dialog used to pick a number unrelated to what
// it displayed — 5 rows paged in a pane showing 12 — so the eye and the key
// disagreed about how far a page was.
func TestAPageIsOneScreenful(t *testing.T) {
	var v Viewport
	press(&v, 100, 14, tui.KeyPageDown)
	if got := v.Offset(); got != 14 {
		t.Errorf("PageDown moved %d rows, want the pane height 14", got)
	}
	press(&v, 100, 14, tui.KeyPageDown)
	if got := v.Offset(); got != 28 {
		t.Errorf("second PageDown landed at %d, want 28", got)
	}
	press(&v, 100, 14, tui.KeyPageUp)
	if got := v.Offset(); got != 14 {
		t.Errorf("PageUp landed at %d, want 14", got)
	}
}

// Paging is clamped at both ends, so holding the key does not run the offset
// off into negative or past the last full pane.
func TestPagingClampsAtBothEnds(t *testing.T) {
	var v Viewport
	press(&v, 40, 10, tui.KeyPageDown, tui.KeyPageDown, tui.KeyPageDown,
		tui.KeyPageDown, tui.KeyPageDown)
	if got := v.Offset(); got != 30 {
		t.Errorf("paging past the end landed at %d, want Max 30", got)
	}
	press(&v, 40, 10, tui.KeyPageUp, tui.KeyPageUp, tui.KeyPageUp, tui.KeyPageUp, tui.KeyPageUp)
	if got := v.Offset(); got != 0 {
		t.Errorf("paging past the top landed at %d, want 0", got)
	}
}

// A body that fits needs no scrolling at all, and every jump is a no-op. A Max
// derived by subtraction alone would go negative here.
func TestABodyThatFitsNeverScrolls(t *testing.T) {
	var v Viewport
	press(&v, 5, 14, tui.KeyEnd, tui.KeyPageDown, tui.KeyDown)

	if got := v.Offset(); got != 0 {
		t.Errorf("a 5-line body in a 14-row pane scrolled to %d, want 0", got)
	}
	if v.Max() != 0 {
		t.Errorf("Max = %d on a body that fits, want 0", v.Max())
	}
	if v.Scrollable() {
		t.Error("a body that fits reported itself scrollable")
	}
	start, end := v.Window()
	if start != 0 || end != 5 {
		t.Errorf("Window = [%d,%d), want the whole body [0,5)", start, end)
	}
}

// A body that SHRINKS under a held offset — a log that rotated, a filter that
// narrowed — must pull the offset back rather than render past the end.
func TestFitPullsAStaleOffsetBack(t *testing.T) {
	var v Viewport
	press(&v, 100, 14, tui.KeyEnd)
	v.Fit(20, 14) // the body shrank underneath us

	if got := v.Offset(); got != 6 {
		t.Errorf("offset after the body shrank = %d, want 6 (20 - 14)", got)
	}
	start, end := v.Window()
	if end > 20 {
		t.Errorf("Window ran past the shortened body: [%d,%d)", start, end)
	}
}

func TestHomeReturnsToTheTop(t *testing.T) {
	var v Viewport
	press(&v, 100, 14, tui.KeyEnd, tui.KeyHome)
	if got := v.Offset(); got != 0 {
		t.Errorf("Home landed at %d, want 0", got)
	}
}

// HandleKey must claim only the keys it handles, or a dialog routing its
// unclaimed keys here would lose every one of its own.
func TestHandleKeyClaimsOnlyScrollKeys(t *testing.T) {
	var v Viewport
	v.Fit(100, 14)
	for _, k := range []tui.KeyKind{tui.KeyUp, tui.KeyDown, tui.KeyPageUp,
		tui.KeyPageDown, tui.KeyHome, tui.KeyEnd} {
		if !v.HandleKey(tui.Key{Kind: k}) {
			t.Errorf("HandleKey did not claim %v", k)
		}
	}
	for _, k := range []tui.KeyKind{tui.KeyEsc, tui.KeyEnter, tui.KeyTab, tui.KeyLeft, tui.KeyRight} {
		if v.HandleKey(tui.Key{Kind: k}) {
			t.Errorf("HandleKey wrongly claimed %v", k)
		}
	}
}

// The indicators are how a reader knows there is more. They must appear only
// when there IS more, on the side there is more of.
func TestRowsMarkOnlyTheDirectionsWithMore(t *testing.T) {
	var v Viewport
	th := vpTheme()

	v.Fit(100, 14)
	top := strings.Join(v.Rows(th, body(100)), "\n")
	if strings.Contains(top, "more above") {
		t.Errorf("top of the body claimed content above it:\n%s", top)
	}
	if !strings.Contains(top, "more below") {
		t.Errorf("top of a 100-line body did not offer more below:\n%s", top)
	}

	press(&v, 100, 14, tui.KeyEnd)
	bottom := strings.Join(v.Rows(th, body(100)), "\n")
	if !strings.Contains(bottom, "more above") {
		t.Errorf("bottom of the body did not report content above:\n%s", bottom)
	}
	if strings.Contains(bottom, "more below") {
		t.Errorf("bottom of the body claimed content below it:\n%s", bottom)
	}

	v.Fit(5, 14)
	fits := strings.Join(v.Rows(th, body(5)), "\n")
	if strings.Contains(fits, "more") {
		t.Errorf("a body that fits offered more in some direction:\n%s", fits)
	}
}

// Reveal moves the minimum distance, so a cursor walking down a list scrolls
// one line at a time instead of jumping the cursor to the middle of the pane.
func TestRevealScrollsTheMinimumDistance(t *testing.T) {
	var v Viewport
	v.Fit(100, 10)

	v.Reveal(9) // already the last visible row
	if got := v.Offset(); got != 0 {
		t.Errorf("revealing an already-visible line scrolled to %d, want 0", got)
	}
	v.Reveal(10) // one past the bottom
	if got := v.Offset(); got != 1 {
		t.Errorf("revealing one past the bottom scrolled to %d, want 1", got)
	}
	v.Reveal(0) // back to the top
	if got := v.Offset(); got != 0 {
		t.Errorf("revealing line 0 scrolled to %d, want 0", got)
	}
	// Never past the last full pane, even for a line near the end.
	v.Reveal(99)
	if got := v.Offset(); got != 90 {
		t.Errorf("revealing the last line scrolled to %d, want Max 90", got)
	}
}
