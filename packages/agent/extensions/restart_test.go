package extensions

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// RestartExtension is the crash-recovery primitive behind chat/extconn's
// restart budget: stop (or reap) + respawn from the installed manifest,
// with registrations — commands here, tools and the connector role the
// same way — re-established by the fresh process, and no crash notice
// for the deliberate teardown.
func TestRestartExtension(t *testing.T) {
	tmp := testsupport.TempDir(t)
	writeMockExtension(t, filepath.Join(tmp, "extensions"))
	hooks := &stubHooks{}
	mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-test", hooks)
	if errs := mgr.Discover(context.Background()); len(errs) > 0 {
		t.Fatalf("discover: %v", errs)
	}
	mgr.WaitForReady(time.Second)
	defer mgr.Stop(200 * time.Millisecond)

	before, ok := mgr.ExtensionByName("mock")
	if !ok {
		t.Fatal("mock should be loaded")
	}

	reloads := 0
	mgr.SetOnReload(func() { reloads++ })
	if err := mgr.RestartExtension(context.Background(), "mock"); err != nil {
		t.Fatalf("RestartExtension: %v", err)
	}

	after, ok := mgr.ExtensionByName("mock")
	if !ok {
		t.Fatal("mock should be loaded after restart")
	}
	if after == before {
		t.Error("restart should produce a FRESH extension instance")
	}
	if !after.Ready() {
		t.Error("respawned extension should be ready")
	}
	// The fresh process re-registered its command (the same path
	// re-registers tools and the connector role).
	found := false
	for _, c := range mgr.Commands() {
		if c.Name == "ping" {
			found = true
		}
	}
	if !found {
		t.Error("respawned extension's registrations should be live again")
	}
	if reloads == 0 {
		t.Error("RestartExtension should fire onReload for registry rebuilds")
	}
	if crashNoticed(hooks) {
		t.Error("a deliberate restart must NOT surface 'exited unexpectedly'")
	}
}

// A name that is no longer installed cannot respawn and says so.
func TestRestartExtensionUninstalled(t *testing.T) {
	tmp := testsupport.TempDir(t)
	hooks := &stubHooks{}
	mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-test", hooks)
	defer mgr.Stop(200 * time.Millisecond)

	err := mgr.RestartExtension(context.Background(), "ghost")
	if err == nil || !strings.Contains(err.Error(), "did not respawn") {
		t.Errorf("RestartExtension(ghost) = %v, want the did-not-respawn error", err)
	}
}
