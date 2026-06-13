package agent

import (
	"fmt"

	"terva.sh/terva/packages/provider"
)

// providerSpec is one row of the provider registry (roadmap B4): the
// single source of truth for everything terva keys on a provider id.
// It replaces five parallel switches that had to be kept in sync by
// hand — NewClient dispatch, default model, the known-provider list,
// the alias map, and the env-var hint — plus the env-var lookup half
// of credential resolution. Adding a provider is now one entry here
// instead of edits scattered across build.go and config.go.
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
	// an API key (auth method "apikey"). nil for providers with no env
	// key path (codex subscription, ollama, bedrock's bespoke AWS
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
				return r.wrapWithRefresh(provider.NewAnthropicOAuth(r.Credential, r.BaseURL))
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
			return r.wrapWithRefresh(provider.NewOpenAICodex(r.Credential, r.AccountID, r.BaseURL))
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
			inner := provider.NewKimiCodingWithHeaders(r.Credential, r.BaseURL, kimiCodeHeaders())
			if r.AuthMethod == "oauth" {
				return r.wrapWithRefresh(inner)
			}
			return inner
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

// providerByID indexes providerSpecs by canonical id.
var providerByID map[string]*providerSpec

// knownProviders is the ordered list of canonical provider ids,
// derived from providerSpecs. Resolve walks it for fallback priority;
// keep it a []string so existing range loops are untouched.
var knownProviders []string

// providerAliases maps every alias to its canonical id, derived from
// providerSpecs. Kept as a map[string]string for canonicalProvider
// and the alias round-trip test.
var providerAliases map[string]string

func init() {
	providerByID = make(map[string]*providerSpec, len(providerSpecs))
	knownProviders = make([]string, 0, len(providerSpecs))
	providerAliases = map[string]string{}
	for i := range providerSpecs {
		spec := &providerSpecs[i]
		if _, dup := providerByID[spec.id]; dup {
			panic(fmt.Sprintf("provider registry: duplicate id %q", spec.id))
		}
		providerByID[spec.id] = spec
		knownProviders = append(knownProviders, spec.id)
		for _, a := range spec.aliases {
			if canon, dup := providerAliases[a]; dup {
				panic(fmt.Sprintf("provider registry: alias %q already maps to %q", a, canon))
			}
			if _, clash := providerByID[a]; clash {
				panic(fmt.Sprintf("provider registry: alias %q collides with a canonical id", a))
			}
			providerAliases[a] = spec.id
		}
	}
}
