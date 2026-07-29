package workspace

import (
	"errors"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// TestSwitchReusesClient pins the wrong-backend regression where it now lives.
// A same-provider model whose models.json baseUrl points at a different endpoint
// must NOT be swapped in place on the old, endpoint-bound client; only an
// identical provider+baseURL (and no forced rebuild) may reuse it.
//
// This decision used to live in the TUI (modes.swapModel) and was tested there;
// the workspace owns it now, so the coverage moved with it.
func TestSwitchReusesClient(t *testing.T) {
	const prov = "openai-compatible"
	a := provider.Model{Provider: prov, ID: "edge-a", BaseURL: "http://a.local/v1"}
	b := provider.Model{Provider: prov, ID: "edge-b", BaseURL: "http://b.local/v1"}
	sameAsA := provider.Model{Provider: prov, ID: "same-b", BaseURL: "http://a.local/v1"}
	otherProv := provider.Model{Provider: "anthropic", ID: "claude-x", BaseURL: "http://a.local/v1"}

	cases := []struct {
		name         string
		curProv      string
		cur          provider.Model
		curErr       error
		target       provider.Model
		forceRebuild bool
		wantReuse    bool
	}{
		{"same provider + same endpoint reuses", prov, a, nil, sameAsA, false, true},
		{"different baseURL rebuilds (wrong-backend bug)", prov, a, nil, b, false, false},
		{"different provider rebuilds", prov, a, nil, otherProv, false, false},
		{"forceRebuild always rebuilds", prov, a, nil, sameAsA, true, false},
		{"unresolvable current model rebuilds", prov, provider.Model{}, errors.New("unknown"), sameAsA, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := switchReusesClient(c.curProv, c.cur, c.curErr, c.target, c.forceRebuild); got != c.wantReuse {
				t.Errorf("switchReusesClient = %v, want %v", got, c.wantReuse)
			}
		})
	}
}

// The fast path end-to-end: two models on one endpoint swap the id on the live
// agent and record the new session model, with no client rebuild (which would
// need credentials).
func TestSwitchModelSameEndpointSwapsInPlace(t *testing.T) {
	provider.SetUserModels([]provider.Model{
		{Provider: "openai-compatible", ID: "same-a", DisplayName: "A", ContextWindow: 8192, MaxOutput: 4096, BaseURL: "http://same.local/v1", Source: "user"},
		{Provider: "openai-compatible", ID: "same-b", DisplayName: "B", ContextWindow: 8192, MaxOutput: 4096, BaseURL: "http://same.local/v1", Source: "user"},
	})
	t.Cleanup(func() { provider.SetUserModels(nil) })

	// switchModel persists the session meta and broadcasts session_updated, so
	// the session needs a real transcript file behind it.
	sess, err := core.NewSessionAtPath(filepath.Join(testsupport.TempDir(t), "s.jsonl"), "/ws", "openai-compatible", "same-a", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	w := &Workspace{}
	s := newTestSession()
	s.ws = w
	s.sess = sess
	s.agent = &core.Agent{Model: "same-a"}
	s.setModel("openai-compatible", "same-a", false)

	if err := w.switchModel(s, "openai-compatible", "same-b", false); err != nil {
		t.Fatalf("switchModel: %v", err)
	}
	if s.agent.Model != "same-b" {
		t.Errorf("in-place agent Model = %q, want same-b", s.agent.Model)
	}
	if prov, model := s.currentModel(); prov != "openai-compatible" || model != "same-b" {
		t.Errorf("session model = %s/%s, want openai-compatible/same-b", prov, model)
	}
}

// A bare model id (no provider) must resolve against the CURRENT provider
// before falling back to a global first-match: several ids exist under both an
// api-key provider and a subscription one (openai's gpt-5.5 precedes
// openai-codex's in the catalog), and the panel's /model command sends bare
// ids. A global first-match would silently hop providers — usually onto one
// with no credential, failing a switch the user meant as "same backend".
func TestSwitchModelBareIDPrefersCurrentProvider(t *testing.T) {
	provider.SetUserModels([]provider.Model{
		// The foreign twin comes FIRST so a global first-match would pick it.
		{Provider: "openai", ID: "dup-model", DisplayName: "Dup (api)", ContextWindow: 8192, MaxOutput: 4096, BaseURL: "http://other.local/v1", Source: "user"},
		{Provider: "openai-compatible", ID: "dup-model", DisplayName: "Dup", ContextWindow: 8192, MaxOutput: 4096, BaseURL: "http://same.local/v1", Source: "user"},
		{Provider: "openai-compatible", ID: "cur-model", DisplayName: "Cur", ContextWindow: 8192, MaxOutput: 4096, BaseURL: "http://same.local/v1", Source: "user"},
	})
	t.Cleanup(func() { provider.SetUserModels(nil) })

	sess, err := core.NewSessionAtPath(filepath.Join(testsupport.TempDir(t), "s.jsonl"), "/ws", "openai-compatible", "cur-model", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	w := &Workspace{}
	s := newTestSession()
	s.ws = w
	s.sess = sess
	s.agent = &core.Agent{Model: "cur-model"}
	s.setModel("openai-compatible", "cur-model", false)

	// Same endpoint under the current provider, so the preferred resolution
	// swaps in place; landing on openai would instead rebuild (and fail on
	// credentials), so an error here IS the regression.
	if err := w.switchModel(s, "", "dup-model", false); err != nil {
		t.Fatalf("switchModel bare id: %v", err)
	}
	if prov, model := s.currentModel(); prov != "openai-compatible" || model != "dup-model" {
		t.Errorf("session model = %s/%s, want openai-compatible/dup-model (bare id hopped providers)", prov, model)
	}
}

// A mid-session model swap must refresh a host-routed dispatch tool's
// inherited provider/model, or a sub-agent spawned afterward follows the
// stale pre-swap route (and resolves tiers against the wrong model).
func TestSwitchModelRefreshesHostRoutedTool(t *testing.T) {
	provider.SetUserModels([]provider.Model{
		{Provider: "openai-compatible", ID: "same-a", DisplayName: "A", ContextWindow: 8192, MaxOutput: 4096, BaseURL: "http://same.local/v1", Source: "user"},
		{Provider: "openai-compatible", ID: "same-b", DisplayName: "B", ContextWindow: 8192, MaxOutput: 4096, BaseURL: "http://same.local/v1", Source: "user"},
	})
	t.Cleanup(func() { provider.SetUserModels(nil) })

	sess, err := core.NewSessionAtPath(filepath.Join(testsupport.TempDir(t), "s.jsonl"), "/ws", "openai-compatible", "same-a", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	w := &Workspace{}
	s := newTestSession()
	s.ws = w
	s.sess = sess
	s.agent = &core.Agent{Model: "same-a"}
	s.setModel("openai-compatible", "same-a", false)

	// All three host-routed dispatch tools must be refreshed generically.
	spawn := &tools.SwarmSpawnTool{HostProvider: "openai-compatible", HostModel: "same-a"}
	actor := &tools.ActorSpawnTool{HostProvider: "openai-compatible", HostModel: "same-a"}
	convene := &tools.RaatiConveneTool{HostProvider: "openai-compatible", HostModel: "same-a"}
	s.agent.SetTools(core.Registry{
		"swarm_spawn":   spawn,
		"actor_spawn":   actor,
		"raati_convene": convene,
	})

	if err := w.switchModel(s, "openai-compatible", "same-b", false); err != nil {
		t.Fatalf("switchModel: %v", err)
	}
	for name, got := range map[string]tools.HostRouted{"swarm_spawn": spawn, "actor_spawn": actor, "raati_convene": convene} {
		// Read the fields directly (sequential, post-swap) to confirm the swap landed.
		var hp, hm string
		switch v := got.(type) {
		case *tools.SwarmSpawnTool:
			hp, hm = v.HostProvider, v.HostModel
		case *tools.ActorSpawnTool:
			hp, hm = v.HostProvider, v.HostModel
		case *tools.RaatiConveneTool:
			hp, hm = v.HostProvider, v.HostModel
		}
		if hp != "openai-compatible" || hm != "same-b" {
			t.Errorf("%s host = %s/%s, want openai-compatible/same-b (refreshed on swap)", name, hp, hm)
		}
	}
}
