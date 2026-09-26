package build

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"terva.sh/terva/packages/agent/modelreg"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// Every client and agent terva builds reads terva's registry, not the built-in
// catalog a client or agent given none reads (docs/plans/model-catalog.md).
// The row lives in modelreg alone, so the output budget on the wire and the
// agent's gauge can only come from there.
func TestTervaClientsAndAgentsReadTervaRegistry(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	const id = "terva-registry-only-model"
	modelreg.RegisterExtraModel(provider.Model{
		Provider: "anthropic", ID: id, DisplayName: id,
		ContextWindow: 123456, MaxOutput: 3210, Source: "live",
	})
	if _, err := provider.Builtin().FindModel("anthropic", id); err == nil {
		t.Fatal("the built-in catalog holds terva's row, so this test cannot tell the two apart")
	}

	bodies := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		select {
		case bodies <- b:
		default:
		}
		http.Error(w, "stop here", http.StatusBadRequest)
	}))
	defer srv.Close()

	c := Resolved{Provider: "anthropic", Credential: "k", AuthMethod: "apikey", BaseURL: srv.URL}.NewClient()
	ch, err := c.Stream(context.Background(), provider.Request{
		Model:    id,
		Messages: []provider.Message{{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hi"}}}},
	})
	// The server refuses every request, so an error is expected here. What the
	// test reads is the body that reached it: a client that cannot find the row
	// fails before it sends anything.
	if err == nil {
		for range ch {
		}
	}
	var body struct {
		MaxTokens int `json:"max_tokens"`
	}
	select {
	case b := <-bodies:
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatalf("request body: %v", err)
		}
	default:
		t.Fatalf("no request reached the server (Stream: %v)", err)
	}
	if body.MaxTokens != 3210 {
		t.Errorf("max_tokens on the wire = %d, want terva's row's 3210", body.MaxTokens)
	}

	r, err := Resolve(Args{Provider: "anthropic", Model: id}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	ag := r.NewAgent(core.AllowAll)
	if ag.Catalog() != provider.ModelCatalog(modelreg.Registry()) {
		t.Error("build.NewAgent did not give the agent terva's registry")
	}
	if _, window := ag.ContextUsage(); window != 123456 {
		t.Errorf("agent context window = %d, want terva's row's 123456", window)
	}
}
