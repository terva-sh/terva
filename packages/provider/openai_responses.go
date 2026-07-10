package provider

// OpenAI Responses API client (api.openai.com/v1/responses).
//
// Strategy: reuse the existing codexClient (which already speaks the
// Responses wire format) via a header-rewriting RoundTripper. The codex
// client always sends `chatgpt-account-id` and `openai-beta`; the public
// Responses API on api.openai.com accepts but doesn't require those —
// we strip the account-id header on its way out so the request looks
// like a normal authenticated OpenAI call.
//
// This is a separate provider from `openai` (which is Chat Completions);
// users opt in by passing `--provider openai-responses` or by picking a
// model whose catalog entry tags it under that provider id.
//
// Auth: API key via Authorization: Bearer (set by codexClient itself).
// Different from openai-codex, which uses OAuth subscription tokens.

import (
	"net/http"
	"strings"
)

const openaiResponsesDefaultBaseURL = "https://api.openai.com/v1/responses"

// openaiResponsesTransport rewrites headers so the codex client (which
// is designed for chatgpt.com OAuth tokens) can target the public
// OpenAI Responses API with a normal API key.
type openaiResponsesTransport struct {
	inner http.RoundTripper
}

func (t *openaiResponsesTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	// The codex client always sets chatgpt-account-id; api.openai.com
	// doesn't need it and may reject the OAuth identity headers, so
	// strip them.
	clone.Header.Del("chatgpt-account-id")
	clone.Header.Del("openai-beta")
	clone.Header.Del("originator")
	// Keep Authorization: Bearer <key> as set by the codex client.
	return t.inner.RoundTrip(clone)
}

// NewOpenAIResponses returns an OpenAI Responses-API client (API-key
// flow). Uses the same wire format as the ChatGPT Codex backend but
// with the public api.openai.com endpoint and standard Bearer auth.
//
// baseURL may be empty; defaults to https://api.openai.com/v1/responses.
func NewOpenAIResponses(apiKey, baseURL string) Client {
	if baseURL == "" {
		baseURL = openaiResponsesDefaultBaseURL
	}
	httpClient := &http.Client{
		Transport: &openaiResponsesTransport{inner: http.DefaultTransport},
		Timeout:   0,
	}
	inner := &codexClient{
		cred:      StaticCredential(apiKey),
		accountID: "", // unused; transport strips the header
		baseURL:   strings.TrimRight(baseURL, "/"),
		http:      httpClient,
	}
	return &renamedClient{inner: inner, name: "openai-responses"}
}
