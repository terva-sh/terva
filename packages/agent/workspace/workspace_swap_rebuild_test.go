package workspace

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/testsupport"
)

// A model swap and a tool-set rebuild are two events that touch the same
// things, and until this test the second silently undid half of the first.
//
// build.ApplyModelSwap does three things: install the client + model on the
// agent, re-bind terva_status's provider identity, and re-point the host-routed
// dispatch tools (swarm_spawn, actor_spawn, raati_convene) at the new route.
// Only the first lives on the agent. The other two live on TOOL INSTANCES — and
// rebuildTools re-resolves the whole registry from s.args, minting fresh
// instances stamped with whatever provider+model that resolve produced.
//
// s.args was never updated by a swap, so the resolve produced the LAUNCH model,
// and the two tool-borne steps were quietly reverted by the next rebuild. Five
// things fire one: an extension reload, a live MCP toggle, an approval-mode
// switch, entering plan mode, and a trust flip. After any of them a sub-agent
// spawned by the model ran on the pre-swap model — real tokens at a model the
// user had switched away from — and terva_status told the model it was running
// somewhere it was not.
//
// The daemon restart path already got this right: buildSession seeds
// args.Provider/args.Model from session meta, which switchModel does write. It
// was only the LIVE session that kept re-resolving from its launch values.

func swapSession(t *testing.T) (*Workspace, *wsSession) {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	// Both providers need a resolvable credential: the swap below is
	// cross-provider (the case that moves the status identity as well as the
	// route), and switchModel refuses a target it cannot reach.
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_API_KEY", "test-key")

	// swarm_spawn is the host-routed tool available to a plain coding session,
	// and it is only injected when auto-swarm is on.
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

// hostRoute reads the route off the LIVE swarm_spawn instance in the agent's
// registry — the one a spawn would actually dispatch through, so a rebuild that
// replaced the instance is visible here even though the old object still holds
// the right answer. Fields read bare: these tests are sequential and the only
// writer (SetHost) has already run.
func hostRoute(t *testing.T, s *wsSession) (string, string) {
	t.Helper()
	tl, ok := s.agent.LookupTool("swarm_spawn")
	if !ok {
		t.Fatal("swarm_spawn is not in the agent's registry")
	}
	st, ok := tl.(*tools.SwarmSpawnTool)
	if !ok {
		t.Fatalf("swarm_spawn is %T, want *tools.SwarmSpawnTool", tl)
	}
	return st.HostProvider, st.HostModel
}

// statusReport calls terva_status the way the model would, so the assertion is
// on what the model is TOLD rather than on a field.
func statusReport(t *testing.T, s *wsSession) string {
	t.Helper()
	tl, ok := s.agent.LookupTool("terva_status")
	if !ok {
		t.Fatal("terva_status is not in the agent's registry")
	}
	res, err := tl.Execute(context.Background(), nil, func(string) {})
	if err != nil {
		t.Fatalf("terva_status: %v", err)
	}
	return toolText(res)
}

const swapTarget = "claude-sonnet-4-5-20250929"

// The end-to-end case, driven by real verbs: a cross-provider switch followed
// by a trust flip, which is one of the five rebuild triggers and the one whose
// whole point is to re-resolve the tool set.
func TestAModelSwapSurvivesTheNextToolSetRebuild(t *testing.T) {
	w, s := swapSession(t)
	ctx := context.Background()

	if p, m := hostRoute(t, s); p != "openai" || m != "gpt-5" {
		t.Fatalf("precondition: host route = %s/%s, want openai/gpt-5", p, m)
	}

	if err := w.switchModel(s, "anthropic", swapTarget, false); err != nil {
		t.Fatalf("switchModel: %v", err)
	}
	// The swap's own work, asserted as the precondition the rebuild must not
	// undo rather than as the property under test (that is #485's test).
	if p, m := hostRoute(t, s); p != "anthropic" || m != swapTarget {
		t.Fatalf("precondition: the swap itself did not move the host route (%s/%s)", p, m)
	}
	if got := statusReport(t, s); !strings.Contains(got, "provider: anthropic") {
		t.Fatalf("precondition: the swap itself did not move terva_status:\n%s", got)
	}

	// A real trigger, not a direct rebuildTools call: Trust fans a rebuild out
	// to every open session, and it is synchronous by the time it returns.
	if err := w.Trust(ctx, false); err != nil {
		t.Fatalf("Trust: %v", err)
	}

	if p, m := hostRoute(t, s); p != "anthropic" || m != swapTarget {
		t.Errorf("after a rebuild the host route fell back to %s/%s — every sub-agent "+
			"spawned from here runs on the pre-swap model, at the pre-swap provider, "+
			"and resolves its tier against the wrong catalog entry", p, m)
	}
	if got := statusReport(t, s); !strings.Contains(got, "provider: anthropic") {
		t.Errorf("after a rebuild terva_status reports the pre-swap provider — the model "+
			"is told it is running somewhere it is not:\n%s", got)
	}
}

// Every trigger, not just the one with a convenient verb. A rebuild fires for
// five reasons and they all run the same re-resolve, so a fix that only tracked
// the trust path would still lose the route on an extension reload.
func TestNoRebuildReasonRevertsTheHostRoute(t *testing.T) {
	w, s := swapSession(t)

	if err := w.switchModel(s, "anthropic", swapTarget, false); err != nil {
		t.Fatalf("switchModel: %v", err)
	}
	for _, reason := range []string{
		"extension-reload", "extension-context", "tool-withdrawal", "approval-mode", "trust",
	} {
		s.rebuildTools(reason)
		if p, m := hostRoute(t, s); p != "anthropic" || m != swapTarget {
			t.Fatalf("rebuildTools(%q) reverted the host route to %s/%s", reason, p, m)
		}
	}
}

// The same-endpoint id swap — the shortcut ApplyModelSwap spells with a nil
// Client, and the path the stuck-loop escalation takes automatically, without
// the user having asked for anything. It moves the model but not the provider,
// so the route is the only thing to lose.
func TestASameEndpointIDSwapSurvivesARebuild(t *testing.T) {
	w, s := swapSession(t)

	if err := w.switchModel(s, "openai", "gpt-5-pro", false); err != nil {
		t.Fatalf("switchModel: %v", err)
	}
	s.rebuildTools("approval-mode")

	if p, m := hostRoute(t, s); p != "openai" || m != "gpt-5-pro" {
		t.Errorf("after a rebuild the host route = %s/%s, want openai/gpt-5-pro", p, m)
	}
}

// A cross-endpoint swap drops the launch-time key/URL overrides, because they
// pin the endpoint the session has just left. switchModel already clears them
// on the copy it resolves the replacement client from; the session's own args
// have to lose them too, or the next rebuild re-resolves against a base URL
// belonging to the old provider and terva_status reports it to the model.
func TestACrossEndpointSwapDropsTheLaunchEndpointPins(t *testing.T) {
	w, s := swapSession(t)

	s.mu.Lock()
	s.args.APIKey, s.args.BaseURL = "launch-key", "http://launch.local/v1"
	s.mu.Unlock()

	if err := w.switchModel(s, "anthropic", swapTarget, false); err != nil {
		t.Fatalf("switchModel: %v", err)
	}
	if a := s.argsSnapshot(); a.APIKey != "" || a.BaseURL != "" {
		t.Errorf("after a cross-provider swap the args still pin the old endpoint "+
			"(key=%q url=%q) — the next rebuild resolves anthropic against openai's "+
			"credentials and base URL", a.APIKey, a.BaseURL)
	}

	// The same-endpoint shortcut must NOT clear them: nothing moved, and the
	// overrides are how a self-hosted session reaches its own backend at all.
	w2, s2 := swapSession(t)
	s2.mu.Lock()
	s2.args.APIKey, s2.args.BaseURL = "launch-key", "http://launch.local/v1"
	s2.mu.Unlock()
	if err := w2.switchModel(s2, "openai", "gpt-5-pro", false); err != nil {
		t.Fatalf("switchModel same endpoint: %v", err)
	}
	if a := s2.argsSnapshot(); a.APIKey != "launch-key" || a.BaseURL != "http://launch.local/v1" {
		t.Errorf("an id swap on the same endpoint dropped the launch overrides "+
			"(key=%q url=%q) — a self-hosted session loses its backend on /model", a.APIKey, a.BaseURL)
	}
}
