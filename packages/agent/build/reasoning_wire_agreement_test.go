package build

import (
	"testing"

	"terva.sh/terva/packages/provider"
)

// TestReasoningWireTableMatchesTheRealClient checks provider's reasoning-wire
// TABLE against the client the registry actually constructs, for every
// registered provider, with no escape set.
//
// Two facts have to agree. provider.reasoningWireWiring maps a provider id to a
// reasoning wire, and it is what /reasoning and the web ReasoningPick use to
// TELL the operator what a rung sends — they hold a Model, never a Client, so
// they cannot ask the client directly. providerSpecs[].newClient decides what
// the request ACTUALLY carries. When the two disagree, the picker describes a
// knob the wire does not have.
//
// They disagreed for two providers simultaneously, and both survived a census
// that was supposed to cover exactly this:
//
//   - vercel-ai-gateway was ABSENT from the table, so it fell through to the
//     OpenAI-compatible default — while NewVercelGatewayAnthropic builds an
//     anthropicClient. /reasoning reported "effort: medium" for a request
//     carrying thinking{enabled, budget_tokens:8192}: a field the Anthropic
//     Messages body has no slot for, and a budget the dialog never mentioned.
//     109 catalog rows, Reasoning:true. This is the kimi bug, which the repo
//     had already diagnosed and written a trap comment about, recurring
//     untouched in the provider added next to it.
//
//   - azure-openai-responses was classified as the Codex wire on the strength
//     of its name, while NewAzureOpenAIResponses builds an openaiClient and
//     azure_openai.go's own header states the Chat Completions choice
//     deliberately. The ladder offered "maximum" and "high" as two rungs whose
//     requests were byte-identical. ~30 catalog rows.
//
// The old census could not have caught either. It asserted only that a provider
// was classified SOMEWHERE, never that the classification was RIGHT, and it
// carried an escape set that named vercel-ai-gateway outright — plus a dead
// entry, "azure-openai", which is an alias and not a registry id at all. This
// guard has no escape set by construction: agreement is checked against the
// constructed client, so there is nothing to opt out of.
func TestReasoningWireTableMatchesTheRealClient(t *testing.T) {
	for _, spec := range providerSpecs {
		r := Resolved{
			Provider:   spec.id,
			Credential: "dummy-key",
			AuthMethod: "apikey",
			// A dummy BaseURL is required, not cosmetic. NewAzureOpenAI returns
			// an unimplementedClient unless a base URL or resource name is
			// resolvable, and it reads AMBIENT env to decide — so without this
			// the guard would assert against a stub on CI and against the
			// developer's real Azure config locally.
			BaseURL: "https://example.invalid",
		}
		c := r.NewClient()
		if c == nil {
			t.Errorf("%s: NewClient returned nil", spec.id)
			continue
		}
		got := provider.ClientReasoningWire(c)
		want := provider.ProviderReasoningWire(spec.id)

		if got == "unknown" {
			// An undeclared client is the failure this guard's zero value was
			// designed to surface, not something to skip past: before
			// reasoningWireUnknown existed, "undeclared" and "OpenAI-compat"
			// were the same value.
			t.Errorf("%s: the constructed client declares no ReasoningWire; "+
				"set it in that client's Capabilities()", spec.id)
			continue
		}
		if got != want {
			t.Errorf("%s: reasoningWireWiring says %q but the registry builds a client that speaks %q.\n"+
				"  /reasoning and the web picker describe the rung using the TABLE, so this tells the operator "+
				"something the request does not do.\n"+
				"  Fix the table row (or the client), and remember the answer is NOT guessable from the "+
				"provider name — that is how both of the last two of these happened.",
				spec.id, want, got)
		}
	}
}

// TestReasoningWireAgreementGuardHasTeeth pins that the guard above compares
// something. A green agreement check reads identically whether every provider
// agrees or the comparison is vacuous — which is precisely how the previous
// census stayed green over two live mismatches.
//
// The two known-good anchors are checked directly: anthropic must resolve to
// the anthropic wire and openai to openai-compat. If a refactor made
// ClientReasoningWire or ProviderReasoningWire return a constant, or made both
// return the same string for everything, these fail.
func TestReasoningWireAgreementGuardHasTeeth(t *testing.T) {
	cases := []struct{ providerID, want string }{
		{"anthropic", "anthropic"},
		{"openai", "openai-compat"},
		{"google", "gemini"},
		{"openai-codex", "codex"},
		{"amazon-bedrock", "none"},
		// The two that were wrong. Named explicitly so a regression reads as
		// "this exact bug came back" rather than as an anonymous table diff.
		{"vercel-ai-gateway", "anthropic"},
		{"azure-openai-responses", "openai-compat"},
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		r := Resolved{Provider: tc.providerID, Credential: "dummy-key", AuthMethod: "apikey", BaseURL: "https://example.invalid"}
		c := r.NewClient()
		if c == nil {
			t.Fatalf("%s: NewClient returned nil", tc.providerID)
		}
		got := provider.ClientReasoningWire(c)
		if got != tc.want {
			t.Errorf("%s: client speaks %q, want %q", tc.providerID, got, tc.want)
		}
		if table := provider.ProviderReasoningWire(tc.providerID); table != tc.want {
			t.Errorf("%s: table says %q, want %q", tc.providerID, table, tc.want)
		}
		seen[got] = true
	}
	// If everything collapsed to one string, the agreement check above proves
	// nothing at all.
	if len(seen) < 4 {
		t.Fatalf("expected several distinct wires across these providers, saw %d (%v) — "+
			"the wire lookup has probably collapsed to a constant", len(seen), seen)
	}
}
