package modes

import (
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/tui"
)

// newInteractiveForSwapTest builds the minimal Interactive that swapModel
// touches, bound to a fake carrier.
func newInteractiveForSwapTest(c *fakeCarrier) *Interactive {
	return &Interactive{
		view:  &tui.View{},
		turns: newTurnEngine(),
		dirty: make(chan struct{}, 1),
		cfg: InteractiveConfig{
			Carrier:        c,
			CarrierSession: "s1",
			Provider:       "openai-compatible",
			Model:          "edge-a",
		},
	}
}

// The /model picker's only job is to drive the service's SwitchModel verb — the
// workspace owns the rebuild-vs-reuse decision (same provider+endpoint swaps the
// model in place; a different baseUrl rebuilds the endpoint-bound client, the
// wrong-backend regression covered by TestSwitchReusesClient in packages/agent).
// The TUI then reads the authoritative post-swap identity back off ResumeSession
// rather than racing the pump's session_updated handling.
func TestApplyModelSelectionDrivesSwitchModel(t *testing.T) {
	c := newFakeCarrier()
	c.infos = map[string]ctrlproto.SessionInfo{
		"s1": {ID: "s1", Provider: "openai-compatible", Model: "edge-b"},
	}
	i := newInteractiveForSwapTest(c)

	i.applyModelSelection("openai-compatible", "edge-b")

	select {
	case got := <-c.switches:
		if got != [2]string{"openai-compatible", "edge-b"} {
			t.Fatalf("SwitchModel called with %v, want openai-compatible/edge-b", got)
		}
	default:
		t.Fatal("applyModelSelection did not call SwitchModel")
	}
	if i.cfg.Model != "edge-b" {
		t.Errorf("cfg.Model = %q, want edge-b (read back from ResumeSession)", i.cfg.Model)
	}
}

// An empty model id is a no-op: no wire call at all.
func TestApplyModelSelectionIgnoresEmptyModel(t *testing.T) {
	c := newFakeCarrier()
	i := newInteractiveForSwapTest(c)

	i.applyModelSelection("openai-compatible", "")

	select {
	case got := <-c.switches:
		t.Fatalf("an empty model must not switch; got %v", got)
	default:
	}
}

// The rescue picker routes through the same verb; the workspace's rebuild is
// what drops launch-time --api-key / --base-url overrides.
func TestApplyRescueModelSelectionDrivesSwitchModel(t *testing.T) {
	c := newFakeCarrier()
	i := newInteractiveForSwapTest(c)

	i.applyRescueModelSelection("anthropic", "claude-x")

	select {
	case got := <-c.switches:
		if got != [2]string{"anthropic", "claude-x"} {
			t.Fatalf("rescue SwitchModel called with %v, want anthropic/claude-x", got)
		}
	default:
		t.Fatal("applyRescueModelSelection did not call SwitchModel")
	}
}
