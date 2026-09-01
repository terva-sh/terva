package workspace

import (
	"errors"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/build"
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

// thinkingModels installs two models on one endpoint that want DIFFERENT
// thinking levels — the shape of the ask this exists for: luna at max, sol at
// medium, without re-setting the level by hand on every switch.
func thinkingModels(t *testing.T) (luna, sol provider.Model) {
	t.Helper()
	provider.SetUserModels([]provider.Model{
		{Provider: "openai-compatible", ID: "luna", DisplayName: "Luna", ContextWindow: 8192, MaxOutput: 4096,
			BaseURL: "http://same.local/v1", Source: "user", Reasoning: true, DefaultReasoning: "max"},
		{Provider: "openai-compatible", ID: "sol", DisplayName: "Sol", ContextWindow: 8192, MaxOutput: 4096,
			BaseURL: "http://same.local/v1", Source: "user", Reasoning: true, DefaultReasoning: "medium"},
	})
	t.Cleanup(func() { provider.SetUserModels(nil) })

	luna, err := provider.FindModel("openai-compatible", "luna")
	if err != nil {
		t.Fatalf("FindModel(luna): %v", err)
	}
	sol, err = provider.FindModel("openai-compatible", "sol")
	if err != nil {
		t.Fatalf("FindModel(sol): %v", err)
	}
	// The whole feature rests on the merge marking these as the OPERATOR's
	// choice; without the flag they would sit below the global instead.
	if !luna.DefaultReasoningSet || !sol.DefaultReasoningSet {
		t.Fatalf("user models did not come back as operator choices (luna=%v sol=%v)",
			luna.DefaultReasoningSet, sol.DefaultReasoningSet)
	}
	return luna, sol
}

func thinkingSession(t *testing.T, w *Workspace, prov, model string) *wsSession {
	t.Helper()
	sess, err := core.NewSessionAtPath(filepath.Join(testsupport.TempDir(t), "s.jsonl"), "/ws", prov, model, "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	s := newTestSession()
	s.ws = w
	s.sess = sess
	s.agent = &core.Agent{Model: model}
	s.setModel(prov, model, false)
	return s
}

// Switching models re-decides the thinking level, because the level is
// resolved per MODEL. Nothing re-resolved on the switch path before, so the
// level baked in at build — the model you STARTED on — rode along onto every
// model you moved to, and a per-model default was dead config for anyone who
// switches mid-session. That is the whole reason the setting felt missing.
func TestSwitchModelRecomputesTheThinkingLevel(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	luna, _ := thinkingModels(t)

	w := &Workspace{}
	s := thinkingSession(t, w, "openai-compatible", "luna")
	// Where a fresh build leaves the session it booted on.
	applyRawReasoning(s.agent, build.ResolveRawReasoning("", luna, ""))
	if s.agent.Reasoning != "max" {
		t.Fatalf("built on luna at %q, want max", s.agent.Reasoning)
	}

	if err := w.switchModel(s, "openai-compatible", "sol", false); err != nil {
		t.Fatalf("switchModel: %v", err)
	}
	if s.agent.Reasoning != "medium" {
		t.Errorf("after switching to sol the agent thinks at %q, want medium — it kept luna's level", s.agent.Reasoning)
	}

	// And back, so this is a re-resolve rather than a one-way nudge.
	if err := w.switchModel(s, "openai-compatible", "luna", false); err != nil {
		t.Fatalf("switchModel back: %v", err)
	}
	if s.agent.Reasoning != "max" {
		t.Errorf("back on luna the agent thinks at %q, want max", s.agent.Reasoning)
	}
}

// The blocking half: a level the USER set for this session is rung 1 and must
// survive a model switch. A re-resolve that clobbered it would trade one
// annoyance for a worse one — a deliberate choice evaporating under you.
func TestSwitchModelKeepsASessionsOwnThinkingLevel(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	thinkingModels(t)

	w := &Workspace{}
	s := thinkingSession(t, w, "openai-compatible", "luna")
	s.setReasoning("low")
	applyRawReasoning(s.agent, "low")

	if err := w.switchModel(s, "openai-compatible", "sol", false); err != nil {
		t.Fatalf("switchModel: %v", err)
	}
	if s.agent.Reasoning != "low" {
		t.Errorf("session level = %q after the switch, want the user's low", s.agent.Reasoning)
	}
}

// A global change reaches un-overridden sessions, but it must not outrank an
// operator's per-model level: that inversion is what made models.json
// defaultReasoning dead config the moment anyone touched /settings.
func TestGlobalThinkingChangeYieldsToThePerModelLevel(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	thinkingModels(t)

	w := &Workspace{}
	withDefault := thinkingSession(t, w, "openai-compatible", "sol")
	plain := thinkingSession(t, w, "openai-compatible", "no-such-model")

	w.sessions = map[string]*wsSession{"a": withDefault, "b": plain}
	w.applyReasoning("high")

	if withDefault.agent.Reasoning != "medium" {
		t.Errorf("sol thinks at %q after a global change, want its own medium", withDefault.agent.Reasoning)
	}
	// A session whose model has no per-model level still follows the global.
	if plain.agent.Reasoning != "high" {
		t.Errorf("un-defaulted session = %q, want the global high", plain.agent.Reasoning)
	}
}
