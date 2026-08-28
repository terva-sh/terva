package build

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
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
	// set. It takes [clientConfig] rather than the whole [Resolved]:
	// across every entry below these closures read five fields, and
	// depending on the 57-field container for five of them is what
	// pinned this registry inside `build` (07's question 8, step b).
	// Lives here rather than the provider package because it needs the
	// OAuth refresh wrapper, which is agent-side.
	newClient func(c clientConfig) provider.Client
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
		newClient: func(c clientConfig) provider.Client {
			if c.AuthMethod == "oauth" {
				return provider.NewAnthropicOAuthSource(c.credentialSource(), c.BaseURL)
			}
			return provider.NewAnthropic(c.Credential, c.BaseURL)
		},
	},
	{
		id:           "openai",
		defaultModel: "gpt-5",
		envHint:      "OPENAI",
		apiKeyEnv:    []string{"OPENAI_API_KEY"},
		newClient:    func(c clientConfig) provider.Client { return provider.NewOpenAI(c.Credential, c.BaseURL) },
	},
	{
		id:           "openai-codex",
		aliases:      []string{"codex"},
		defaultModel: "gpt-5.5",
		envHint:      "OPENAI",
		// No apiKeyEnv: the ChatGPT/Codex subscription route
		// intentionally ignores OPENAI_API_KEY so both can coexist.
		newClient: func(c clientConfig) provider.Client {
			return provider.NewOpenAICodexSource(c.credentialSource(), c.AccountID, c.BaseURL)
		},
	},
	{
		id:           "openai-responses",
		defaultModel: "gpt-5",
		envHint:      "OPENAI",
		apiKeyEnv:    []string{"OPENAI_API_KEY"},
		newClient:    func(c clientConfig) provider.Client { return provider.NewOpenAIResponses(c.Credential, c.BaseURL) },
	},
	{
		id:           "kimi",
		aliases:      []string{"kimi-code"},
		defaultModel: "kimi-for-coding",
		envHint:      "KIMI",
		apiKeyEnv:    []string{"KIMI_API_KEY", "MOONSHOT_API_KEY"},
		newClient: func(c clientConfig) provider.Client {
			// kimi-coding speaks anthropic-messages on
			// api.kimi.com/coding; subscription OAuth wraps the same
			// Anthropic-shaped client.
			if c.AuthMethod == "oauth" {
				return provider.NewKimiCodingSourceWithHeaders(c.credentialSource(), c.BaseURL, kimiCodeHeaders())
			}
			return provider.NewKimiCodingWithHeaders(c.Credential, c.BaseURL, kimiCodeHeaders())
		},
	},
	{
		id:           "deepseek",
		defaultModel: "deepseek-v4-pro",
		envHint:      "DEEPSEEK",
		apiKeyEnv:    []string{"DEEPSEEK_API_KEY"},
		newClient:    func(c clientConfig) provider.Client { return provider.NewDeepSeek(c.Credential, c.BaseURL) },
	},
	{
		id:      "google",
		aliases: []string{"gemini", "googleai", "google-ai"},
		// A rolling alias on purpose. The previous default, gemini-2.5-pro, was
		// retired under a fresh key (404 "no longer available to new users"), so
		// a first `terva --provider google` failed before it sent a token — and a
		// pinned id guarantees that recurs at every retirement. "latest" only
		// moves forward, so this default cannot go stale the same way.
		//
		// It resolves to gemini-3.7-flash today (probed via the modelVersion
		// field on a live response, 2026-08-14); its catalog row must be repriced
		// whenever that target moves.
		defaultModel: "gemini-flash-latest",
		envHint:      "GEMINI",
		apiKeyEnv:    []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		newClient:    func(c clientConfig) provider.Client { return provider.NewGemini(c.Credential, c.BaseURL) },
	},
	{
		id:             "ollama",
		noDefaultModel: true,
		envHint:        "OLLAMA",
		newClient:      func(c clientConfig) provider.Client { return provider.NewOpenAI(c.Credential, c.BaseURL) },
	},
	{
		id:           "moonshotai",
		aliases:      []string{"moonshot"},
		defaultModel: "kimi-k2.6",
		envHint:      "MOONSHOT",
		apiKeyEnv:    []string{"MOONSHOT_API_KEY"},
		newClient:    func(c clientConfig) provider.Client { return provider.NewMoonshot(c.Credential, c.BaseURL) },
	},
	{
		id:           "moonshotai-cn",
		defaultModel: "kimi-k2.6",
		envHint:      "MOONSHOT",
		apiKeyEnv:    []string{"MOONSHOT_API_KEY"},
		newClient:    func(c clientConfig) provider.Client { return provider.NewMoonshotCN(c.Credential, c.BaseURL) },
	},
	{
		id:           "cerebras",
		defaultModel: "qwen-3-235b-a22b-instruct-2507",
		envHint:      "CEREBRAS",
		apiKeyEnv:    []string{"CEREBRAS_API_KEY"},
		newClient:    func(c clientConfig) provider.Client { return provider.NewCerebras(c.Credential, c.BaseURL) },
	},
	{
		id:           "groq",
		defaultModel: "llama-3.3-70b-versatile",
		envHint:      "GROQ",
		apiKeyEnv:    []string{"GROQ_API_KEY"},
		newClient:    func(c clientConfig) provider.Client { return provider.NewGroq(c.Credential, c.BaseURL) },
	},
	{
		id:           "xai",
		defaultModel: "grok-code-fast-1",
		envHint:      "XAI",
		apiKeyEnv:    []string{"XAI_API_KEY"},
		newClient:    func(c clientConfig) provider.Client { return provider.NewXAI(c.Credential, c.BaseURL) },
	},
	{
		id:           "together",
		defaultModel: "Qwen/Qwen3-Coder-480B-A35B-Instruct",
		envHint:      "TOGETHER",
		apiKeyEnv:    []string{"TOGETHER_API_KEY"},
		newClient:    func(c clientConfig) provider.Client { return provider.NewTogether(c.Credential, c.BaseURL) },
	},
	{
		id:           "huggingface",
		aliases:      []string{"hf"},
		defaultModel: "moonshotai/Kimi-K2-Instruct",
		envHint:      "HF",
		apiKeyEnv:    []string{"HF_TOKEN"},
		newClient:    func(c clientConfig) provider.Client { return provider.NewHuggingFace(c.Credential, c.BaseURL) },
	},
	{
		id:           "openrouter",
		defaultModel: "anthropic/claude-sonnet-4.5",
		envHint:      "OPENROUTER",
		apiKeyEnv:    []string{"OPENROUTER_API_KEY"},
		newClient:    func(c clientConfig) provider.Client { return provider.NewOpenRouter(c.Credential, c.BaseURL) },
	},
	{
		id:           "mistral",
		defaultModel: "mistral-large-latest",
		envHint:      "MISTRAL",
		apiKeyEnv:    []string{"MISTRAL_API_KEY"},
		newClient:    func(c clientConfig) provider.Client { return provider.NewMistral(c.Credential, c.BaseURL) },
	},
	{
		id:           "zai",
		defaultModel: "glm-4.7",
		envHint:      "ZAI",
		apiKeyEnv:    []string{"ZAI_API_KEY"},
		newClient:    func(c clientConfig) provider.Client { return provider.NewZAI(c.Credential, c.BaseURL) },
	},
	{
		id:           "xiaomi",
		defaultModel: "mimo-v2.5",
		envHint:      "XIAOMI",
		apiKeyEnv:    []string{"XIAOMI_API_KEY"},
		newClient:    func(c clientConfig) provider.Client { return provider.NewXiaomi(c.Credential, c.BaseURL) },
	},
	{
		id:           "xiaomi-token-plan-ams",
		defaultModel: "mimo-v2.5",
		envHint:      "XIAOMI_TOKEN_PLAN_AMS",
		apiKeyEnv:    []string{"XIAOMI_TOKEN_PLAN_AMS_API_KEY"},
		newClient: func(c clientConfig) provider.Client {
			return provider.NewXiaomiTokenPlan("ams", c.Credential, c.BaseURL)
		},
	},
	{
		id:           "xiaomi-token-plan-cn",
		defaultModel: "mimo-v2.5",
		envHint:      "XIAOMI_TOKEN_PLAN_CN",
		apiKeyEnv:    []string{"XIAOMI_TOKEN_PLAN_CN_API_KEY"},
		newClient: func(c clientConfig) provider.Client {
			return provider.NewXiaomiTokenPlan("cn", c.Credential, c.BaseURL)
		},
	},
	{
		id:           "xiaomi-token-plan-sgp",
		defaultModel: "mimo-v2.5",
		envHint:      "XIAOMI_TOKEN_PLAN_SGP",
		apiKeyEnv:    []string{"XIAOMI_TOKEN_PLAN_SGP_API_KEY"},
		newClient: func(c clientConfig) provider.Client {
			return provider.NewXiaomiTokenPlan("sgp", c.Credential, c.BaseURL)
		},
	},
	{
		id:           "minimax",
		defaultModel: "MiniMax-M2.7",
		envHint:      "MINIMAX",
		apiKeyEnv:    []string{"MINIMAX_API_KEY"},
		newClient:    func(c clientConfig) provider.Client { return provider.NewMinimaxAnthropic(c.Credential, c.BaseURL) },
	},
	{
		id:           "minimax-cn",
		defaultModel: "MiniMax-M2.7",
		envHint:      "MINIMAX_CN",
		apiKeyEnv:    []string{"MINIMAX_CN_API_KEY", "MINIMAX_API_KEY"},
		newClient:    func(c clientConfig) provider.Client { return provider.NewMinimaxCNAnthropic(c.Credential, c.BaseURL) },
	},
	{
		id:           "fireworks",
		defaultModel: "accounts/fireworks/models/kimi-k2p6",
		envHint:      "FIREWORKS",
		apiKeyEnv:    []string{"FIREWORKS_API_KEY"},
		newClient:    func(c clientConfig) provider.Client { return provider.NewFireworksAnthropic(c.Credential, c.BaseURL) },
	},
	{
		id:           "vercel-ai-gateway",
		aliases:      []string{"ai-gateway", "vercel"},
		defaultModel: "anthropic/claude-sonnet-4.5",
		envHint:      "AI_GATEWAY",
		apiKeyEnv:    []string{"AI_GATEWAY_API_KEY"},
		newClient: func(c clientConfig) provider.Client {
			return provider.NewVercelGatewayAnthropic(c.Credential, c.BaseURL)
		},
	},
	{
		id:           "opencode",
		defaultModel: "claude-sonnet-4-5",
		envHint:      "OPENCODE",
		apiKeyEnv:    []string{"OPENCODE_API_KEY"},
		newClient:    func(c clientConfig) provider.Client { return provider.NewOpenCode(c.Credential, c.BaseURL) },
	},
	{
		id:           "opencode-go",
		defaultModel: "kimi-k2.6",
		envHint:      "OPENCODE",
		apiKeyEnv:    []string{"OPENCODE_API_KEY"},
		newClient:    func(c clientConfig) provider.Client { return provider.NewOpenCodeGo(c.Credential, c.BaseURL) },
	},
	{
		id:           "amazon-bedrock",
		aliases:      []string{"bedrock", "aws-bedrock", "amazon"},
		defaultModel: "anthropic.claude-sonnet-4-5-20250929-v1:0",
		envHint:      "AWS",
		// No apiKeyEnv: bedrock has bespoke multi-source AWS credential
		// resolution (handled specially in ResolveCredentialFull).
		newClient: func(c clientConfig) provider.Client { return provider.NewBedrock(c.Credential, c.BaseURL) },
	},
	{
		id:           "google-vertex",
		aliases:      []string{"vertex", "gcp-vertex"},
		defaultModel: "gemini-2.5-pro",
		envHint:      "GOOGLE_CLOUD",
		apiKeyEnv:    []string{"GOOGLE_CLOUD_API_KEY"},
		newClient:    func(c clientConfig) provider.Client { return provider.NewGoogleVertex(c.Credential, c.BaseURL) },
	},
	{
		id:           "azure-openai-responses",
		aliases:      []string{"azure", "azure-openai"},
		defaultModel: "gpt-5",
		envHint:      "AZURE_OPENAI",
		apiKeyEnv:    []string{"AZURE_OPENAI_API_KEY"},
		newClient: func(c clientConfig) provider.Client {
			return provider.NewAzureOpenAIResponses(c.Credential, c.BaseURL)
		},
	},
	{
		id:           "github-copilot",
		aliases:      []string{"copilot", "github"},
		defaultModel: "claude-sonnet-4.5",
		envHint:      "COPILOT_GITHUB_TOKEN",
		apiKeyEnv:    []string{"COPILOT_GITHUB_TOKEN", "GITHUB_COPILOT_TOKEN"},
		newClient:    func(c clientConfig) provider.Client { return provider.NewGithubCopilot(c.Credential, c.BaseURL) },
	},
	{
		id:        "cloudflare-workers-ai",
		aliases:   []string{"cloudflare", "workers-ai"},
		envHint:   "CLOUDFLARE",
		apiKeyEnv: []string{"CLOUDFLARE_API_KEY"},
		newClient: func(c clientConfig) provider.Client { return provider.NewCloudflareWorkersAI(c.Credential, c.BaseURL) },
	},
	{
		id:        "cloudflare-ai-gateway",
		envHint:   "CLOUDFLARE",
		apiKeyEnv: []string{"CLOUDFLARE_API_KEY"},
		newClient: func(c clientConfig) provider.Client { return provider.NewCloudflareAIGateway(c.Credential, c.BaseURL) },
	},
	{
		id:             "openai-compatible",
		noDefaultModel: true,
		// envHint left empty: falls back to ANTHROPIC, matching the
		// historical envVarName behavior for this id.
		newClient: func(c clientConfig) provider.Client { return provider.NewOpenAI(c.Credential, c.BaseURL) },
	},
}

// The registry: what providers exist, in what order, under what names.
//
// It is NOT fixed at startup. A named openai-compatible endpoint becomes a
// provider the moment the operator finishes /login, on the login goroutine,
// while other sessions are resolving credentials on theirs. registryMu guards
// all four collections and nothing outside this file touches them directly —
// otherwise creating an endpoint mid-session is a concurrent map write against
// every Resolve in flight.
var registryMu sync.RWMutex

// providerByID indexes providerSpecs by canonical id.
var providerByID map[string]*providerSpec

// knownProviders is the ordered list of canonical provider ids: the static
// specs first, in registry order, then each endpoint as it registers. Resolve
// walks it for fallback priority, so built-ins are preferred over an
// endpoint — see ProviderIDs.
var knownProviders []string

// providerAliases maps every alias to its canonical id, derived from
// providerSpecs. Kept as a map[string]string for canonicalProvider
// and the alias round-trip test.
var providerAliases map[string]string

// endpointProviders records which ids RegisterEndpoint added. A built-in and an
// endpoint are the same shape once registered, and only this says which is
// which — so UnregisterEndpoint can refuse to delete a provider terva ships,
// whatever id it is handed.
var endpointProviders map[string]bool

// ProviderIDs returns the ordered canonical provider ids. A COPY: the registry
// grows when an endpoint is created, and a caller mid-range over the live slice
// would be reading it as it is reallocated.
func ProviderIDs() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return append([]string(nil), knownProviders...)
}

// specFor returns the registry entry for a canonical id.
func specFor(id string) (*providerSpec, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	s, ok := providerByID[id]
	return s, ok
}

// aliasFor resolves an alias to its canonical id.
func aliasFor(name string) (string, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	canon, ok := providerAliases[name]
	return canon, ok
}

func init() {
	providerByID = make(map[string]*providerSpec, len(providerSpecs))
	knownProviders = make([]string, 0, len(providerSpecs))
	providerAliases = map[string]string{}
	endpointProviders = map[string]bool{}
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
// init() stored in providerByID stay valid. Its credential resolves from the
// endpoint's APIKeyEnv (set as apiKeyEnv) or auth.json — never inline config.
func RegisterEndpoint(id string, ep config.EndpointConfig) error {
	registryMu.Lock()
	defer registryMu.Unlock()
	return registerEndpointLocked(id, ep)
}

// RegisterOrReplaceEndpoint registers an endpoint, dropping any earlier
// registration under the same name first.
//
// Replacing is what EDITING an endpoint needs. RegisterEndpoint refuses a name
// already in the registry — correct when the name belongs to something else,
// wrong when it belongs to the very endpoint being saved, which is what /login
// does every time an operator corrects a port. It also keeps the id list honest:
// a second registration must not leave two rows for one backend.
//
// The spec captures the base URL for its client closure, so a stale registration
// is stale in that respect too (Resolve supplies the URL from config on top of
// it, so this is belt-and-braces rather than the primary reason).
//
// A name held by a BUILT-IN provider is still refused — only an endpoint this
// package registered can be displaced.
func RegisterOrReplaceEndpoint(id string, ep config.EndpointConfig) error {
	registryMu.Lock()
	defer registryMu.Unlock()
	unregisterEndpointLocked(CanonicalEndpointID(id))
	return registerEndpointLocked(id, ep)
}

func registerEndpointLocked(id string, ep config.EndpointConfig) error {
	// Canonical, not merely trimmed: this is the single point every registration
	// path reaches, so normalising it here means no caller can register a
	// spelling that Resolve will then fail to find.
	id = CanonicalEndpointID(id)
	if id == "" || strings.TrimSpace(ep.BaseURL) == "" {
		return fmt.Errorf("needs a name and baseUrl")
	}
	if _, exists := providerByID[id]; exists {
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
		newClient: func(c clientConfig) provider.Client {
			return provider.NewOpenAI(c.Credential, firstNonEmpty(c.BaseURL, baseURL))
		},
	}
	providerByID[id] = s
	knownProviders = append(knownProviders, id)
	endpointProviders[id] = true
	return nil
}

// UnregisterEndpoint removes a dynamically registered endpoint provider: the
// operator deleted it (AuthEndpointRemove), or a test is restoring the global
// registry (-count>1 reruns a test in the same process, where a leftover
// registration collides with itself).
//
// A built-in provider is never removed, whatever id is passed. The registry
// holds both in the same map, so without endpointProviders a caller could
// unregister "anthropic" and leave the process unable to resolve it.
func UnregisterEndpoint(id string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	unregisterEndpointLocked(id)
}

func unregisterEndpointLocked(id string) {
	id = CanonicalEndpointID(id)
	if !endpointProviders[id] {
		return
	}
	delete(endpointProviders, id)
	delete(providerByID, id)
	for i, k := range knownProviders {
		if k == id {
			knownProviders = append(knownProviders[:i], knownProviders[i+1:]...)
			break
		}
	}
}

// endpointCredential resolves a named endpoint's auth: the key stored under its
// own id when the server wants one, and the harmless sentinel bearer when it
// does not — which is the common case, since most local servers ignore the
// header entirely. Never an error: an endpoint is defined by its address, not by
// a credential, so "keyless" is a configuration and not a failure.
func endpointCredential(id, argsKey string) (cred, method string) {
	storedKey, _, _, _ := ResolveCredentialFull(id, argsKey)
	return firstNonEmpty(storedKey, "openai-compatible"), "apikey"
}

// EndpointDefaultModel picks a default model id for a named endpoint from the
// active catalog, or "" when discovery has not found it any.
//
// An endpoint has no baked-in default and the login form does not ask for one:
// the models it serves are whatever its /v1/models list said, so the default has
// to come from there. First in catalog order, which for a discovered endpoint is
// the order the server itself listed — the single-model servers this mostly
// serves (llama.cpp, vLLM) have exactly one, and on a server with several the
// operator picks in /model anyway.
func EndpointDefaultModel(id string) string {
	for _, m := range provider.ModelsForProvider(CanonicalEndpointID(id)) {
		if strings.TrimSpace(m.ID) != "" {
			return m.ID
		}
	}
	return ""
}

// IsEndpointProvider reports whether id is a user-defined endpoint (vs a
// built-in provider), according to cfg. Resolve uses it to give the id
// openai-compatible treatment: an open catalogue and an optional credential.
//
// It asks the CONFIG, not the registry, because the two answer different
// questions: the registry says "can this process build a client for it", and
// config.json says "is this the operator's own server". Startup registration
// can have skipped a malformed entry, and the answer here must still be yes.
func IsEndpointProvider(id string, cfg config.Config) bool {
	if _, ok := cfg.Endpoints[id]; ok {
		return true
	}
	// A config written before ids were normalised still holds the operator's
	// original spelling until the repair migrates it, and this question is asked
	// on the very boot that precedes the repair.
	_, ok := cfg.Endpoints[CanonicalEndpointID(id)]
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
	if _, builtIn := specFor(name); builtIn {
		name += "-ep" // never propose a name that collides with a built-in provider
	}
	base := name
	for i := 2; used[name]; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	used[name] = true
	return name
}

// CanonicalEndpointID is the one spelling of an endpoint id that the rest of
// terva will find. It must agree with canonicalProvider, which every resolve
// path runs a provider name through before looking it up.
//
// 🪤 They disagreed, and it cost a working backend. ValidEndpointName permits
// A-Z, so an endpoint saved as "NeoT" was stored verbatim as the config key, as
// the registry id, and as the Provider field of every model discovered from it
// — while Resolve asked for canonicalProvider("NeoT") = "neot". Go maps are
// case-sensitive, so the endpoint branch missed, the credential lookup missed,
// the fallback scan ran, and the operator watched terva switch to the default
// provider with no error naming the cause. Discovery kept working the whole
// time, which made it look like a model-picker bug.
//
// Normalising here, at every point an id is STORED, is what keeps the two
// spellings from diverging again. A tolerant lookup would have papered over it
// only at the sites someone remembered to change.
func CanonicalEndpointID(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
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

// clientConfig is the slice of [Resolved] a provider's newClient actually reads.
//
// The registry entries used to take the whole Resolved. Across all ~35 of them
// the closures touch five fields, and that dependency on the DI container's
// output type is what made provider_registry.go unliftable — a subpackage taking
// Resolved would import `build`, which imports the registry (07's question 8).
// Narrowing the parameter is the step that has to come before any move.
type clientConfig struct {
	Provider   string // selects the OAuth token to refresh; see credentialSource
	Credential string
	BaseURL    string
	AuthMethod string
	AccountID  string
}

func (c clientConfig) credentialSource() provider.CredentialSource {
	tokenProvider := c.Provider
	if tokenProvider == "openai-codex" {
		tokenProvider = "openai"
	}
	var mu sync.Mutex
	return func(context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		tok, _ := config.RefreshIfExpired(tokenProvider, config.LoadOAuthToken(tokenProvider))
		if tok == nil {
			return "", nil
		}
		return tok.AccessToken, nil
	}
}

func kimiCodeHeaders() map[string]string {
	host, _ := os.Hostname()
	if host == "" {
		host = "unknown"
	}
	deviceID := ""
	if home, err := os.UserHomeDir(); err == nil {
		if b, err := os.ReadFile(filepath.Join(home, ".kimi", "device_id")); err == nil {
			deviceID = strings.TrimSpace(string(b))
		}
	}
	if deviceID == "" {
		deviceID = "zot" // rename:keep — bound to existing kimi device sessions
	}
	return map[string]string{
		"User-Agent":         "KimiCLI/1.41.0",
		"X-Msh-Platform":     "kimi_cli",
		"X-Msh-Version":      "1.41.0",
		"X-Msh-Device-Name":  host,
		"X-Msh-Device-Model": runtime.GOOS + "-" + runtime.GOARCH,
		"X-Msh-Os-Version":   runtime.GOOS,
		"X-Msh-Device-Id":    deviceID,
	}
}
