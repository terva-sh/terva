package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// notifyHooks records Notify calls so a test can assert the user was told
// something, rather than only that the driver kept waiting.
type notifyHooks struct {
	stubHooks
	mu   sync.Mutex
	msgs []string
}

func (n *notifyHooks) Notify(extName, level, message string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.msgs = append(n.msgs, extName+": "+message)
}

func (n *notifyHooks) seen() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.msgs...)
}

// writeBootstrappingExt writes an extension whose launcher reports progress
// with `bootstrap` frames for longer than the hello timeout before finally
// saying hello — the shape of a launcher that compiles first. reports is how
// many progress frames it emits; sleep is how long it pauses between them.
func writeBootstrappingExt(t *testing.T, home, name, cmd string, reports int, sleep string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script extension; skip on windows")
	}
	dir := filepath.Join(home, "extensions", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	hello := fmt.Sprintf(`{"type":"hello","name":"%s","version":"1.0","capabilities":["commands"]}`, name)
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	for i := 0; i < reports; i++ {
		fmt.Fprintf(&b, "sleep %s\n", sleep)
		fmt.Fprintf(&b, "printf '%%s\\n' '{\"type\":\"bootstrap\",\"message\":\"compiling step %d\"}'\n", i+1)
	}
	b.WriteString("printf '%s\\n' '" + hello + "'\n")
	b.WriteString("printf '%s\\n' '{\"type\":\"register_command\",\"name\":\"" + cmd + "\"}'\n")
	b.WriteString("printf '%s\\n' '{\"type\":\"ready\"}'\n")
	b.WriteString("while IFS= read -r line; do case \"$line\" in *'\"type\":\"shutdown\"'*) exit 0;; esac; done\n")
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(b.String()), 0o755); err != nil {
		t.Fatal(err)
	}
	mfb, _ := json.Marshal(map[string]any{"name": name, "exec": "./run.sh"})
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), mfb, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestBootstrapFramesBuyTimeAndAreReported is the liveness/bootstrap split.
// A launcher that has to build before it can exec the protocol speaker cannot
// answer "are you alive?" — the thing that would answer is the artifact being
// built. Emitting `bootstrap` frames turns the deadline into a measure of
// SILENCE, so a build that takes several times the hello timeout still loads.
//
// The timings are what make it a real test, and the ratio between them is what
// keeps it honest on a loaded machine: twelve ~0.3s steps against a 3-second
// deadline means the TOTAL (~3.6s) exceeds the deadline while every individual
// gap sits a full order of magnitude under it. So a CI box that stretches each
// sleep threefold still passes, and the only way to fail is for the deadline to
// be measuring elapsed time rather than silence — which is exactly the
// behaviour before this change, where the extension is killed at 3s and
// builtcmd never registers.
func TestBootstrapFramesBuyTimeAndAreReported(t *testing.T) {
	home := testsupport.TempDir(t)
	writeBootstrappingExt(t, home, "builder", "builtcmd", 12, "0.3")

	hooks := &notifyHooks{}
	mgr := New(home, "", "0.0.0-test", "anthropic", "claude-opus-4-7", hooks)
	mgr.SetHelloTimeout(3 * time.Second)
	defer mgr.Stop(2 * time.Second)

	if errs := mgr.Discover(context.Background()); len(errs) > 0 {
		t.Fatalf("discover: %v", errs)
	}
	mgr.WaitForReady(testsupport.ExtReadyGrace)

	if !mgr.HasCommand("builtcmd") {
		t.Fatal("a launcher reporting progress was killed anyway — the deadline is still measuring elapsed time, not silence")
	}
	// Buying time silently would be its own failure: a two-minute build that
	// looks identical to a hang is the problem this is meant to solve.
	msgs := hooks.seen()
	if len(msgs) == 0 {
		t.Fatal("no bootstrap progress was reported to the host; a slow build stays indistinguishable from a hang")
	}
	if !strings.Contains(msgs[0], "compiling step 1") {
		t.Errorf("first report = %q, want the launcher's own message", msgs[0])
	}
}

// TestSilenceStillEndsTheHandshake is the other half: bootstrap frames must
// buy time only while progress keeps arriving. An extension that goes quiet
// after reporting once is as dead as one that never spoke, and must still be
// killed on schedule rather than holding a slot on the strength of an old
// promise.
func TestSilenceStillEndsTheHandshake(t *testing.T) {
	home := testsupport.TempDir(t)
	// One report, then a pause far longer than the deadline before hello.
	writeBootstrappingExt(t, home, "stalled", "stalledcmd", 1, "0")
	dir := filepath.Join(home, "extensions", "stalled")
	script, err := os.ReadFile(filepath.Join(dir, "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	stalled := strings.Replace(string(script), "printf '%s\\n' '{\"type\":\"hello\"",
		"sleep 30\nprintf '%s\\n' '{\"type\":\"hello\"", 1)
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(stalled), 0o755); err != nil {
		t.Fatal(err)
	}

	mgr := New(home, "", "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	mgr.SetHelloTimeout(2 * time.Second)
	defer mgr.Stop(2 * time.Second)

	start := time.Now()
	errs := mgr.Discover(context.Background())
	elapsed := time.Since(start)

	if len(errs) == 0 {
		t.Fatal("an extension that reported once and then went silent was not skipped")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("Discover took %s; one bootstrap frame must not license an unbounded wait", elapsed)
	}
	if mgr.HasCommand("stalledcmd") {
		t.Error("the stalled extension registered anyway")
	}
}

// TestFailedLoadStaysVisible: Load rolls a failed extension's name claim back
// so the name stays retryable — which also erased it from every map the host
// reports on, leaving `terva ext doctor` able to say only "not loaded" while
// the reason sat in a log file. The verdict has to outlive the rollback.
func TestFailedLoadStaysVisible(t *testing.T) {
	home := testsupport.TempDir(t)
	dir := filepath.Join(home, "extensions", "silent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("shell-script extension; skip on windows")
	}
	// Opens, never speaks, stays alive — the daemon/REPL/typo'd-path case.
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\ncat >/dev/null\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	mfb, _ := json.Marshal(map[string]any{"name": "silent", "exec": "./run.sh"})
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), mfb, 0o644); err != nil {
		t.Fatal(err)
	}

	mgr := New(home, "", "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	mgr.SetHelloTimeout(time.Second)
	defer mgr.Stop(2 * time.Second)
	if errs := mgr.Discover(context.Background()); len(errs) == 0 {
		t.Fatal("a never-handshaking extension should have reported an error")
	}

	var found bool
	for _, d := range mgr.Diagnostics() {
		if d.Name != "silent" {
			continue
		}
		found = true
		if d.FailedReason == "" {
			t.Error("the diagnostic carries no reason — the user is back to reading log files")
		}
		if d.Dir != dir {
			t.Errorf("diagnostic Dir = %q, want %q so the doctor can match it to an installed extension", d.Dir, dir)
		}
	}
	if !found {
		t.Error("a failed extension vanished from Diagnostics entirely; `ext doctor` can only report its absence")
	}
	if fs := mgr.Failures(); len(fs) != 1 || fs[0].Name != "silent" {
		t.Errorf("Failures() = %+v, want the one failed load", fs)
	}
}
