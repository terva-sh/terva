package modes

// Credential flows: /login (api key, oauth, manual code), /logout, and
// live auth events from the auth manager.

import (
	"strings"

	"terva.sh/terva/packages/agent/identity"
	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/provider/auth"
)

func (i *Interactive) openLogoutDialog() {
	if i.cfg.AuthManager == nil {
		i.mu.Lock()
		i.statusErr = i18n.T("no auth manager configured")
		i.mu.Unlock()
		i.invalidate()
		return
	}
	store := i.cfg.AuthManager.Store()
	if store == nil {
		i.mu.Lock()
		i.statusErr = i18n.T("auth store is not available")
		i.mu.Unlock()
		i.invalidate()
		return
	}
	creds, err := store.Load()
	if err != nil {
		i.mu.Lock()
		i.statusErr = i18n.T("read auth store: %s", err.Error())
		i.mu.Unlock()
		i.invalidate()
		return
	}

	var items []dialogs.LogoutItem
	for _, p := range []string{"anthropic", "kimi", "google", "github-copilot"} {
		if creds.Has(p) {
			method := creds.Method(p)
			if method == "oauth" {
				method = "subscription"
			}
			items = append(items, dialogs.LogoutItem{
				Label:  dialogs.ProviderLabel(p),
				Target: p,
				Method: method,
			})
		}
	}
	if creds.OpenAI.APIKey != "" {
		items = append(items, dialogs.LogoutItem{Label: dialogs.ProviderLabel("openai"), Target: "openai", Method: "api key"})
	}
	if creds.OpenAI.OAuth != nil {
		items = append(items, dialogs.LogoutItem{Label: dialogs.ProviderLabel("openai-codex"), Target: "openai-codex", Method: "subscription"})
	}
	for p, c := range creds.AdditionalAPIKeyCreds {
		if c.APIKey != "" {
			items = append(items, dialogs.LogoutItem{Label: dialogs.ProviderLabel(p), Target: p, Method: "api key"})
		}
	}
	if len(items) == 0 {
		i.mu.Lock()
		i.statusOK = i18n.T("no credentials stored; already logged out")
		i.statusErr = ""
		i.mu.Unlock()
		i.invalidate()
		return
	}
	if len(items) > 1 {
		items = append(items, dialogs.LogoutItem{Label: "all", Target: "all"})
	}

	i.logoutDialog.Open(items)
	i.invalidate()
}

// doLogout clears credentials for the given provider (or all providers)
// from auth.json. If the active agent was using those credentials, it
// is torn down so the user is forced through /login before their next
// prompt.
//
// target: "anthropic" | "openai" | "kimi" | "github-copilot" | "all"
func (i *Interactive) doLogout(target string) {
	if i.cfg.AuthManager == nil {
		i.mu.Lock()
		i.statusErr = i18n.T("no auth manager configured")
		i.mu.Unlock()
		return
	}
	store := i.cfg.AuthManager.Store()
	if store == nil {
		i.mu.Lock()
		i.statusErr = i18n.T("auth store is not available")
		i.mu.Unlock()
		return
	}

	var providers []string
	switch target {
	case "", "all":
		providers = append([]string{"anthropic", "openai", "openai-codex", "kimi", "google", "github-copilot"}, auth.APIKeyProviders()...)
	case "anthropic", "openai", "openai-codex", "kimi", "google", "github-copilot":
		providers = []string{target}
	default:
		known := false
		for _, p := range auth.APIKeyProviders() {
			if target == p {
				known = true
				break
			}
		}
		if !known {
			i.mu.Lock()
			i.statusErr = i18n.T("unknown provider: %s", target)
			i.mu.Unlock()
			return
		}
		providers = []string{target}
	}

	var errs []string
	clearedCurrent := false
	for _, p := range providers {
		var err error
		switch p {
		case "openai":
			err = store.ClearAPIKey("openai")
		case "openai-codex":
			err = store.ClearOAuth("openai")
		default:
			err = store.Clear(p)
		}
		if err != nil {
			errs = append(errs, p+": "+err.Error())
			continue
		}
		if p == "kimi" && i.cfg.SetKimiCLIFallbackDisabled != nil {
			if err := i.cfg.SetKimiCLIFallbackDisabled(true); err != nil {
				errs = append(errs, p+": "+err.Error())
				continue
			}
		}
		if p == i.cfg.Provider {
			clearedCurrent = true
		}
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	if len(errs) > 0 {
		i.statusErr = i18n.T("logout errors: %s", strings.Join(errs, "; "))
		return
	}
	i.statusErr = ""
	if clearedCurrent {
		// The credential this session's daemon agent was using is gone. Close
		// the prompt gate so nothing goes out until /login; the daemon holds the
		// agent and drops the stale client on its side.
		i.setReadyLocked(false)
		i.statusOK = i18n.T("logged out of %s. type /login to sign back in.", strings.Join(providers, ", "))
	} else {
		i.statusOK = i18n.T("logged out of %s", strings.Join(providers, ", "))
	}
}

func providerSetupInfo(provider string) (string, []string, bool) {
	docsURL := identity.RawDocURL("docs/providers.md")
	switch provider {
	case "amazon-bedrock":
		return "Amazon Bedrock setup", []string{
			"Amazon Bedrock uses AWS credentials instead of a generic terva API-key entry.",
			"Configure an AWS profile, IAM keys, bearer token, or role-based credentials.",
			"",
			"For Bedrock API keys, set:",
			"  AWS_BEARER_TOKEN_BEDROCK=...",
			"  AWS_REGION=us-east-1",
			"",
			"Docs:",
			"  " + docsURL,
		}, true
	case "google-vertex":
		return "Google Vertex AI setup", []string{
			"Google Vertex AI usually uses Google Cloud credentials and project settings.",
			"Set a Google API key, application-default credentials, or a service account.",
			"",
			"Common environment:",
			"  GOOGLE_CLOUD_API_KEY=...",
			"  GOOGLE_CLOUD_PROJECT=...",
			"  GOOGLE_CLOUD_LOCATION=us-central1",
			"",
			"Docs:",
			"  " + docsURL,
		}, true
	case "cloudflare-workers-ai":
		return "Cloudflare Workers AI setup", []string{
			"Cloudflare Workers AI needs both an API token and an account ID.",
			"",
			"Set:",
			"  CLOUDFLARE_API_KEY=...",
			"  CLOUDFLARE_ACCOUNT_ID=...",
			"",
			"Docs:",
			"  " + docsURL,
		}, true
	case "cloudflare-ai-gateway":
		return "Cloudflare AI Gateway setup", []string{
			"Cloudflare AI Gateway needs an API token, account ID, and gateway ID.",
			"",
			"Set:",
			"  CLOUDFLARE_API_KEY=...",
			"  CLOUDFLARE_ACCOUNT_ID=...",
			"  CLOUDFLARE_GATEWAY_ID=...",
			"",
			"Docs:",
			"  " + docsURL,
		}, true
	case "azure-openai-responses":
		return "Azure OpenAI Responses setup", []string{
			"Azure OpenAI needs an API key plus your Azure endpoint or deployment setup.",
			"",
			"Set:",
			"  AZURE_OPENAI_API_KEY=...",
			"  AZURE_OPENAI_BASE_URL=https://your-resource.openai.azure.com",
			"  AZURE_OPENAI_API_VERSION=2024-02-01",
			"",
			"Docs:",
			"  " + docsURL,
		}, true
	default:
		return "", nil, false
	}
}

func (i *Interactive) startAPIKeyFlow(provider string) {
	if title, lines, ok := providerSetupInfo(provider); ok {
		i.dialog.ShowInfo(title, lines)
		return
	}
	if provider == "kimi" && i.cfg.SetKimiCLIFallbackDisabled != nil {
		_ = i.cfg.SetKimiCLIFallbackDisabled(false)
	}
	url, err := i.cfg.AuthManager.StartAPIKey(provider)
	if err != nil {
		i.dialog.ShowResult(false, err.Error())
		return
	}
	// The compat endpoint needs a base URL and a model as well as a key, so
	// a single paste box cannot express it. Collect the fields in the TUI —
	// that is the only path that works headless. The browser form is still
	// running at url for anyone who prefers it, so show that too.
	if provider == dialogs.CompatProvider {
		i.dialog.ShowCompatForm(url)
		return
	}
	i.dialog.ShowWaiting(url)
}

func (i *Interactive) startOAuthFlow(provider string) {
	if provider == "kimi" && i.cfg.SetKimiCLIFallbackDisabled != nil {
		_ = i.cfg.SetKimiCLIFallbackDisabled(false)
	}
	// Always run the manual/copy-code flow in parallel with the local
	// callback server so headless environments (docker, SSH) can paste
	// the authorization code directly without first pressing 'p'.
	_, err := i.cfg.AuthManager.StartOAuth(provider)
	if err != nil {
		i.dialog.ShowResult(false, err.Error())
		return
	}
	manualURL, mErr := i.cfg.AuthManager.StartManualOAuth(provider)
	if mErr == nil {
		i.dialog.ShowWaiting(manualURL)
	} else {
		i.dialog.ShowResult(false, mErr.Error())
	}
}

func (i *Interactive) startManualOAuthFlow(provider string) {
	if i.cfg.AuthManager == nil {
		return
	}
	i.cfg.AuthManager.CancelOAuth()
	url, err := i.cfg.AuthManager.StartManualOAuth(provider)
	if err != nil {
		i.dialog.ShowResult(false, err.Error())
		return
	}
	i.dialog.SetURL(url)
	i.invalidate()
}

func (i *Interactive) submitManualOAuthCode(code string) {
	if i.cfg.AuthManager == nil {
		return
	}
	go func() {
		// CompleteManualOAuth already emits an "error" (or "success")
		// event on AuthManager.Events(), which the main Run() loop
		// consumes via handleAuthEvent and routes to the loginDialog on
		// the main goroutine. Mutating i.dialog here would race that
		// goroutine — loginDialog has no mutex — so we just trigger the
		// exchange and let the event path deliver the result. The
		// returned error is surfaced via the emitted event; the only
		// non-emitting branches (empty code / no flow in progress) are
		// unreachable from the dialog submit path, which gates on a
		// non-empty SubmitCode with a manual flow already started.
		_ = i.cfg.AuthManager.CompleteManualOAuth(i.runCtx, code)
	}()
}

// submitAPIKey stores a key pasted into the login dialog. On a headless
// host the api-key page (loopback, random port) cannot be reached, so this
// is the only way to finish the flow.
//
// Like submitManualOAuthCode, the result comes back over
// AuthManager.Events(), which Run() consumes via handleAuthEvent on the main
// goroutine. Touching i.dialog from here would race that — loginDialog has
// no mutex — so the event path owns the result. Every return path of the
// Complete* calls emits, so nothing can be swallowed.
func (i *Interactive) submitAPIKey(provider, key string) {
	if i.cfg.AuthManager == nil {
		return
	}
	go func() {
		_ = i.cfg.AuthManager.CompleteAPIKey(i.runCtx, provider, key)
	}()
}

// submitCompatAPIKey stores an openai-compatible endpoint entered in the
// login dialog's compat form: base URL and model id, plus an optional key
// and default context window.
func (i *Interactive) submitCompatAPIKey(baseURL, model, key string, contextWindow int) {
	if i.cfg.AuthManager == nil {
		return
	}
	go func() {
		_ = i.cfg.AuthManager.CompleteCompatAPIKey(i.runCtx, baseURL, model, key, contextWindow)
	}()
}

func (i *Interactive) handleAuthEvent(ev auth.Event) {
	switch ev.Kind {
	case "started":
		i.dialog.ShowWaiting(ev.URL)
	case "browser_open":
		// no-op
	case "error":
		i.dialog.ShowResult(false, ev.Message)
	case "success":
		// A fresh openai-compatible login is the only api-key flow that
		// also carries a target model (captured in the login form). Persist
		// the provider+model so the rebuilt agent — and the next launch —
		// point at the local/custom endpoint. Other providers rely on the
		// auto-fallback in Resolve and don't need this.
		if ev.Provider == "openai-compatible" && i.cfg.AuthManager != nil {
			baseURL, mdl, ctxWin := i.cfg.AuthManager.Store().Extras("openai-compatible")
			if mdl != "" {
				// Register the model into the live catalog so it appears
				// in the /model picker without a restart, then persist the
				// pick so the rebuilt agent and the next launch use it.
				// (A full /v1/models discovery also runs in the background.)
				if baseURL != "" {
					if ctxWin <= 0 {
						ctxWin = 32768
					}
					provider.RegisterExtraModel(provider.Model{
						Provider:      "openai-compatible",
						ID:            mdl,
						DisplayName:   mdl,
						ContextWindow: ctxWin,
						MaxOutput:     8192,
						BaseURL:       baseURL,
						Source:        "openai-compatible",
					})
				}
				// Just configured this endpoint+model via /login: make it the
				// global default (the prior behavior), not merely session-only.
				if i.cfg.PromoteModelDefault != nil {
					_ = i.cfg.PromoteModelDefault("openai-compatible", mdl, "global")
				}
				// Discover the rest of the endpoint's models in the
				// background so they all appear in /model without a restart.
				if i.cfg.RefreshCompatModels != nil {
					i.cfg.RefreshCompatModels()
				}
			}
		}
		// Any fresh login can unlock more models than the baked catalog
		// (opencode-go is a subscription tier whose /v1/models list is
		// upstream-controlled; openrouter, kimi, … likewise). Force a live
		// re-discovery so the full set lands in /model without a restart —
		// the cache freshness gate would otherwise skip it. Harmless for
		// providers without /v1/models discovery and for openai-compatible
		// (handled by RefreshCompatModels above; refreshModels skips it).
		if i.cfg.RefreshModels != nil {
			i.cfg.RefreshModels()
		}
		// Apply the fresh credential. The workspace owns the agents:
		// CarrierLogin refreshes its credential/defaults and ensures a session
		// (creating the first one on a credential-less boot, rebuilding the
		// live session's client on a re-login).
		i.finishCarrierLogin(ev)
	}
}

// finishCarrierLogin is the carrier half of the login-success handler: the
// host's CarrierLogin closure does the workspace-side work, then the TUI
// binds onto the returned session. A first login (credential-less boot) comes
// back with a fresh session to switch onto; a re-login returns the current
// binding rebuilt in place (its client was hot-swapped, the crutch agent
// handle is unchanged), so only the labels need refreshing.
func (i *Interactive) finishCarrierLogin(ev auth.Event) {
	if i.cfg.CarrierLogin == nil {
		// A remote/replay carrier: credentials belong to the daemon, and the
		// login dialog should not have been reachable (the auto-open and
		// /login both key off CarrierLogin). Fail soft if it was.
		i.dialog.ShowResult(false, i18n.T("this session's credentials are owned by the daemon"))
		return
	}
	info, err := i.cfg.CarrierLogin(i.carrierSession())
	if err != nil {
		i.dialog.ShowResult(false, err.Error())
		return
	}
	if i.carrierSession() != info.ID {
		if err := i.SwitchCarrierSession(info.ID); err != nil {
			i.dialog.ShowResult(false, err.Error())
			return
		}
	} else if !i.ready() {
		// Same session, re-authenticated: reopen the prompt gate. The daemon
		// re-armed the session's agent with the new credential when the login
		// landed; the TUI holds none, so opening the gate is all there is to do.
		i.setReady(true)
	}
	i.mu.Lock()
	if info.Provider != "" {
		i.cfg.Provider = info.Provider
	}
	if info.Model != "" {
		i.cfg.Model = info.Model
	}
	i.statusErr = ""
	i.statusOK = i18n.T("logged in to %s via %s", ev.Provider, ev.Method)
	i.mu.Unlock()
	i.dialog.ShowResult(true, "")
	// --resume on a credential-less boot: the picker deferred to the login
	// dialog at startup; now that a session exists, arm it. The login dialog
	// sits above the session browser in the overlay order, so the picker
	// surfaces when the user dismisses the login result. One-shot: a later
	// re-login must not reopen it.
	if i.bootSessionsPending {
		i.bootSessionsPending = false
		i.openSessionsDialog()
	}
}
