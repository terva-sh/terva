package provider

import (
	"sort"
	"testing"
)

// The census: every provider that ships a reasoning-capable model must be
// classified in reasoningWireWiring on purpose.
//
// Written empty in the sense the repo means it — the list of subjects is not
// typed out here, it is scanned from the built-in catalog, so the first run IS
// the audit and a provider added later enrolls itself. The default
// (OpenAI-compatible) is right for most providers and therefore dangerous: it
// would silently give a correct-looking answer for a new Anthropic-wire or
// Responses-wire provider, and the /reasoning dialog would print a budget the
// request never carries.
//
// Adding a provider to reasoningWireWiring is a one-line decision. Being
// forced to make it is the point.
func TestEveryReasoningProviderIsClassified(t *testing.T) {
	// Providers whose reasoning models legitimately take the OpenAI-compatible
	// effort knob. Listing them here is the deliberate act; the failure message
	// explains what a newcomer has to decide.
	//
	// 🪤 "kimi" was listed here and should never have been: its client is
	// Anthropic-wire (Kimi Code). This set is the census's escape hatch, so a
	// wrong entry in it is invisible — the guard passes and the /reasoning
	// dialog quietly describes a knob the provider does not accept. Before
	// adding a provider here, check which client the registry actually builds
	// for it, not what its name suggests.
	openAICompat := map[string]bool{
		"openai": true, "openrouter": true, "opencode": true, "opencode-go": true,
		"moonshotai": true, "moonshotai-cn": true, "deepseek": true,
		"cerebras": true, "groq": true, "xai": true, "together": true,
		"huggingface": true, "mistral": true, "zai": true, "xiaomi": true,
		"xiaomi-token-plan-ams": true, "xiaomi-token-plan-cn": true,
		"xiaomi-token-plan-sgp": true, "ollama": true, "vercel-ai-gateway": true,
		"github-copilot": true, "cloudflare-ai-gateway": true,
		"cloudflare-workers-ai": true, "azure-openai": true,
	}

	// 🪤 Catalog, NOT builtinCatalog. This scanned builtinCatalog, which is the
	// third-party EXTENDED list and says so at the top of its own file: the
	// curated rows — anthropic, openai, openai-codex, kimi, deepseek, google —
	// "are not duplicated here". So the census structurally could not see the
	// providers most worth auditing, and kimi's misclassification survived it
	// for exactly that reason. Catalog is the union (models.go plus
	// builtinCatalog, appended in catalog_builtin.go's init).
	//
	// A census that cannot see its subject is worse than no census: it reports
	// a clean audit of a list it was never shown.
	seen := map[string]bool{}
	for _, m := range Catalog {
		if !m.Reasoning || seen[m.Provider] {
			continue
		}
		seen[m.Provider] = true
		if _, classified := reasoningWireWiring[m.Provider]; classified {
			continue
		}
		if openAICompat[m.Provider] {
			continue
		}
		t.Errorf("provider %q ships reasoning models but is classified nowhere.\n"+
			"Decide which wire its client speaks (see packages/agent/build/provider_registry.go):\n"+
			"  - Anthropic wire (thinking budget / adaptive effort) -> add to reasoningWireWiring\n"+
			"  - Responses/Codex wire (effort enum)                 -> add to reasoningWireWiring\n"+
			"  - Gemini wire (thinkingBudget / thinkingLevel)       -> add to reasoningWireWiring\n"+
			"  - sends no reasoning control                         -> add as reasoningWireNone\n"+
			"  - OpenAI-compatible reasoning_effort                 -> add to this test's openAICompat set\n"+
			"Falling through to the default silently would make the /reasoning dialog\n"+
			"describe a knob this provider does not accept.", m.Provider)
	}

	if len(seen) == 0 {
		t.Fatal("scanned the builtin catalog and found no reasoning models at all — " +
			"this guard is passing vacuously, which is worse than failing")
	}
	var names []string
	for p := range seen {
		names = append(names, p)
	}
	sort.Strings(names)
	t.Logf("classified %d providers shipping reasoning models: %v", len(names), names)
}
