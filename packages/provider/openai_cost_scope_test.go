package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An OpenAI-compatible endpoint that lists a model id the built-in openai row
// also lists is priced from its own row. The stream looked the id up bare, so
// it priced every turn at the first provider's rates while the request builder
// sized the same turn from the endpoint's row.
func TestOpenAICompatiblePricesUnderItsOwnProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"hi"}}],` +
			`"usage":{"prompt_tokens":1000000,"completion_tokens":0}}` + "\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	reg := NewRegistry()
	reg.SetUserModels([]Model{
		{Provider: "openai", ID: "shared-id", ContextWindow: 100_000, PriceInput: 10},
		{Provider: "my-endpoint", ID: "shared-id", ContextWindow: 100_000, PriceInput: 1},
	})
	c := WithCatalog(NewOpenAICompatibleAs("my-endpoint", "k", srv.URL), reg)
	evs, err := c.Stream(context.Background(), Request{
		Model:    "shared-id",
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got Usage
	for ev := range evs {
		if e, ok := ev.(EventUsage); ok {
			got = e.Usage
		}
	}
	if got.InputTokens != 1_000_000 {
		t.Fatalf("InputTokens = %d, want 1000000: the fixture is not reaching the decoder", got.InputTokens)
	}
	if got.CostUSD != 1 {
		t.Errorf("cost = %v, want 1 from my-endpoint's row (10 is the openai row's price)", got.CostUSD)
	}
}
