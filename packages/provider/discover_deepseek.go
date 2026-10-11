package provider

import "context"

// DiscoverDeepSeek lists model IDs available to a direct DeepSeek account.
// Known metadata is supplied by the host catalog, not inferred from model names.
//
// Unstable: per-vendor model discovery carries no promise before 1.0.
func DiscoverDeepSeek(ctx context.Context, apiKey, baseURL string) ([]Model, error) {
	if baseURL == "" {
		baseURL = "https://api.deepseek.com/v1"
	}
	rows, err := DiscoverOpenAICompatible(ctx, baseURL, apiKey, 0, nil)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		// The direct list advertises IDs only. Catalog and user layers supply limits.
		rows[i] = Model{Provider: "deepseek", ID: rows[i].ID,
			Source: "live",
			Caps:   map[Capability]bool{CapImageInput: false}}
	}
	return rows, nil
}
