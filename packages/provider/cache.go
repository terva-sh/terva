package provider

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// ModelCache is the on-disk shape for discovered models.
type ModelCache struct {
	FetchedAt time.Time `json:"fetched_at"`
	Version   int       `json:"version,omitempty"`
	// Endpoints is an opaque signature of the user's configured
	// OpenAI-compatible endpoints at write time. The discoverer re-runs when
	// it changes, so adding/removing an endpoint refreshes without waiting out
	// CacheTTL. (Computed by the agent layer; the cache just carries it.)
	Endpoints string  `json:"endpoints,omitempty"`
	Models    []Model `json:"models"`
}

// CacheTTL is how long a discovered list is considered fresh.
const CacheTTL = 6 * time.Hour

// ModelCacheVersion is bumped whenever the discovery LOGIC changes — a new
// provider or endpoint is added to refreshModels. A cache written by an older
// binary carries a lower version and is treated as stale even within CacheTTL,
// so a newly-added source (e.g. opencode-go) is picked up on the next launch
// instead of waiting out the time-based TTL.
//
//	v2: added opencode / opencode-go /v1/models discovery.
//	v3: added user-defined endpoint (config.json "endpoints") discovery.
//	v4: openai-compatible discovery asserts image-input per model id
//	    (text-only by default), so cached entries re-resolve their caps.
const ModelCacheVersion = 4

// LoadCache reads the model cache from path. Returns an empty ModelCache
// (no error) if the file does not exist.
func LoadCache(path string) (ModelCache, error) {
	var c ModelCache
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
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

// SaveCache writes the cache atomically.
func SaveCache(path string, c ModelCache) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// IsFresh reports whether the cache was fetched within CacheTTL.
func (c ModelCache) IsFresh() bool {
	if c.FetchedAt.IsZero() {
		return false
	}
	return time.Since(c.FetchedAt) < CacheTTL
}

// IsCurrent reports whether the cache is fresh AND written by a binary with the
// same discovery set (ModelCacheVersion). A version mismatch forces
// re-discovery so newly-added providers appear without waiting out CacheTTL.
func (c ModelCache) IsCurrent() bool {
	return c.IsFresh() && c.Version == ModelCacheVersion
}
