package extensions

import (
	"context"
	"testing"
	"time"
)

// An extension named in SetDisabledExtensions is never loaded: its
// command doesn't register and it isn't in All(). The same extension
// loads normally when not disabled (control).
func TestSetDisabledExtensionsSkipsLoad(t *testing.T) {
	hello := `{"type":"hello","name":"blocked-ext","version":"1.0","capabilities":["commands"]}`
	body := `printf '%s\n' '{"type":"register_command","name":"blockedcmd"}'
printf '%s\n' '{"type":"ready"}'
while IFS= read -r line; do
  case "$line" in *'"type":"shutdown"'*) exit 0;; esac
done
`
	tmp := writeShellExt(t, "blocked-ext", hello, body)

	// Disabled: must not load.
	mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	mgr.SetDisabledExtensions([]string{"blocked-ext"})
	if errs := mgr.Discover(context.Background()); len(errs) > 0 {
		t.Fatalf("discover: %v", errs)
	}
	defer mgr.Stop(2 * time.Second)
	mgr.WaitForReady(500 * time.Millisecond)
	if mgr.HasCommand("blockedcmd") {
		t.Error("a disabled extension's command registered — it should never have loaded")
	}
	if n := len(mgr.All()); n != 0 {
		t.Errorf("disabled extension present in All(): %d", n)
	}

	// Control: the same extension loads when not disabled.
	mgr2 := New(tmp, "", "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	if errs := mgr2.Discover(context.Background()); len(errs) > 0 {
		t.Fatalf("control discover: %v", errs)
	}
	defer mgr2.Stop(2 * time.Second)
	mgr2.WaitForReady(time.Second)
	if !mgr2.HasCommand("blockedcmd") {
		t.Error("control: extension should load when not disabled")
	}
}
