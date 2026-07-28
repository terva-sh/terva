package workspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"terva.sh/terva/packages/core"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/testsupport"
)

// writeGatedToolExtension writes a shell extension that stays completely silent
// — no hello — until `gate` exists, then registers one tool and goes ready.
// The gate turns "the extension has not handshaked yet" into something the test
// controls instead of a race it has to guess at.
func writeGatedToolExtension(t *testing.T, home, gate string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script extension; skip on windows")
	}
	dir := filepath.Join(home, "extensions", "gated-tool")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"while [ ! -f '" + gate + "' ]; do sleep 0.2; done\n" +
		`printf '%s\n' '{"type":"hello","name":"gated-tool","version":"0.1","capabilities":["tools"]}'` + "\n" +
		`printf '%s\n' '{"type":"register_tool","name":"gated_tool","description":"a tool that arrives late","schema":{"type":"object"}}'` + "\n" +
		`printf '%s\n' '{"type":"ready"}'` + "\n" +
		"while IFS= read -r line; do case \"$line\" in *'\"type\":\"shutdown\"'*) exit 0;; esac; done\n"
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	mfb, _ := json.Marshal(map[string]any{"name": "gated-tool", "exec": "./run.sh"})
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), mfb, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSessionMaterializesWhileExtensionsStillHandshake pins both halves of the
// decoupling:
//
//   - CreateSession returns a live session while the extension subprocess is
//     still silent, so terva's startup no longer scales with how slow the
//     slowest extension's runtime is to boot.
//   - awaitExtensions — the barrier launchTurn takes before every turn — does
//     not come back until that extension's tools are in the agent, so the model
//     never sees a turn with a half-registered extension set.
//
// The gate makes the mutation test exact: with the old synchronous
// setupWebExtensions, CreateSession would block in the hello handshake until
// extdriver.HelloTimeout, kill the extension, and gated_tool would never
// appear — so the second assertion fails. Assertions in this order also catch
// the opposite mistake (dropping the barrier), which leaves the tool missing
// after awaitExtensions returns.
func TestSessionMaterializesWhileExtensionsStillHandshake(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("OPENAI_API_KEY", "test-key")
	gate := filepath.Join(testsupport.TempDir(t), "release")
	writeGatedToolExtension(t, home, gate)

	args := build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t), NoMCP: true}
	w, err := NewWorkspace(args, "test")
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	defer w.Close()

	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	s := w.existing(info.ID)
	if s == nil {
		t.Fatal("session not materialized")
	}
	// The extension is still parked on the gate, so the session came up without
	// waiting for it — the whole point.
	if _, ok := s.agent.LookupTool("gated_tool"); ok {
		t.Fatal("the session waited for the extension handshake before materializing")
	}

	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s.awaitExtensions(context.Background())

	if _, ok := s.agent.LookupTool("gated_tool"); !ok {
		t.Error("the turn barrier released before the extension's tools were merged — the first turn would run without them")
	}
}

// TestTurnWaitsForTheBackgroundExtensionStart pins the barrier itself: every
// turn path funnels through launchTurn, and its goroutine must join the
// background extension start before calling the provider. Without it a user who
// types fast enough races their own extensions and gets a first turn with an
// incomplete tool set — the precise failure decoupling the start would
// otherwise introduce.
//
// The turn slot is claimed before the wait, deliberately: the client shows a
// running turn rather than a request that seems to have vanished.
func TestTurnWaitsForTheBackgroundExtensionStart(t *testing.T) {
	cl := &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})}
	close(cl.release) // the provider never gates here; the extension barrier is what's under test
	s := newTurnTestSession(t, cl)
	extReady := make(chan struct{})
	s.extReady = extReady

	if err := s.prompt("hi", nil, core.UserMessageExtras{}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	// Negative window: with the barrier removed the turn reaches the provider in
	// microseconds, so 500ms is a ~1000x margin rather than a race.
	select {
	case <-cl.started:
		t.Fatal("the turn reached the provider before the extension start finished")
	case <-time.After(500 * time.Millisecond):
	}

	close(extReady)
	select {
	case <-cl.started:
	case <-time.After(10 * time.Second):
		t.Fatal("closing extReady did not release the turn")
	}
}
