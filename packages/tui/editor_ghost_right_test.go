package tui_test

// Right arrow as a second accept key, alongside Tab.
//
// The gesture is borrowed from shell autosuggestion, where Right is what your
// hands already do. It is safe to give away here because an offer is only ever
// drawn while the buffer is EMPTY, and on an empty buffer Right had nothing to
// move toward: the caret is at rune 0 of the only line, so both branches of
// moveRight decline. The key was inert at exactly the moment an offer shows.
//
// So these guards come in pairs: Right accepts when an offer is up, and Right
// still moves the caret in every case where it moved one before.

import (
	"testing"

	"terva.sh/terva/packages/tui"
)

// The headline: Right takes the offer, the same as Tab.
func TestRightArrowAcceptsTheOffer(t *testing.T) {
	ed := ghostEditor(t, "run the tests")

	ed.HandleKey(tui.Key{Kind: tui.KeyRight})

	if ed.Value() != "run the tests" {
		t.Fatalf("Value after Right = %q, want the accepted line", ed.Value())
	}
	if ed.Ghost() != "" {
		t.Fatalf("the offer survived being accepted: %q", ed.Ghost())
	}
	// It is the user's own text now. A second Right must move the caret, not
	// paste the line again.
	ed.HandleKey(tui.Key{Kind: tui.KeyRight})
	if ed.Value() != "run the tests" {
		t.Fatalf("a second Right changed the buffer to %q", ed.Value())
	}
}

// Tab and Right must do the SAME thing, not merely both do something. A pair
// of accept keys that diverge is worse than one, because only one of them gets
// used and the other rots.
func TestRightAndTabAcceptIdentically(t *testing.T) {
	viaTab := ghostEditor(t, "run the tests")
	viaTab.HandleKey(tui.Key{Kind: tui.KeyTab})

	viaRight := ghostEditor(t, "run the tests")
	viaRight.HandleKey(tui.Key{Kind: tui.KeyRight})

	if viaTab.Value() != viaRight.Value() {
		t.Errorf("Tab gave %q, Right gave %q", viaTab.Value(), viaRight.Value())
	}
	if viaTab.Ghost() != viaRight.Ghost() {
		t.Errorf("Tab left ghost %q, Right left ghost %q", viaTab.Ghost(), viaRight.Ghost())
	}
	tabR, tabC := viaTab.CursorR, viaTab.CursorC
	rightR, rightC := viaRight.CursorR, viaRight.CursorC
	if tabR != rightR || tabC != rightC {
		t.Errorf("caret after Tab = (%d,%d), after Right = (%d,%d)", tabR, tabC, rightR, rightC)
	}
}

// The other half of the bargain. With no offer up, Right is still cursor
// movement — this is the regression that would make the feature a net loss.
func TestRightStillMovesTheCaretWithNoOffer(t *testing.T) {
	ed := tui.NewEditor(ghostPrompt)
	ed.SetValue("abc")
	ed.CursorC = 0

	ed.HandleKey(tui.Key{Kind: tui.KeyRight})

	if ed.CursorC != 1 {
		t.Fatalf("caret column = %d, want 1: Right stopped moving the cursor", ed.CursorC)
	}
	if ed.Value() != "abc" {
		t.Fatalf("Right edited the buffer: %q", ed.Value())
	}
}

// An offer is HELD but not visible while the user is typing, and the held
// offer must not hijack their cursor movement.
func TestRightMovesTheCaretWhileAnOfferIsMerelyHeld(t *testing.T) {
	ed := ghostEditor(t, "run the tests")
	ed.SetValue("abc") // hides the offer without discarding it
	ed.SetGhost("run the tests")
	ed.CursorC = 0

	ed.HandleKey(tui.Key{Kind: tui.KeyRight})

	if ed.CursorC != 1 {
		t.Fatalf("caret column = %d, want 1: a hidden offer stole the movement key", ed.CursorC)
	}
	if ed.Value() != "abc" {
		t.Fatalf("an unseen offer was accepted: %q", ed.Value())
	}
}

// Alt+Right stays word navigation. Accepting is the PLAIN gesture, and a user
// who has learned alt+arrow for words must keep it.
func TestAltRightStaysWordNavigation(t *testing.T) {
	ed := ghostEditor(t, "run the tests")
	ed.SetValue("one two")
	ed.SetGhost("run the tests")
	ed.CursorC = 0

	ed.HandleKey(tui.Key{Kind: tui.KeyRight, Alt: true})

	if ed.Value() != "one two" {
		t.Fatalf("Alt+Right accepted an offer: %q", ed.Value())
	}
	if ed.CursorC == 0 {
		t.Fatal("Alt+Right did not move by a word")
	}
}

// GhostVisible is exported for the host, which intercepts keys before the
// editor sees them. It must answer the same question Render asks: showing, not
// merely held.
func TestGhostVisibleReportsWhatIsOnScreen(t *testing.T) {
	ed := ghostEditor(t, "run the tests")
	if !ed.GhostVisible() {
		t.Fatal("an offer on an empty composer should report visible")
	}

	ed.SetValue("typing")
	ed.SetGhost("run the tests")
	if ed.GhostVisible() {
		t.Error("an offer behind the user's text should not report visible")
	}

	// Erasing the typed text puts the offer back on screen: the composer is
	// empty again and the offer was never consumed, only hidden behind what the
	// user was writing.
	ed.Clear()
	if !ed.GhostVisible() {
		t.Error("an emptied composer should show the offer it was holding again")
	}
}
