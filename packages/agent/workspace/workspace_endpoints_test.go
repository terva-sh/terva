package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider/auth"
	"terva.sh/terva/packages/testsupport"
)

// seedConfig writes a config.json into a scratch TERVA_HOME. The endpoints map is
// the point: it is the only record terva keeps of a machine the operator owns.
func seedConfig(t *testing.T, body string) {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if body != "" {
		if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// endpointWorkspace is a workspace whose auth group is live, in the TERVA_HOME the
// caller already seeded — the mutating verbs refuse outright without it.
func endpointWorkspace(t *testing.T) *Workspace {
	t.Helper()
	w := &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}
	m := auth.NewManager(auth.NewStore(filepath.Join(config.TervaHome(), "auth.json")))
	t.Cleanup(m.Close)
	w.EnableAuth(m, AuthOptions{})
	return w
}

// The bug this whole feature exists to fix: a named endpoint is its own provider
// with its own /v1/models discovery, and the pane could not see one. auth.Describe
// enumerates the static catalog and overlays stored credentials — a named endpoint
// is in NEITHER, because it is not built in and a local server usually needs no
// key. So terva shipped the capability and hid it.
func TestAuthProvidersListsNamedEndpoints(t *testing.T) {
	seedConfig(t, `{"endpoints":{
		"workshop-3090": {"baseUrl":"http://3090.box:8000/v1","contextWindow":131072},
		"little-box":    {"baseUrl":"http://mini.box:1234/v1"}
	}}`)

	v := providers(t)
	ep := find(t, v, "workshop-3090")
	if !ep.Endpoint {
		t.Error("the row is not marked as an endpoint; a client cannot tell it apart from a built-in provider, and would offer the wrong control")
	}
	if ep.BaseURL != "http://3090.box:8000/v1" {
		t.Errorf("base url = %q, want the configured one", ep.BaseURL)
	}
	// No key was stored for it, and most local servers want none. The row must not
	// claim a credential it does not have.
	if ep.Method != "" {
		t.Errorf("method = %q, want empty: nothing was stored for this endpoint", ep.Method)
	}
	find(t, v, "little-box")

	// The built-ins must still be there — endpoints are added to the list, not
	// swapped in for it.
	find(t, v, "anthropic")
}

// A half-written entry is not a backend. A row for it would offer to remove
// something that never worked, and would appear in /model as a provider that
// fails on the first turn.
func TestAuthProvidersSkipsEndpointWithNoBaseURL(t *testing.T) {
	seedConfig(t, `{"endpoints":{"broken":{"contextWindow":8192}}}`)
	for _, p := range providers(t).Providers {
		if p.ID == "broken" {
			t.Fatal("an endpoint with no base URL was listed; it cannot serve anything")
		}
	}
}

// The name becomes a PROVIDER ID — it keys config.json, auth.json, and the model
// picker's grouping. A collision with a shipped provider would shadow the real one
// in all three, and the operator would be debugging a machine they own against a
// name they never chose.
func TestValidEndpointNameRefusesCollisionsAndJunk(t *testing.T) {
	for _, bad := range []string{"", "anthropic", "openai", "openai-compatible", "has space", "slash/es", "quote\"s"} {
		if err := ValidEndpointName(bad); err == nil {
			t.Errorf("ValidEndpointName(%q) = nil, want a refusal", bad)
		}
	}
	for _, ok := range []string{"workshop-3090", "little_box", "box.local", "a1"} {
		if err := ValidEndpointName(ok); err != nil {
			t.Errorf("ValidEndpointName(%q) = %v, want it allowed", ok, err)
		}
	}
}

// Removing an endpoint forgets the DEFINITION, and that is the whole point: a
// logout would forget only a secret and leave the machine configured.
func TestAuthEndpointRemoveForgetsTheDefinition(t *testing.T) {
	seedConfig(t, `{"endpoints":{"gone":{"baseUrl":"http://x.invalid/v1"},"kept":{"baseUrl":"http://y.invalid/v1"}}}`)

	w := endpointWorkspace(t)
	if err := w.AuthEndpointRemove(context.Background(), ctrlproto.AuthEndpointRemoveParams{ID: "gone"}); err != nil {
		t.Fatalf("AuthEndpointRemove: %v", err)
	}

	c, err := config.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if _, still := c.Endpoints["gone"]; still {
		t.Error("the endpoint is still in config.json; nothing else records that machine, so it was not removed")
	}
	if _, ok := c.Endpoints["kept"]; !ok {
		t.Error("removing one endpoint deleted another")
	}
}

// "Removed" for something that was never there lets a typo look like a success —
// and the operator walks away believing a backend is gone while it still serves
// the agent.
func TestAuthEndpointRemoveRefusesAnUnknownName(t *testing.T) {
	seedConfig(t, `{"endpoints":{"real":{"baseUrl":"http://x.invalid/v1"}}}`)
	w := endpointWorkspace(t)
	err := w.AuthEndpointRemove(context.Background(), ctrlproto.AuthEndpointRemoveParams{ID: "typo"})
	if err == nil {
		t.Fatal("removing an endpoint that does not exist reported success")
	}
}
