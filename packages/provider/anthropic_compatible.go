package provider

// The operator's own Anthropic-Messages backend.
//
// `openai-compatible` has always let terva point at any server speaking OpenAI
// Chat Completions. This is its twin for the other wire terva already drives:
// a LiteLLM deployment in anthropic mode, a Bedrock/Vertex shim, a corporate
// gateway in front of Claude, or a local router. Same shape as that provider —
// a single shared slot in auth.json, plus as many NAMED endpoints in
// config.json as the operator wants, each its own provider id.
//
// The wire client is the same anthropicClient that serves anthropic, kimi,
// minimax and fireworks. What is new here is that the knobs those providers
// hardcode become the operator's: which header carries the key, which
// anthropic-version to claim, which betas to ask for, and whether to send
// cache_control at all. Those four are not decoration — each one is a backend
// that otherwise fails every turn with an error naming none of them.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// The two shared compatible slots: the provider ids whose backend the OPERATOR
// supplies rather than terva shipping an address for it.
const (
	// OpenAICompatProvider speaks OpenAI Chat Completions.
	OpenAICompatProvider = "openai-compatible"
	// AnthropicCompatProvider speaks the Anthropic Messages API.
	AnthropicCompatProvider = "anthropic-compatible"
)

// IsCompatProvider reports whether id is one of the shared compatible slots —
// the providers configured by describing an endpoint rather than by handing over
// a credential.
//
// It is what separates "has no credential, so it is not usable" from "needs no
// credential, and is usable the moment it has a base URL". Every caller that got
// that wrong reported a working local server as a provider the user was signed
// out of.
//
// Named endpoints are NOT included: they are their own provider ids and are
// recognised through config (see build.IsEndpointProvider), because only the
// config knows which names the operator has defined.
func IsCompatProvider(id string) bool {
	return id == OpenAICompatProvider || id == AnthropicCompatProvider
}

// anthropicCompatMaxOutput is the per-response cap stamped on a model
// discovered from an Anthropic-compatible endpoint.
//
// 🪤 Not cosmetic, and not optional. The Messages API requires max_tokens —
// anthRequest serialises it WITHOUT omitempty — and buildRequest falls back to
// the model's MaxOutput when the turn carries no budget. A discovered model
// with MaxOutput 0 therefore sends `"max_tokens": 0` and the server rejects
// EVERY turn. The OpenAI-compatible path never had to care: Chat Completions
// omits the field and the server picks its own default.
//
// 8192 matches what the openai-compatible slot's default model already gets
// (see the agent's LoadCompatModel). It is a floor to make the endpoint work,
// not a claim about the model — pin the real value per model in models.json.
const anthropicCompatMaxOutput = 8192

// AnthropicCompatOptions are the per-endpoint wire settings for an
// Anthropic-Messages-compatible backend. The zero value is what
// api.anthropic.com itself wants, so an endpoint that needs none of this
// configures none of it.
type AnthropicCompatOptions struct {
	// APIVersion overrides the `anthropic-version` header. Empty means
	// terva's compiled default.
	APIVersion string
	// Beta is the `anthropic-beta` header, comma-separated as Anthropic
	// spells it (e.g. "context-1m-2025-08-07").
	Beta string
	// BearerAuth carries the key as `authorization: Bearer` rather than
	// `x-api-key`. Gateways that front Claude with an OpenAI-style front door
	// commonly want this.
	BearerAuth bool
	// DisableCaching omits every cache_control breakpoint. Needed for servers
	// that validate the request body strictly rather than ignoring fields they
	// do not implement.
	DisableCaching bool
}

// IsZero reports whether these are the defaults — nothing the operator chose.
// Callers use it to tell "no options were supplied" from "options were supplied
// and they happen to be the defaults", which are the same request but different
// answers to "should I fall back to another source".
func (o AnthropicCompatOptions) IsZero() bool { return o == AnthropicCompatOptions{} }

// headers renders the options that travel as request headers.
func (o AnthropicCompatOptions) headers() map[string]string {
	if beta := strings.TrimSpace(o.Beta); beta != "" {
		return map[string]string{"anthropic-beta": beta}
	}
	return nil
}

// NewAnthropicCompatOpts builds an Anthropic-Messages client for a third-party
// or operator-run endpoint, identifying as `name`.
//
// It is NewAnthropicCompat plus the operator's wire settings. The existing
// constructor stays as it is: minimax, fireworks and vercel-ai-gateway are
// endpoints terva itself knows the shape of, and they have no operator to ask.
func NewAnthropicCompatOpts(name, apiKey, baseURL string, o AnthropicCompatOptions) Client {
	if baseURL == "" {
		baseURL = anthropicDefaultBaseURL
	}
	return &anthropicClient{
		cred:       StaticCredential(apiKey),
		baseURL:    strings.TrimRight(baseURL, "/"),
		name:       name,
		apiVersion: strings.TrimSpace(o.APIVersion),
		bearerAuth: o.BearerAuth,
		noCache:    o.DisableCaching,
		headers:    o.headers(),
		http:       &http.Client{Timeout: 0},
	}
}

// NewAnthropicCompatible is the shared `anthropic-compatible` slot: the one
// endpoint a login with no name writes, and the one a second such login
// replaces. Named endpoints go through NewAnthropicCompatOpts with their own id.
func NewAnthropicCompatible(apiKey, baseURL string, o AnthropicCompatOptions) Client {
	return NewAnthropicCompatOpts(AnthropicCompatProvider, apiKey, baseURL, o)
}

// DiscoverAnthropicCompatible lists the models an operator-configured
// Anthropic-compatible endpoint reports from GET {base}/v1/models.
//
// The response shape is Anthropic's — `data[].id` with an optional
// `display_name`, paged by `has_more`/`last_id` — which is what separates this
// from DiscoverOpenAICompatible beyond the auth header. Neither shape carries a
// context window, so every row falls back to defaultCtx exactly as the OpenAI
// path does.
//
// A server that does not implement /v1/models is a normal, working endpoint:
// this returns an error and the caller moves on with the default model the
// operator typed. That is the same degradation the OpenAI-compatible slot
// already accepts, and the reason the probe treats a 404 as reachable.
func DiscoverAnthropicCompatible(ctx context.Context, baseURL, key string, defaultCtx int, o AnthropicCompatOptions) ([]Model, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("empty base url")
	}
	rows, err := listAnthropicModels(ctx, baseURL, func(req *http.Request) {
		ApplyAnthropicCompatAuth(req, key, o)
	})
	if err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(rows))
	for _, d := range rows {
		if d.ID == "" {
			continue
		}
		display := d.DisplayName
		if display == "" {
			display = d.ID
		}
		out = append(out, Model{
			Provider:      AnthropicCompatProvider,
			ID:            d.ID,
			DisplayName:   display,
			ContextWindow: defaultCtx,
			// See anthropicCompatMaxOutput: zero here is a 400 on every turn.
			MaxOutput: anthropicCompatMaxOutput,
			BaseURL:   baseURL,
			Source:    "live",
			Caps:      visionCapsFromID(d.ID),
		})
	}
	return out, nil
}

// ApplyAnthropicCompatAuth sets the headers an Anthropic-compatible endpoint
// needs to answer at all. Shared by discovery and the login probe (which lives
// in the auth subpackage) so the two cannot disagree about what "reachable with
// this key" means — a probe that authenticated differently from the request it
// is vouching for would pass and then leave the first turn to fail with a 401
// the operator has just been told cannot happen.
func ApplyAnthropicCompatAuth(req *http.Request, key string, o AnthropicCompatOptions) {
	version := strings.TrimSpace(o.APIVersion)
	if version == "" {
		version = anthropicAPIVersion
	}
	req.Header.Set("anthropic-version", version)
	if beta := strings.TrimSpace(o.Beta); beta != "" {
		req.Header.Set("anthropic-beta", beta)
	}
	// Optional on purpose: a local router in front of a subscription, or a
	// gateway on a trusted network, commonly wants no key at all.
	if key == "" {
		return
	}
	if o.BearerAuth {
		req.Header.Set("authorization", "Bearer "+key)
		return
	}
	req.Header.Set("x-api-key", key)
}

// anthropicModelRow is one entry of an Anthropic /v1/models page.
type anthropicModelRow struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// listAnthropicModels pages GET {baseURL}/v1/models to exhaustion, letting the
// caller supply auth.
//
// Extracted so DiscoverAnthropic (first-party, x-api-key) and
// DiscoverAnthropicCompatible (operator's endpoint, either header) share one
// parser and one paging loop. They had every reason to drift: the compat path
// needs a configurable version header and an optional key, which is exactly the
// kind of difference that gets implemented by copying the function.
func listAnthropicModels(ctx context.Context, baseURL string, auth func(*http.Request)) ([]anthropicModelRow, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	var out []anthropicModelRow
	after := ""
	for {
		url := strings.TrimRight(baseURL, "/") + "/v1/models?limit=1000"
		if after != "" {
			url += "&after_id=" + after
		}
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}
		auth(req)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := readBodyCapped(resp.Body, maxDiscoveryBodyBytes)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("anthropic discover http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var page struct {
			Data    []anthropicModelRow `json:"data"`
			HasMore bool                `json:"has_more"`
			LastID  string              `json:"last_id"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("anthropic discover parse: %w", err)
		}
		out = append(out, page.Data...)
		if !page.HasMore || page.LastID == "" {
			break
		}
		after = page.LastID
	}
	return out, nil
}
