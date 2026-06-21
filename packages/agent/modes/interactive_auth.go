package modes

// Credential flows: /login (api key, oauth, manual code), /logout, and
// live auth events from the auth manager.

import (
	"strings"

	"terva.sh/terva/packages/agent/identity"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/provider/auth"
)

func (i *Interactive) openLogoutDialog() {
	if i.cfg.AuthManager == nil {
		i.mu.Lock()
		i.statusErr = "no auth manager configured"
		i.mu.Unlock()
		i.invalidate()
		return
	}
	store := i.cfg.AuthManager.Store()
	if store == nil {
		i.mu.Lock()
		i.statusErr = "auth store is not available"
		i.mu.Unlock()
		i.invalidate()
		return
	}
	creds, err := store.Load()
	if err != nil {
		i.mu.Lock()
		i.statusErr = "read auth store: " + err.Error()
		i.mu.Unlock()
		i.invalidate()
		return
	}

	var items []logoutItem
	for _, p := range []string{"anthropic", "kimi", "google", "github-copilot"} {
		if creds.Has(p) {
			method := creds.Method(p)
			if method == "oauth" {
				method = "subscription"
			}
			items = append(items, logoutItem{
				label:  providerLabel(p),
				target: p,
				method: method,
			})
		}
	}
	if creds.OpenAI.APIKey != "" {
		items = append(items, logoutItem{label: providerLabel("openai"), target: "openai", method: "api key"})
	}
	if creds.OpenAI.OAuth != nil {
		items = append(items, logoutItem{label: providerLabel("openai-codex"), target: "openai-codex", method: "subscription"})
	}
	for p, c := range creds.AdditionalAPIKeyCreds {
		if c.APIKey != "" {
			items = append(items, logoutItem{label: providerLabel(p), target: p, method: "api key"})
		}
	}
	if len(items) == 0 {
		i.mu.Lock()
		i.statusOK = "no credentials stored; already logged out"
		i.statusErr = ""
		i.mu.Unlock()
		i.invalidate()
		return
	}
	if len(items) > 1 {
		items = append(items, logoutItem{label: "all", target: "all"})
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
		i.statusErr = "no auth manager configured"
		i.mu.Unlock()
		return
	}
	store := i.cfg.AuthManager.Store()
	if store == nil {
		i.mu.Lock()
		i.statusErr = "auth store is not available"
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
			i.statusErr = "unknown provider: " + target
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
		i.statusErr = "logout errors: " + strings.Join(errs, "; ")
		return
	}
	i.statusErr = ""
	if clearedCurrent {
		// The running agent was using a credential we just wiped. Drop
		// it so prompts can't go out with the stale client, and hint at
		// /login.
		i.turns.SetAgent(nil)
		i.statusOK = "logged out of " + strings.Join(providers, ", ") + ". type /login to sign back in."
	} else {
		i.statusOK = "logged out of " + strings.Join(providers, ", ")
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
	i.dialog.url = url
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
		// Rebuild the agent with the fresh credential.
		ag, prov, model, err := i.cfg.BuildAgent()
		if err != nil {
			i.dialog.ShowResult(false, err.Error())
			return
		}
		i.turns.SetAgent(ag)
		i.mu.Lock()
		i.cfg.Provider = prov
		i.cfg.Model = model
		i.statusErr = ""
		i.statusOK = "logged in to " + ev.Provider + " via " + ev.Method
		i.mu.Unlock()
		i.applyChatTools(i.chatBridge != nil && i.chatBridge.Active())
		i.dialog.ShowResult(true, "")
	}
}
