package agent

import (
	"context"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/provider/auth"
)

// What a successful login means, beyond a credential landing on disk.
//
// This used to live inside the TUI's auth-event handler, which is the wrong
// place for it twice over: a daemon serving the web panel cannot import the TUI,
// so a second consumer would have had to reimplement it — and if it forgot to,
// the login would still "succeed" while the thing the user just configured never
// appeared. An openai-compatible login that does not register its model is a
// login that did nothing the user can see.
//
// So it lives here, above both frontends, and the TUI reaches it through a
// single seam (Config.ApplyLogin) rather than through four separate closures.

// ApplyLoginStart is the pre-flight for a login attempt.
//
// Only kimi has one: terva may fall back to the official Kimi Code CLI's token
// when it finds one, and choosing to log in to kimi properly means that fallback
// should stop being used. Doing it at START rather than on success is
// deliberate — it was written that way, and a half-finished login that has
// already told terva "stop borrowing the CLI's token" is the safer half.
func ApplyLoginStart(providerID string) error {
	if providerID == "kimi" {
		return config.SetKimiCLIFallbackDisabled(false)
	}
	return nil
}

// ApplyLogout is the counterpart: a kimi logout re-enables the CLI fallback, so
// logging out actually stops terva using the subscription.
func ApplyLogout(providerID string) error {
	if providerID == "kimi" {
		return config.SetKimiCLIFallbackDisabled(true)
	}
	return nil
}

// ApplyLoginSuccess makes a freshly-stored credential usable without a restart.
//
// promoteDefault persists a model as the global default; it is passed in rather
// than reached for because the TUI and the daemon resolve "default" through
// different config plumbing. A nil promoteDefault skips that step, which is the
// right degradation: the model is still registered and still selectable, it just
// is not made the default.
func ApplyLoginSuccess(store *auth.Store, providerID string, promoteDefault func(providerName, model, scope string) error) {
	// openai-compatible is the only api-key login that also carries a target
	// model — captured in the login form, because a custom endpoint has no
	// catalog entry to fall back on. Register it into the live catalog so it
	// appears in the model picker immediately, then make it the default: the
	// user just pointed terva at this endpoint, and having to then go and select
	// the model by hand would be a strange thing to ask of them.
	if providerID == "openai-compatible" && store != nil {
		baseURL, model, ctxWin := store.Extras("openai-compatible")
		if model != "" {
			if baseURL != "" {
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
			if promoteDefault != nil {
				_ = promoteDefault("openai-compatible", model, "global")
			}
			// And discover the rest of the endpoint's models in the background,
			// so they all appear without a restart.
			RefreshCompatModelsAsync()
		}
	}

	// A NAMED endpoint has the same problem from the other end: it carries no
	// model at all, because the form deliberately does not ask for one — a named
	// endpoint discovers what its server serves. So the login would leave behind a
	// provider the user can see and cannot run: the session builds with an empty
	// model id, and the server rejects the turn.
	//
	// Discover now, synchronously, unlike the async refreshes below. The caller is
	// about to build a session on this provider (CarrierLogin), and a default that
	// lands after that session is built is a default the user does not get. The
	// endpoint was probed seconds ago by saveEndpoint, so this is a second call to
	// a server already known to answer.
	if cfg, err := config.LoadConfig(); err == nil {
		if ep, ok := cfg.Endpoints[providerID]; ok {
			adoptEndpointModels(providerID, ep, promoteDefault)
		}
	}

	// Any fresh login can unlock more models than the baked catalog knows about
	// — opencode-go is a subscription tier whose /v1/models list is
	// upstream-controlled, and openrouter and kimi are likewise live. Force a
	// re-discovery, because the cache freshness gate would otherwise skip it.
	// Harmless for providers with no discovery endpoint, and for
	// openai-compatible (handled above; the forced refresh skips it).
	RefreshModelsForceAsync()
}

// adoptEndpointModels lists a named endpoint's models, registers them into the
// active catalog, and makes one the default.
//
// Silent on failure by design: the endpoint IS saved and registered by the time
// this runs, so a server that went away between the probe and here leaves the
// operator with a provider they can still pick a model on once it is back —
// which beats failing a login over a model list.
func adoptEndpointModels(id string, ep config.EndpointConfig, promoteDefault func(providerName, model, scope string) error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	defCtx := ep.ContextWindow
	if defCtx <= 0 {
		defCtx = unknownModelContext
	}
	// The key is optional and usually absent; ResolveCredential finds it under
	// the endpoint's own id when the server does want one.
	key, _, _ := build.ResolveCredential(id, "")
	live, err := provider.DiscoverOpenAICompatible(ctx, ep.BaseURL, key, defCtx)
	if err != nil || len(live) == 0 {
		return
	}
	for _, m := range live {
		// DiscoverOpenAICompatible stamps every model "openai-compatible" — it
		// does not know which endpoint asked. Re-stamp, or these land under the
		// shared slot and the endpoint's picker stays empty.
		m.Provider = id
		m.BaseURL = ep.BaseURL
		m.Source = "live"
		provider.RegisterExtraModel(m)
	}
	if promoteDefault == nil {
		return
	}
	if model := build.EndpointDefaultModel(id); model != "" {
		_ = promoteDefault(id, model, "global")
	}
}
