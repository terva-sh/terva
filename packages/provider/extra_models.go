package provider

// Hand-curated catalog supplements that the bulk builtin catalog
// (catalog_builtin.go) doesn't cover.
//
//   - openai-responses: terva-specific provider id for the public OpenAI
//     Responses API (api.openai.com/v1/responses). Separate from
//     `openai` (Chat Completions) and `openai-codex` (ChatGPT subscription).
//   - kimi/kimi-k2-thinking: matches the Anthropic-Messages endpoint
//     at api.kimi.com/coding; not present in the bulk builtin file
//     because the `kimi` provider lives in models.go alongside the
//     other curated providers.

func init() { Catalog = append(Catalog, supplementCatalog...) }

var supplementCatalog = []Model{
	// ----- kimi (anthropic-messages on api.kimi.com/coding) -----
	{Provider: "kimi", ID: "kimi-k2-thinking", DisplayName: "Kimi K2 Thinking",
		ContextWindow: 262144, MaxOutput: 32000, Reasoning: true,
		BaseURL: "https://api.kimi.com/coding"},

	// ----- openai-responses (public OpenAI Responses API) -----
	{Provider: "openai-responses", ID: "gpt-5", DisplayName: "GPT-5 (Responses)",
		ContextWindow: 400000, MaxOutput: 128000, Reasoning: true,
		PriceInput: 1.25, PriceOutput: 10.00, PriceCacheRead: 0.125},
	{Provider: "openai-responses", ID: "gpt-5-mini", DisplayName: "GPT-5 mini (Responses)",
		ContextWindow: 400000, MaxOutput: 128000, Reasoning: true,
		PriceInput: 0.25, PriceOutput: 2.00, PriceCacheRead: 0.025},
	{Provider: "openai-responses", ID: "gpt-5-nano", DisplayName: "GPT-5 nano (Responses)",
		ContextWindow: 400000, MaxOutput: 128000, Reasoning: true,
		PriceInput: 0.05, PriceOutput: 0.40, PriceCacheRead: 0.005},
	{Provider: "openai-responses", ID: "gpt-5-codex", DisplayName: "GPT-5 Codex (Responses)",
		ContextWindow: 400000, MaxOutput: 128000, Reasoning: true,
		PriceInput: 1.25, PriceOutput: 10.00, PriceCacheRead: 0.125},
	{Provider: "openai-responses", ID: "o3", DisplayName: "o3 (Responses)",
		ContextWindow: 200000, MaxOutput: 100000, Reasoning: true,
		PriceInput: 2.00, PriceOutput: 8.00, PriceCacheRead: 0.50},
	{Provider: "openai-responses", ID: "o4-mini", DisplayName: "o4-mini (Responses)",
		ContextWindow: 200000, MaxOutput: 100000, Reasoning: true,
		PriceInput: 1.10, PriceOutput: 4.40, PriceCacheRead: 0.275},
}
