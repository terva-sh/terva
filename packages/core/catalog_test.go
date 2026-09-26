package core

import (
	"context"
	"sync"
	"testing"

	"terva.sh/terva/packages/provider"
)

// The agent reads the catalog its host passes in. A row only a private
// registry holds reaches the output budget and the context gauge once
// SetCatalog names that registry, and not before.
func TestAgentReadsTheCatalogItIsGiven(t *testing.T) {
	reg := provider.NewRegistry()
	reg.SetUserModels([]provider.Model{{
		Provider: "anthropic", ID: "agent-private-model", DisplayName: "Private",
		ContextWindow: 123456, MaxOutput: 777, Source: "user",
	}})

	a := &Agent{maxTokens: 5}
	a.SetModel("agent-private-model")
	if a.maxTokens != 5 {
		t.Fatalf("an agent with no catalog set sized its budget from a private row: MaxTokens = %d", a.maxTokens)
	}
	if _, window := a.ContextUsage(); window != 0 {
		t.Fatalf("an agent with no catalog set gauged a private row: window = %d", window)
	}

	// The model is already selected: SetCatalog alone re-derives its budget.
	a.SetCatalog(reg)
	if a.Catalog() != provider.ModelCatalog(reg) {
		t.Fatal("Catalog does not return the catalog SetCatalog set")
	}
	if a.maxTokens != 777 {
		t.Errorf("after SetCatalog, MaxTokens = %d, want the private row's 777", a.maxTokens)
	}
	a.maxTokens = 5
	a.SetModel("agent-private-model")
	if a.maxTokens != 777 {
		t.Errorf("after SetModel, MaxTokens = %d, want the private row's 777", a.maxTokens)
	}
	if _, window := a.ContextUsage(); window != 123456 {
		t.Errorf("context window = %d, want the private row's 123456", window)
	}

	// Another agent in the same process does not see it.
	b := &Agent{model: "agent-private-model"}
	if _, window := b.ContextUsage(); window != 0 {
		t.Errorf("a second agent sees the first agent's catalog: window = %d", window)
	}

	a.SetCatalog(nil)
	if a.Catalog() != provider.Builtin() {
		t.Error("SetCatalog(nil) must restore Builtin")
	}
}

// Two agents in one process, each with its own catalog, run at once and each
// sends the budget its own catalog gives the same model id. With the catalog
// in package state this was impossible: the second catalog replaced the first.
func TestTwoAgentsRunWithDifferentCatalogs(t *testing.T) {
	const id = "shared-model-id"
	catalog := func(window, maxOut int) *provider.Registry {
		r := provider.NewRegistry()
		r.SetUserModels([]provider.Model{{
			Provider: "anthropic", ID: id, DisplayName: id,
			ContextWindow: window, MaxOutput: maxOut, Source: "user",
		}})
		return r
	}
	type run struct {
		client        *captureClient
		agent         *Agent
		window, max   int
		gotMax, gotWn int
		err           error
	}
	runs := []*run{
		{client: &captureClient{}, window: 100000, max: 1111},
		{client: &captureClient{}, window: 200000, max: 2222},
	}
	for _, r := range runs {
		r.agent = mustNew(r.client, id, WithAssembler(&testFrame{system: "sys"}), WithTools(Registry{}), WithGate(AllowAll))
		r.agent.SetCatalog(catalog(r.window, r.max))
	}

	var wg sync.WaitGroup
	for _, r := range runs {
		wg.Add(1)
		go func(r *run) {
			defer wg.Done()
			r.err = r.agent.Prompt(context.Background(), "go", nil, nil)
			r.gotMax = r.client.lastReq.MaxTokens
			_, r.gotWn = r.agent.ContextUsage()
		}(r)
	}
	wg.Wait()

	for i, r := range runs {
		if r.err != nil {
			t.Fatalf("agent %d: Prompt: %v", i, r.err)
		}
		if r.gotMax != r.max {
			t.Errorf("agent %d sent max_tokens %d, want its own catalog's %d", i, r.gotMax, r.max)
		}
		if r.gotWn != r.window {
			t.Errorf("agent %d gauges a window of %d, want its own catalog's %d", i, r.gotWn, r.window)
		}
	}
}
