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
	"terva.sh/terva/packages/testsupport"
)

// TestLiveClaudeDryRun drives the REAL claudeBackend (real `claude` CLI, real
// inference) through the real worker.Runner and a real Swarm — spawn, steer,
// stop — the smoke the fake-CLI test cannot be: it confirms `claude -p
// --input-format stream-json` actually stays alive for multi-turn stdin
// steering (verified 2.1.210: one live process answered two turns, each
// bracketed init…result, exiting cleanly on stdin EOF).
//
// Guarded behind TERVA_LIVE_CLAUDE=1 because it SPENDS MONEY and needs a
// logged-in `claude` on PATH, so it never runs in CI. Kept in the tree as the
// reproducible end-to-end proof; re-run with:
//
//	TERVA_LIVE_CLAUDE=1 go test ./packages/agent/worker -run TestLiveClaudeDryRun -v
//
// Uses haiku and tool-free trivial prompts to keep the spend to cents.
func TestLiveClaudeDryRun(t *testing.T) {
	if os.Getenv("TERVA_LIVE_CLAUDE") != "1" {
		t.Skip("live spend; set TERVA_LIVE_CLAUDE=1 to run")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the inbox is a unix socket")
	}
	repo := testsupport.TempDir(t)
	// Isolate config, and lend the real credential: claude brings its own, but
	// the terva this spawns resolves against the home it inherits.
	tervaHomeWithCredentials(t, "")
	r, err := build.Resolve(build.Args{CWD: repo}, false)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// Spend the least this subscription allows, whatever the machine's
	// configured default happens to be.
	pinWeakTier(t, &r)

	backend, err := Lookup(BackendClaude)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}

	f := swarm.New(swarm.Config{
		Root:     testsupport.TempDir(t),
		RepoRoot: repo,
		NewRunner: func(a *swarm.Agent) swarm.Runner {
			return NewRunner(a, backend, r, nil)
		},
	})
	defer f.StopAll()

	a, err := f.Spawn(context.Background(),
		"Do not use any tools. Reply with exactly the word ALPHA and nothing else.")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	t.Logf("spawned %s in %s", a.ID, a.Dir)

	waitForTranscript(t, a, "ALPHA")
	t.Log("turn 1 (opening task) round-tripped: ALPHA")

	if err := retrySend(t, f, a.ID, "Do not use any tools. Now reply with exactly the word BETA and nothing else.", 5*time.Second); err != nil {
		t.Fatalf("steer: %v", err)
	}
	waitForTranscript(t, a, "BETA")
	t.Log("turn 2 (live steer) round-tripped: BETA")

	if err := f.Stop(a.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	a.Wait()
	t.Logf("final status: %s", a.Status())

	// Durable + raw records.
	evs, _ := swarm.ReadEventLog(a.EventLogPath)
	var types []string
	for _, e := range evs {
		types = append(types, e.Type)
	}
	t.Logf("events.jsonl types: %s", strings.Join(types, ", "))
	if v := findEventField(evs, "agent_ready", "version"); v != nil {
		t.Logf("live CLI version stamp: %v", v)
	}
	rawPath := filepath.Join(filepath.Dir(a.EventLogPath), "raw.jsonl")
	if raw, err := os.ReadFile(rawPath); err == nil {
		t.Logf("raw.jsonl retained %d bytes of the real vendor stream", len(raw))
	} else {
		t.Errorf("raw stream not retained: %v", err)
	}

	full := strings.Join(a.Transcript(), "\n")
	if !strings.Contains(full, "ALPHA") || !strings.Contains(full, "BETA") {
		t.Errorf("expected both replies in transcript, got:\n%s", full)
	}
}
