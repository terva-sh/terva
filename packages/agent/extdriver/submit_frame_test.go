package extdriver

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// submitRecorder captures HostHooks.Submit calls; other callbacks are the
// embedded no-ops.
type submitRecorder struct {
	stubHooks
	mu      sync.Mutex
	submits []string
}

func (r *submitRecorder) Submit(text string) {
	r.mu.Lock()
	r.submits = append(r.submits, text)
	r.mu.Unlock()
}

func (r *submitRecorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.submits...)
}

// TestSpontaneousSubmit verifies an extension can queue a plain prompt via a
// submit frame outside any command response, that the text is trimmed, and
// that an empty/whitespace-only submit is dropped.
func TestSpontaneousSubmit(t *testing.T) {
	tmp := testsupport.TempDir(t)
	dir := filepath.Join(tmp, "submit-mock")
	hello := `{"type":"hello","name":"submit-mock","version":"0.1","capabilities":["submit"]}`
	body := `printf '%s\n' '{"type":"ready"}'
printf '%s\n' '{"type":"submit","text":"  explain this repository briefly  "}'
printf '%s\n' '{"type":"submit","text":"   "}'
while IFS= read -r line; do
  case "$line" in *'"type":"shutdown"'*) exit 0;; esac
done
`
	writeShellExt(t, dir, hello, body)

	hooks := &submitRecorder{}
	d := New(tmp, "", "0.0.0-test", "anthropic", "opus", hooks)
	if err := d.Load(context.Background(), dir, Manifest{Name: "submit-mock", Exec: "./run.sh"}); err != nil {
		t.Fatalf("load: %v", err)
	}
	defer d.Stop(2 * time.Second)

	deadline := time.Now().Add(3 * time.Second)
	for len(hooks.got()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	got := hooks.got()
	if len(got) != 1 {
		t.Fatalf("Submit calls = %#v, want one non-empty call (empty must be dropped)", got)
	}
	if got[0] != "explain this repository briefly" {
		t.Fatalf("Submit text = %q, want trimmed", got[0])
	}
}
