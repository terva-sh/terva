package tui

// The colour of an un-accepted composer offer.
//
// The offer is drawn on the same row, in the same place, as the text the user
// would type. Nothing else distinguishes the two, so the shade IS the feature:
// get it wrong and the model's idea reads as something already typed.

import (
	"strings"
	"testing"
)

// Zero means "not set", not "black". Every theme file written before this slot
// existed has a zero here, and those themes must still draw an offer that
// reads as secondary text.
func TestGhostColorFallsBackToMuted(t *testing.T) {
	if got := (Theme{Muted: 244}).GhostColor(); got != 244 {
		t.Errorf("an unset ghost slot should fall back to muted, got %d", got)
	}
	if got := (Theme{Muted: 244, Ghost: 99}).GhostColor(); got != 99 {
		t.Errorf("a set ghost slot should win, got %d", got)
	}
}

// The property that matters is not a specific number: it is that an offer
// never renders in the same colour as the user's own text, in any built-in.
func TestEveryBuiltInDistinguishesAnOfferFromTyping(t *testing.T) {
	for _, tc := range []struct {
		name string
		th   Theme
		// lighter reports which direction "receding" is for this theme. On a
		// dark background the foreground is bright and an offer recedes by
		// going darker; on a light background it is the other way round, so
		// "darker" would make the offer MORE prominent, not less.
		lighter bool
	}{
		{"Dark", Dark, false},
		{"Light", Light, true},
		{"DarkDaltonized", DarkDaltonized, false},
		{"LightDaltonized", LightDaltonized, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ghost := tc.th.GhostColor()
			if ghost == tc.th.FG {
				t.Fatalf("an offer renders in the typing colour (%d), so nothing tells them apart", ghost)
			}
			// 232-255 is the xterm greyscale ramp, ascending from near-black.
			if tc.lighter && ghost <= tc.th.FG {
				t.Errorf("ghost %d is not lighter than fg %d, so it does not recede on a light background", ghost, tc.th.FG)
			}
			if !tc.lighter && ghost >= tc.th.FG {
				t.Errorf("ghost %d is not darker than fg %d, so it does not recede on a dark background", ghost, tc.th.FG)
			}
		})
	}
}

// GhostText is what the editor is handed. It has to actually colour the text,
// and it has to close the sequence — an unterminated SGR would bleed into
// everything painted after the composer.
func TestGhostTextColoursAndClosesTheSequence(t *testing.T) {
	got := Dark.GhostText("run the tests")

	if !strings.Contains(got, "run the tests") {
		t.Fatalf("the offer text did not survive styling: %q", got)
	}
	if got == "run the tests" {
		t.Fatal("the offer was not styled at all")
	}
	if !strings.Contains(got, sgrFG(Dark.GhostColor())) {
		t.Errorf("the ghost colour is not in the output: %q", got)
	}
	if !strings.HasSuffix(got, reset) {
		t.Errorf("the styling is left open, so it bleeds into later rows: %q", got)
	}
	// The distinction the user actually sees: an offer must not be painted the
	// same way ordinary foreground text is.
	if got == Dark.FG256(Dark.FG, "run the tests") {
		t.Error("an offer is painted exactly like typed text")
	}
}
