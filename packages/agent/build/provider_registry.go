package build

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider"
)

// providerSpec is one row of the provider registry (roadmap B4): the
// single source of truth for everything terva keys on a provider id.
// It replaces five parallel switches that had to be kept in sync by
// hand — NewClient dispatch, default model, the known-provider list,
// the alias map, and the env-var hint — plus the env-var lookup half
// of credential resolution. Adding a provider is now one entry here
// instead of edits scattered across go and config.go.
type providerSpec struct {
	// id is the canonical provider id.
	id string
	// aliases are alternate spellings users reach for ("bedrock" →
	// "amazon-bedrock"); canonicalProvider resolves them to id.
	aliases []string
	// defaultModel is the model terva picks when the caller didn't.
	// Empty means "use the global provider.DefaultModel"; set
	// noDefaultModel for providers that genuinely have none (ollama,
	// openai-compatible), where the caller must supply one.
	defaultModel   string
	noDefaultModel bool
	// envHint is the short token shown in "set <X>_API_KEY" guidance
	// when no credential is found. Empty falls back to "ANTHROPIC".
	envHint string
	// apiKeyEnv lists the environment variables checked, in order, for
	// an API Key (auth method "apikey"). nil for providers with no env
	// Key path (codex subscription, ollama, bedrock's bespoke AWS
	// resolution, openai-compatible).
	apiKeyEnv []string
	// newClient builds the provider.Client for a resolved credential
	// set. Lives here rather than the provider package because it
	// needs Resolved + the OAuth refresh wrapper, which are agent-side.
	newClient func(r Resolved) provider.Client
}

// providerSpecs is the registry, ordered. Order is meaningful: the
// auto-fallback path (Resolve) walks it to pick the first logged-in
// provider, so anthropic-first / cloud-gateways-last is deliberate.
var providerSpecs = []providerSpec{
	{
		id:      "anthropic",
		envHint: "ANTHROPIC",
		// ANTHROPIC_OAUTH_TOKEN is handled specially in
		// ResolveCredentialFull (it yields method "oauth"); the
		// apikey env is listed here.
		apiKeyEnv: []string{"ANTHROPIC_API_KEY"},
		newClient: func(r Resolved) provider.Client {
			if r.AuthMethod == "oauth" {
				return provider.NewAnthropicOAuthSource(r.credentialSource(), r.BaseURL)
			}
			return provider.NewAnthropic(r.Credential, r.BaseURL)
		},
	},
	{
		id:           "openai",
		defaultModel: "gpt-5",
		envHint:      "OPENAI",
		apiKeyEnv:    []string{"OPENAI_API_KEY"},
		newClient:    func(r Resolved) provider.Client { return provider.NewOpenAI(r.Credential, r.BaseURL) },
	},
	{
		id:           "openai-codex",
		aliases:      []string{"codex"},
		defaultModel: "gpt-5.5",
		envHint:      "OPENAI",
		// No apiKeyEnv: the ChatGPT/Codex subscription route
		// intentionally ignores OPENAI_API_KEY so both can coexist.
		newClient: func(r Resolved) provider.Client {
			return provider.NewOpenAICodexSource(r.credentialSource(), r.AccountID, r.BaseURL)
		},
	},
	{
		id:           "openai-responses",
		defaultModel: "gpt-5",
		envHint:      "OPENAI",
		apiKeyEnv:    []string{"OPENAI_API_KEY"},
		newClient:    func(r Resolved) provider.Client { return provider.NewOpenAIResponses(r.Credential, r.BaseURL) },
	},
	{
		id:           "kimi",
		aliases:      []string{"kimi-code"},
		defaultModel: "kimi-for-coding",
		envHint:      "KIMI",
		apiKeyEnv:    []string{"KIMI_API_KEY", "MOONSHOT_API_KEY"},
		newClient: func(r Resolved) provider.Client {
			// kimi-coding speaks anthropic-messages on
			// api.kimi.com/coding; subscription OAuth wraps the same
			// Anthropic-shaped client.
			if r.AuthMethod == "oauth" {
				return provider.NewKimiCodingSourceWithHeaders(r.credentialSource(), r.BaseURL, kimiCodeHeaders())
			}
			return provider.NewKimiCodingWithHeaders(r.Credential, r.BaseURL, kimiCodeHeaders())
		},
	},
	{
		id:           "deepseek",
		defaultModel: "deepseek-v4-pro",
		envHint:      "DEEPSEEK",
		apiKeyEnv:    []string{"DEEPSEEK_API_KEY"},
		newClient:    func(r Resolved) provider.Client { return provider.NewDeepSeek(r.Credential, r.BaseURL) },
	},
	{
		id:           "google",
		aliases:      []string{"gemini", "googleai", "google-ai"},
		defaultModel: "gemini-2.5-pro",
		envHint:      "GEMINI",
		apiKeyEnv:    []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		newClient:    func(r Resolved) provider.Client { return provider.NewGemini(r.Credential, r.BaseURL) },
	},
	{
		id:             "ollama",
		noDefaultModel: true,
		envHint:        "OLLAMA",
		newClient:      func(r Resolved) provider.Client { return provider.NewOpenAI(r.Credential, r.BaseURL) },
	},
	{
		id:           "moonshotai",
		aliases:      []string{"moonshot"},
		defaultModel: "kimi-k2.6",
		envHint:      "MOONSHOT",
		apiKeyEnv:    []string{"MOONSHOT_API_KEY"},
		newClient:    func(r Resolved) provider.Client { return provider.NewMoonshot(r.Credential, r.BaseURL) },
	},
	{
		id:           "moonshotai-cn",
		defaultModel: "kimi-k2.6",
		envHint:      "MOONSHOT",
		apiKeyEnv:    []string{"MOONSHOT_API_KEY"},
		newClient:    func(r Resolved) provider.Client { return provider.NewMoonshotCN(r.Credential, r.BaseURL) },
	},
	{
		id:           "cerebras",
		defaultModel: "qwen-3-235b-a22b-instruct-2507",
		envHint:      "CEREBRAS",
		apiKeyEnv:    []string{"CEREBRAS_API_KEY"},
		newClient:    func(r Resolved) provider.Client { return provider.NewCerebras(r.Credential, r.BaseURL) },
	},
	{
		id:           "groq",
		defaultModel: "llama-3.3-70b-versatile",
		envHint:      "GROQ",
		apiKeyEnv:    []string{"GROQ_API_KEY"},
		newClient:    func(r Resolved) provider.Client { return provider.NewGroq(r.Credential, r.BaseURL) },
	},
	{
		id:           "xai",
		defaultModel: "grok-code-fast-1",
		envHint:      "XAI",
		apiKeyEnv:    []string{"XAI_API_KEY"},
		newClient:    func(r Resolved) provider.Client { return provider.NewXAI(r.Credential, r.BaseURL) },
	},
	{
		id:           "together",
		defaultModel: "Qwen/Qwen3-Coder-480B-A35B-Instruct",
		envHint:      "TOGETHER",
		apiKeyEnv:    []string{"TOGETHER_API_KEY"},
		newClient:    func(r Resolved) provider.Client { return provider.NewTogether(r.Credential, r.BaseURL) },
	},
	{
		id:           "huggingface",
		aliases:      []string{"hf"},
		defaultModel: "moonshotai/Kimi-K2-Instruct",
		envHint:      "HF",
		apiKeyEnv:    []string{"HF_TOKEN"},
		newClient:    func(r Resolved) provider.Client { return provider.NewHuggingFace(r.Credential, r.BaseURL) },
	},
	{
		id:           "openrouter",
		defaultModel: "anthropic/claude-sonnet-4.5",
		envHint:      "OPENROUTER",
		apiKeyEnv:    []string{"OPENROUTER_API_KEY"},
		newClient:    func(r Resolved) provider.Client { return provider.NewOpenRouter(r.Credential, r.BaseURL) },
	},
	{
		id:           "mistral",
		defaultModel: "mistral-large-latest",
		envHint:      "MISTRAL",
		apiKeyEnv:    []string{"MISTRAL_API_KEY"},
		newClient:    func(r Resolved) provider.Client { return provider.NewMistral(r.Credential, r.BaseURL) },
	},
	{
		id:           "zai",
		defaultModel: "glm-4.7",
		envHint:      "ZAI",
		apiKeyEnv:    []string{"ZAI_API_KEY"},
		newClient:    func(r Resolved) provider.Client { return provider.NewZAI(r.Credential, r.BaseURL) },
	},
	{
		id:           "xiaomi",
		defaultModel: "mimo-v2.5",
		envHint:      "XIAOMI",
		apiKeyEnv:    []string{"XIAOMI_API_KEY"},
		newClient:    func(r Resolved) provider.Client { return provider.NewXiaomi(r.Credential, r.BaseURL) },
	},
	{
		id:           "xiaomi-token-plan-ams",
		defaultModel: "mimo-v2.5",
		envHint:      "XIAOMI_TOKEN_PLAN_AMS",
		apiKeyEnv:    []string{"XIAOMI_TOKEN_PLAN_AMS_API_KEY"},
		newClient: func(r Resolved) provider.Client {
			return provider.NewXiaomiTokenPlan("ams", r.Credential, r.BaseURL)
		},
	},
	{
		id:           "xiaomi-token-plan-cn",
		defaultModel: "mimo-v2.5",
		envHint:      "XIAOMI_TOKEN_PLAN_CN",
		apiKeyEnv:    []string{"XIAOMI_TOKEN_PLAN_CN_API_KEY"},
		newClient: func(r Resolved) provider.Client {
			return provider.NewXiaomiTokenPlan("cn", r.Credential, r.BaseURL)
		},
	},
	{
		id:           "xiaomi-token-plan-sgp",
		defaultModel: "mimo-v2.5",
		envHint:      "XIAOMI_TOKEN_PLAN_SGP",
		apiKeyEnv:    []string{"XIAOMI_TOKEN_PLAN_SGP_API_KEY"},
		newClient: func(r Resolved) provider.Client {
			return provider.NewXiaomiTokenPlan("sgp", r.Credential, r.BaseURL)
		},
	},
	{
		id:           "minimax",
		defaultModel: "MiniMax-M2.7",
		envHint:      "MINIMAX",
		apiKeyEnv:    []string{"MINIMAX_API_KEY"},
		newClient:    func(r Resolved) provider.Client { return provider.NewMinimaxAnthropic(r.Credential, r.BaseURL) },
	},
	{
		id:           "minimax-cn",
		defaultModel: "MiniMax-M2.7",
		envHint:      "MINIMAX_CN",
		apiKeyEnv:    []string{"MINIMAX_CN_API_KEY", "MINIMAX_API_KEY"},
		newClient:    func(r Resolved) provider.Client { return provider.NewMinimaxCNAnthropic(r.Credential, r.BaseURL) },
	},
	{
		id:           "fireworks",
		defaultModel: "accounts/fireworks/models/kimi-k2p6",
		envHint:      "FIREWORKS",
		apiKeyEnv:    []string{"FIREWORKS_API_KEY"},
		newClient:    func(r Resolved) provider.Client { return provider.NewFireworksAnthropic(r.Credential, r.BaseURL) },
	},
	{
		id:           "vercel-ai-gateway",
		aliases:      []string{"ai-gateway", "vercel"},
		defaultModel: "anthropic/claude-sonnet-4.5",
		envHint:      "AI_GATEWAY",
		apiKeyEnv:    []string{"AI_GATEWAY_API_KEY"},
		newClient: func(r Resolved) provider.Client {
			return provider.NewVercelGatewayAnthropic(r.Credential, r.BaseURL)
		},
	},
	{
		id:           "opencode",
		defaultModel: "claude-sonnet-4-5",
		envHint:      "OPENCODE",
		apiKeyEnv:    []string{"OPENCODE_API_KEY"},
		newClient:    func(r Resolved) provider.Client { return provider.NewOpenCode(r.Credential, r.BaseURL) },
	},
	{
		id:           "opencode-go",
		defaultModel: "kimi-k2.6",
		envHint:      "OPENCODE",
		apiKeyEnv:    []string{"OPENCODE_API_KEY"},
		newClient:    func(r Resolved) provider.Client { return provider.NewOpenCodeGo(r.Credential, r.BaseURL) },
	},
	{
		id:           "amazon-bedrock",
		aliases:      []string{"bedrock", "aws-bedrock", "amazon"},
		defaultModel: "anthropic.claude-sonnet-4-5-20250929-v1:0",
		envHint:      "AWS",
		// No apiKeyEnv: bedrock has bespoke multi-source AWS credential
		// resolution (handled specially in ResolveCredentialFull).
		newClient: func(r Resolved) provider.Client { return provider.NewBedrock(r.Credential, r.BaseURL) },
	},
	{
		id:           "google-vertex",
		aliases:      []string{"vertex", "gcp-vertex"},
		defaultModel: "gemini-2.5-pro",
		envHint:      "GOOGLE_CLOUD",
		apiKeyEnv:    []string{"GOOGLE_CLOUD_API_KEY"},
		newClient:    func(r Resolved) provider.Client { return provider.NewGoogleVertex(r.Credential, r.BaseURL) },
	},
	{
		id:           "azure-openai-responses",
		aliases:      []string{"azure", "azure-openai"},
		defaultModel: "gpt-5",
		envHint:      "AZURE_OPENAI",
		apiKeyEnv:    []string{"AZURE_OPENAI_API_KEY"},
		newClient: func(r Resolved) provider.Client {
			return provider.NewAzureOpenAIResponses(r.Credential, r.BaseURL)
		},
	},
	{
		id:           "github-copilot",
		aliases:      []string{"copilot", "github"},
		defaultModel: "claude-sonnet-4.5",
		envHint:      "COPILOT_GITHUB_TOKEN",
		apiKeyEnv:    []string{"COPILOT_GITHUB_TOKEN", "GITHUB_COPILOT_TOKEN"},
		newClient:    func(r Resolved) provider.Client { return provider.NewGithubCopilot(r.Credential, r.BaseURL) },
	},
	{
		id:        "cloudflare-workers-ai",
		aliases:   []string{"cloudflare", "workers-ai"},
		envHint:   "CLOUDFLARE",
		apiKeyEnv: []string{"CLOUDFLARE_API_KEY"},
		newClient: func(r Resolved) provider.Client { return provider.NewCloudflareWorkersAI(r.Credential, r.BaseURL) },
	},
	{
		id:        "cloudflare-ai-gateway",
		envHint:   "CLOUDFLARE",
		apiKeyEnv: []string{"CLOUDFLARE_API_KEY"},
		newClient: func(r Resolved) provider.Client { return provider.NewCloudflareAIGateway(r.Credential, r.BaseURL) },
	},
	{
		id:             "openai-compatible",
		noDefaultModel: true,
		// envHint left empty: falls back to ANTHROPIC, matching the
		// historical envVarName behavior for this id.
		newClient: func(r Resolved) provider.Client { return provider.NewOpenAI(r.Credential, r.BaseURL) },
	},
}

// ProviderByID indexes providerSpecs by canonical id.
var ProviderByID map[string]*providerSpec

// KnownProviders is the ordered list of canonical provider ids,
// derived from providerSpecs. Resolve walks it for fallback priority;
// keep it a []string so existing range loops are untouched.
var KnownProviders []string

// providerAliases maps every alias to its canonical id, derived from
// providerSpecs. Kept as a map[string]string for canonicalProvider
// and the alias round-trip test.
var providerAliases map[string]string

func init() {
	ProviderByID = make(map[string]*providerSpec, len(providerSpecs))
	KnownProviders = make([]string, 0, len(providerSpecs))
	providerAliases = map[string]string{}
	for i := range providerSpecs {
		spec := &providerSpecs[i]
		if _, dup := ProviderByID[spec.id]; dup {
			panic(fmt.Sprintf("provider registry: duplicate id %q", spec.id))
		}
		ProviderByID[spec.id] = spec
		KnownProviders = append(KnownProviders, spec.id)
		for _, a := range spec.aliases {
			if canon, dup := providerAliases[a]; dup {
				panic(fmt.Sprintf("provider registry: alias %q already maps to %q", a, canon))
			}
			if _, clash := ProviderByID[a]; clash {
				panic(fmt.Sprintf("provider registry: alias %q collides with a canonical id", a))
			}
			providerAliases[a] = spec.id
		}
	}
}

var registerEndpointsOnce sync.Once

// RegisterEndpointsFromConfig registers each user-defined OpenAI-compatible
// endpoint (Config.Endpoints) as its own provider, so Resolve, the discoverer,
// and the picker treat it like any other provider id. Idempotent (runs once);
// call it early in startup, before the first Resolve. Endpoints are a USER-
// layer concept (LoadConfig, not the project layer), so a cloned repo can't
// redirect the agent to an arbitrary backend.
func RegisterEndpointsFromConfig() {
	registerEndpointsOnce.Do(func() {
		cfg, err := config.LoadConfig()
		if err != nil {
			return
		}
		for id, ep := range cfg.Endpoints {
			if err := RegisterEndpoint(id, ep); err != nil {
				fmt.Fprintf(os.Stderr, "terva: endpoint %q ignored: %v\n", id, err)
			}
		}
	})
}

// RegisterEndpoint adds one OpenAI-compatible endpoint as a dynamic provider.
// The spec is heap-allocated (not appended to providerSpecs) so the pointers
// init() stored in ProviderByID stay valid. Its credential resolves from the
// endpoint's APIKeyEnv (set as apiKeyEnv) or auth.json — never inline config.
func RegisterEndpoint(id string, ep config.EndpointConfig) error {
	id = strings.TrimSpace(id)
	if id == "" || strings.TrimSpace(ep.BaseURL) == "" {
		return fmt.Errorf("needs a name and baseUrl")
	}
	if _, exists := ProviderByID[id]; exists {
		return fmt.Errorf("name collides with an existing provider id")
	}
	if _, alias := providerAliases[id]; alias {
		return fmt.Errorf("name collides with a provider alias")
	}
	var apiKeyEnv []string
	if ep.APIKeyEnv != "" {
		apiKeyEnv = []string{ep.APIKeyEnv}
	}
	baseURL := ep.BaseURL
	s := &providerSpec{
		id:             id,
		noDefaultModel: true,
		apiKeyEnv:      apiKeyEnv,
		newClient: func(r Resolved) provider.Client {
			return provider.NewOpenAI(r.Credential, firstNonEmpty(r.BaseURL, baseURL))
		},
	}
	ProviderByID[id] = s
	KnownProviders = append(KnownProviders, id)
	return nil
}

// isEndpointProvider reports whether id is a user-defined endpoint (vs a
// built-in provider). Used by Resolve to give it openai-compatible treatment.
func isEndpointProvider(id string, cfg config.Config) bool {
	_, ok := cfg.Endpoints[id]
	return ok
}

// EndpointNameFor derives a stable, valid endpoint id from a base URL's host,
// avoiding collisions with built-in provider ids and with names already used.
func EndpointNameFor(rawURL string, used map[string]bool) string {
	host := rawURL
	if u, err := url.Parse(rawURL); err == nil && u.Hostname() != "" {
		host = u.Hostname()
		switch host {
		case "localhost", "127.0.0.1", "0.0.0.0":
			if p := u.Port(); p != "" {
				host = "local-" + p
			} else {
				host = "local"
			}
		}
	}
	name := SanitizeID(host)
	if name == "" {
		name = "endpoint"
	}
	if _, builtIn := ProviderByID[name]; builtIn {
		name += "-ep" // never propose a name that collides with a built-in provider
	}
	base := name
	for i := 2; used[name]; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	used[name] = true
	return name
}

// SanitizeID lowercases s and keeps [a-z0-9-], turning separators into dashes.
func SanitizeID(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == '.' || r == '_' || r == ' ' || r == ':':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
