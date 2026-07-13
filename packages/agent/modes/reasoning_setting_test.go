package modes

import (
	"errors"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/modes/dialogs"
)

// /settings → thinking level routes through the daemon's settings surface,
// which persists the level AND applies it live to every session's agent via
// the locked Agent.SetReasoning.
//
// The TUI used to do both halves itself, and its half of the second was a bare
// `ag.Reasoning = level` — an unsynchronized write to a field the turn loop
// reads under the agent's own mutex. That whole hazard is gone: the TUI holds
// no agent to write (plan 4.1), so dispatching the surface action is all it can
// do. This pins that it dispatches the right one and only updates its local
// label.
func TestReasoningSettingRoutesThroughSurfaceAction(t *testing.T) {
	fc := newFakeCarrier()

	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = fc
	i.cfg.CarrierSession = "s1"

	i.applySettingChange(dialogs.SettingsAction{Toggle: true, Enum: true, Key: "reasoning", StringValue: "high"})

	select {
	case act := <-fc.surfActs:
		if act.id != "settings" || act.action != "set" {
			t.Fatalf("acted on %q/%q, want settings/set", act.id, act.action)
		}
		if act.args["key"] != "reasoning" || act.args["value"] != "high" {
			t.Fatalf("args = %v, want key=reasoning value=high", act.args)
		}
	case <-time.After(time.Second):
		t.Fatal("no SurfaceAction dispatched — the level never reached the daemon")
	}

	if i.cfg.Reasoning != "high" {
		t.Fatalf("local label = %q, want high", i.cfg.Reasoning)
	}
}

// A failed round-trip must not claim success on the status line.
func TestReasoningSettingSurfacesDaemonError(t *testing.T) {
	fc := newFakeCarrier()
	fc.surfActErr = errors.New("daemon said no")

	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = fc
	i.cfg.CarrierSession = "s1"
	i.cfg.Reasoning = "low"

	i.applySettingChange(dialogs.SettingsAction{Toggle: true, Enum: true, Key: "reasoning", StringValue: "high"})
	<-fc.surfActs

	i.mu.Lock()
	gotErr, gotOK, level := i.statusErr, i.statusOK, i.cfg.Reasoning
	i.mu.Unlock()

	if gotErr == "" {
		t.Fatal("a rejected setting reported no error")
	}
	if gotOK != "" {
		t.Fatalf("a rejected setting reported success: %q", gotOK)
	}
	if level != "low" {
		t.Fatalf("a rejected setting still moved the local label to %q", level)
	}
}
