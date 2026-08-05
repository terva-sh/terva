package workspace

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider"
)

// The bug, from the operator's chair: they registered a named openai-compatible
// endpoint in the web UI and watched it appear in the provider pane — that pane
// reads config, so it saw the endpoint fine. Then the web's model picker showed
// them nothing, and they had to open the TUI to work with the models at all.
//
// Models() filtered the catalog through a credential check. A local server that
// wants no key has no credential to check, so every one of its models was dropped
// on the way to the picker. The provider pane and the model picker were asking two
// different questions and only one of them was the right one.
func TestModelsListsAKeylessNamedEndpointsModels(t *testing.T) {
	seedCreds(t, "")
	if err := config.MutateConfig(func(c *config.Config) {
		c.Endpoints = map[string]config.EndpointConfig{
			"workshop": {BaseURL: "http://127.0.0.1:1234/v1"},
		}
	}); err != nil {
		t.Fatal(err)
	}
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()
	provider.SetUserModels([]provider.Model{{
		Provider:      "workshop",
		ID:            "qwen3-coder",
		ContextWindow: 262144,
	}})

	w := &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}
	res, err := w.Models(context.Background(), "")
	models := res.Models
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	for _, m := range models {
		if m.Provider == "workshop" && m.ID == "qwen3-coder" {
			return
		}
	}
	t.Fatalf("the endpoint's model never reached the picker: %d models listed, none from workshop", len(models))
}

// TestModelsCurrentReflectsFramedSession pins Fix 1 of the per-session
// model-selection plan: models.list flags Current from the FRAMED session's own
// model, so the picker reflects the session the client is viewing. Framing a
// different session must move the highlight; framing no session must highlight
// nothing (the workspace default is empty here), never another session's model.
func TestModelsCurrentReflectsFramedSession(t *testing.T) {
	seedCreds(t, "")
	if err := config.MutateConfig(func(c *config.Config) {
		c.Endpoints = map[string]config.EndpointConfig{
			"workshop": {BaseURL: "http://127.0.0.1:1234/v1"},
		}
	}); err != nil {
		t.Fatal(err)
	}
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()
	provider.SetUserModels([]provider.Model{
		{Provider: "workshop", ID: "qwen-a", ContextWindow: 262144},
		{Provider: "workshop", ID: "qwen-b", ContextWindow: 262144},
	})

	// Two live sessions, each on a different model.
	w := &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{
		"a": {provider: "workshop", model: "qwen-a"},
		"b": {provider: "workshop", model: "qwen-b"},
	}}

	currentID := func(sess string) string {
		t.Helper()
		res, err := w.Models(context.Background(), sess)
		models := res.Models
		if err != nil {
			t.Fatalf("Models(%q): %v", sess, err)
		}
		var cur string
		for _, m := range models {
			if m.Current {
				if cur != "" {
					t.Fatalf("Models(%q): more than one Current row (%s and %s)", sess, cur, m.ID)
				}
				cur = m.ID
			}
		}
		return cur
	}

	if got := currentID("a"); got != "qwen-a" {
		t.Errorf("framing session a: Current = %q, want qwen-a", got)
	}
	if got := currentID("b"); got != "qwen-b" {
		t.Errorf("framing session b: Current = %q, want qwen-b", got)
	}
	if got := currentID(""); got != "" {
		t.Errorf("framing no session: Current = %q, want none (empty workspace default)", got)
	}
}

// The bug, from the operator's chair: the same model id is reachable through more
// than one provider — a subscription plan and a metered key, say — and the picker
// rendered both as the same bare string. In the ★ Favorites list, which is flat and
// carries no provider heading, that produced two visually IDENTICAL rows, and
// choosing the one that spends the subscription instead of the one that bills per
// token was a coin flip.
//
// Provider was already on the wire. The auth METHOD was not: it was resolved on
// every models.list and thrown away. Without it a picker can say the rows differ
// but not which one costs money.
func TestModelsCarryProviderAuthSoDuplicateIDsAreDistinguishable(t *testing.T) {
	seedCreds(t, `{"anthropic":{"oauth":{"access_token":"tok"}},"deepseek":{"api_key":"sk-d"}}`)
	if err := config.MutateConfig(func(c *config.Config) {
		// Keyless: reachable on a base URL alone, so it has no credential and
		// therefore no honest method to report.
		c.Endpoints = map[string]config.EndpointConfig{
			"workshop": {BaseURL: "http://127.0.0.1:1234/v1"},
		}
	}); err != nil {
		t.Fatal(err)
	}
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()
	// One model id, three backends — the collision the report describes.
	provider.SetUserModels([]provider.Model{
		{Provider: "anthropic", ID: "deep-v4-pro", ContextWindow: 200000},
		{Provider: "deepseek", ID: "deep-v4-pro", ContextWindow: 200000},
		{Provider: "workshop", ID: "deep-v4-pro", ContextWindow: 200000},
	})

	w := &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}
	res, err := w.Models(context.Background(), "")
	models := res.Models
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	got := map[string]string{}
	for _, m := range models {
		if m.ID == "deep-v4-pro" {
			got[m.Provider] = m.Auth
		}
	}

	// A subscription and a metered key must be tellable apart...
	if got["anthropic"] != "oauth" {
		t.Errorf("anthropic (oauth token seeded): Auth = %q, want oauth", got["anthropic"])
	}
	if got["deepseek"] != "apikey" {
		t.Errorf("deepseek (api key seeded): Auth = %q, want apikey", got["deepseek"])
	}
	// ...and a keyless backend must report NOTHING rather than be guessed into one
	// bucket: an empty method means "unknown", which the picker renders as no badge.
	if v, ok := got["workshop"]; !ok {
		t.Error("the keyless endpoint's model never reached the picker at all")
	} else if v != "" {
		t.Errorf("keyless endpoint: Auth = %q, want empty (unknown, not a guess)", v)
	}
	if len(got) != 3 {
		t.Fatalf("expected all three backends to offer the shared id, got %v", got)
	}
}

// The web picker can only render a name the daemon actually sends, and
// ModelInfo carried no display name at all until now — which is why a
// models.json rename was visible in the TUI and invisible in the browser.
// Renamed is what tells a client it may substitute the name for the id.
func TestModelsCarryTheOperatorsDisplayName(t *testing.T) {
	seedCreds(t, "")
	if err := config.MutateConfig(func(c *config.Config) {
		c.Endpoints = map[string]config.EndpointConfig{
			"workshop": {BaseURL: "http://127.0.0.1:1234/v1"},
		}
	}); err != nil {
		t.Fatal(err)
	}
	provider.ResetCatalogLayers()
	defer provider.ResetCatalogLayers()
	provider.SetUserModels([]provider.Model{
		{Provider: "workshop", ID: "hf.co/unsloth/Qwen3-Coder-30B:Q4_K_XL", DisplayName: "Qwen Coder"},
		{Provider: "workshop", ID: "plain-local-id"},
	})

	w := &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}
	res, err := w.Models(context.Background(), "")
	models := res.Models
	if err != nil {
		t.Fatalf("Models: %v", err)
	}

	byID := map[string]ctrlproto.ModelInfo{}
	for _, m := range models {
		byID[m.ID] = m
	}
	named, ok := byID["hf.co/unsloth/Qwen3-Coder-30B:Q4_K_XL"]
	if !ok {
		t.Fatal("the renamed model never reached the listing")
	}
	if named.DisplayName != "Qwen Coder" || !named.Renamed {
		t.Errorf("renamed model went over the wire as %q renamed=%v", named.DisplayName, named.Renamed)
	}
	plain, ok := byID["plain-local-id"]
	if !ok {
		t.Fatal("the un-renamed model never reached the listing")
	}
	if plain.Renamed {
		t.Errorf("a model with no models.json name must not be flagged renamed (name=%q)", plain.DisplayName)
	}
}
