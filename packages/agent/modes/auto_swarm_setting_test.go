package modes

import (
	"testing"
	"time"

	"terva.sh/terva/packages/agent/modes/dialogs"
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
// never patched. The dialog's own checkbox follows via the shared dialog widget
// (the item's Value flips in place); the config value is the daemon's to own.
func TestAutoSwarmToggleRoutesToDaemon(t *testing.T) {
	fc := newFakeCarrier()

	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = fc
	i.cfg.CarrierSession = "s1"

	i.applySettingChange(dialogs.SettingsAction{Toggle: true, Key: "auto_swarm", Value: true})

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
}

// Without a workspace bound there is no daemon to tell, so no surface action
// goes out (the dialog widget still flips its own checkbox — that is its UI
// state, not this dispatcher's job).
func TestAutoSwarmToggleWithoutCarrierDispatchesNothing(t *testing.T) {
	fc := newFakeCarrier()
	i := newCtrlprotoTestInteractive()
	i.cfg.Carrier = nil // the carrier the fake would have received the action on

	i.applySettingChange(dialogs.SettingsAction{Toggle: true, Key: "auto_swarm", Value: true})

	select {
	case act := <-fc.surfActs:
		t.Fatalf("a carrier-less toggle dispatched a surface action: %+v", act)
	default:
	}
}
