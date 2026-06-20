package extdriver

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// refreshRecorder records Notify (to sequence the test) and RefreshContext
// (the hook under test); all other callbacks are the embedded no-ops.
type refreshRecorder struct {
	stubHooks
	mu        sync.Mutex
	notifies  []string
	refreshes int
}

func (r *refreshRecorder) Notify(_, _, msg string) {
	r.mu.Lock()
	r.notifies = append(r.notifies, msg)
	r.mu.Unlock()
}

func (r *refreshRecorder) RefreshContext() {
	r.mu.Lock()
	r.refreshes++
	r.mu.Unlock()
}

func (r *refreshRecorder) sawNotify(sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, n := range r.notifies {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

func (r *refreshRecorder) refreshCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refreshes
}

// TestRefreshContextUpdatesStaticAndFiresHook drives a register_context
// then a refresh_context frame (protocol 3). The swapped block must be
// what StaticContext() now returns, and the host's RefreshContext hook
// must have fired so a live session can rebuild its prompt.
func TestRefreshContextUpdatesStaticAndFiresHook(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "memory")
	hello := `{"type":"hello","name":"memory","version":"1.0","capabilities":["context","events"]}`
	body := `printf '%s\n' '{"type":"ready"}'
printf '%s\n' '{"type":"register_context","text":"alpha notes"}'
printf '%s\n' '{"type":"refresh_context","text":"beta notes"}'
printf '%s\n' '{"type":"notify","level":"info","message":"DONE"}'
while IFS= read -r line; do case "$line" in *'"type":"shutdown"'*) exit 0;; esac; done
`
	writeShellExt(t, dir, hello, body)

	hooks := &refreshRecorder{}
	d := New(tmp, "", "0.0.0-test", "anthropic", "opus", hooks)
	if err := d.Load(context.Background(), dir, Manifest{Name: "memory", Exec: "./run.sh"}); err != nil {
		t.Fatalf("load: %v", err)
	}
	defer d.Stop(2 * time.Second)

	// The DONE notify is emitted after refresh_context, so seeing it
	// means the refresh was already processed by the in-order read loop.
	deadline := time.Now().Add(3 * time.Second)
	for !hooks.sawNotify("DONE") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !hooks.sawNotify("DONE") {
		t.Fatal("extension never reached DONE; refresh_context may not have been processed")
	}

	if got := d.StaticContext(); !strings.Contains(got, "beta notes") || strings.Contains(got, "alpha notes") {
		t.Errorf("StaticContext after refresh = %q; want the refreshed text, not the original", got)
	}
	if hooks.refreshCount() == 0 {
		t.Error("refresh_context did not fire the RefreshContext host hook")
	}
}
