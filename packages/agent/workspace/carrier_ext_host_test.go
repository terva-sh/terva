package workspace

import (
	"context"
	"testing"
	"time"
)

// TestCarrierExtHostNilSafe pins the adapter's degrade-to-zero contract: before
// login and mid-session-switch there is no resolvable session, so every read
// must return a zero value (never panic) and Invoke must error rather than run
// a command against a session that doesn't exist.
func TestCarrierExtHostNilSafe(t *testing.T) {
	hosts := []CarrierExtHost{
		{}, // nil w, nil sess
		{sess: func() string { return "missing" }},                                                   // nil w
		{w: &Workspace{sessions: map[string]*wsSession{}}, sess: func() string { return "missing" }}, // no such session
	}
	for i, c := range hosts {
		if c.HasCommand("foo") {
			t.Errorf("host %d: HasCommand should be false with no session", i)
		}
		if got := c.CommandOwner("foo"); got != "" {
			t.Errorf("host %d: CommandOwner = %q, want empty", i, got)
		}
		if got := c.Commands(); got != nil {
			t.Errorf("host %d: Commands = %v, want nil", i, got)
		}
		if got := c.StatusSegments(); got != nil {
			t.Errorf("host %d: StatusSegments = %v, want nil", i, got)
		}
		if got := c.ContextSnapshot(); got != nil {
			t.Errorf("host %d: ContextSnapshot = %v, want nil", i, got)
		}
		if err := c.SendPanelKey("e", "p", "k", "t"); err != nil {
			t.Errorf("host %d: SendPanelKey err = %v, want nil", i, err)
		}
		if err := c.SendPanelClose("e", "p"); err != nil {
			t.Errorf("host %d: SendPanelClose err = %v, want nil", i, err)
		}
		_ = c.Reload(context.Background(), 0) // must not panic
		if _, err := c.Invoke(context.Background(), "foo", "", time.Second); err == nil {
			t.Errorf("host %d: Invoke with no session should error", i)
		}
	}
}
