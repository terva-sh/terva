package auth

import (
	"testing"
	"time"
)

// openai is one store slot holding two independent logins: a platform API key
// and a ChatGPT/Codex subscription token. They are established separately, shown
// separately, and revoked separately — and every consumer that wanted to know
// "who is logged in" was reconstructing that split by hand.
func TestOpenAIsTwoLoginsAreReportedSeparately(t *testing.T) {
	var c Credentials
	c.OpenAI.APIKey = "sk-platform"
	c.OpenAI.OAuth = &OAuthToken{AccessToken: "codex-token"}

	got := Describe(c)
	if got["openai"].Method != "apikey" {
		t.Errorf("openai (the platform key) = %q, want apikey", got["openai"].Method)
	}
	if got["openai-codex"].Method != "oauth" {
		t.Errorf("openai-codex (the subscription) = %q, want oauth", got["openai-codex"].Method)
	}

	// And each is independent: clearing the key must not report the
	// subscription as gone.
	c.OpenAI.APIKey = ""
	got = Describe(c)
	if got["openai"].Method != "" {
		t.Errorf("openai = %q after clearing the key, want empty", got["openai"].Method)
	}
	if got["openai-codex"].Method != "oauth" {
		t.Error("clearing the platform key also reported the codex subscription as logged out")
	}
}

// The state a user actually needs to be told about: the subscription is dead and
// needs a re-login. Today the only way they find out is a failed turn.
func TestAnExpiredSubscriptionIsVisibleBeforeItBreaksATurn(t *testing.T) {
	var c Credentials
	c.Anthropic.OAuth = &OAuthToken{
		AccessToken: "stale",
		Expiry:      time.Now().Add(-time.Hour),
	}
	st := Describe(c)["anthropic"]
	if st.Method != "oauth" {
		t.Fatalf("method = %q, want oauth", st.Method)
	}
	if !st.Expired {
		t.Error("an hour-expired token does not report as expired; the panel cannot warn anyone")
	}
}

// The logout list used to be built from a hard-coded four — anthropic, kimi,
// google, github-copilot — plus openai, openai-codex, and any AdditionalAPIKeyCreds
// entry with a non-empty key. Two providers fell through that sieve and could
// never be logged out of individually:
//
//   - deepseek is a NAMED field in Credentials, so it was never in
//     AdditionalAPIKeyCreds, and it was not in the hard-coded four.
//   - a keyless openai-compatible endpoint (base URL set, no key — the normal
//     shape for lm studio / llama.cpp / ollama) has an empty APIKey, so the
//     `c.APIKey != ""` filter dropped it.
func TestEveryStoredCredentialCanBeSeen(t *testing.T) {
	var c Credentials
	c.DeepSeek.APIKey = "sk-deepseek"
	c.AdditionalAPIKeyCreds = map[string]ProviderCreds{
		"openai-compatible": {BaseURL: "http://localhost:1234/v1", Model: "qwen"},
	}

	got := Describe(c)
	if got["deepseek"].Method != "apikey" {
		t.Errorf("deepseek = %q, want apikey — it was invisible to the logout list", got["deepseek"].Method)
	}
	compat := got["openai-compatible"]
	if compat.Method != "apikey" {
		t.Errorf("keyless openai-compatible = %q, want apikey — a local endpoint could not be logged out of", compat.Method)
	}
	if compat.BaseURL != "http://localhost:1234/v1" || compat.Model != "qwen" {
		t.Errorf("compat endpoint config not reported: %+v", compat)
	}
}

// A caller can render the whole picker from Describe alone: every provider that
// can be logged into is present, logged in or not.
func TestDescribeListsEveryLoggableProviderEvenWhenNothingIsStored(t *testing.T) {
	got := Describe(Credentials{})

	for _, p := range append(APIKeyProviders(), OAuthProviders()...) {
		st, ok := got[p]
		if !ok {
			t.Errorf("%s is loggable but absent from Describe", p)
			continue
		}
		if st.Method != "" {
			t.Errorf("%s reports method %q on an empty store", p, st.Method)
		}
	}
	if _, ok := got["openai-codex"]; !ok {
		t.Error("openai-codex is a login id, not a storage id, and must still be listed")
	}
}

// Nothing here may carry a secret: these values go on a screen, and soon a wire.
func TestNoSecretSurvivesDescribe(t *testing.T) {
	var c Credentials
	c.Anthropic.APIKey = "sk-ant-SECRET"
	c.OpenAI.OAuth = &OAuthToken{AccessToken: "TOKEN-SECRET", RefreshToken: "REFRESH-SECRET"}

	for id, st := range Describe(c) {
		for _, field := range []string{st.Method, st.BaseURL, st.Model, st.ID} {
			for _, secret := range []string{"sk-ant-SECRET", "TOKEN-SECRET", "REFRESH-SECRET"} {
				if field == secret {
					t.Errorf("%s: Describe leaked %q", id, secret)
				}
			}
		}
	}
}

// The env-var providers are not a form: there is no key for terva to take, so a
// paste box would be a dead end that looks like a way in. They must be
// identifiable as such before a flow is started.
func TestEnvProvidersAnnounceThemselvesAsUnfillable(t *testing.T) {
	for _, p := range []string{
		"amazon-bedrock", "google-vertex", "azure-openai-responses",
		"cloudflare-workers-ai", "cloudflare-ai-gateway",
	} {
		env, ok := EnvProviderInfo(p)
		if !ok {
			t.Errorf("%s has no setup guidance; a user would get a paste box that stores nothing", p)
			continue
		}
		if env.Title == "" || len(env.Lines) == 0 {
			t.Errorf("%s: empty guidance", p)
		}
		if env.DocPath == "" {
			t.Errorf("%s: no doc path", p)
		}
	}
	if _, ok := EnvProviderInfo("anthropic"); ok {
		t.Error("anthropic is a normal api-key provider, not an env-var one")
	}
}
