package auth

import "time"

// The catalog: who terva can log into, by which method, and what state that
// credential is in right now.
//
// All of this used to live in the TUI — the oauth provider list and the
// env-var setup guidance in packages/agent/modes, and the "which providers are
// logged in, and how" logic reconstructed inline in TWO places (the login
// dialog's picker and the logout dialog's list), with the openai/openai-codex
// split hand-rolled in each. A daemon cannot import the TUI, so a second
// consumer would have meant a third copy. It lives here instead, below every
// consumer, so the TUI and the wire cannot drift on what "logged in" means.
//
// Nothing in this file returns a secret. A ProviderState is safe to put on a
// screen someone else can see, and safe to put on a wire.

// OAuthProviders returns the providers that issue a subscription token usable
// against the same API the model picker drives.
//
// It is deliberately not "every provider that has an OAuth endpoint": Google's
// consumer Gemini Advanced login does not yield Generative Language API
// credentials, and DeepSeek has no subscription product at all, so both are
// api-key only. The order here is declaration order; a caller that renders a
// picker sorts by label.
func OAuthProviders() []string {
	return []string{"anthropic", "openai-codex", "kimi", "github-copilot"}
}

// EnvProvider is a provider terva does not store a credential for at all: it is
// configured through the environment (an AWS profile, a GCP service account, an
// Azure deployment), and the most terva can do is say which variables to set.
//
// DocPath, not a URL: turning a repo path into a link is the presentation
// layer's job, and this package sits below it.
type EnvProvider struct {
	ID      string
	Title   string
	Lines   []string
	DocPath string
}

// EnvProviderInfo returns setup guidance for a provider configured by
// environment variables, and false for every other provider.
//
// A caller must check this BEFORE starting an api-key flow: these providers
// appear in the picker (they are real providers you can use) but there is no
// key for terva to take, so offering a paste box would be a dead end that looks
// like a way in.
func EnvProviderInfo(provider string) (EnvProvider, bool) {
	const docs = "docs/providers.md"
	switch provider {
	case "amazon-bedrock":
		return EnvProvider{
			ID:    provider,
			Title: "Amazon Bedrock setup",
			Lines: []string{
				"Amazon Bedrock uses AWS credentials instead of a generic terva API-key entry.",
				"Configure an AWS profile, IAM keys, bearer token, or role-based credentials.",
				"",
				"For Bedrock API keys, set:",
				"  AWS_BEARER_TOKEN_BEDROCK=...",
				"  AWS_REGION=us-east-1",
			},
			DocPath: docs,
		}, true
	case "google-vertex":
		return EnvProvider{
			ID:    provider,
			Title: "Google Vertex AI setup",
			Lines: []string{
				"Google Vertex AI usually uses Google Cloud credentials and project settings.",
				"Set a Google API key, application-default credentials, or a service account.",
				"",
				"Common environment:",
				"  GOOGLE_CLOUD_API_KEY=...",
				"  GOOGLE_CLOUD_PROJECT=...",
				"  GOOGLE_CLOUD_LOCATION=us-central1",
			},
			DocPath: docs,
		}, true
	case "cloudflare-workers-ai":
		return EnvProvider{
			ID:    provider,
			Title: "Cloudflare Workers AI setup",
			Lines: []string{
				"Cloudflare Workers AI needs both an API token and an account ID.",
				"",
				"Set:",
				"  CLOUDFLARE_API_KEY=...",
				"  CLOUDFLARE_ACCOUNT_ID=...",
			},
			DocPath: docs,
		}, true
	case "cloudflare-ai-gateway":
		return EnvProvider{
			ID:    provider,
			Title: "Cloudflare AI Gateway setup",
			Lines: []string{
				"Cloudflare AI Gateway needs an API token, account ID, and gateway ID.",
				"",
				"Set:",
				"  CLOUDFLARE_API_KEY=...",
				"  CLOUDFLARE_ACCOUNT_ID=...",
				"  CLOUDFLARE_GATEWAY_ID=...",
			},
			DocPath: docs,
		}, true
	case "azure-openai-responses":
		return EnvProvider{
			ID:    provider,
			Title: "Azure OpenAI Responses setup",
			Lines: []string{
				"Azure OpenAI needs an API key plus your Azure endpoint or deployment setup.",
				"",
				"Set:",
				"  AZURE_OPENAI_API_KEY=...",
				"  AZURE_OPENAI_BASE_URL=https://your-resource.openai.azure.com",
				"  AZURE_OPENAI_API_VERSION=2024-02-01",
			},
			DocPath: docs,
		}, true
	}
	return EnvProvider{}, false
}

// ProviderState is one provider's credential state. Every field is safe to show
// to whoever can see the screen: no key, no token, not a prefix of either.
type ProviderState struct {
	// ID is the LOGIN id, not the storage id — "openai-codex" is a distinct
	// login from "openai" even though both land in the same store slot. See
	// Describe.
	ID string

	// Method is "apikey", "oauth", or "" for not logged in.
	Method string

	// Expiry is an OAuth token's expiry; zero for an api key, and for an OAuth
	// token that does not expire.
	Expiry time.Time

	// Expired is the flag that matters to a user: their subscription needs a
	// re-login. Today this surfaces only as a failed turn.
	Expired bool

	// BaseURL, Model and ContextWindow describe an openai-compatible endpoint.
	// They are configuration, not credentials.
	BaseURL       string
	Model         string
	ContextWindow int
}

// Describe reports the state of every provider that can be logged into, keyed
// by LOGIN id.
//
// It takes Credentials by value because Store.Load returns them that way, and
// because the zero value is meaningful: nothing logged in. A caller whose load
// failed can hand the zero value straight in and get a correct, complete
// "nobody is logged in" picture — which is the honest thing to show when the
// credential file cannot be read.
//
// The one wrinkle it exists to own: openai holds two independent credentials in
// one store slot — a platform API key and a ChatGPT/Codex subscription token —
// and the two are logged in, listed, and logged out of separately. So a single
// stored ProviderCreds becomes up to two ProviderStates, "openai" and
// "openai-codex". Both the login picker and the logout list reconstructed that
// by hand, and any third consumer would have reconstructed it again.
func Describe(c Credentials) map[string]ProviderState {
	out := make(map[string]ProviderState)

	// Every provider that can be logged into starts as not-logged-in, so a
	// caller can render the full picker from this map alone rather than
	// intersecting it with a list of its own.
	for _, p := range APIKeyProviders() {
		out[p] = ProviderState{ID: p}
	}
	for _, p := range OAuthProviders() {
		if _, ok := out[p]; !ok {
			out[p] = ProviderState{ID: p}
		}
	}

	set := func(id string, st ProviderState) {
		st.ID = id
		out[id] = st
	}

	for id := range out {
		switch id {
		case "openai", "openai-codex":
			continue // split below
		}
		st := ProviderState{Method: c.Method(id)}
		if p := c.get(id); p != nil {
			if p.OAuth != nil {
				st.Expiry = p.OAuth.Expiry
				st.Expired = p.OAuth.Expired()
			}
			st.BaseURL, st.Model, st.ContextWindow = p.BaseURL, p.Model, p.ContextWindow
		}
		set(id, st)
	}

	// The split. An API key on openai is the platform login; an OAuth token on
	// the same slot is the Codex subscription. Neither implies the other, and
	// clearing one must not report the other as gone.
	openai := ProviderState{}
	if c.OpenAI.APIKey != "" {
		openai.Method = "apikey"
	}
	set("openai", openai)

	codex := ProviderState{}
	if c.OpenAI.OAuth != nil {
		codex.Method = "oauth"
		codex.Expiry = c.OpenAI.OAuth.Expiry
		codex.Expired = c.OpenAI.OAuth.Expired()
	}
	set("openai-codex", codex)

	return out
}
