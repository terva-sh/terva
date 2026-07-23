package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// writeGatedExtAt writes a shell extension that says nothing at all — not even
// hello — until `gate` exists on disk, then registers one command and goes
// ready. The gate makes "the extension has not handshaked yet" a fact the test
// controls rather than a race it has to guess at.
func writeGatedExtAt(t *testing.T, home, name, cmd, gate string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script extension; skip on windows")
	}
	dir := filepath.Join(home, "extensions", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	hello := fmt.Sprintf(`{"type":"hello","name":"%s","version":"1.0","capabilities":["commands"]}`, name)
	script := "#!/bin/sh\n" +
		"while [ ! -f '" + gate + "' ]; do sleep 0.2; done\n" +
		"printf '%s\\n' '" + hello + "'\n" +
		"printf '%s\\n' '{\"type\":\"register_command\",\"name\":\"" + cmd + "\"}'\n" +
		"printf '%s\\n' '{\"type\":\"ready\"}'\n" +
		"while IFS= read -r line; do case \"$line\" in *'\"type\":\"shutdown\"'*) exit 0;; esac; done\n"
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	mfb, _ := json.Marshal(map[string]any{"name": name, "exec": "./run.sh"})
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), mfb, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestStartAsyncReturnsBeforeTheHandshake is the whole point of StartAsync: it
// hands control back to the host while the subprocess is still silent, so a
// slow extension costs startup latency instead of blocking it.
//
// The gate is what makes this a real test rather than a timing guess. The
// extension cannot say hello until the test releases it, and the test only
// releases it AFTER StartAsync has returned — so under a synchronous
// implementation StartAsync would sit in the hello handshake until
// extdriver.HelloTimeout, kill the extension, and the command below would
// never register. Reaching HasCommand at all proves the start was backgrounded.
func TestStartAsyncReturnsBeforeTheHandshake(t *testing.T) {
	home := testsupport.TempDir(t)
	gate := filepath.Join(testsupport.TempDir(t), "release")
	writeGatedExtAt(t, home, "slowstart", "slowcmd", gate)

	mgr := New(home, "", "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	defer mgr.Stop(2 * time.Second)

	mgr.StartAsync(context.Background(), nil, true, testsupport.ExtReadyGrace, func(err error) {
		t.Errorf("unexpected load error: %v", err)
	})

	// Still parked on the gate, so nothing can have signalled ready yet.
	if n := mgr.ReadyCount(); n != 0 {
		t.Fatalf("ReadyCount = %d before the gate opened; the start was not backgrounded", n)
	}

	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	mgr.AwaitStarted(context.Background())

	if !mgr.HasCommand("slowcmd") {
		t.Error("AwaitStarted returned before the extension registered — the host would start its first turn without it")
	}
}

// TestAwaitStartedIsANoOpWithoutStartAsync pins the contract that lets a host
// call AwaitStarted unconditionally: a manager started the old synchronous way
// (or never started at all) must not block on a signal that will never fire.
func TestAwaitStartedIsANoOpWithoutStartAsync(t *testing.T) {
	mgr := New(testsupport.TempDir(t), "", "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.AwaitStarted(context.Background())
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("AwaitStarted blocked on a manager that never called StartAsync")
	}
}

// TestStartAsyncCancelUnblocksAwait: a host that closes mid-start cancels the
// start ctx, and the join must come back rather than hold the shutdown open for
// the full hello timeout plus ready grace.
func TestStartAsyncCancelUnblocksAwait(t *testing.T) {
	home := testsupport.TempDir(t)
	gate := filepath.Join(testsupport.TempDir(t), "never")
	writeGatedExtAt(t, home, "neverready", "nevercmd", gate)

	mgr := New(home, "", "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	defer mgr.Stop(2 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	mgr.StartAsync(ctx, nil, true, time.Minute, nil) // a grace nobody should ever wait out
	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		mgr.AwaitStarted(context.Background())
	}()
	// The deadline discriminates: cancelling aborts the spawn immediately,
	// while an implementation that ignored ctx would sit in the handshake for
	// the full extdriver.HelloTimeout (3s) before giving up.
	select {
	case <-done:
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("cancelling the start ctx did not unblock the background start")
	}
}
