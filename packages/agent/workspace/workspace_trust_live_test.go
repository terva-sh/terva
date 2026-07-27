package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/testsupport"
)

// Workspace Trust has TWO readers, and for a long time only one of them moved.
//
// Per-session state (s.trusted) is updated by setTrusted, so the session info a
// client renders flips immediately. The WORKSPACE verdict (w.trusted) was
// captured once in NewWorkspace and never written again — while injectExtraTools
// hands w.Trusted to swarm_spawn as its gate, commented "live". It was not: the
// tool kept refusing spawns with "this workspace is untrusted" after a
// successful /trust, and only a restart fixed it. defaultModel and the swarm
// worktree prober read the same stale bool.
//
// So: one verdict, and Trust/Untrust must move it.

func trustSession(t *testing.T) (*Workspace, *wsSession) {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	// swarm_spawn is only injected when auto-swarm is on — it is the tool whose
	// gate went stale, so the test needs it in the registry.
	on := true
	if err := config.MutateConfig(func(c *config.Config) { c.AutoSwarmEnabled = &on }); err != nil {
		t.Fatalf("enable auto-swarm: %v", err)
	}

	cwd := testsupport.TempDir(t)
	w, err := NewWorkspace(build.Args{
		Provider: "openai", Model: "gpt-5", CWD: cwd, NoExt: true, NoMCP: true,
	}, "test")
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	s := w.existing(info.ID)
	if s == nil {
		t.Fatal("session did not materialize")
	}
	return w, s
}

// spawnGate reads the verdict the LIVE swarm_spawn tool would consult, through
// the closure the host wired — not through the workspace field directly, so a
// fix that moves the field but leaves the tool pointed elsewhere still fails.
func spawnGate(t *testing.T, s *wsSession) bool {
	t.Helper()
	tl, ok := s.agent.LookupTool("swarm_spawn")
	if !ok {
		t.Fatal("swarm_spawn is not in the agent's registry")
	}
	st, ok := tl.(*tools.SwarmSpawnTool)
	if !ok {
		t.Fatalf("swarm_spawn is %T, want *tools.SwarmSpawnTool", tl)
	}
	if st.Trusted == nil {
		t.Fatal("swarm_spawn has no trust gate wired")
	}
	return st.Trusted()
}

func TestTrustMovesTheWorkspaceVerdictNotJustTheSession(t *testing.T) {
	w, s := trustSession(t)
	ctx := context.Background()

	if w.Trusted() {
		t.Fatal("precondition: a fresh temp cwd should not be trusted")
	}
	if spawnGate(t, s) {
		t.Fatal("precondition: swarm_spawn's gate should start untrusted")
	}

	if err := w.Trust(ctx, false); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	if !w.Trusted() {
		t.Error("after Trust the workspace verdict is still untrusted — every reader " +
			"of w.Trusted() (swarm_spawn's gate, defaultModel, the swarm worktree prober) " +
			"keeps the launch-time answer until terva restarts")
	}
	if !s.trusted.Load() {
		t.Error("after Trust the session verdict is still untrusted")
	}
	if !spawnGate(t, s) {
		t.Error("after Trust swarm_spawn still refuses: \"this workspace is untrusted\"")
	}

	if err := w.Untrust(ctx); err != nil {
		t.Fatalf("Untrust: %v", err)
	}
	if w.Trusted() {
		t.Error("after Untrust the workspace verdict is still trusted")
	}
	if spawnGate(t, s) {
		t.Error("after Untrust swarm_spawn's gate still reports trusted")
	}
}

// A session created AFTER the flip must agree with one created before it: the
// verdict is workspace-global, and a rebuild re-resolves it from the store.
func TestASessionOpenedAfterTrustAgreesWithOneOpenedBefore(t *testing.T) {
	w, before := trustSession(t)
	ctx := context.Background()

	if err := w.Trust(ctx, false); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	after := w.existing(info.ID)
	if after == nil {
		t.Fatal("second session did not materialize")
	}
	// Both true, not merely equal: two untrusted sessions agree too, and that
	// was exactly the broken state — the new session's tools resolved trust
	// from the store while its spawn gate still read the launch verdict.
	if newGate, oldGate := spawnGate(t, after), spawnGate(t, before); !newGate || !oldGate {
		t.Errorf("swarm_spawn gate after Trust: new session=%v, session open at the time=%v; want both trusted", newGate, oldGate)
	}
	if !after.trusted.Load() {
		t.Error("a session opened in a trusted workspace starts untrusted")
	}
}
