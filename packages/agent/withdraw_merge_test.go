package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/extdriver"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// withdrawHookRecorder counts RefreshTools so the test can wait until the
// withdrawal frame has actually been processed by the in-order read loop.
// Everything else is the embedded non-interactive no-op.
type withdrawHookRecorder struct {
	build.NonInteractiveExtHooks
	mu      sync.Mutex
	refresh int
}

func (r *withdrawHookRecorder) RefreshTools() {
	r.mu.Lock()
	r.refresh++
	r.mu.Unlock()
}

func (r *withdrawHookRecorder) refreshCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.refresh
}

// TestWithdrawnToolExcludedFromMergeFeed loads an extension that registers a
// tool then withdraws all of its tools, and proves the model-facing merge
// feed (extToolAdapter.Tools → MergeToolsForMode) drops it, while the raw
// manager view keeps it flagged so the /extensions dialog count is
// unaffected. This is the end-to-end guard that a withdrawn tool can never
// leak back in front of the model.
func TestWithdrawnToolExcludedFromMergeFeed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script extension; skip on windows")
	}
	tmp := testsupport.TempDir(t)
	dir := filepath.Join(tmp, "git")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
printf '%s\n' '{"type":"hello","name":"git","version":"1.0","capabilities":["tools","events"]}'
printf '%s\n' '{"type":"ready"}'
printf '%s\n' '{"type":"register_tool","name":"gitlog","schema":{"type":"object"}}'
printf '%s\n' '{"type":"set_withdrawn_tools","all":true}'
while IFS= read -r line; do case "$line" in *'"type":"shutdown"'*) exit 0;; esac; done
`
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	rec := &withdrawHookRecorder{}
	mgr := extensions.New(tmp, "", "0.0.0-test", "anthropic", "opus", rec)
	if err := mgr.Load(context.Background(), dir, extdriver.Manifest{Name: "git", Exec: "./run.sh"}); err != nil {
		t.Fatalf("load: %v", err)
	}
	defer mgr.Stop(2 * time.Second)

	// RefreshTools fires only after the withdrawal frame, which the read
	// loop processes after register_tool — so its arrival means gitlog is
	// both registered and withdrawn.
	deadline := time.Now().Add(3 * time.Second)
	for rec.refreshCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if rec.refreshCount() == 0 {
		t.Fatal("RefreshTools never fired; withdrawal frame may not have been processed")
	}

	// Raw manager view (what the /extensions dialog reads): gitlog present
	// and flagged, so the registered count stays truthful.
	var rawFound, rawFlagged bool
	for _, ti := range mgr.Tools() {
		if ti.Name == "gitlog" {
			rawFound, rawFlagged = true, ti.Withdrawn
		}
	}
	if !rawFound || !rawFlagged {
		t.Errorf("raw mgr.Tools: found=%v flagged=%v; want gitlog present AND flagged Withdrawn", rawFound, rawFlagged)
	}

	// Model-facing merge feed: gitlog must be gone.
	adapter := &build.ExtToolAdapter{Mgr: mgr}
	for _, ti := range adapter.Tools() {
		if ti.Name == "gitlog" {
			t.Error("withdrawn gitlog leaked into the merge feed (extToolAdapter.Tools)")
		}
	}

	// And therefore absent from a freshly merged registry, even in yolo
	// (a mode that would otherwise admit every tool).
	reg := core.Registry{}
	build.MergeToolsForMode(reg, core.ApprovalYolo, core.NewReadOnlySet(), adapter)
	if _, ok := reg["gitlog"]; ok {
		t.Error("withdrawn gitlog reached the merged tool registry")
	}
}
