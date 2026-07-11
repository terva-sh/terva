package extensions

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

func crashNoticed(hooks *stubHooks) bool {
	hooks.mu.Lock()
	defer hooks.mu.Unlock()
	for _, n := range hooks.notifies {
		if strings.Contains(n, "exited unexpectedly") {
			return true
		}
	}
	return false
}

// StopByName tears down one extension, removes it from the loaded set,
// and — crucially — does NOT surface a crash notice (the fix: a
// deliberate stop is silent). Unknown names report false.
func TestStopByNameSilentAndSurgical(t *testing.T) {
	tmp := testsupport.TempDir(t)
	writeMockExtension(t, filepath.Join(tmp, "extensions"))
	hooks := &stubHooks{}
	mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-test", hooks)
	if errs := mgr.Discover(context.Background()); len(errs) > 0 {
		t.Fatalf("discover: %v", errs)
	}
	mgr.WaitForReady(testsupport.ExtReadyGrace)
	defer mgr.Stop(200 * time.Millisecond)

	if mgr.Count() != 1 {
		t.Fatalf("want 1 loaded, got %d", mgr.Count())
	}
	if !mgr.StopByName("mock", time.Second) {
		t.Fatal("StopByName(mock) should report it was running")
	}
	if mgr.Count() != 0 {
		t.Errorf("mock should be gone, count=%d", mgr.Count())
	}
	if crashNoticed(hooks) {
		t.Error("a deliberate stop must NOT surface 'exited unexpectedly'")
	}
	if mgr.StopByName("does-not-exist", time.Second) {
		t.Error("StopByName(unknown) should report false")
	}
}

// ApplyOne starts or stops a single extension to match `want` and fires
// onReload each time, leaving the crash channel quiet on a stop.
func TestApplyOneStartsAndStops(t *testing.T) {
	tmp := testsupport.TempDir(t)
	writeMockExtension(t, filepath.Join(tmp, "extensions"))
	hooks := &stubHooks{}
	mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-test", hooks)
	reloads := 0
	mgr.SetOnReload(func() { reloads++ })
	defer mgr.Stop(200 * time.Millisecond)

	ctx := context.Background()
	mgr.ApplyOne(ctx, "mock", true, 300*time.Millisecond)
	if mgr.Count() != 1 {
		t.Fatalf("ApplyOne(want=true) should load mock, count=%d", mgr.Count())
	}
	mgr.ApplyOne(ctx, "mock", false, time.Second)
	if mgr.Count() != 0 {
		t.Errorf("ApplyOne(want=false) should stop mock, count=%d", mgr.Count())
	}
	if reloads < 2 {
		t.Errorf("onReload should fire on each ApplyOne, got %d", reloads)
	}
	if crashNoticed(hooks) {
		t.Error("a surgical stop must NOT surface 'exited unexpectedly'")
	}
}
