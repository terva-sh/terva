package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"terva.sh/terva/packages/provider"
)

// ModelCachePath returns the on-disk location of the merged model cache.
func ModelCachePath() string {
	return filepath.Join(TervaHome(), "models-cache.json")
}

// UserModelsPath returns the path to the user's models.json override.
func UserModelsPath() string {
	return filepath.Join(TervaHome(), "models.json")
}

// LoadCachedModels loads the cache file and applies it to the provider
// package so FindModel / ModelsForProvider see live ids immediately.
// Safe to call before any credentials are known.
func LoadCachedModels() {
	c, err := provider.LoadCache(ModelCachePath())
	if err != nil {
		return
	}
	if len(c.Models) > 0 {
		provider.SetLiveModels(c.Models)
	}
}

// LoadUserModels reads $TERVA_HOME/models.json and merges any user-defined
// models into the active catalog. User models take highest precedence.
// Any validation issues (bad provider id, empty model id, malformed
// JSON, negative widths) are surfaced as one warning per line on stderr;
// the well-formed entries from the rest of the file are still loaded.
func LoadUserModels() {
	overrides, warnings := provider.LoadUserModelsWithWarnings(UserModelsPath())
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "terva:", w)
	}
	if len(overrides) == 0 {
		return
	}
	provider.SetUserOverrides(overrides)
}

// LoadCompatModel registers the configured openai-compatible endpoint's
// model into the active catalog so it shows up in the /model picker
// (open-catalogue models have no baked-in entry). No-op when the
// provider isn't configured.
func LoadCompatModel() {
	baseURL, model, ctxWin := AuthStoreFor().Extras("openai-compatible")
	if baseURL == "" || model == "" {
		return
	}
	if ctxWin <= 0 {
		ctxWin = 32768
	}
	provider.RegisterExtraModel(provider.Model{
		Provider:      "openai-compatible",
		ID:            model,
		DisplayName:   model,
		ContextWindow: ctxWin,
		MaxOutput:     8192,
		BaseURL:       baseURL,
		Source:        "openai-compatible",
	})
}

// compatDefaultContext returns the user's configured default context
// window for the openai-compatible endpoint, or 32768 when unset.
func compatDefaultContext() int {
	if _, _, ctxWin := AuthStoreFor().Extras("openai-compatible"); ctxWin > 0 {
		return ctxWin
	}
	return 32768
}

// RefreshCompatModelsAsync discovers the openai-compatible endpoint's
// models in the background and registers them into the active catalog.
// Unlike RefreshModelsAsync it is NOT gated on the 6h model cache: a
// local server's loaded model set changes often and the /v1/models query
// is cheap, so we re-list on every launch (and after a fresh login).
// No-op when the endpoint isn't configured.
func RefreshCompatModelsAsync() {
	baseURL, _, _ := AuthStoreFor().Extras("openai-compatible")
	if baseURL == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		// No re-apply dance needed here: discovery writes the "extra"
		// catalog layer, and the user's models.json lives in the
		// higher-precedence "user" layer, so hand-set overrides (e.g.
		// maxTokens on the default model) win over discovered values
		// by construction.
		key, _, _ := ResolveCredential("openai-compatible", "")
		live, err := provider.DiscoverOpenAICompatible(ctx, baseURL, key, compatDefaultContext())
		if err != nil {
			return
		}
		for _, m := range live {
			provider.RegisterExtraModel(m)
		}
	}()
}

// ValidateAndRepairConfig checks the persisted config.json's
// (Provider, Model) pair against the active catalog and repairs any
// mismatch in-place (and on disk) before any UI renders. Three failure
// modes are handled:
//
//   - cfg.Provider is empty or unknown -> reset to "anthropic".
//   - cfg.Model is empty -> set to the provider's default.
//   - cfg.Model belongs to a different provider than cfg.Provider
//     (e.g. provider=anthropic + model=kimi-for-coding from a stale
//     half-applied switch) -> reset model to the provider's default.
//
// Silent on success; one stderr line per repair. Errors loading or
// saving the file are non-fatal — the caller continues with defaults.
func ValidateAndRepairConfig() {
	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "terva: config.json: %v (using defaults)\n", err)
		return
	}
	changed := false

	if cfg.Provider != "" && !isKnownProvider(cfg.Provider) {
		fmt.Fprintf(os.Stderr, "terva: config.json: unknown provider %q reset to \"anthropic\"\n", cfg.Provider)
		cfg.Provider = "anthropic"
		cfg.Model = ""
		changed = true
	}

	// ollama and openai-compatible are open-catalogue: any model id the
	// local/custom server understands is valid, so never rewrite it here.
	openCatalogue := cfg.Provider == "ollama" || cfg.Provider == "openai-compatible"
	if cfg.Provider != "" && cfg.Model != "" && !openCatalogue {
		if _, err := provider.FindModel(cfg.Provider, cfg.Model); err != nil {
			if m, err := provider.FindModel("", cfg.Model); err == nil {
				fix := defaultModelForProvider(cfg.Provider)
				fmt.Fprintf(os.Stderr,
					"terva: config.json: model %q belongs to provider %q (config has provider=%q); switched model to %q\n",
					cfg.Model, m.Provider, cfg.Provider, fix)
				cfg.Model = fix
				changed = true
			} else if cfg.Provider != "ollama" {
				// Model id not in any catalog. Reset to provider's default.
				fix := defaultModelForProvider(cfg.Provider)
				fmt.Fprintf(os.Stderr,
					"terva: config.json: model %q not found in the active catalog; switched to %q\n",
					cfg.Model, fix)
				cfg.Model = fix
				changed = true
			}
		}
	}

	if changed {
		if err := SaveConfig(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "terva: config.json: failed to persist repair: %v\n", err)
		}
	}
}

// RefreshModelsAsync kicks a background discovery for every provider
// we have credentials for. Refreshed results are merged into the
// active catalog and persisted to the on-disk cache.
//
// Silent on error: discovery is a nice-to-have. Callers can still use
// the baked-in catalog if this fails.
func RefreshModelsAsync() {
	go refreshModels()
}

func refreshModels() {
	cached, _ := provider.LoadCache(ModelCachePath())
	// Re-discover when the cache is stale (time/version) OR the user changed
	// their configured endpoints since it was written — so a newly-added
	// endpoint shows up immediately instead of waiting out the TTL.
	if cached.IsCurrent() && cached.Endpoints == endpointsFingerprint() {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var all []provider.Model

	if cred, method, err := ResolveCredential("anthropic", ""); err == nil && method == "apikey" {
		// /v1/models on Anthropic is API-key only; OAuth tokens can
		// also list models via the bearer header, but we skip OAuth
		// here to avoid surprise rate-limit hits on subscription keys.
		if live, err := provider.DiscoverAnthropic(ctx, cred, ""); err == nil {
			all = append(all, live...)
		}
	}
	if cred, method, err := ResolveCredential("openai", ""); err == nil && method == "apikey" {
		if live, err := provider.DiscoverOpenAI(ctx, cred, ""); err == nil {
			all = append(all, live...)
		}
	}
	if cred, method, err := ResolveCredential("kimi", ""); err == nil && method == "apikey" {
		if live, err := provider.DiscoverOpenAI(ctx, cred, "https://api.kimi.com/coding/v1"); err == nil {
			for i := range live {
				live[i].Provider = "kimi"
				live[i].Source = "live"
			}
			all = append(all, live...)
		}
	}
	if cred, method, err := ResolveCredential("google", ""); err == nil && method == "apikey" {
		if live, err := provider.DiscoverGoogle(ctx, cred, ""); err == nil {
			all = append(all, live...)
		}
	}
	if _, _, err := ResolveCredential("openrouter", ""); err == nil {
		// /models is public; gate on a credential so the picker only
		// fills with OpenRouter's hundreds of routes for users who use it.
		if live, err := provider.DiscoverOpenRouter(ctx, ""); err == nil {
			all = append(all, live...)
		}
	}
	// opencode.ai Zen gateways are openai-compatible; their model lists are
	// upstream-controlled (opencode-go is a subscription tier with its own
	// set), so discover from /v1/models rather than trusting the baked
	// catalog. opencode's Anthropic-protocol Claude models live at a
	// different endpoint and aren't enumerated here — they stay baked.
	for _, oc := range []struct{ id, baseURL string }{
		{"opencode", "https://opencode.ai/zen/v1"},
		{"opencode-go", "https://opencode.ai/zen/go/v1"},
	} {
		// /models is public; gate only on the user having connected this
		// provider (any credential/method), like the openrouter block.
		cred, _, err := ResolveCredential(oc.id, "")
		if err != nil {
			continue
		}
		live, err := provider.DiscoverOpenAICompatible(ctx, oc.baseURL, cred, compatDefaultContext())
		if err != nil {
			continue
		}
		for i := range live {
			live[i].Provider = oc.id
			live[i].Source = "live"
			live[i].BaseURL = oc.baseURL
		}
		all = append(all, live...)
	}
	// User-defined OpenAI-compatible endpoints (config.json "endpoints"): each
	// is its own provider; discover its /v1/models list. The key is optional
	// (most local servers need none) and resolves via APIKeyEnv/auth.json.
	if uc, err := LoadConfig(); err == nil {
		for id, ep := range uc.Endpoints {
			if strings.TrimSpace(ep.BaseURL) == "" {
				continue
			}
			cred, _, _ := ResolveCredential(id, "")
			defCtx := compatDefaultContext()
			if ep.ContextWindow > 0 {
				defCtx = ep.ContextWindow
			}
			live, derr := provider.DiscoverOpenAICompatible(ctx, ep.BaseURL, cred, defCtx)
			if derr != nil {
				continue
			}
			for i := range live {
				live[i].Provider = id
				live[i].Source = "live"
				live[i].BaseURL = ep.BaseURL
			}
			all = append(all, live...)
		}
	}
	if len(all) == 0 {
		return
	}
	// SetLiveModels writes only the "live" catalog layer; the compat
	// default model (extra layer) and models.json entries (user layer)
	// survive this landing at any time relative to other refreshes —
	// precedence is structural, not call-ordered.
	provider.SetLiveModels(all)
	_ = provider.SaveCache(ModelCachePath(), provider.ModelCache{
		FetchedAt: time.Now().UTC(),
		Version:   provider.ModelCacheVersion,
		Endpoints: endpointsFingerprint(),
		Models:    all,
	})
}

// endpointsFingerprint is a stable signature of the user's configured
// OpenAI-compatible endpoints (id + base URL). It rides the model cache so a
// change to the endpoint set forces a re-discovery on the next launch instead
// of waiting out CacheTTL.
func endpointsFingerprint() string {
	cfg, err := LoadConfig()
	if err != nil || len(cfg.Endpoints) == 0 {
		return ""
	}
	ids := make([]string, 0, len(cfg.Endpoints))
	for id := range cfg.Endpoints {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var sb strings.Builder
	for _, id := range ids {
		sb.WriteString(id)
		sb.WriteByte('=')
		sb.WriteString(cfg.Endpoints[id].BaseURL)
		sb.WriteByte(';')
	}
	return sb.String()
}
