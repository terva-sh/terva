package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// The ChatGPT subscription backend REJECTS prompt-cache lifetime parameters.
// Do not add them back.
//
// Measured 2026-08-04 against
// https://chatgpt.com/backend-api/codex/responses with a live subscription
// token, same request three ways:
//
//	no cache options        -> 200, response echoes "prompt_cache_retention":"24h"
//	prompt_cache_options    -> 400 {"detail":"Unsupported parameter: prompt_cache_options"}
//	prompt_cache_retention  -> 400 {"detail":"Unsupported parameter: prompt_cache_retention"}
//
// Two things follow. The parameter is a hard failure, not a no-op, so sending
// it would break every request on this backend rather than merely fail to help
// — a dead session traded for a slow one. And there was nothing to buy: the
// backend already applies 24h retention by default, which is 48x the 30m the
// public API documents as its only accepted value.
//
// The public developers.openai.com guide documents prompt_cache_options for
// GPT-5.6+, and that guide describes api.openai.com. terva does not talk to
// api.openai.com on this provider. A capability read off vendor docs for a
// different surface is not evidence about this one.
func TestCodexSendsNoPromptCacheLifetimeParameters(t *testing.T) {
	c := NewOpenAICodex("token", "acct", "https://example.test").(*codexClient)
	for _, model := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5"} {
		wire, err := c.buildRequest(Request{
			Model:    model,
			Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
		})
		if err != nil {
			t.Fatalf("%s: %v", model, err)
		}
		b, err := json.Marshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		// Asserted on the marshaled JSON rather than a struct field: an empty
		// object is still an unsupported parameter to a backend that takes
		// none, and a nil check cannot see the difference.
		for _, banned := range []string{"prompt_cache_options", "prompt_cache_retention"} {
			if strings.Contains(string(b), banned) {
				t.Errorf("%s: request carries %q, which this backend 400s:\n%s", model, banned, b)
			}
		}
	}
}
