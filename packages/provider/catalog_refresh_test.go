package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCurrentClaudeCatalogRequests(t *testing.T) {
	for _, tc := range []struct {
		id                    string
		input, output, cached float64
	}{
		{"claude-fable-5-2", 8, 40, 0.2},
		{"claude-sonnet-5-5", 2, 10, 0.1},
		{"claude-sonnet-5", 2, 10, 0.2},
	} {
		t.Run(tc.id, func(t *testing.T) {
			m, err := Builtin().FindModel("anthropic", tc.id)
			if err != nil {
				t.Fatal(err)
			}
			if m.Speculative || !m.AdaptiveThinking || m.ContextWindow != 1000000 || m.MaxOutput != 128000 {
				t.Fatalf("invalid model metadata: %+v", m)
			}
			if m.PriceInput != tc.input || m.PriceOutput != tc.output || m.PriceCacheRead != tc.cached || m.PriceCacheWrite != tc.input*1.25 {
				t.Fatalf("incorrect price sheet: %+v", m)
			}
			temp := float32(0.5)
			body, err := (&anthropicClient{}).buildRequest(Request{
				Model: tc.id, Reasoning: "high", ReasoningSet: true, Temperature: &temp,
			})
			if err != nil {
				t.Fatal(err)
			}
			if body.Thinking == nil || body.Thinking.Type != "adaptive" || body.Temperature != nil {
				t.Fatalf("model received a budget or sampling parameter: %+v", body)
			}
		})
	}
	for _, id := range []string{"claude-opus-4-5", "claude-opus-4-6", "claude-opus-4-7", "claude-opus-4-8", "claude-opus-5", "claude-sonnet-4-6", "claude-sonnet-5", "claude-fable-5"} {
		m, err := Builtin().FindModel("anthropic", id)
		if err != nil || m.Speculative {
			t.Errorf("released model %s still unavailable offline: %+v, %v", id, m, err)
		}
	}
}

func TestDeepSeekFlashVisionSurvivesDiscovery(t *testing.T) {
	for _, tc := range []struct {
		id                    string
		vision                bool
		input, output, cached float64
	}{
		{"deepseek-flash", true, 0.3, 1.2, 0.006},
		{"deepseek-v4-flash", true, 0.3, 1.2, 0.006},
		{"deepseek-v4-flash-vision-exp", true, 0.3, 1.2, 0.006},
		{"deepseek-v4-pro", false, 1.32, 3.96, 0.044},
	} {
		t.Run(tc.id, func(t *testing.T) {
			reg := NewRegistry()
			reg.SetLiveModels([]Model{{Provider: "deepseek", ID: tc.id,
				Caps: map[Capability]bool{CapImageInput: false}}})
			m, err := reg.FindModel("deepseek", tc.id)
			if err != nil {
				t.Fatal(err)
			}
			if m.Has(CapImageInput) != tc.vision || m.ContextWindow != 1000000 || m.MaxOutput != 384000 {
				t.Fatalf("metadata lost after discovery: %+v", m)
			}
			if m.PriceInput != tc.input || m.PriceOutput != tc.output || m.PriceCacheRead != tc.cached {
				t.Fatalf("incorrect peak price sheet: %+v", m)
			}
			client := newOpenAICompat("deepseek", "synthetic", "https://example.test", "").(*openaiClient)
			WithCatalog(client, reg)
			body, err := client.buildRequest(Request{Model: tc.id, Messages: []Message{{Role: RoleUser,
				Content: []Content{TextBlock{Text: "Describe this image"}, ImageBlock{Data: imageCanary, MimeType: "image/png"}},
			}}})
			if err != nil {
				t.Fatal(err)
			}
			wire, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(wire), canaryB64()); got != tc.vision {
				t.Fatalf("image on wire = %v, want %v: %s", got, tc.vision, wire)
			}
		})
	}
}

func TestRetiredDirectModelsAreAbsent(t *testing.T) {
	for provider, ids := range map[string][]string{
		"anthropic":   {"claude-3-7-sonnet-20250219", "claude-3-5-sonnet-20241022", "claude-3-haiku-20240307", "claude-opus-4-1", "claude-sonnet-4-0"},
		"groq":        {"llama3-70b-8192", "llama3-8b-8192", "moonshotai/kimi-k2-instruct-0905", "qwen-qwq-32b"},
		"opencode-go": {"deepseek-flash", "kimi-k2.5", "minimax-m2.7", "qwen3.6-plus", "step-5-preview-free"},
		"openai": {"gpt-4", "gpt-4-turbo", "gpt-4.1-nano", "gpt-4o-2024-05-13", "o1", "o1-pro", "o3-mini", "o4-mini",
			"gpt-5-chat-latest", "gpt-5-codex", "gpt-5.1-chat-latest", "gpt-5.1-codex", "gpt-5.1-codex-max", "gpt-5.1-codex-mini",
			"gpt-5.2-chat-latest", "gpt-5.2-codex", "gpt-5.3-chat-latest", "o3-deep-research", "o4-mini-deep-research"},
		"openai-responses": {"gpt-5-codex", "o4-mini"},
	} {
		for _, id := range ids {
			if _, err := Builtin().FindModel(provider, id); err == nil {
				t.Errorf("retired model %s/%s remains selectable", provider, id)
			}
		}
	}
	// Groq exempts enterprise committed-spend contracts from these retirements.
	for _, id := range []string{"qwen/qwen3-32b", "meta-llama/llama-4-scout-17b-16e-instruct"} {
		if _, err := Builtin().FindModel("groq", id); err != nil {
			t.Error(err)
		}
	}
}

func TestPublicEndpointCleanupPreservesOtherRoutes(t *testing.T) {
	for _, id := range []string{"gpt-5-pro", "gpt-5.2-pro", "gpt-5.3-codex", "gpt-5.3-codex-spark", "gpt-5.4-pro", "gpt-5.5-pro", "o3-pro"} {
		if _, err := Builtin().FindModel("openai", id); err == nil {
			t.Errorf("model %s remains on Chat Completions without compatible tool support", id)
		}
	}
	for prov, ids := range map[string][]string{
		"openai":           {"gpt-5", "gpt-5-mini", "gpt-5-nano", "o3", "gpt-4.1", "gpt-4.1-mini", "gpt-4o", "gpt-4o-mini", "gpt-4o-2024-08-06", "gpt-4o-2024-11-20", "gpt-5.4-nano"},
		"openai-responses": {"gpt-5", "o3", "gpt-6-sol"},
		"openai-codex":     {"gpt-5.3-codex-spark", "gpt-5.4", "gpt-5.5", "gpt-6-sol"},
		"opencode":         {"gpt-5.1-codex-max", "gpt-5.2-codex", "gpt-5.4-pro"},
		"anthropic":        {"claude-sonnet-4-5"},
	} {
		for _, id := range ids {
			if _, err := Builtin().FindModel(prov, id); err != nil {
				t.Errorf("cleanup removed a separately available or not-yet-retired model: %s/%s: %v", prov, id, err)
			}
		}
	}
}

func TestSDKDefaultUsesCurrentClaudeRequest(t *testing.T) {
	if DefaultModel.ID != "claude-sonnet-5-5" || DefaultModel.Speculative || !DefaultModel.AdaptiveThinking {
		t.Fatalf("invalid SDK default: %+v", DefaultModel)
	}
	temp := float32(0.5)
	body, err := (&anthropicClient{}).buildRequest(Request{
		Model: DefaultModel.ID, Reasoning: "high", ReasoningSet: true, Temperature: &temp,
	})
	if err != nil {
		t.Fatal(err)
	}
	if body.Thinking == nil || body.Thinking.Type != "adaptive" || body.Temperature != nil || body.MaxTokens != DefaultModel.MaxOutput {
		t.Fatalf("invalid SDK default request: %+v", body)
	}
}

func TestOpenRouterSonnet55FlatPricing(t *testing.T) {
	m, err := Builtin().FindModel("openrouter", "anthropic/claude-sonnet-5.5")
	if err != nil {
		t.Fatal(err)
	}
	if m.ContextWindow != 1000000 || m.MaxOutput != 128000 || m.ContextSurchargeAt != 0 || len(m.LongContextTiers) != 0 {
		t.Fatalf("invalid seed limits or stale surcharge: %+v", m)
	}
	for _, input := range []int{200000, 200001, 800000} {
		u := Usage{InputTokens: input, OutputTokens: 1000, CacheReadTokens: 10000, CacheWriteTokens: 10000}
		want := (float64(input)*2 + 1000*10 + 10000*0.1 + 10000*2.5) / 1e6
		if got := computeCost(m, u); !near(got, want) {
			t.Errorf("input %d: cost %g, want %g", input, got, want)
		}
	}
}
