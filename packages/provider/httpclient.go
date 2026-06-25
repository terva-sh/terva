package provider

import (
	"crypto/tls"
	"net/http"
)

// NewHTTPClient returns a provider HTTP client. When insecureTLS is true,
// ONLY this client skips TLS certificate verification — the process-wide
// http.DefaultTransport is left untouched, so auth, model discovery, and
// every other provider keep normal certificate validation. The insecure
// transport is a clone of the default, so timeouts/proxies are preserved.
func NewHTTPClient(insecureTLS bool) *http.Client {
	if !insecureTLS {
		return &http.Client{Timeout: 0}
	}
	tr, ok := http.DefaultTransport.(*http.Transport)
	if ok {
		tr = tr.Clone()
	} else {
		tr = &http.Transport{}
	}
	if tr.TLSClientConfig != nil {
		tr.TLSClientConfig = tr.TLSClientConfig.Clone()
	} else {
		tr.TLSClientConfig = &tls.Config{}
	}
	tr.TLSClientConfig.InsecureSkipVerify = true //nolint:gosec // opt-in via --insecure, scoped to an explicit --base-url
	return &http.Client{Timeout: 0, Transport: tr}
}

// WithHTTPClient scopes an HTTP client to a concrete provider client.
// Only the OpenAI-compatible client (used by the openai-compatible and
// ollama providers) is handled, because --insecure is gated to exactly
// those providers in build.go — those are plain http.Client-based clients
// with no wrapped transport. Any other client type is returned unchanged
// so it keeps normal certificate verification (fail-safe: a naive swap of
// a wrapped-transport provider would otherwise silently bypass nothing or
// miss the inner transport).
func WithHTTPClient(c Client, httpClient *http.Client) Client {
	if httpClient == nil {
		return c
	}
	// Reach the concrete openai-compatible client through any wrapper layers
	// (e.g. pollingUsageClient around openrouter/deepseek) and point its
	// transport at the scoped --insecure client. The wrapper itself keeps a
	// normal TLS-verifying client for its own usage fetch (the safe default).
	if v := innerOpenAI(c); v != nil {
		v.http = httpClient
	}
	return c
}

// innerOpenAI returns the *openaiClient at the core of c, looking through any
// wrapper layers (pollingUsageClient, …). nil when c is not backed by one.
func innerOpenAI(c Client) *openaiClient {
	for cur := c; cur != nil; {
		if v, ok := cur.(*openaiClient); ok {
			return v
		}
		u, ok := cur.(unwrapper)
		if !ok {
			break
		}
		cur = u.Unwrap()
	}
	return nil
}
