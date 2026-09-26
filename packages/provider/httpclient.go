package provider

import (
	"crypto/tls"
	"net/http"
	"time"
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

// WithHTTPClient scopes an HTTP client to a concrete provider client: the
// OpenAI-compatible one, the Anthropic-Messages one, and the Gemini one that
// Vertex also rides. Any other client type is returned unchanged.
//
// Where the client already wraps its transport to exchange a credential for a
// short-lived token (GitHub Copilot, Vertex with a service account or an
// authorized user), httpClient goes UNDER that wrapper, and the exchange goes
// through it too. So a host's proxy, custom transport or test server sees
// every request the client makes for inference and for its token. Replacing
// the wrapper instead would send the raw credential with no exchange at all.
//
// ⚠️ A pollingUsageClient (openrouter, deepseek, kimi) keeps its own
// TLS-verifying client for the usage fetch, the safe default for terva's
// --insecure. That one request does not go through httpClient.
//
// 🪤 terva gates --insecure in build.go to the providers the OpenAI and
// Anthropic clients serve, and the two facts have to stay in step: extending
// the gate to a client this does not handle would accept the flag, report
// nothing, and leave the self-signed endpoint failing its TLS handshake.
func WithHTTPClient(c Client, httpClient *http.Client) Client {
	if httpClient == nil {
		return c
	}
	// Reach the concrete client through any wrapper layers (e.g.
	// pollingUsageClient around openrouter/deepseek/kimi, renamedClient
	// around Vertex).
	if v := innerOpenAI(c); v != nil {
		v.http = layerOver(v.http, httpClient)
	}
	if v := innerAnthropic(c); v != nil {
		v.http = layerOver(v.http, httpClient)
	}
	if v := innerGemini(c); v != nil {
		v.http = layerOver(v.http, httpClient)
	}
	return c
}

// tokenLayer is a transport that wraps another to add a short-lived token.
// over rebuilds it on a host's HTTP client, token cache included, so that
// both its requests and its token exchange use that client.
type tokenLayer interface {
	http.RoundTripper
	over(base *http.Client) http.RoundTripper
}

// layerOver returns the client to install when a host scopes host onto a
// client that currently uses cur: host itself, or a copy of host whose
// transport is cur's token layer rebuilt on host.
func layerOver(cur, host *http.Client) *http.Client {
	if cur != nil {
		if layer, ok := cur.Transport.(tokenLayer); ok {
			c := *host
			c.Transport = layer.over(host)
			return &c
		}
	}
	return host
}

// tokenExchangeTimeout bounds a token exchange when the client it runs on has
// no timeout of its own. The inference client has none, because a stream can
// run for minutes, and an exchange that hangs would stall the turn.
const tokenExchangeTimeout = 30 * time.Second

// exchangeClient derives the HTTP client for a token exchange from base.
func exchangeClient(base *http.Client) *http.Client {
	c := *base
	if c.Timeout == 0 {
		c.Timeout = tokenExchangeTimeout
	}
	return &c
}

// roundTripperOf is base's transport, or the default one when base has none,
// which is what http.Client itself would use.
func roundTripperOf(base *http.Client) http.RoundTripper {
	if base.Transport != nil {
		return base.Transport
	}
	return http.DefaultTransport
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

// innerAnthropic is innerOpenAI for the Anthropic-Messages client.
func innerAnthropic(c Client) *anthropicClient {
	for cur := c; cur != nil; {
		if v, ok := cur.(*anthropicClient); ok {
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

// innerGemini returns the *geminiClient at the core of c, as innerOpenAI does.
func innerGemini(c Client) *geminiClient {
	for cur := c; cur != nil; {
		if v, ok := cur.(*geminiClient); ok {
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
