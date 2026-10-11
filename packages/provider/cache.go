package provider

import (
	"encoding/json"
	"time"
)

// ModelCache is the on-disk shape for discovered models.
//
// Unstable: terva's on-disk file format carries no promise before 1.0.
type ModelCache struct {
	FetchedAt time.Time `json:"fetched_at"`
	Version   int       `json:"version,omitempty"`
	// Endpoints is an opaque signature of the user's configured
	// OpenAI-compatible endpoints at write time. The discoverer re-runs when
	// it changes, so adding/removing an endpoint refreshes without waiting out
	// the six-hour cache TTL. (Computed by the agent layer; the cache just carries it.)
	Endpoints string `json:"endpoints,omitempty"`
	// BedrockRegion scopes discovered IDs and runtime URLs to one AWS region.
	BedrockRegion string `json:"bedrock_region,omitempty"`
	// BedrockSigning forces discovery when signing keys become available.
	BedrockSigning bool `json:"bedrock_signing,omitempty"`
	// BedrockScope separates profiles and signing adapters without secret values.
	BedrockScope string `json:"bedrock_scope,omitempty"`
	// Bedrock freshness does not control retries for other discovery sources.
	BedrockFetchedAt time.Time `json:"bedrock_fetched_at,omitempty"`
	BedrockRetryAt   time.Time `json:"bedrock_retry_at,omitempty"`
	// BedrockDiagnostic holds sanitized text to suppress repeated notices.
	BedrockDiagnostic string  `json:"bedrock_diagnostic,omitempty"`
	Models            []Model `json:"models"`
}

// cacheTTL is how long a discovered list is considered fresh.
const cacheTTL = 6 * time.Hour

// ModelCacheVersion is bumped whenever the discovery LOGIC changes — a new
// provider or endpoint is added to refreshModels. A cache written by an older
// binary carries a lower version and is treated as stale even within the
// six-hour TTL, so a newly-added source (e.g. opencode-go) is picked up on the next launch
// instead of waiting out the time-based TTL.
//
//	v2: added opencode / opencode-go /v1/models discovery.
//	v3: added user-defined endpoint (config.json "endpoints") discovery.
//	v4: openai-compatible discovery asserts image-input per model id
//	    (text-only by default), so cached entries re-resolve their caps.
//	v5: added region-scoped Bedrock foundation and inference-profile discovery.
//	v6: separated profile and signing-adapter scopes for refreshable credentials.
//
// Unstable: terva's on-disk file format carries no promise before 1.0.
const ModelCacheVersion = 6

// ParseModelCache decodes a model cache as the file holds it. A model with no
// Source is marked "cache". Reading and writing the file is the host's (terva's
// is packages/agent/modelfiles): decision 0021, rule 5.
//
// Unstable: terva's on-disk file format carries no promise before 1.0.
func ParseModelCache(b []byte) (ModelCache, error) {
	var c ModelCache
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	for i := range c.Models {
		if c.Models[i].Source == "" {
			c.Models[i].Source = "cache"
		}
	}
	return c, nil
}

// MarshalModelCache encodes c in the shape the cache file holds.
//
// Unstable: terva's on-disk file format carries no promise before 1.0.
func MarshalModelCache(c ModelCache) ([]byte, error) {
	return json.MarshalIndent(c, "", "  ")
}

// IsFresh reports whether the cache was fetched within the last six hours.
func (c ModelCache) IsFresh() bool {
	if c.FetchedAt.IsZero() {
		return false
	}
	return time.Since(c.FetchedAt) < cacheTTL
}

// IsCurrent checks the shared list's freshness and discovery version.
// Hosts check Bedrock scope and freshness separately. A version mismatch
// forces discovery without waiting for the six-hour TTL.
func (c ModelCache) IsCurrent() bool {
	return c.IsFresh() && c.Version == ModelCacheVersion
}
