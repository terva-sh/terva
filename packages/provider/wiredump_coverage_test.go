package provider

import (
	"bytes"
	"encoding/json"
	"testing"
)

// The coverage census for the wire dump.
//
// wireBody used to switch on a hand-written list of eight provider ids, so ten
// real providers answered "wire dump is not implemented" while speaking a wire
// this file dumps for somebody else — and every NAMED ENDPOINT did too, because
// an endpoint's id is whatever the operator typed at /login and no static list
// can hold it. The arm is now chosen by the wire, so the question worth asking
// is no longer "is this id in the list" but "does each provider reach the
// builder for the wire it actually speaks".
//
// The expected field is the assertion that catches a mis-routed arm: a provider
// sent to the wrong builder still dumps, and still looks like JSON, but names
// the wrong input array — "messages" for a Responses request, "contents" for a
// chat one.
func dumpInputField(t *testing.T, providerName string) string {
	t.Helper()
	out, err := DumpRequestJSONL(providerName, "", wireReq(userMsg("one")))
	if err != nil {
		t.Fatalf("%s: %v", providerName, err)
	}
	head, _, _ := bytes.Cut(out, []byte("\n"))
	var h struct {
		Field    string `json:"_field"`
		Provider string `json:"_provider"`
	}
	if err := json.Unmarshal(head, &h); err != nil {
		t.Fatalf("%s: header is not JSON: %v", providerName, err)
	}
	if h.Provider != providerName {
		t.Errorf("%s: header reports _provider %q; the dump must name the provider asked for", providerName, h.Provider)
	}
	return h.Field
}

func TestWireDumpCoversEveryProviderWire(t *testing.T) {
	cases := []struct{ provider, field, why string }{
		// Responses route.
		{"openai-codex", "input", "the Codex backend"},
		{"openai-responses", "input", "same wire, api-key flow; was refused outright"},

		// Gemini.
		{"google", "contents", "generativelanguage"},
		{"google-vertex", "contents", "the same gemini client behind a renamedClient"},

		// Anthropic Messages, including the third parties that only look
		// OpenAI-shaped.
		{"anthropic", "messages", "the real thing"},
		{"kimi", "messages", "Kimi Code is Anthropic-wire despite the name"},
		{"minimax", "messages", "Anthropic-wire third party"},
		{"fireworks", "messages", "Anthropic-wire third party"},
		{"vercel-ai-gateway", "messages", "Anthropic-wire, and it fooled the census once"},

		// Chat completions. Every one of these but the first four used to be
		// refused, while being the SAME client the first four use.
		{"openai", "messages", ""},
		{"deepseek", "messages", ""},
		{"ollama", "messages", ""},
		{"openai-compatible", "messages", ""},
		{"groq", "messages", "was refused"},
		{"xai", "messages", "was refused"},
		{"openrouter", "messages", "was refused"},
		{"mistral", "messages", "was refused"},
		{"azure", "messages", "was refused"},
		{"github-copilot", "messages", "was refused"},
		{"together", "messages", "was refused"},
		{"cerebras", "messages", "was refused"},
		{"zai", "messages", "was refused"},
	}
	for _, c := range cases {
		if got := dumpInputField(t, c.provider); got != c.field {
			t.Errorf("%s: input field %q, want %q (%s)", c.provider, got, c.field, c.why)
		}
	}
}

// A named endpoint is the case no static list could ever have covered: the id
// is invented by the operator at /login, and registerEndpointLocked builds it
// with provider.NewOpenAI — so the OpenAI-compatible default is correct for it
// BY CONSTRUCTION, not by luck.
//
// This asserts the provider-side half here, where an id unknown to every table
// is exactly what an endpoint looks like. The other half — that the registry
// really does build an openai client for a registered endpoint — is pinned in
// packages/agent/build, which can see the registry that provider cannot import.
func TestWireDumpHandlesANamedEndpointId(t *testing.T) {
	for _, id := range []string{"neot", "box-a", "gw.internal", "lm-studio-upstairs"} {
		if got := dumpInputField(t, id); got != "messages" {
			t.Errorf("endpoint %q: input field %q, want messages", id, got)
		}
	}
}
