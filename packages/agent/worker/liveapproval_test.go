package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// TestLiveClaudeApprovalBridge is the whole approval chain proven end to end with
// the REAL parts: a real `claude` worker, spawning the REAL `terva
// mcp-approval-bridge` as its --permission-prompt-tool, relaying over the
// runner's real socket to a Confirmer here. It drives one Write that Claude
// gates (verified: default mode consults the tool for file edits): the request
// reaches the Confirmer labelled with the worker id, and the verdict decides
// whether the file appears in the lease — allow creates it, deny blocks it (the
// safety-critical direction).
//
// Guarded behind TERVA_LIVE_CLAUDE=1 because it SPENDS MONEY (two haiku turns)
// and needs a logged-in `claude` on PATH, so CI never pays it. Re-run with:
//
//	TERVA_LIVE_CLAUDE=1 go test ./packages/agent/worker -run TestLiveClaudeApprovalBridge -v
func TestLiveClaudeApprovalBridge(t *testing.T) {
	if os.Getenv("TERVA_LIVE_CLAUDE") != "1" {
		t.Skip("live spend; set TERVA_LIVE_CLAUDE=1 to run")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the approval socket is a unix socket")
	}

	// Build a real terva so `terva mcp-approval-bridge` exists when claude spawns
	// it — os.Executable() inside `go test` is the test harness, which has no
	// subcommands. Point the backend's bridge command at it.
	tervaBin := buildTerva(t)
	old := tervaExe
	tervaExe = func() (string, error) { return tervaBin, nil }
	defer func() { tervaExe = old }()

	backend, err := Lookup(BackendClaude)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}

	t.Run("allow creates the file", func(t *testing.T) {
		runLiveApproval(t, backend, core.ConfirmDecision{Allow: true}, true)
	})
	t.Run("deny blocks the file", func(t *testing.T) {
		runLiveApproval(t, backend, core.ConfirmDecision{Allow: false, Reason: "the human said no"}, false)
	})
}

func runLiveApproval(t *testing.T, backend Backend, decision core.ConfirmDecision, wantFile bool) {
	repo := testsupport.TempDir(t)
	// An "ask" posture is what makes the bridge load-bearing: it maps to Claude's
	// default permission mode, where every non-safe tool call is delegated to the
	// permission tool. (A default resolve comes out yolo -> bypassPermissions, and
	// the tool is never consulted — a real dispatcher would inherit its own ask
	// posture to the worker.)
	r, err := build.Resolve(build.Args{CWD: repo, Approval: "ask"}, false)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	r.Model = "claude-haiku-4-5" // cheapest

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
		"Use the Write tool to create a file named approved.txt whose contents are exactly OK. Do nothing else.")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	t.Logf("spawned %s in %s", a.ID, a.Dir)

	// The Write needs permission -> bridge -> our Confirmer.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && rec.count() == 0 {
		time.Sleep(200 * time.Millisecond)
	}
	if rec.count() == 0 {
		t.Logf("=== TRANSCRIPT ===\n%s", strings.Join(a.Transcript(), "\n"))
		if raw, rerr := os.ReadFile(filepath.Join(filepath.Dir(a.EventLogPath), "raw.jsonl")); rerr == nil {
			t.Logf("=== raw.jsonl (%d bytes) ===\n%s", len(raw), string(raw))
		} else {
			t.Logf("no raw.jsonl: %v", rerr)
		}
		t.Fatal("the worker's Write never reached the orchestrator's Confirmer")
	}
	tool, preview := rec.last()
	t.Logf("approval reached the human: tool=%q preview=%q", tool, preview)
	if tool != "Write" {
		t.Errorf("gated tool = %q, want Write", tool)
	}
	if !strings.Contains(preview, "worker "+a.ID) {
		t.Errorf("card must be labelled with the worker id, got %q", preview)
	}

	// Give the verdict time to flow back and the turn to settle.
	file := filepath.Join(a.Dir, "approved.txt")
	settle := time.Now().Add(30 * time.Second)
	for time.Now().Before(settle) {
		if _, err := os.Stat(file); (err == nil) == wantFile && a.Status() != swarm.StatusRunning {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	_ = f.Stop(a.ID)
	a.Wait()

	_, statErr := os.Stat(file)
	got := statErr == nil
	if got != wantFile {
		t.Errorf("file present = %v, want %v (decision allow=%v)", got, wantFile, decision.Allow)
	}

	// The real claude result envelope carried total_cost_usd; it should have
	// ridden the translated task_end onto the snapshot, whatever the verdict.
	if cost := a.Snapshot().CostUSD; cost <= 0 {
		t.Errorf("worker cost not recorded on the snapshot: %v", cost)
	} else {
		t.Logf("worker cost on snapshot: $%.6f", cost)
	}
}

// recordingConfirmer records every approval it is asked and answers with a fixed
// decision — the test's stand-in for the human's card.
type recordingConfirmer struct {
	decision core.ConfirmDecision

	mu    sync.Mutex
	calls []struct{ tool, preview string }
}

func (c *recordingConfirmer) Confirm(tool, preview string) core.ConfirmDecision {
	c.mu.Lock()
	c.calls = append(c.calls, struct{ tool, preview string }{tool, preview})
	d := c.decision
	c.mu.Unlock()
	return d
}

func (c *recordingConfirmer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

func (c *recordingConfirmer) last() (string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.calls) == 0 {
		return "", ""
	}
	x := c.calls[len(c.calls)-1]
	return x.tool, x.preview
}

func buildTerva(t *testing.T) string {
	t.Helper()
	out := filepath.Join(testsupport.TempDir(t), "terva")
	cmd := exec.Command("go", "build", "-o", out, "terva.sh/terva/cmd/terva")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build terva: %v\n%s", err, b)
	}
	return out
}
