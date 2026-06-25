package extdriver

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// recordingHooks captures Notify messages so a test can prove the read
// loop kept processing frames after a malformed one. All other callbacks
// are the embedded no-ops.
type recordingHooks struct {
	stubHooks
	mu       sync.Mutex
	notifies []string
}

func (r *recordingHooks) Notify(_, _, msg string) {
	r.mu.Lock()
	r.notifies = append(r.notifies, msg)
	r.mu.Unlock()
}

func (r *recordingHooks) sawNotify(sub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, n := range r.notifies {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

// writeShellExt writes a minimal /bin/sh extension that prints helloJSON
// then runs body, and returns its dir. Driver.Load takes the manifest
// directly, so no extension.json is needed at this layer.
func writeShellExt(t *testing.T, dir, helloJSON, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script extension; skip on windows")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' '" + helloJSON + "'\n" + body
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestOnMalformedFrameFiresAndReadLoopSurvives drives a stray non-JSON
// line and a typed-but-unknown frame through the read loop. The observer
// must see both (with distinguishing reasons and the raw line), and a
// valid notify emitted afterwards must still arrive — proving the loop
// kept reading rather than wedging on the bad frames.
func TestOnMalformedFrameFiresAndReadLoopSurvives(t *testing.T) {
	tmp := testsupport.TempDir(t)
	dir := filepath.Join(tmp, "noisy")
	hello := `{"type":"hello","name":"noisy","version":"1.0","capabilities":["events"]}`
	body := `printf '%s\n' '{"type":"ready"}'
printf '%s\n' 'this is not json at all'
printf '%s\n' '{"type":"bogus"}'
printf '%s\n' '{"type":"notify","level":"info","message":"STILL-ALIVE"}'
while IFS= read -r line; do
  case "$line" in *'"type":"shutdown"'*) exit 0;; esac
done
`
	writeShellExt(t, dir, hello, body)

	type hit struct{ ext, raw, reason string }
	var mu sync.Mutex
	var hits []hit

	hooks := &recordingHooks{}
	d := New(tmp, "", "0.0.0-test", "anthropic", "opus", hooks)
	d.SetOnMalformedFrame(func(ext, raw, reason string) {
		mu.Lock()
		hits = append(hits, hit{ext, raw, reason})
		mu.Unlock()
	})

	if err := d.Load(context.Background(), dir, Manifest{Name: "noisy", Exec: "./run.sh"}); err != nil {
		t.Fatalf("load: %v", err)
	}
	defer d.Stop(2 * time.Second)

	// Wait until the read loop has processed all three trailing frames
	// (the notify is last, so seeing it means the two malformed frames
	// were already handled).
	deadline := time.Now().Add(3 * time.Second)
	for !hooks.sawNotify("STILL-ALIVE") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !hooks.sawNotify("STILL-ALIVE") {
		t.Fatal("notify after the malformed frames never arrived — read loop did not survive")
	}

	mu.Lock()
	defer mu.Unlock()
	var sawInvalidJSON, sawUnknownType bool
	for _, h := range hits {
		if h.ext != "noisy" {
			t.Errorf("hit attributed to %q, want \"noisy\"", h.ext)
		}
		switch {
		case strings.HasPrefix(h.reason, "invalid json"):
			sawInvalidJSON = true
			if h.raw != "this is not json at all" {
				t.Errorf("invalid-json raw = %q, want the stray line verbatim", h.raw)
			}
		case strings.Contains(h.reason, `unknown frame type "bogus"`):
			sawUnknownType = true
			if h.raw != `{"type":"bogus"}` {
				t.Errorf("unknown-type raw = %q, want the frame verbatim", h.raw)
			}
		}
	}
	if !sawInvalidJSON {
		t.Errorf("OnMalformedFrame never fired for the stray non-JSON line; hits=%+v", hits)
	}
	if !sawUnknownType {
		t.Errorf("OnMalformedFrame never fired for the unknown frame type; hits=%+v", hits)
	}
}

// TestOnMalformedFrameUnsetIsNoop confirms the live host's default —
// no observer — leaves the read loop logging-and-swallowing as before:
// the malformed frames don't wedge it and a later notify still arrives.
func TestOnMalformedFrameUnsetIsNoop(t *testing.T) {
	tmp := testsupport.TempDir(t)
	dir := filepath.Join(tmp, "quiet")
	hello := `{"type":"hello","name":"quiet","version":"1.0","capabilities":["events"]}`
	body := `printf '%s\n' '{"type":"ready"}'
printf '%s\n' 'stray line'
printf '%s\n' '{"type":"notify","level":"info","message":"OK"}'
while IFS= read -r line; do
  case "$line" in *'"type":"shutdown"'*) exit 0;; esac
done
`
	writeShellExt(t, dir, hello, body)

	hooks := &recordingHooks{}
	d := New(tmp, "", "0.0.0-test", "anthropic", "opus", hooks)
	// No SetOnMalformedFrame — exercises the nil path.
	if err := d.Load(context.Background(), dir, Manifest{Name: "quiet", Exec: "./run.sh"}); err != nil {
		t.Fatalf("load: %v", err)
	}
	defer d.Stop(2 * time.Second)

	deadline := time.Now().Add(3 * time.Second)
	for !hooks.sawNotify("OK") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !hooks.sawNotify("OK") {
		t.Fatal("read loop did not survive a stray line with no malformed-frame observer set")
	}
}
