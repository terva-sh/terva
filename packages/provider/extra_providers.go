package provider

// Extra third-party providers.
//
// Most are OpenAI Chat Completions–compatible, so they reuse `openaiClient`
// with a different name + base URL. A handful speak the Anthropic Messages
// API and reuse `anthropicClient` via newAnthropicCompat below.
//
// Providers with a non-trivial protocol (Bedrock Converse, Vertex SSE, Azure
// Responses, Mistral Conversations) are stubbed so the host wiring compiles.
// Filling those in is its own work — Bedrock needs SigV4 + Converse stream
// parsing, Vertex needs ADC, Azure needs the Responses API shape, Mistral
// needs its bespoke Conversations API.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ----------------------------------------------------------------------
// OpenAI Chat Completions–compatible providers. All of these just route
// Chat Completions to a different base URL with a different name. The
// default Authorization: Bearer header from openaiClient is correct for
// every one of them.
// ----------------------------------------------------------------------

// newOpenAICompat is the shared constructor for OpenAI-completions clones.
func newOpenAICompat(name, apiKey, baseURL, fallbackBaseURL string) Client {
	if baseURL == "" {
		baseURL = fallbackBaseURL
	}
	return &openaiClient{
		cred:    StaticCredential(apiKey),
		baseURL: strings.TrimRight(baseURL, "/"),
		name:    name,
		http:    &http.Client{Timeout: 0},
	}
}

// NewOpenAICompatibleAs is a Chat Completions client for a backend the OPERATOR
// supplied, identifying as `name`.
//
// 🪤 ollama, the shared openai-compatible slot, and every named OpenAI endpoint
// used to be built with NewOpenAI, which hardcodes the name "openai". Three
// separate backends therefore claimed to BE OpenAI, and the claim was not
// cosmetic:
//
//   - buildRequest forwards prompt_cache_key when `c.name == "openai"`, guarded
//     by a comment saying the parameter is for the real backend only because
//     "an unknown parameter risks a 400" elsewhere. The guard could never fire:
//     the LM Studio box it was protecting was called "openai" too.
//   - core.Agent looks up the model with FindModel(client.Name(), …) to decide
//     whether it may send an image, and an endpoint's discovered models are
//     stamped with the endpoint id, so the lookup missed.
//   - replaceForeignCompactions keys on the same name, so a compaction from a
//     different provider read as native.
//   - Every error, and the rescue picker behind it, named a vendor the request
//     never reached — the same failure the Anthropic client's own c.Name()
//     comment was written about.
//
// Rate-limit parsing is unaffected: rateLimitSpecFor defaults every unlisted
// provider to the standard spec, and a server that sends no x-ratelimit-*
// headers reports nothing under either name.
func NewOpenAICompatibleAs(name, apiKey, baseURL string) Client {
	return newOpenAICompat(name, apiKey, baseURL, openaiDefaultBaseURL)
}

// NewOpenAICompatible is the shared `openai-compatible` slot: the endpoint a
// login with no name writes. Named endpoints go through NewOpenAICompatibleAs
// with their own id, exactly as the Anthropic pair does.
func NewOpenAICompatible(apiKey, baseURL string) Client {
	return NewOpenAICompatibleAs(OpenAICompatProvider, apiKey, baseURL)
}

// NewMoonshot is the global Moonshot AI endpoint (Kimi-K2 family by id).
// Provider id is `moonshotai`.
//
// Unstable: a per-vendor constructor carries no promise before 1.0. The
// protocol clients (NewAnthropic, NewAnthropicCompatible, NewOpenAI,
// NewOpenAICompatible, NewOpenAIResponses, NewGemini) are the stable way to
// reach a model.
func NewMoonshot(apiKey, baseURL string) Client {
	return newOpenAICompat("moonshotai", apiKey, baseURL, "https://api.moonshot.ai/v1")
}

// NewMoonshotCN is the China-region Moonshot endpoint. Same model ids as
// the global flavor, different base URL.
//
// Unstable: a per-vendor constructor carries no promise before 1.0. The
// protocol clients (NewAnthropic, NewAnthropicCompatible, NewOpenAI,
// NewOpenAICompatible, NewOpenAIResponses, NewGemini) are the stable way to
// reach a model.
func NewMoonshotCN(apiKey, baseURL string) Client {
	return newOpenAICompat("moonshotai-cn", apiKey, baseURL, "https://api.moonshot.cn/v1")
}

// NewCerebras: ultra-fast inference (Llama/Qwen/GPT-OSS/GLM).
//
// Unstable: a per-vendor constructor carries no promise before 1.0. The
// protocol clients (NewAnthropic, NewAnthropicCompatible, NewOpenAI,
// NewOpenAICompatible, NewOpenAIResponses, NewGemini) are the stable way to
// reach a model.
func NewCerebras(apiKey, baseURL string) Client {
	return newOpenAICompat("cerebras", apiKey, baseURL, "https://api.cerebras.ai/v1")
}

// NewGroq: LPU inference (Llama/Kimi/Qwen/GPT-OSS).
//
// Unstable: a per-vendor constructor carries no promise before 1.0. The
// protocol clients (NewAnthropic, NewAnthropicCompatible, NewOpenAI,
// NewOpenAICompatible, NewOpenAIResponses, NewGemini) are the stable way to
// reach a model.
func NewGroq(apiKey, baseURL string) Client {
	return newOpenAICompat("groq", apiKey, baseURL, "https://api.groq.com/openai/v1")
}

// NewXAI: xAI Grok.
//
// Unstable: a per-vendor constructor carries no promise before 1.0. The
// protocol clients (NewAnthropic, NewAnthropicCompatible, NewOpenAI,
// NewOpenAICompatible, NewOpenAIResponses, NewGemini) are the stable way to
// reach a model.
func NewXAI(apiKey, baseURL string) Client {
	return newOpenAICompat("xai", apiKey, baseURL, "https://api.x.ai/v1")
}

// NewTogether: Together.ai aggregator.
//
// Unstable: a per-vendor constructor carries no promise before 1.0. The
// protocol clients (NewAnthropic, NewAnthropicCompatible, NewOpenAI,
// NewOpenAICompatible, NewOpenAIResponses, NewGemini) are the stable way to
// reach a model.
func NewTogether(apiKey, baseURL string) Client {
	return newOpenAICompat("together", apiKey, baseURL, "https://api.together.ai/v1")
}

// NewHuggingFace: HF inference router.
//
// Unstable: a per-vendor constructor carries no promise before 1.0. The
// protocol clients (NewAnthropic, NewAnthropicCompatible, NewOpenAI,
// NewOpenAICompatible, NewOpenAIResponses, NewGemini) are the stable way to
// reach a model.
func NewHuggingFace(apiKey, baseURL string) Client {
	return newOpenAICompat("huggingface", apiKey, baseURL, "https://router.huggingface.co/v1")
}

// NewZAI: Z.AI GLM family.
//
// Unstable: a per-vendor constructor carries no promise before 1.0. The
// protocol clients (NewAnthropic, NewAnthropicCompatible, NewOpenAI,
// NewOpenAICompatible, NewOpenAIResponses, NewGemini) are the stable way to
// reach a model.
func NewZAI(apiKey, baseURL string) Client {
	return newOpenAICompat("zai", apiKey, baseURL, "https://api.z.ai/api/coding/paas/v4")
}

// NewXiaomi: Xiaomi MiMo family (default endpoint).
//
// Unstable: a per-vendor constructor carries no promise before 1.0. The
// protocol clients (NewAnthropic, NewAnthropicCompatible, NewOpenAI,
// NewOpenAICompatible, NewOpenAIResponses, NewGemini) are the stable way to
// reach a model.
func NewXiaomi(apiKey, baseURL string) Client {
	return newOpenAICompat("xiaomi", apiKey, baseURL, "https://api.xiaomimimo.com/v1")
}

// NewXiaomiTokenPlan creates a regional Xiaomi token-plan client.
// region must be "ams", "cn", or "sgp", matching the three
// `xiaomi-token-plan-*` provider ids.
//
// Unstable: a per-vendor constructor carries no promise before 1.0. The
// protocol clients (NewAnthropic, NewAnthropicCompatible, NewOpenAI,
// NewOpenAICompatible, NewOpenAIResponses, NewGemini) are the stable way to
// reach a model.
func NewXiaomiTokenPlan(region, apiKey, baseURL string) Client {
	var fallback string
	var name string
	switch region {
	case "ams":
		fallback = "https://token-plan-ams.xiaomimimo.com/v1"
		name = "xiaomi-token-plan-ams"
	case "cn":
		fallback = "https://token-plan-cn.xiaomimimo.com/v1"
		name = "xiaomi-token-plan-cn"
	case "sgp":
		fallback = "https://token-plan-sgp.xiaomimimo.com/v1"
		name = "xiaomi-token-plan-sgp"
	default:
		panic(fmt.Sprintf("xiaomi token-plan: unknown region %q", region))
	}
	return newOpenAICompat(name, apiKey, baseURL, fallback)
}

// NewOpenRouter: OpenRouter aggregator. Unlocks dozens of upstream
// models with one key.
//
// Usage (/usage): wrapped in a pollingUsageClient that lazily fetches
// GET /api/v1/key (works with the normal inference key) for the key's
// credit limit/remaining + lifetime spend. The dialog renders it as
// `Credits`; no subscription windows.
//
// Unstable: a per-vendor constructor carries no promise before 1.0. The
// protocol clients (NewAnthropic, NewAnthropicCompatible, NewOpenAI,
// NewOpenAICompatible, NewOpenAIResponses, NewGemini) are the stable way to
// reach a model.
func NewOpenRouter(apiKey, baseURL string) Client {
	base := firstNonEmptyString(baseURL, openrouterDefaultBaseURL)
	inner := newOpenAICompat("openrouter", apiKey, base, openrouterDefaultBaseURL)
	return newPollingUsageClient(inner, usagePollTTL, fetchOpenRouterUsage(&http.Client{Timeout: 0}, apiKey, base))
}

// NewOpenCode and NewOpenCodeGo live in opencode.go. They are the one pair
// here that is not a bare newOpenAICompat call: the Zen gateway rejects a
// request with no `x-opencode-session` header, so both wire a per-
// conversation session id and terva's own user agent.

// ----------------------------------------------------------------------
// Anthropic Messages–compatible providers. These speak Anthropic's wire
// format but live behind a third party's base URL. They reuse
// anthropicClient with a custom name.
// ----------------------------------------------------------------------

// newAnthropicCompat returns an anthropicClient pinned to a non-default
// base URL and identifying as `name` for cost / logging purposes. Auth is
// API key (x-api-key header). For OAuth-fronted compatibles (rare) use
// NewAnthropicOAuthSource and rename via NameClient.
func newAnthropicCompat(name, apiKey, baseURL string, opts ...ClientOption) Client {
	if baseURL == "" {
		baseURL = anthropicDefaultBaseURL
	}
	return &anthropicClient{
		cred:    StaticCredential(apiKey),
		baseURL: strings.TrimRight(baseURL, "/"),
		name:    name,
		http:    &http.Client{Timeout: 0},
		host:    applyClientOptions(opts),
	}
}

// NewKimiCodingWithHeaders is the Kimi Code client: Kimi behind the
// Anthropic Messages API at https://api.kimi.com/coding (replaces the
// older OpenAI-completions-on-/coding/v1 wiring).
//
// Unstable: a per-vendor constructor carries no promise before 1.0. The
// protocol clients (NewAnthropic, NewAnthropicCompatible, NewOpenAI,
// NewOpenAICompatible, NewOpenAIResponses, NewGemini) are the stable way to
// reach a model.
func NewKimiCodingWithHeaders(apiKey, baseURL string, headers map[string]string, opts ...ClientOption) Client {
	return NewKimiCodingSourceWithHeaders(StaticCredential(apiKey), baseURL, headers, opts...)
}

// NewKimiCodingSourceWithHeaders is NewKimiCodingWithHeaders with a
// CredentialSource, so the subscription OAuth token can rotate without
// rebuilding the client. Kimi authenticates via x-api-key (not Bearer), so the
// client stays in non-oauth mode; only the credential value rotates.
//
// Unstable: a per-vendor constructor carries no promise before 1.0. The
// protocol clients (NewAnthropic, NewAnthropicCompatible, NewOpenAI,
// NewOpenAICompatible, NewOpenAIResponses, NewGemini) are the stable way to
// reach a model.
func NewKimiCodingSourceWithHeaders(cred CredentialSource, baseURL string, headers map[string]string, opts ...ClientOption) Client {
	if baseURL == "" {
		baseURL = "https://api.kimi.com/coding"
	}
	base := strings.TrimRight(baseURL, "/")
	inner := &anthropicClient{
		cred:    cred,
		baseURL: base,
		name:    "kimi",
		headers: headers,
		http:    &http.Client{Timeout: 0},
		host:    applyClientOptions(opts),
	}
	// Usage (/usage): Kimi Code reports subscription windows (5h rolling +
	// weekly) from a dedicated GET {base}/v1/usages — body-only, nothing in
	// response headers — so it takes the SAME pollingUsageClient wrapper as
	// OpenRouter/DeepSeek rather than the header-parse path baked into the
	// openai/codex clients. This is the anthropic-protocol proof that the poll
	// mechanism is decomposed from the wire format: the wrapper only calls
	// Stream/Name/Unwrap on its inner, and capability/reporter probes reach the
	// anthropic client through Unwrap, so nothing about kimi's behavior forks.
	return newPollingUsageClient(inner, usagePollTTL, fetchKimiUsage(&http.Client{Timeout: 0}, cred, base, headers))
}

// NewMinimaxAnthropic is the anthropic-messages flavor on
// api.minimax.io/anthropic, catalogued under provider=minimax.
//
// Unstable: a per-vendor constructor carries no promise before 1.0. The
// protocol clients (NewAnthropic, NewAnthropicCompatible, NewOpenAI,
// NewOpenAICompatible, NewOpenAIResponses, NewGemini) are the stable way to
// reach a model.
func NewMinimaxAnthropic(apiKey, baseURL string, opts ...ClientOption) Client {
	return newAnthropicCompat("minimax", apiKey, firstNonEmptyString(baseURL, "https://api.minimax.io/anthropic"), opts...)
}

// NewMinimaxCNAnthropic is the CN-region MiniMax (anthropic-messages).
//
// Unstable: a per-vendor constructor carries no promise before 1.0. The
// protocol clients (NewAnthropic, NewAnthropicCompatible, NewOpenAI,
// NewOpenAICompatible, NewOpenAIResponses, NewGemini) are the stable way to
// reach a model.
func NewMinimaxCNAnthropic(apiKey, baseURL string, opts ...ClientOption) Client {
	return newAnthropicCompat("minimax-cn", apiKey, firstNonEmptyString(baseURL, "https://api.minimaxi.com/anthropic"), opts...)
}

// NewFireworksAnthropic is the main Fireworks route. The
// anthropic-messages-compatible endpoint at api.fireworks.ai/inference
// expects Anthropic-style request bodies; use this rather than the
// OpenAI flavor.
//
// Unstable: a per-vendor constructor carries no promise before 1.0. The
// protocol clients (NewAnthropic, NewAnthropicCompatible, NewOpenAI,
// NewOpenAICompatible, NewOpenAIResponses, NewGemini) are the stable way to
// reach a model.
func NewFireworksAnthropic(apiKey, baseURL string, opts ...ClientOption) Client {
	return newAnthropicCompat("fireworks", apiKey, firstNonEmptyString(baseURL, "https://api.fireworks.ai/inference"), opts...)
}

// NewVercelGatewayAnthropic — Vercel AI Gateway anthropic-messages route.
//
// Unstable: a per-vendor constructor carries no promise before 1.0. The
// protocol clients (NewAnthropic, NewAnthropicCompatible, NewOpenAI,
// NewOpenAICompatible, NewOpenAIResponses, NewGemini) are the stable way to
// reach a model.
func NewVercelGatewayAnthropic(apiKey, baseURL string, opts ...ClientOption) Client {
	return newAnthropicCompat("vercel-ai-gateway", apiKey, firstNonEmptyString(baseURL, "https://ai-gateway.vercel.sh"), opts...)
}

// ----------------------------------------------------------------------
// Stubbed providers — bigger protocol surface, follow-up work.
// ----------------------------------------------------------------------

type unimplementedClient struct {
	name string
	hint string
	// wire is the reasoning wire of the client this stub STANDS IN FOR. A stub
	// is returned when ambient configuration is missing (no vertex project, no
	// Azure resource name, no copilot token), which means whether a provider
	// is checkable at all would otherwise depend on the machine running the
	// test. Declaring the wire here keeps the build-side agreement guard total:
	// google-vertex is a gemini-wire provider whether or not this developer has
	// GOOGLE_CLOUD_PROJECT set.
	wire reasoningWire
}

func (c *unimplementedClient) Name() string { return c.name }

func (c *unimplementedClient) Capabilities() ClientCapabilities {
	return ClientCapabilities{ReasoningWire: c.wire}
}

func (c *unimplementedClient) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	return nil, fmt.Errorf("provider %q not yet implemented: %s", c.name, c.hint)
}

// NewBedrock returns an AWS Bedrock client. See amazon_bedrock.go for the
// hand-rolled Converse-Stream wire-format parser. BedrockConfig says how cfg
// authenticates.
//
// Unstable: a per-vendor constructor carries no promise before 1.0. The
// protocol clients (NewAnthropic, NewAnthropicCompatible, NewOpenAI,
// NewOpenAICompatible, NewOpenAIResponses, NewGemini) are the stable way to
// reach a model.
func NewBedrock(cfg BedrockConfig, baseURL string) Client {
	return newBedrockClient(cfg, baseURL)
}

// NewGoogleVertex returns a Vertex AI client. See google_vertex.go for
// the full auth + URL-rewrite implementation, and VertexConfig for what it
// needs.
//
// Unstable: a per-vendor constructor carries no promise before 1.0. The
// protocol clients (NewAnthropic, NewAnthropicCompatible, NewOpenAI,
// NewOpenAICompatible, NewOpenAIResponses, NewGemini) are the stable way to
// reach a model.
func NewGoogleVertex(v VertexConfig, opts ...ClientOption) Client {
	return newVertex(v, opts...)
}

// NewAzureOpenAIResponses delegates to the real Azure OpenAI client.
// Despite the provider id mentioning "responses", terva uses Azure's
// Chat Completions endpoint (older but functionally complete for our
// agent loop) to avoid duplicating the full openai-responses wire
// client. Models register under provider id `azure-openai-responses`
// so user catalogs keep working unchanged.
//
// Unstable: a per-vendor constructor carries no promise before 1.0. The
// protocol clients (NewAnthropic, NewAnthropicCompatible, NewOpenAI,
// NewOpenAICompatible, NewOpenAIResponses, NewGemini) are the stable way to
// reach a model.
func NewAzureOpenAIResponses(apiKey, baseURL string, cfg AzureOpenAIConfig) Client {
	return newAzureOpenAI(apiKey, baseURL, cfg)
}

// NewMistral returns a Mistral client using their OpenAI-compatible Chat
// Completions endpoint at https://api.mistral.ai/v1. Mistral also offers a
// bespoke "Conversations" API, but the OpenAI-compat endpoint supports
// the same models with tool calling and streaming, so we use that for
// simplicity (no extra wire format to maintain).
//
// Unstable: a per-vendor constructor carries no promise before 1.0. The
// protocol clients (NewAnthropic, NewAnthropicCompatible, NewOpenAI,
// NewOpenAICompatible, NewOpenAIResponses, NewGemini) are the stable way to
// reach a model.
func NewMistral(apiKey, baseURL string) Client {
	return newOpenAICompat("mistral", apiKey, baseURL, "https://api.mistral.ai/v1")
}

// Cloudflare endpoints carry `{CLOUDFLARE_ACCOUNT_ID}` and (for the AI
// Gateway) `{CLOUDFLARE_GATEWAY_ID}` placeholders, filled from a
// CloudflareConfig at client-construction time. Workers AI uses standard
// `Authorization: Bearer <key>`; AI Gateway uses `cf-aig-authorization`
// (the upstream Authorization header is passed through to whichever
// downstream provider the gateway forwards to).

func resolveCloudflareURL(template string, cfg CloudflareConfig) (string, error) {
	out := template
	for _, p := range []struct{ placeholder, value, field, hint string }{
		{"{CLOUDFLARE_ACCOUNT_ID}", cfg.AccountID, "AccountID", cfg.AccountIDHint},
		{"{CLOUDFLARE_GATEWAY_ID}", cfg.GatewayID, "GatewayID", cfg.GatewayIDHint},
	} {
		if !strings.Contains(out, p.placeholder) {
			continue
		}
		if p.value == "" {
			if p.hint != "" {
				return "", errors.New(p.hint)
			}
			return "", fmt.Errorf("CloudflareConfig.%s is required but not set", p.field)
		}
		out = strings.ReplaceAll(out, p.placeholder, p.value)
	}
	return out, nil
}

// NewCloudflareWorkersAI returns an OpenAI-compatible client pinned to
// the Workers AI base URL with the account ID substituted. Returns an
// erroring client (deferred error on first Stream call) when an ID the URL
// needs is missing, so the constructor signature stays the same.
//
// Unstable: a per-vendor constructor carries no promise before 1.0. The
// protocol clients (NewAnthropic, NewAnthropicCompatible, NewOpenAI,
// NewOpenAICompatible, NewOpenAIResponses, NewGemini) are the stable way to
// reach a model.
func NewCloudflareWorkersAI(apiKey, baseURL string, cfg CloudflareConfig) Client {
	if baseURL == "" {
		baseURL = "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1"
	}
	resolved, err := resolveCloudflareURL(baseURL, cfg)
	if err != nil {
		return &unimplementedClient{name: "cloudflare-workers-ai", hint: err.Error(), wire: reasoningWireOpenAICompat}
	}
	return newOpenAICompat("cloudflare-workers-ai", apiKey, resolved, "")
}

// NewCloudflareAIGateway returns the AI Gateway client (OpenAI compat
// route). Sends `cf-aig-authorization` instead of `Authorization` so
// the gateway authenticates the caller (downstream-provider auth is
// configured per-gateway in the Cloudflare dashboard).
//
// Unstable: a per-vendor constructor carries no promise before 1.0. The
// protocol clients (NewAnthropic, NewAnthropicCompatible, NewOpenAI,
// NewOpenAICompatible, NewOpenAIResponses, NewGemini) are the stable way to
// reach a model.
func NewCloudflareAIGateway(apiKey, baseURL string, cfg CloudflareConfig) Client {
	if baseURL == "" {
		baseURL = "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/compat"
	}
	resolved, err := resolveCloudflareURL(baseURL, cfg)
	if err != nil {
		return &unimplementedClient{name: "cloudflare-ai-gateway", hint: err.Error(), wire: reasoningWireOpenAICompat}
	}
	return &openaiClient{
		cred:    StaticCredential(apiKey),
		baseURL: strings.TrimRight(resolved, "/"),
		name:    "cloudflare-ai-gateway",
		headers: map[string]string{"cf-aig-authorization": "Bearer " + apiKey},
		http:    &http.Client{Timeout: 0},
	}
}

// NewGithubCopilot returns a GitHub Copilot client. The provided
// credential must be a GitHub Personal Access Token (PAT) with Copilot
// access enabled; terva trades it for a short-lived Copilot token on
// every inference request (cached in memory until ~5min before expiry).
//
// Wire format: OpenAI Chat Completions. Copilot-specific headers
// (X-Initiator, Openai-Intent, Editor-Version, Editor-Plugin-Version,
// Copilot-Integration-Id, User-Agent) are added by the refresh
// transport. The model id passes through unchanged.
//
// baseURL is ignored: the canonical host is read from `proxy-ep=...` in
// the short-lived token's value.
//
// Unstable: a per-vendor constructor carries no promise before 1.0. The
// protocol clients (NewAnthropic, NewAnthropicCompatible, NewOpenAI,
// NewOpenAICompatible, NewOpenAIResponses, NewGemini) are the stable way to
// reach a model.
func NewGithubCopilot(apiKey, _ string) Client {
	if apiKey == "" {
		return &unimplementedClient{name: "github-copilot", hint: "set COPILOT_GITHUB_TOKEN", wire: reasoningWireOpenAICompat}
	}
	return newGithubCopilotClient(apiKey)
}

// ----------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------

func firstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
