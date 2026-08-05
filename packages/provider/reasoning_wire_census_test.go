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
	openAICompat := map[string]bool{
		"openai": true, "openrouter": true, "opencode": true, "opencode-go": true,
		"kimi": true, "moonshotai": true, "moonshotai-cn": true, "deepseek": true,
		"cerebras": true, "groq": true, "xai": true, "together": true,
		"huggingface": true, "mistral": true, "zai": true, "xiaomi": true,
		"xiaomi-token-plan-ams": true, "xiaomi-token-plan-cn": true,
		"xiaomi-token-plan-sgp": true, "ollama": true, "vercel-ai-gateway": true,
		"github-copilot": true, "cloudflare-ai-gateway": true,
		"cloudflare-workers-ai": true, "azure-openai": true,
	}

	seen := map[string]bool{}
	for _, m := range builtinCatalog {
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
