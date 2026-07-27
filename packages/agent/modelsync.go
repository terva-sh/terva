package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider"
)

// ModelCachePath returns the on-disk location of the merged model cache.
func ModelCachePath() string {
	return filepath.Join(config.TervaHome(), "models-cache.json")
}

// UserModelsPath returns the path to the user's models.json override.
func UserModelsPath() string { return config.UserModelsPath() }

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
	baseURL, model, ctxWin := config.AuthStoreFor().Extras("openai-compatible")
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

// EnsureEndpointModels gives each configured endpoint a model list BEFORE the
// first Resolve, when the on-disk cache has none for it.
//
// An endpoint's models are DISCOVERED, never baked in, so on the first launch
// after one is created the catalog has never heard of it — and Resolve is then
// choosing between refusing to boot and asking the server to run the empty model
// id. The async refreshes are the right tool everywhere else and the wrong one
// here: they land after the session is already built.
//
// Short-deadlined and concurrent, because this sits on the startup path: a
// server that is switched off must cost a second or two, not twenty, and must
// never be the reason terva did not start. Endpoints the cache already covers
// are skipped entirely, so the common launch does no I/O at all.
func EnsureEndpointModels() {
	cfg, err := config.LoadConfig()
	if err != nil || len(cfg.Endpoints) == 0 {
		return
	}
	type pending struct {
		id string
		ep config.EndpointConfig
	}
	var todo []pending
	for id, ep := range cfg.Endpoints {
		if strings.TrimSpace(ep.BaseURL) == "" {
			continue
		}
		if len(provider.ModelsForProvider(id)) > 0 {
			continue // the cache already answered for this one
		}
		todo = append(todo, pending{id, ep})
	}
	if len(todo) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), endpointWarmupTimeout)
	defer cancel()
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, p := range todo {
		wg.Add(1)
		go func(p pending) {
			defer wg.Done()
			defCtx := p.ep.ContextWindow
			if defCtx <= 0 {
				defCtx = unknownModelContext
			}
			cred, _, _ := build.ResolveCredential(p.id, "")
			live, err := provider.DiscoverOpenAICompatible(ctx, p.ep.BaseURL, cred, defCtx)
			if err != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, m := range live {
				// Discovery stamps everything "openai-compatible"; it does not
				// know which endpoint asked.
				m.Provider = p.id
				m.BaseURL = p.ep.BaseURL
				m.Source = "live"
				provider.RegisterExtraModel(m)
			}
		}(p)
	}
	wg.Wait()
}

// endpointWarmupTimeout bounds the whole startup warm-up, not each endpoint:
// they run concurrently, so several unreachable servers cost the same as one.
const endpointWarmupTimeout = 3 * time.Second

// unknownModelContext is the window assumed for a model nobody can describe:
// the provider's /v1/models list names it, the baked catalog has never heard of
// it, and no hint came back with it. Deliberately conservative — under-guessing
// only makes terva compact earlier than it needs to, while over-guessing
// overflows the model's real window and the request fails outright.
//
// It is a floor for the genuinely unknown, not a default for the merely
// unlisted: a model in the baked catalog keeps its curated window (MergeCatalog
// preserves it), so this is only reached by models missing from the catalog.
// `just models-sync` reports exactly which those are.
const unknownModelContext = 32768

// compatDefaultContext returns the user's configured default context window for
// THE openai-compatible endpoint, falling back to unknownModelContext.
//
// It applies to that provider only. It used to be handed to the opencode and
// opencode-go discoveries as well, which meant a context window the user had
// configured for their own local server — an LM Studio box, say — silently
// became the assumed window for a completely unrelated hosted gateway's models.
func compatDefaultContext() int {
	if _, _, ctxWin := config.AuthStoreFor().Extras("openai-compatible"); ctxWin > 0 {
		return ctxWin
	}
	return unknownModelContext
}

// RefreshCompatModelsAsync discovers the openai-compatible endpoint's
// models in the background and registers them into the active catalog.
// Unlike RefreshModelsAsync it is NOT gated on the 6h model cache: a
// local server's loaded model set changes often and the /v1/models query
// is cheap, so we re-list on every launch (and after a fresh login).
// No-op when the endpoint isn't configured.
func RefreshCompatModelsAsync() {
	baseURL, _, _ := config.AuthStoreFor().Extras("openai-compatible")
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
		key, _, _ := build.ResolveCredential("openai-compatible", "")
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
	cfg, err := config.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "terva: config.json: %v (using defaults)\n", err)
		return
	}
	changed := false

	if cfg.Provider != "" && !build.IsKnownProvider(cfg.Provider) {
		fmt.Fprintf(os.Stderr, "terva: config.json: unknown provider %q reset to \"anthropic\"\n", cfg.Provider)
		cfg.Provider = "anthropic"
		cfg.Model = ""
		changed = true
	}

	// ollama, openai-compatible and the operator's own named endpoints are
	// open-catalogue: any model id the local/custom server understands is valid,
	// so never rewrite it here.
	//
	// Endpoints were missing from this list, and the omission was not cosmetic.
	// An endpoint's models are DISCOVERED, and discovery has not run yet on the
	// launch right after /login created it — so the id the operator just chose
	// was "not in the active catalog", got repaired to the endpoint's default
	// (which is "", since an endpoint has none), and terva then asked the server
	// to run the empty model.
	openCatalogue := cfg.Provider == "ollama" || cfg.Provider == "openai-compatible" ||
		build.IsEndpointProvider(cfg.Provider, cfg)
	if cfg.Provider != "" && cfg.Model != "" && !openCatalogue {
		if _, err := provider.FindModel(cfg.Provider, cfg.Model); err != nil {
			if m, err := provider.FindModel("", cfg.Model); err == nil {
				fix := build.DefaultModelForProvider(cfg.Provider)
				fmt.Fprintf(os.Stderr,
					"terva: config.json: model %q belongs to provider %q (config has provider=%q); switched model to %q\n",
					cfg.Model, m.Provider, cfg.Provider, fix)
				cfg.Model = fix
				changed = true
			} else if cfg.Provider != "ollama" {
				// Model id not in any catalog. Reset to provider's default.
				fix := build.DefaultModelForProvider(cfg.Provider)
				fmt.Fprintf(os.Stderr,
					"terva: config.json: model %q not found in the active catalog; switched to %q\n",
					cfg.Model, fix)
				cfg.Model = fix
				changed = true
			}
		}
	}

	if changed {
		if err := config.SaveConfig(cfg); err != nil {
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
	launchModelRefresh(false)
}

// RefreshModelsForceAsync runs a discovery that ignores the on-disk cache
// freshness gate. It's the runtime counterpart to RefreshModelsAsync: when a
// credential is added mid-session (e.g. /login to opencode-go), the just-
// connected provider's /v1/models list must land without a restart — even
// when a still-fresh cache written for some OTHER provider would normally
// short-circuit discovery. The cache fingerprint tracks user-defined
// endpoints, not which built-in providers have credentials, so a new login
// alone doesn't invalidate it; forcing does.
func RefreshModelsForceAsync() {
	launchModelRefresh(true)
}

// modelRefreshWG tracks every in-flight background discovery so tests can
// join them; production callers never wait (discovery is fire-and-forget by
// design, bounded by refreshModels' internal 20s context).
var modelRefreshWG sync.WaitGroup

func launchModelRefresh(force bool) {
	// The cache path is resolved HERE, not inside the goroutine: the refresh
	// outlives its caller by up to 20s of HTTP, and $TERVA_HOME is mutable
	// while it runs (t.Setenv). A write-time resolve let a refresh leaked by
	// one test drop models-cache.json into a LATER test's scratch home in the
	// middle of its TempDir cleanup — the "directory not empty" flake. The
	// credential/config reads inside stay live on purpose; they only decide
	// WHAT is discovered, never where the write lands.
	cachePath := ModelCachePath()
	modelRefreshWG.Add(1)
	go func() {
		defer modelRefreshWG.Done()
		refreshModels(cachePath, force)
	}()
}

// waitModelRefresh blocks until every in-flight background discovery has
// finished. Test-only: a test whose call graph reaches RefreshModels*Async
// (e.g. via ApplyLoginSuccess) must register `t.Cleanup(waitModelRefresh)`
// after its TERVA_HOME setup, or the leaked goroutine outlives the test.
func waitModelRefresh() { modelRefreshWG.Wait() }

func refreshModels(cachePath string, force bool) {
	cached, _ := provider.LoadCache(cachePath)
	if refreshGated(cached, force) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var all []provider.Model

	if cred, method, err := build.ResolveCredential("anthropic", ""); err == nil && method == "apikey" {
		// /v1/models on Anthropic is API-key only; OAuth tokens can
		// also list models via the bearer header, but we skip OAuth
		// here to avoid surprise rate-limit hits on subscription keys.
		if live, err := provider.DiscoverAnthropic(ctx, cred, ""); err == nil {
			all = append(all, live...)
		}
	}
	if cred, method, err := build.ResolveCredential("openai", ""); err == nil && method == "apikey" {
		if live, err := provider.DiscoverOpenAI(ctx, cred, ""); err == nil {
			all = append(all, live...)
		}
	}
	if cred, method, err := build.ResolveCredential("kimi", ""); err == nil && method == "apikey" {
		if live, err := provider.DiscoverOpenAI(ctx, cred, "https://api.kimi.com/coding/v1"); err == nil {
			for i := range live {
				live[i].Provider = "kimi"
				live[i].Source = "live"
			}
			all = append(all, live...)
		}
	}
	if cred, method, err := build.ResolveCredential("google", ""); err == nil && method == "apikey" {
		if live, err := provider.DiscoverGoogle(ctx, cred, ""); err == nil {
			all = append(all, live...)
		}
	}
	if _, _, err := build.ResolveCredential("openrouter", ""); err == nil {
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
		cred, _, err := build.ResolveCredential(oc.id, "")
		if err != nil {
			continue
		}
		// unknownModelContext, not compatDefaultContext: these are hosted
		// gateways, and the openai-compatible endpoint's configured window is
		// the user's own local server's, which has nothing to do with them.
		// Anything in the baked catalog keeps its curated window regardless —
		// MergeCatalog preserves it — so this only lands on models the catalog
		// is missing, which `just models-sync` reports.
		live, err := provider.DiscoverOpenAICompatible(ctx, oc.baseURL, cred, unknownModelContext)
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
	if uc, err := config.LoadConfig(); err == nil {
		for id, ep := range uc.Endpoints {
			if strings.TrimSpace(ep.BaseURL) == "" {
				continue
			}
			cred, _, _ := build.ResolveCredential(id, "")
			// This endpoint's own configured window, or the unknown floor —
			// never the openai-compatible provider's, which belongs to a
			// different server entirely.
			defCtx := unknownModelContext
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
	_ = provider.SaveCache(cachePath, provider.ModelCache{
		FetchedAt: time.Now().UTC(),
		Version:   provider.ModelCacheVersion,
		Endpoints: endpointsFingerprint(),
		Models:    all,
	})
}

// refreshGated reports whether a discovery run should be skipped because the
// on-disk cache is still authoritative. A forced run (a credential just
// changed mid-session) never skips: the cache fingerprint tracks user-defined
// endpoints, not which built-in providers have credentials, so a fresh login
// alone wouldn't otherwise invalidate it. An unforced run skips a cache that
// is current (fresh + matching discovery version) AND whose endpoint
// fingerprint is unchanged.
func refreshGated(cached provider.ModelCache, force bool) bool {
	return !force && cached.IsCurrent() && cached.Endpoints == endpointsFingerprint()
}

// endpointsFingerprint is a stable signature of the user's configured
// OpenAI-compatible endpoints (id + base URL). It rides the model cache so a
// change to the endpoint set forces a re-discovery on the next launch instead
// of waiting out CacheTTL.
func endpointsFingerprint() string {
	cfg, err := config.LoadConfig()
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
