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
// Every arm constructs the concrete client directly rather than taking a live
// Client, because a dump must work with no credential and no network. That is
// only sound while the builders stay pure functions of the request plus the
// client's own name — which is what buildRequestPurity's test asserts, so a
// builder that starts reading connection state fails there rather than silently
// dumping something the wire would never carry.
func wireBody(providerName, authMethod string, req Request) (any, string, error) {
	switch providerName {
	case "openai-codex":
		b, err := (&codexClient{}).buildRequest(req)
		return b, "input", err
	case "openai", "deepseek", "openai-compatible", "ollama":
		b, err := (&openaiClient{name: providerName}).buildRequest(req)
		return b, "messages", err
	case "kimi":
		// 🪤 Kimi Code is Kimi behind the ANTHROPIC Messages API, and the
		// registry builds it that way in every auth mode. It sat in the
		// OpenAI arm above and dumped a body terva has never sent it.
		//
		// Always non-oauth: kimi authenticates with x-api-key rather than
		// Bearer, so NewKimiCodingSourceWithHeaders leaves the client in
		// api-key mode even when the CREDENTIAL is a subscription token.
		// Honoring authMethod here would put Anthropic's identity block in a
		// kimi dump, which is the same class of lie in the other direction.
		// The name is what routes cost and catalog lookup to kimi's own rows.
		b, err := (&anthropicClient{name: "kimi"}).buildRequest(req)
		return b, "messages", err
	case "google":
		b, _, err := (&geminiClient{}).buildRequest(req)
		return b, "contents", err
	case "anthropic":
		// 🪤 The only provider whose MODE changes the body, which is why
		// authMethod exists at all. A subscription request carries the Claude
		// Code identity as its first system block — on its own cache
		// breakpoint — and renames tools to Anthropic's canonical casing. Dump
		// the api-key shape for an OAuth user and the two things most worth
		// looking at, the cached prefix and the tool names, are both wrong.
		b, err := (&anthropicClient{oauth: authMethod == "oauth"}).buildRequest(req)
		return b, "messages", err
	default:
		return nil, "", fmt.Errorf("wire dump is not implemented for provider %q (supported: openai-codex, openai, anthropic, google, kimi, deepseek, openai-compatible, ollama)", providerName)
	}
}
