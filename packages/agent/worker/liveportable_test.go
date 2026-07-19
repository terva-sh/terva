package worker

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// TestLiveTervaPortableApprovalBridge proves the terva:portable approval carrier
// end to end with the REAL parts: a real `terva rpc --portable --approval-socket`
// worker spawns the REAL `terva mcp-approval-bridge` as its MCP server (via
// terva's own client), routes its confirm gate through it, and the request
// relays over the runner's socket to a Confirmer here. This is the config-OPAQUE
// twin of TestLiveClaudeApprovalBridge — the SAME bridge and socket, a different
// MCP client — so the conformance parallel now covers approvals, not just
// prompts. Allow creates the file; deny blocks it.
//
// Guarded behind TERVA_LIVE_PORTABLE=1 because it SPENDS on terva's own
// subscription and needs a logged-in terva, so CI never pays it. Re-run with:
//
//	TERVA_LIVE_PORTABLE=1 go test ./packages/agent/worker -run TestLiveTervaPortableApprovalBridge -v
func TestLiveTervaPortableApprovalBridge(t *testing.T) {
	if os.Getenv("TERVA_LIVE_PORTABLE") != "1" {
		t.Skip("live spend on terva's own subscription; set TERVA_LIVE_PORTABLE=1 to run")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the approval socket is a unix socket")
	}

	// The worker IS `terva rpc`, and it spawns `terva mcp-approval-bridge` — both
	// self-exec THIS binary. os.Executable() in `go test` is the harness, so point
	// the seam at a freshly-built terva.
	tervaBin := buildTerva(t)
	old := tervaExe
	tervaExe = func() (string, error) { return tervaBin, nil }
	defer func() { tervaExe = old }()

	backend, err := Lookup(BackendTervaPortable)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}

	t.Run("allow creates the file", func(t *testing.T) {
		runLivePortableApproval(t, backend, core.ConfirmDecision{Allow: true}, true)
	})
	t.Run("deny blocks the file", func(t *testing.T) {
		runLivePortableApproval(t, backend, core.ConfirmDecision{Allow: false, Reason: "the human said no"}, false)
	})
}

func runLivePortableApproval(t *testing.T, backend Backend, decision core.ConfirmDecision, wantFile bool) {
	repo := testsupport.TempDir(t)
	// An "ask" posture gates every tool call, so the worker's write must consult
	// the bridge. The child terva rpc self-resolves its provider/model from the
	// machine's configured subscription (no override).
	r, err := build.Resolve(build.Args{CWD: repo, Approval: "ask"}, false)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	rec := &recordingConfirmer{decision: decision}
	f := swarm.New(swarm.Config{
		Root:     testsupport.TempDir(t),
		RepoRoot: repo,
		NewRunner: func(a *swarm.Agent) swarm.Runner {
			return NewRunner(a, backend, r, rec)
		},
	})
	defer f.StopAll()

	a, err := f.Spawn(context.Background(),
		"Use the write tool to create a file named approved.txt whose contents are exactly OK. Do nothing else.")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	t.Logf("spawned %s in %s", a.ID, a.Dir)

	// The write needs permission -> terva's confirm gate -> the bridge -> our
	// Confirmer.
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) && rec.count() == 0 {
		time.Sleep(200 * time.Millisecond)
	}
	if rec.count() == 0 {
		t.Logf("=== TRANSCRIPT ===\n%s", strings.Join(a.Transcript(), "\n"))
		t.Fatal("the worker's tool call never reached the orchestrator's Confirmer")
	}
	tool, preview := rec.last()
	t.Logf("approval reached the human: tool=%q preview=%q", tool, preview)
	if !strings.Contains(preview, "worker "+a.ID) {
		t.Errorf("card must be labelled with the worker id, got %q", preview)
	}

	file := filepath.Join(a.Dir, "approved.txt")
	settle := time.Now().Add(40 * time.Second)
	for time.Now().Before(settle) {
		if _, err := os.Stat(file); (err == nil) == wantFile {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = f.Stop(a.ID)
	a.Wait()

	_, statErr := os.Stat(file)
	if got := statErr == nil; got != wantFile {
		t.Errorf("file present = %v, want %v (decision allow=%v)", got, wantFile, decision.Allow)
	}
}
