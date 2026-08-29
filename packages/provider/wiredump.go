package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// DumpRequestJSONL renders req exactly as providerName would put it on the
// wire, as newline-delimited JSON: one header line carrying every field except
// the input array, then one line per input item, verbatim.
//
// The line-per-item shape is the entire point, and it is not cosmetic. A
// provider prompt cache matches on an exact byte PREFIX, so the only question
// worth asking of two requests is "which item is the first that differs" — and
// that is `diff a.jsonl b.jsonl`, reading the first changed line number. As one
// pretty-printed blob the same question is a manual scan of several hundred
// kilobytes, which is why the cache investigation could establish that terva's
// own message list was append-only and never that the SERIALIZED body was.
//
// Deliberately no per-line index: a pure append leaves every earlier line
// byte-identical, so diff reports the minimal edit and the first differing line
// IS the first differing item. Numbering the lines would renumber the whole
// file whenever an item was inserted early, turning the one signal worth having
// into noise.
//
// 🪤 That append-only property is the OpenAI/Codex wire's, not a guarantee of
// this function. Anthropic marks its cache breakpoint on the last user message,
// so appending a turn moves the mark and rewrites the line that used to carry
// it — one line of expected churn on every diff, before any real change. See
// TestDumpRequestJSONLAnthropicRewritesOnlyTheCacheBreakpoint, which pins that
// to the breakpoint alone.
//
// authMethod ("apikey" | "oauth" | "") selects the auth MODE to build for. It
// is not a credential and nothing is resolved from it — but on Anthropic the
// mode changes the body itself (see wireBody), so a dump that ignored it would
// show a subscription user a request they never send.
//
// This builds the body only. It opens no connection, needs no credential, and
// is safe to run against a session file that is in use.
func DumpRequestJSONL(providerName, authMethod string, req Request) ([]byte, error) {
	body, inputField, err := wireBody(providerName, authMethod, req)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal %s request: %w", providerName, err)
	}
	// Round-trip through a map so the input array can be lifted out without
	// each provider's body struct needing to know it is being dumped.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("re-read %s request: %w", providerName, err)
	}
	items := []json.RawMessage{}
	if in, ok := obj[inputField]; ok {
		if err := json.Unmarshal(in, &items); err != nil {
			return nil, fmt.Errorf("%s request field %q is not an array: %w", providerName, inputField, err)
		}
		delete(obj, inputField)
	}

	var out bytes.Buffer
	// Marshaling a map sorts keys, so the header is stable across dumps and
	// never shows up in a diff for a reason that is not a real change.
	head, err := json.Marshal(map[string]any{
		"_dump":     "header",
		"_provider": providerName,
		"_field":    inputField,
		"request":   obj,
	})
	if err != nil {
		return nil, err
	}
	out.Write(head)
	out.WriteByte('\n')
	for _, it := range items {
		// Compact so an item that a provider struct marshals with different
		// whitespace cannot read as a content change.
		var c bytes.Buffer
		if err := json.Compact(&c, it); err != nil {
			return nil, err
		}
		out.Write(c.Bytes())
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}

// wireBody builds the provider-specific request body and names the field that
// holds the input array.
//
// The arm is chosen by the WIRE the provider's client speaks, never by the
// provider id. A hand-written list of ids stood here and was a third copy of a
// mapping this package already keeps once. It named eight ids, and the tree has
// far more than eight: groq, xai, openrouter, mistral, azure, github-copilot,
// together, cerebras, zai and openai-responses all answered "not implemented"
// while speaking a wire dumped right here for somebody else. Every named
// endpoint answered the same way, and no fixed list could ever have held them,
// because an endpoint's id is whatever the operator typed at /login.
//
// reasoningWireFamily is that single mapping. It is census-guarded, and
// TestReasoningWireTableMatchesTheRealClient checks it against the client the
// registry actually CONSTRUCTS, with no escape set — so an arm reached through
// it cannot drift from the client that would really serialize the turn. Its
// default is the OpenAI-compatible wire, which is correct by construction for a
// named endpoint: registerEndpointLocked builds every one with provider.NewOpenAI.
//
// Every arm constructs the concrete client directly rather than taking a live
// Client, because a dump must work with no credential and no network. That is
// only sound while the builders stay pure functions of the request plus the
// client's own name — which is what buildRequestPurity's test asserts, so a
// builder that starts reading connection state fails there rather than silently
// dumping something the wire would never carry.
//
// 🪤 providerName is handed to the client it builds, and that is not cosmetic.
// anthropicClient.buildRequest resolves its model with FindModel(c.Name(), …)
// and feeds the result to enforceImageInput, so the name decides which catalog
// row is found, whether images survive the turn, and how max_tokens is clamped.
// Passing a "close enough" id here would dump a body the wire never carries.
func wireBody(providerName, authMethod string, req Request) (any, string, error) {
	switch wire := reasoningWireFamily(providerName); wire {
	case reasoningWireCodex:
		// openai-codex and openai-responses both land here, and both are
		// faithful. NewOpenAIResponses wraps a codexClient in a renamedClient,
		// which overrides Name() on the WRAPPER only — the inner builder reports
		// "openai-codex" in production exactly as this bare one does.
		b, err := (&codexClient{}).buildRequest(req)
		return b, "input", err

	case reasoningWireGemini:
		// google and google-vertex alike: geminiClient carries no name of its
		// own and resolves FindModel("google", …), and google-vertex is that
		// same client behind a renamedClient.
		b, _, err := (&geminiClient{}).buildRequest(req)
		return b, "contents", err

	case reasoningWireAnthropic:
		// 🪤 anthropic is the only provider whose MODE changes the body, which
		// is why authMethod exists at all. A subscription request carries the
		// Claude Code identity as its first system block — on its own cache
		// breakpoint — and renames tools to Anthropic's canonical casing. Dump
		// the api-key shape for an OAuth user and the two things most worth
		// looking at, the cached prefix and the tool names, are both wrong.
		//
		// 🪤 The third parties on this wire (kimi, minimax, minimax-cn,
		// fireworks, vercel-ai-gateway) are always non-oauth. Kimi Code is Kimi
		// behind the ANTHROPIC Messages API and authenticates with x-api-key
		// rather than Bearer, so the registry leaves the client in api-key mode
		// even when the CREDENTIAL is a subscription token. Honouring
		// authMethod for them would put Anthropic's identity block in a kimi
		// dump, which is the same class of lie in the other direction.
		b, err := (&anthropicClient{
			name:  providerName,
			oauth: providerName == "anthropic" && authMethod == "oauth",
		}).buildRequest(req)
		return b, "messages", err

	case reasoningWireOpenAICompat:
		b, err := (&openaiClient{name: providerName}).buildRequest(req)
		return b, "messages", err

	default:
		// reasoningWireNone (amazon-bedrock) and the unknown zero value.
		//
		// This is the one seam in routing a body dump through a REASONING
		// classifier: "none" says the provider takes no reasoning knob, which
		// is a different claim from "has no request body". bedrockClient has a
		// buildRequest like everyone else. Bedrock keeps the answer it has
		// always given here; giving it a real one means its own arm, and that
		// is deliberately not smuggled into this change.
		return nil, "", fmt.Errorf("wire dump is not implemented for provider %q (its client speaks the %q wire, which has no arm here)", providerName, wire)
	}
}
