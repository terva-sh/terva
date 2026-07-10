package workspace

import (
	"errors"
	"path/filepath"
	"testing"

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
	s.setModel("openai-compatible", "same-a")

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
