package extensions

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestConcurrentLoadSameNameClaimsOnce: many concurrent loads of the same
// extension name must yield exactly one tracked subprocess (the atomic
// name-claim in Driver.Load), with no orphaned process that later reports
// a crash. Run under -race this also guards the d.ext access.
func TestConcurrentLoadSameNameClaimsOnce(t *testing.T) {
	tmp := t.TempDir()
	writeMockExtension(t, filepath.Join(tmp, "extensions"))
	hooks := &stubHooks{}
	mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-test", hooks)
	defer mgr.Stop(200 * time.Millisecond)

	mf := Manifest{Name: "mock", Exec: "./run.sh"}
	dir := filepath.Join(tmp, "extensions", "mock")

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = mgr.Load(context.Background(), dir, mf)
		}()
	}
	wg.Wait()

	if c := mgr.Count(); c != 1 {
		t.Fatalf("concurrent same-name loads must claim once; tracked count = %d", c)
	}
	mgr.WaitForReady(time.Second)
	if crashNoticed(hooks) {
		t.Error("a concurrent-load orphan must not surface 'exited unexpectedly'")
	}
}
