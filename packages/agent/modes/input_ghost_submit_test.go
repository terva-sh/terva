package modes

// Sending a message ends the offer, and it is the send path's own job now.
//
// Clear used to do it, as a side effect of every route to an empty composer.
// That is what made erasing your own typing destroy a suggestion you might
// still want. Clear no longer ends the offer, so the one case that genuinely
// must — the conversation moving on — says so explicitly, and this is the guard
// that the explicit statement is actually there.

import (
	"testing"

	"terva.sh/terva/packages/tui"
)

// A submitted message moves the conversation past the point the offer was
// proposed against, so the offer must not survive it. Left standing it would
// reappear on the empty composer the instant the message went out, still
// looking current while describing a conversation that no longer exists.
func TestSendingAMessageEndsTheOffer(t *testing.T) {
	i := NewInteractive(InteractiveConfig{Theme: tui.Dark})
	i.ed.SetGhost("run the tests")
	if !i.ed.GhostVisible() {
		t.Fatal("fixture: the offer is not showing")
	}

	// Type a message of the user's own over the offer, then send it. The turn
	// itself does not have to start: the offer is dropped above the
	// readiness gate, which is what makes this drivable without a carrier.
	for _, r := range "ship it" {
		i.handleKey(t.Context(), tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	i.handleKey(t.Context(), tui.Key{Kind: tui.KeyEnter})

	if got := i.ed.Ghost(); got != "" {
		t.Errorf("the offer outlived the message that was sent: %q", got)
	}
	if i.ed.GhostVisible() {
		t.Error("the offer is back on the emptied composer after a send")
	}
}

// The companion, and the whole point of the change: the SAME emptied composer,
// reached by erasing instead of sending, keeps the offer. If this and the test
// above ever agree, one of them is wrong.
func TestErasingAMessageKeepsTheOffer(t *testing.T) {
	i := NewInteractive(InteractiveConfig{Theme: tui.Dark})
	i.ed.SetGhost("run the tests")

	for _, r := range "ship it" {
		i.handleKey(t.Context(), tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	i.handleKey(t.Context(), tui.Key{Kind: tui.KeyEsc})

	if !i.ed.IsEmpty() {
		t.Fatalf("fixture: Esc did not empty the composer: %q", i.ed.Value())
	}
	if !i.ed.GhostVisible() {
		t.Errorf("erasing the typed text took the offer with it: %q", i.ed.Ghost())
	}
}
