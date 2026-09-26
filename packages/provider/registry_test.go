package provider

import "testing"

// privateModel is a row no built-in catalog carries, so a lookup that finds
// it proves which catalog answered.
var privateModel = Model{
	Provider: "anthropic", ID: "private-catalog-model", DisplayName: "Private",
	ContextWindow: 200000, MaxOutput: 4321, Source: "user",
}

func privateRegistry() *Registry {
	r := NewRegistry()
	r.SetUserModels([]Model{privateModel})
	return r
}

// A Registry is a value: a layer written to one is invisible to another and
// to Builtin. This is what lets two engines in one
// process see different models (decision 0021).
func TestRegistriesAreIndependent(t *testing.T) {
	withCatalogState(t)
	r := privateRegistry()

	if _, err := r.FindModel("anthropic", privateModel.ID); err != nil {
		t.Fatalf("the registry that holds the row cannot find it: %v", err)
	}
	if r.Revision() == 0 {
		t.Error("a layer write must bump the registry's revision")
	}
	if _, err := NewRegistry().FindModel("anthropic", privateModel.ID); err == nil {
		t.Error("a fresh registry sees another registry's user layer")
	}
	if _, err := testReg.FindModel("anthropic", privateModel.ID); err == nil {
		t.Error("the tests' registry sees a private registry's user layer")
	}
	if _, err := Builtin().FindModel("anthropic", privateModel.ID); err == nil {
		t.Error("Builtin sees a registry's user layer")
	}

	// And the other way: a write to another registry does not reach r.
	testReg.SetUserModels([]Model{{Provider: "anthropic", ID: "other-only-model", MaxOutput: 1, Source: "user"}})
	if _, err := r.FindModel("", "other-only-model"); err == nil {
		t.Error("a private registry sees another registry's user layer")
	}
	if _, err := Builtin().FindModel("", "other-only-model"); err == nil {
		t.Error("Builtin sees a registry's user layer")
	}

	// Builtin still answers for the compiled-in list.
	if len(catalog) == 0 {
		t.Fatal("empty built-in catalog")
	}
	if _, err := Builtin().FindModel(catalog[0].Provider, catalog[0].ID); err != nil {
		t.Errorf("Builtin cannot find a built-in row: %v", err)
	}

	r.Reset()
	if _, err := r.FindModel("anthropic", privateModel.ID); err == nil {
		t.Error("Reset left the user layer in place")
	}
}

// A client reads model metadata through the catalog WithCatalog gives it, and
// WithCatalog reaches the concrete client through a wrapper. The output limit
// on the request is the observable: only the private registry knows it.
func TestClientReadsTheCatalogItIsGiven(t *testing.T) {
	withCatalogState(t)
	req := Request{
		Model:    privateModel.ID,
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	}

	anth := NewAnthropic("k", "")
	if _, err := anth.(*anthropicClient).buildRequest(req); err == nil {
		t.Fatal("a client with no catalog set found a row only a private registry holds")
	}
	if got := WithCatalog(anth, privateRegistry()); got != anth {
		t.Fatal("WithCatalog must return the client it was given")
	}
	wire, err := anth.(*anthropicClient).buildRequest(req)
	if err != nil {
		t.Fatalf("anthropic buildRequest through the private catalog: %v", err)
	}
	if wire.MaxTokens != privateModel.MaxOutput {
		t.Errorf("anthropic max_tokens = %d, want the private row's %d", wire.MaxTokens, privateModel.MaxOutput)
	}

	// Through a wrapper, the way google-vertex and openai-responses arrive.
	inner := NewOpenAI("k", "")
	WithCatalog(&renamedClient{inner: inner, name: "wrapped"}, privateRegistry())
	owire, err := inner.(*openaiClient).buildRequest(req)
	if err != nil {
		t.Fatalf("openai buildRequest through the private catalog: %v", err)
	}
	if owire.MaxTokens == nil || *owire.MaxTokens != privateModel.MaxOutput {
		t.Errorf("openai max_tokens = %v, want the private row's %d", owire.MaxTokens, privateModel.MaxOutput)
	}

	// nil restores Builtin, which does not hold the row but does hold the
	// compiled-in models.
	WithCatalog(anth, nil)
	if _, err := anth.(*anthropicClient).buildRequest(req); err == nil {
		t.Error("WithCatalog(nil) left the private catalog in place")
	}
	var builtin Model
	for _, m := range catalog {
		if m.Provider == "anthropic" && m.MaxOutput > 0 {
			builtin = m
			break
		}
	}
	bwire, err := anth.(*anthropicClient).buildRequest(Request{Model: builtin.ID, Messages: req.Messages})
	if err != nil {
		t.Fatalf("a client with no catalog cannot build a request for a built-in model: %v", err)
	}
	if bwire.MaxTokens != builtin.MaxOutput {
		t.Errorf("built-in max_tokens = %d, want %d", bwire.MaxTokens, builtin.MaxOutput)
	}
}

// The discovery helpers borrow capabilities from the rows they are handed,
// and from nowhere else. Another registry holds a row of the same id with
// other answers, so a helper that read a registry behind the caller's back
// would be caught.
func TestDiscoveryBorrowsFromTheCatalogPassedIn(t *testing.T) {
	withCatalogState(t)
	testReg.SetLiveModels([]Model{{Provider: "anthropic", ID: "gateway-model", MaxOutput: 999, Reasoning: true}})
	known := []Model{{Provider: "anthropic", ID: "gateway-model", MaxOutput: 777, Reasoning: true}}
	if got := anthropicCompatCaps(known, "gateway-model"); got.MaxOutput != 777 {
		t.Errorf("anthropicCompatCaps MaxOutput = %d, want 777 from the rows passed in", got.MaxOutput)
	}
	if got := anthropicCompatCaps(nil, "gateway-model"); got.MaxOutput == 777 || got.MaxOutput == 999 {
		t.Error("anthropicCompatCaps found a row it was not handed")
	}
	if !openAICompatCaps(known, "gateway-model").Reasoning {
		t.Error("openAICompatCaps did not borrow reasoning from the rows passed in")
	}
	if openAICompatCaps(nil, "gateway-model").Reasoning {
		t.Error("openAICompatCaps found a row it was not handed")
	}
}
