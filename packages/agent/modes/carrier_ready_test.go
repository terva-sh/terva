package modes

import (
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/tui"
)

// The prompt gate is declared by the host, not inferred. Two plausible
// inferences are both wrong, and this is the one that would ship a bug:
// a replay carrier binds a CarrierSession with no agent and no way to
// prompt, so "CarrierSession != \"\"" would hand it a working prompt gate.
func TestReadyIsNotInferredFromSessionID(t *testing.T) {
	i := NewInteractive(InteractiveConfig{
		Theme:          tui.Dark,
		Terminal:       nil,
		Carrier:        newFakeCarrier(),
		CarrierSession: "replay-1", // bound...
		Ready:          false,      // ...but read-only
	})
	if i.ready() {
		t.Fatal("a replay binding opened the prompt gate")
	}
}

// The prompt gate is a plain flag the host sets, not a property read off any
// agent — the TUI holds none (plan 4.1). setReady moves it in both directions.
func TestReadyIsAPlainGate(t *testing.T) {
	i := NewInteractive(InteractiveConfig{
		Theme:   tui.Dark,
		Carrier: newFakeCarrier(),
		Ready:   false,
	})
	if i.ready() {
		t.Fatal("gate opened without setReady")
	}
	i.setReady(true)
	if !i.ready() {
		t.Fatal("setReady(true) did not open the gate")
	}
	i.setReady(false)
	if i.ready() {
		t.Fatal("setReady(false) did not close the gate")
	}
}

// Binding to a session that resolves reopens the gate, even if a /logout
// had closed it on the previous binding.
func TestSwitchCarrierSessionOpensTheGate(t *testing.T) {
	fc := newFakeCarrier()
	fc.infos = map[string]ctrlproto.SessionInfo{"s2": {ID: "s2"}}

	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = fc
	i.cfg.CarrierSession = "s1"
	i.setReady(false) // as if /logout had closed it

	if err := i.SwitchCarrierSession("s2"); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if !i.ready() {
		t.Fatal("binding a resolved session left the prompt gate shut")
	}
}
