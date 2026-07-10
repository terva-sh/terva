package modes

import (
	"testing"
	"time"
)

// /settings → auto-swarm routes to the daemon's settings surface, which
// persists the flag and re-derives every session's tool set and system prompt
// (rebuildTools → injectExtraTools reads config.AutoSwarmEnabled()).
//
// The TUI used to do the live half itself: snapshot the tool registry, strip
// swarm_spawn, rebuild a fresh SwarmSpawnTool bound to its own dispatcher, and
// SetTools — plus a string append/trim on agent.System. That whole class of
// patching is now impossible: the TUI holds no agent (plan 4.1), so dispatching
// the surface action is all it can do. Derived state is re-derived daemon-side,
// never patched.
func TestAutoSwarmToggleRoutesToDaemon(t *testing.T) {
	fc := newFakeCarrier()

	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = fc
	i.cfg.CarrierSession = "s1"

	i.applySettingToggle("auto_swarm_enabled", true)

	select {
	case act := <-fc.surfActs:
		if act.id != "settings" || act.action != "set" {
			t.Fatalf("acted on %q/%q, want settings/set", act.id, act.action)
		}
		if act.args["key"] != "auto_swarm" || act.args["value"] != "true" {
			t.Fatalf("args = %v, want key=auto_swarm value=true", act.args)
		}
	case <-time.After(time.Second):
		t.Fatal("no SurfaceAction dispatched — the toggle never reached the daemon")
	}

	if i.cfg.AutoSwarmEnabled == nil || !*i.cfg.AutoSwarmEnabled {
		t.Fatal("the dialog's local checkbox did not follow")
	}
}

// Without a workspace bound there is no daemon to tell: the local checkbox
// follows (it is just this dialog's UI state), but no surface action goes out.
func TestAutoSwarmToggleWithoutCarrierDispatchesNothing(t *testing.T) {
	fc := newFakeCarrier()
	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = nil // the carrier the fake would have received the action on

	i.applySettingToggle("auto_swarm_enabled", true)

	select {
	case act := <-fc.surfActs:
		t.Fatalf("a carrier-less toggle dispatched a surface action: %+v", act)
	default:
	}
	if i.cfg.AutoSwarmEnabled == nil || !*i.cfg.AutoSwarmEnabled {
		t.Fatal("the local checkbox should still follow the toggle")
	}
}
