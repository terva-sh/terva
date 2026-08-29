package build

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider"
)

// The named-endpoint half of the wire-dump fix.
//
// provider.wireBody picks its arm from reasoningWireFamily, whose default is
// the OpenAI-compatible wire. For a named endpoint that default is the ONLY
// possible answer: the id is invented by the operator at /login, so no static
// table in provider can ever list it. The provider-side test asserts the dump
// handles such an id; it cannot assert that the default is RIGHT, because
// provider cannot see the registry that builds the client.
//
// This can. registerEndpointLocked builds every endpoint with
// provider.NewOpenAI, so "openai-compat" is correct by construction rather than
// by luck — and if someone ever gives endpoints a different client, the dump
// would start printing a body the endpoint never receives. That is the failure
// this test exists to make loud, and it is the same class of bug as the kimi
// and vercel-ai-gateway misclassifications that the reasoning-wire guard next
// door was written for.
func TestNamedEndpointSpeaksTheWireItsDumpAssumes(t *testing.T) {
	provider.ResetCatalogLayers()
	t.Cleanup(provider.ResetCatalogLayers)

	const id = "neot"
	if err := RegisterOrReplaceEndpoint(id, config.EndpointConfig{
		BaseURL: "http://vllm.example:8000/v1",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { UnregisterEndpoint(id) })

	r := Resolved{
		Provider:   id,
		Credential: "dummy-key",
		AuthMethod: "apikey",
		BaseURL:    "http://vllm.example:8000/v1",
	}
	c := r.NewClient()
	if c == nil {
		t.Fatal("NewClient returned nil for a registered endpoint")
	}

	got := provider.ClientReasoningWire(c)
	want := provider.ProviderReasoningWire(id)
	if got != "openai-compat" {
		t.Errorf("a registered endpoint builds a client speaking %q, not openai-compat; "+
			"the wire dump assumes the OpenAI-compatible default for every endpoint id "+
			"and would now print a body this endpoint never receives", got)
	}
	if got != want {
		t.Errorf("endpoint %q: the client speaks %q but the table answers %q", id, got, want)
	}
}

// Non-vacuity: the endpoint really does reach a dump, and it is the chat-wire
// one. Without this the test above could pass on a pair of agreeing strings
// while --dump-prompt=wire still refused the endpoint outright, which is the
// bug that started this.
func TestNamedEndpointActuallyDumps(t *testing.T) {
	provider.ResetCatalogLayers()
	t.Cleanup(provider.ResetCatalogLayers)

	const id = "neot"
	if err := RegisterOrReplaceEndpoint(id, config.EndpointConfig{
		BaseURL: "http://vllm.example:8000/v1",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { UnregisterEndpoint(id) })

	out, err := provider.DumpRequestJSONL(id, "apikey", provider.Request{
		Model:    "qwen3.8-27b-abl",
		System:   "you are a test",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "one"}}}},
	})
	if err != nil {
		t.Fatalf("a registered endpoint must be dumpable, got: %v", err)
	}
	head := string(out)
	if i := strings.IndexByte(head, '\n'); i >= 0 {
		head = head[:i]
	}
	if !strings.Contains(head, `"_field":"messages"`) {
		t.Errorf("endpoint dump should name the chat-wire input array, got header: %s", head)
	}
	if !strings.Contains(head, `"_provider":"`+id+`"`) {
		t.Errorf("endpoint dump should name the endpoint it was asked for, got header: %s", head)
	}
}
