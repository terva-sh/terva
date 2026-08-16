package modes

// Right arrow reaches the composer's offer, past the host's own key handling.
//
// This is the guard the editor-level tests cannot give. `handleInputHistoryKey`
// claims Left/Right BEFORE `ed.HandleKey` ever runs, and it engages on an EMPTY
// composer — which is the only state where an offer is drawn. Its Right path
// ends in `ed.Clear()`, and Clear drops the ghost. So the accept gesture would
// have DELETED the thing it was aimed at, while every guard in packages/tui
// stayed green, because none of them route through the host.
//
// Both preconditions hold exactly when an offer is up: the composer is empty by
// definition, and history is non-empty because an offer only follows a reply,
// which the user's own message provoked. There is no lucky case.

import (
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// ghostHost is an Interactive with recall history and an offer standing, which
// is the collision: both features want the empty composer and the Right key.
func ghostHost(t *testing.T, offer string) *Interactive {
	t.Helper()
	// inputHistoryIndex MUST start at -1, as NewInteractive sets it. The zero
	// value means "already browsing history", which inverts recall's first
	// guard: a bare literal makes `index < 0 && !IsEmpty()` false and lets the
	// branch run over a composer the user is writing in — a state the real TUI
	// never boots into. A fixture that skips this tests a machine that does not
	// exist.
	i := &Interactive{ed: tui.NewEditor(""), inputHistoryIndex: -1}
	i.carrierMessages = []provider.Message{userMsg("an earlier prompt")}
	i.recordInput("an earlier prompt", true)
	i.ed.SetGhost(offer)
	if !i.ed.GhostVisible() {
		t.Fatal("fixture: the offer is not showing")
	}
	if len(i.inputHistory()) == 0 {
		t.Fatal("fixture: no recall history, so the interceptor would not fire anyway")
	}
	return i
}

// The offer survives the host and is accepted, rather than being cleared by the
// recall branch on its way past.
func TestRightArrowReachesTheOfferPastRecall(t *testing.T) {
	i := ghostHost(t, "run the tests")

	if i.handleInputHistoryKey(tui.Key{Kind: tui.KeyRight}) {
		t.Fatal("recall consumed Right while an offer was showing")
	}
	// Not consumed, so the editor gets it — which is what the real handler does
	// next.
	i.ed.HandleKey(tui.Key{Kind: tui.KeyRight})

	if got := i.ed.Value(); got != "run the tests" {
		t.Fatalf("composer = %q, want the accepted offer", got)
	}
	if got := i.ed.Ghost(); got != "" {
		t.Fatalf("the offer survived acceptance: %q", got)
	}
}

// The failure this replaces, stated as its own guard: the offer must still be
// there after the host has had the key.
func TestRightArrowDoesNotDiscardTheOffer(t *testing.T) {
	i := ghostHost(t, "run the tests")

	i.handleInputHistoryKey(tui.Key{Kind: tui.KeyRight})

	if i.ed.Ghost() == "" {
		t.Fatal("the host cleared the offer instead of yielding the key to it")
	}
	if !i.ed.GhostVisible() {
		t.Fatal("the offer is no longer on screen after the host handled Right")
	}
}

// Recall itself is untouched. Left still opens history over an empty composer
// with an offer standing: browsing installs a buffer, and whoever installs a
// buffer owns the composer — the same rule that ends an offer on submit.
func TestLeftStillOpensRecallWithAnOfferShowing(t *testing.T) {
	i := ghostHost(t, "run the tests")

	if !i.handleInputHistoryKey(tui.Key{Kind: tui.KeyLeft}) {
		t.Fatal("Left no longer opens recall")
	}
	if got := i.ed.Value(); got != "an earlier prompt" {
		t.Fatalf("composer = %q, want the recalled prompt", got)
	}
	if got := i.ed.Ghost(); got != "" {
		t.Fatalf("the offer outlived the buffer that replaced it: %q", got)
	}
}

// With no offer up, Right is recall's again — the yield is scoped to the case
// it exists for, and takes nothing away the rest of the time.
func TestRightStillDrivesRecallWithNoOffer(t *testing.T) {
	i := ghostHost(t, "run the tests")
	i.ed.SetGhost("")

	if !i.handleInputHistoryKey(tui.Key{Kind: tui.KeyRight}) {
		t.Fatal("Right stopped driving recall once no offer was showing")
	}
}

// An offer that is merely HELD — the user is typing over it — does not take the
// key either. Recall's own guard declines a non-empty composer, and the yield
// must not reach past that.
func TestAHeldOfferDoesNotTakeRightFromRecall(t *testing.T) {
	i := ghostHost(t, "run the tests")
	i.ed.SetValue("half a thought")
	i.ed.SetGhost("run the tests")

	if i.ed.GhostVisible() {
		t.Fatal("fixture: the offer should be hidden behind the typed text")
	}
	// Recall declines because the composer is not empty; the point is that it
	// declines for ITS OWN reason and not because a hidden offer claimed the key.
	if i.handleInputHistoryKey(tui.Key{Kind: tui.KeyRight}) {
		t.Fatal("recall browsed away the user's half-written line")
	}
	if got := i.ed.Value(); got != "half a thought" {
		t.Fatalf("composer = %q, want the user's own text untouched", got)
	}
}
