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
// It is a floor to make the endpoint work, not a claim about the model. A model
// whose id matches a first-party Claude row takes that row's real cap instead
// (see anthropicCompatCaps), so this applies only to an id terva has never
// heard of: a gateway's private alias, or a model newer than this build.
//
// 16384 rather than the original 8192. The floor is also a ceiling, because
// buildRequest clamps the turn's budget to MaxOutput, so 8192 silently
// truncated every long answer from a modern Claude backend.
//
// 🪤 The two failure modes are not alike, and the number is a bet between
// them. Too low truncates, quietly, on every long answer. Too high sends a
// max_tokens the server refuses, which fails the turn outright. Neither is
// safe, so "be conservative" gives no answer here: 8192 was not the cautious
// choice, it was the one whose damage nobody saw.
//
// This sat at 32768 first. A terva-review on PR #1300 pointed out that the
// value lands on every id with no first-party Claude row, which is exactly a
// gateway's private alias or a model newer than this build, and a backend
// capping under it turns from working to rejecting. 16384 halves that exposed
// band while staying at twice the old truncation point, and it stays well
// under the 64000 and 128000 the current families allow.
//
// It remains a guess about a model terva has never heard of. Pin the real
// value per model in models.json, which wins over this.
const anthropicCompatMaxOutput = 16384

// anthropicCompatCapabilities are the per-model facts an Anthropic /v1/models
// listing does not carry: whether the model thinks, which thinking mode it
// takes, and how much output it will emit.
type anthropicCompatCapabilities struct {
	Reasoning        bool
	AdaptiveThinking bool
	MaxOutput        int
}

// anthropicCompatCaps decides those facts for one discovered id, preferring
// the row terva already curates for the first-party Claude model of the same
// name.
//
// 🪤 The bug this closes is silent, which is why it survived. An Anthropic
// /v1/models page carries an id and a display name, nothing else, so every
// discovered row had Reasoning false. buildRequest gates the entire thinking
// block on that field, so a request went out with no `thinking` and no
// `output_config` however the operator set /reasoning. Nothing failed: the
// model still thought at whatever depth the backend defaults to, and the
// effort knob in the interface simply did nothing. Measured against one
// gateway in September 2026, an unset effort spent about 1700 thinking tokens
// on a prompt where "high" spent about 3400.
//
// Only capability flags travel. Prices stay at zero, because a gateway in
// front of a subscription charges nothing per token and inheriting Anthropic's
// list rates would invent a bill the operator never gets. The context window
// stays with the operator's endpoint config, which is the one number they did
// tell us.
func anthropicCompatCaps(known []Model, id string) anthropicCompatCapabilities {
	out := anthropicCompatCapabilities{MaxOutput: anthropicCompatMaxOutput}
	if m, err := findIn(known, "anthropic", id); err == nil {
		out.Reasoning = m.Reasoning
		out.AdaptiveThinking = m.AdaptiveThinking
		if m.MaxOutput > 0 {
			out.MaxOutput = m.MaxOutput
		}
		return out
	}
	out.Reasoning = claudeThinkingFromID(id)
	out.AdaptiveThinking = out.Reasoning && adaptiveThinkingFromID(id)
	return out
}

// claudeThinkingFromID reports whether an id names a Claude family that
// supports extended thinking, for a gateway id with no catalog row.
//
// The list names families rather than reading a version number, and it stays
// short on purpose. The two errors are not symmetric: a miss costs the operator
// the effort knob and nothing else, while a false hit sends a thinking block to
// a model that has none and the API rejects every turn.
func claudeThinkingFromID(id string) bool {
	l := strings.ToLower(id)
	for _, marker := range []string{
		"claude-3-7",
		"claude-sonnet-4", "claude-opus-4", "claude-haiku-4",
		"claude-sonnet-5", "claude-opus-5", "claude-haiku-5",
		"fable",
	} {
		if strings.Contains(l, marker) {
			return true
		}
	}
	return false
}

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
func NewAnthropicCompatOpts(name, apiKey, baseURL string, o AnthropicCompatOptions, opts ...ClientOption) Client {
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
		host:       applyClientOptions(opts),
	}
}

// NewAnthropicCompatible is the shared `anthropic-compatible` slot: the one
// endpoint a login with no name writes, and the one a second such login
// replaces. Named endpoints go through NewAnthropicCompatOpts with their own id.
func NewAnthropicCompatible(apiKey, baseURL string, o AnthropicCompatOptions, opts ...ClientOption) Client {
	return NewAnthropicCompatOpts(AnthropicCompatProvider, apiKey, baseURL, o, opts...)
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
//
// known is the catalog the discovered rows borrow capability flags from (see
// anthropicCompatCaps): the host's, passed in, since the wire holds none.
func DiscoverAnthropicCompatible(ctx context.Context, baseURL, key string, defaultCtx int, o AnthropicCompatOptions, known []Model) ([]Model, error) {
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
		caps := anthropicCompatCaps(known, d.ID)
		out = append(out, Model{
			Provider:      AnthropicCompatProvider,
			ID:            d.ID,
			DisplayName:   display,
			ContextWindow: defaultCtx,
			// See anthropicCompatMaxOutput: zero here is a 400 on every turn.
			MaxOutput:        caps.MaxOutput,
			Reasoning:        caps.Reasoning,
			AdaptiveThinking: caps.AdaptiveThinking,
			BaseURL:          baseURL,
			Source:           "live",
			Caps: mergeCaps(visionCapsFromID(d.ID), map[Capability]bool{
				CapReasoning: caps.Reasoning,
			}),
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
