package modes

// The offer's styling is actually WIRED, at both places an editor gets one.
//
// tui.Editor has always had the GhostStyle hook and a guard proving styling
// never leaks into the buffer. What it never had was a caller: the hook sat
// nil, so the offer drew in the same shade as the user's own typing and the
// package's own tests could not tell, because a nil hook is a legal editor.
// That is the same shape as the share_file wire gap — a feature built
// correctly and never connected.
//
// So these assert through the styler the host installed, not through a
// hand-made one. The colour is checked as OUTPUT rather than by comparing
// function values, which Go cannot do anyway.

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/tui"
)

// The composer the constructor hands back must already know how to dim an
// offer. A nil hook here is the bug: it renders the model's idea as if the
// user had typed it.
func TestTheComposerIsBuiltWithAnOfferStyle(t *testing.T) {
	i := NewInteractive(InteractiveConfig{Theme: tui.Dark, Carrier: newFakeCarrier()})

	if i.ed.GhostStyle == nil {
		t.Fatal("the editor was built with no offer styling, so an offer reads as typed text")
	}
	got := i.ed.GhostStyle("run the tests")
	if !strings.Contains(got, "run the tests") {
		t.Fatalf("the offer text did not survive styling: %q", got)
	}
	if got == "run the tests" {
		t.Fatal("the offer style is a pass-through, so nothing distinguishes an offer")
	}
	if want := tui.Dark.GhostText("run the tests"); got != want {
		t.Errorf("the composer styles an offer as %q, the theme says %q", got, want)
	}
}

// The offer must be drawn dimmer than the prompt row's own text, end to end:
// this reads what Render actually emits rather than trusting the hook alone.
func TestARenderedOfferCarriesTheDimColour(t *testing.T) {
	i := NewInteractive(InteractiveConfig{Theme: tui.Dark, Carrier: newFakeCarrier()})
	i.ed.SetGhost("run the tests")

	lines, _, _ := i.ed.Render(80)
	if len(lines) == 0 {
		t.Fatal("the composer rendered no rows")
	}
	row := lines[0]

	if !strings.Contains(row, "run the tests") {
		t.Fatalf("the offer is not on the composer row: %q", row)
	}
	if !strings.Contains(row, tui.Dark.GhostText("run the tests")) {
		t.Errorf("the offer is not painted in the ghost colour: %q", row)
	}
	// The distinction the user sees. An offer drawn in the ordinary foreground
	// is the bug, whatever else the row contains.
	if strings.Contains(row, tui.Dark.FG256(tui.Dark.FG, "run the tests")) {
		t.Errorf("the offer is painted in the typing colour: %q", row)
	}
}

// Switching themes must re-bind the styler, not leave the old palette's method
// value behind. A ghost standing across a dark/light switch would otherwise
// keep a shade that recedes on the OTHER background — the one colour that does
// not recede on this one.
func TestSwitchingThemesRestylesAStandingOffer(t *testing.T) {
	i := NewInteractive(InteractiveConfig{Theme: tui.Dark, Carrier: newFakeCarrier()})
	i.ed.SetGhost("run the tests")

	before := styledOffer(t, i)
	if before != tui.Dark.GhostText("run the tests") {
		t.Fatalf("fixture: the composer did not start on the dark styler: %q", before)
	}

	i.applyThemeNow("light")

	after := styledOffer(t, i)
	if after == before {
		t.Fatal("the offer style survived a theme switch, so it still uses the old palette")
	}
	if want := tui.Light.GhostText("run the tests"); after != want {
		t.Errorf("after switching to light the styler gives %q, the light theme says %q", after, want)
	}
	// And the offer itself is still standing — restyling must not consume it.
	if i.ed.Ghost() != "run the tests" {
		t.Errorf("the theme switch discarded the offer: %q", i.ed.Ghost())
	}
}

// styledOffer runs the composer's installed styler, failing by NAME when there
// is none.
//
// Calling a nil hook directly panics, and a panic is the one failure that
// cannot say which wiring broke — it reads the same whether the constructor
// never installed a styler or the theme switch dropped it. This test exists to
// tell those two apart, so it must not fail in a way that cannot.
func styledOffer(t *testing.T, i *Interactive) string {
	t.Helper()
	if i.ed.GhostStyle == nil {
		t.Fatal("the composer has no offer styling installed")
	}
	return i.ed.GhostStyle("run the tests")
}
