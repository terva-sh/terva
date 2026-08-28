package modes

// The "continue on X/Y for this session" row, end to end through the real Run
// loop. The unit tests either side of this seam prove the dialog RETURNS the
// offer and that the workspace can CREATE a session from the pair; what only a
// booted TUI shows is that a keystroke actually connects them — the row is
// rendered, reachable, and its verb reaches the host hook.

import (
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// bootWithOffer starts a credential-less TUI carrying a provider-switch offer,
// exactly as cli_ctrlproto assembles one when boot finds the pinned provider's
// login lapsed and another provider usable.
func bootWithOffer(t *testing.T, record func(providerID, model string)) *harness {
	t.Helper()
	return startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = false // no session: the pinned provider is dead
		cfg.Provider, cfg.Model = "anthropic", "claude-opus-5"
		cfg.LoginNotice = "anthropic login expired"
		cfg.ProviderSwitchOffer = ProviderOffer{Provider: "openai", Model: "gpt-5"}
		cfg.CarrierUseProvider = func(providerID, model string) (ctrlproto.SessionInfo, error) {
			record(providerID, model)
			return ctrlproto.SessionInfo{ID: "switched", Provider: providerID, Model: model}, nil
		}
	})
}

// The offer is on screen at boot, under a status line naming the lapsed pin.
func TestBootOffersTheWorkingProviderBesideTheLapsedPin(t *testing.T) {
	h := bootWithOffer(t, func(string, string) {})
	h.waitText("anthropic login expired")
	h.waitText("continue on")
	h.waitText("gpt-5")
}

// Down, down, enter — and the host hook runs with the offered pair.
//
// This is the assertion the seam needed: the dialog's UseOffer verb was new,
// and nothing but this connects it to CarrierUseProvider. Wire it to the wrong
// field (Provider vs Model) or forget the overlay branch entirely and every
// other test in this change still passes.
func TestTakingTheOfferBindsASessionOnThatPair(t *testing.T) {
	var mu sync.Mutex
	var gotProvider, gotModel string
	h := bootWithOffer(t, func(providerID, model string) {
		mu.Lock()
		gotProvider, gotModel = providerID, model
		mu.Unlock()
	})
	h.waitText("continue on")

	h.term.Type("\x1b[B") // down
	h.term.Type("\x1b[B") // down
	h.term.Type("\r")     // enter

	// The session is bound, which is what reopens the prompt.
	h.waitText("this session is on")
	mu.Lock()
	defer mu.Unlock()
	if gotProvider != "openai" || gotModel != "gpt-5" {
		t.Fatalf("CarrierUseProvider got %q/%q, want openai/gpt-5", gotProvider, gotModel)
	}
}

// Taking the offer must say the pin is still the default. "continue for this
// session" is a promise about scope, and the confirmation is where the user
// finds out whether it was kept.
func TestTakingTheOfferSaysThePinIsStillTheDefault(t *testing.T) {
	h := bootWithOffer(t, func(string, string) {})
	h.waitText("continue on")
	h.term.Type("\x1b[B")
	h.term.Type("\x1b[B")
	h.term.Type("\r")
	h.waitText("anthropic is still your default")
}

// No offer, no row — and the cursor must not be able to reach a third line.
// Down-down-enter lands on "subscription" and opens the provider picker, which
// is the pre-existing behaviour for a credential-less boot.
func TestWithoutAnOfferTheMethodPickerIsUnchanged(t *testing.T) {
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = false
		cfg.CarrierUseProvider = func(string, string) (ctrlproto.SessionInfo, error) {
			t.Error("CarrierUseProvider ran with no offer configured")
			return ctrlproto.SessionInfo{}, nil
		}
	})
	h.waitText("choose login method")
	if got := h.term.Screen().Text(); strings.Contains(got, "continue on") {
		t.Fatalf("offer row rendered with no offer configured; screen:\n%s", got)
	}
	h.term.Type("\x1b[B")
	h.term.Type("\x1b[B")
	h.term.Type("\r")
	h.waitText("pick a provider")
}
