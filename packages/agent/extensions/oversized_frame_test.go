package extensions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/testsupport"
)

// An over-cap tool-call argument must come back as a prompt is_error
// result (no 60s timeout) and must NOT kill the extension — a normal
// subsequent call still works.
func TestInvokeToolOversizedArgsReturnsErrorNotKill(t *testing.T) {
	stub := buildEchoStub(t)
	tmp := testsupport.TempDir(t)
	extDir := filepath.Join(tmp, "extensions", "echo")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mfb, _ := json.Marshal(map[string]any{"name": "echo", "exec": stub})
	if err := os.WriteFile(filepath.Join(extDir, "extension.json"), mfb, 0o644); err != nil {
		t.Fatal(err)
	}

	mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	if errs := mgr.Discover(context.Background()); len(errs) > 0 {
		t.Fatalf("discover: %v", errs)
	}
	defer mgr.Stop(2 * time.Second)
	mgr.WaitForReady(3 * time.Second)
	if !mgr.HasTool("bigtool") {
		t.Fatal("bigtool not registered")
	}

	// Oversized args: must return promptly with is_error. If it actually
	// sent the frame and killed the ext, we'd block until this 60s
	// timeout instead.
	big, _ := json.Marshal(map[string]string{"payload": strings.Repeat("x", extproto.MaxToolCallBytes+1)})
	start := time.Now()
	res, err := mgr.InvokeTool(context.Background(), "bigtool", json.RawMessage(big), 60*time.Second)
	if err != nil {
		t.Fatalf("InvokeTool returned a transport error: %v", err)
	}
	if !res.IsError {
		t.Errorf("oversized args should yield an is_error result, got %+v", res)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("oversized call took %s — it should be rejected immediately, not time out", elapsed)
	}

	// The extension must still be alive: a normal call succeeds.
	small, _ := json.Marshal(map[string]string{"payload": "hello"})
	res2, err := mgr.InvokeTool(context.Background(), "bigtool", json.RawMessage(small), 5*time.Second)
	if err != nil {
		t.Fatalf("extension did not survive the oversized call: %v", err)
	}
	if res2.IsError {
		t.Errorf("follow-up call errored: %+v", res2)
	}
}

// The host read loop must survive an oversized extension→host frame:
// skip it (logged), keep reading, and NOT report the extension as
// crashed. A valid frame sent AFTER the oversized one still arrives.
func TestHostReadLoopSurvivesOversizedFrame(t *testing.T) {
	hello := `{"type":"hello","name":"flooder","version":"1.0","capabilities":["events"]}`
	// After ready: emit one line larger than the read cap, then a valid
	// notify. If the read loop died on the big line, the notify never
	// arrives and a crash notice fires instead.
	body := `printf '%s\n' '{"type":"ready"}'
head -c ` + strconv.Itoa(extproto.MaxFrameBytes+1000) + ` /dev/zero | tr '\0' x
printf '\n'
printf '%s\n' '{"type":"notify","level":"info","message":"ALIVE-AFTER-OVERSIZED"}'
while IFS= read -r line; do
  case "$line" in *'"type":"shutdown"'*) exit 0;; esac
done
`
	tmp := writeShellExt(t, "flooder", hello, body)
	hooks := &stubHooks{}
	mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-opus-4-7", hooks)
	if errs := mgr.Discover(context.Background()); len(errs) > 0 {
		t.Fatalf("discover: %v", errs)
	}
	defer mgr.Stop(2 * time.Second)

	deadline := time.Now().Add(3 * time.Second)
	var sawAlive bool
	for time.Now().Before(deadline) {
		hooks.mu.Lock()
		for _, n := range hooks.notifies {
			if strings.Contains(n, "ALIVE-AFTER-OVERSIZED") {
				sawAlive = true
			}
		}
		hooks.mu.Unlock()
		if sawAlive {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawAlive {
		t.Fatal("notify after the oversized frame never arrived — read loop did not survive")
	}
	hooks.mu.Lock()
	defer hooks.mu.Unlock()
	for _, n := range hooks.notifies {
		if strings.Contains(n, "exited unexpectedly") {
			t.Errorf("oversized frame was reported as a crash: %q", n)
		}
	}
}
