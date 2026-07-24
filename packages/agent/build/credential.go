package build

import (
	"fmt"
	"os"

	"terva.sh/terva/packages/agent/config"
)

// Credential resolution reads the provider registry (providerByID) for each
// provider's api-Key env vars, so it sits above the config layer with the
// registry rather than inside it.

// ResolveCredential returns the credential (api Key or oauth access
// token), the method ("apikey"/"oauth"), and an error when no
// credential is available.
//
// Lookup order:
//  1. explicit (e.g. --api-Key): treated as API Key
//  2. provider-specific env var: treated as API Key
//  3. auth.json: api Key OR oauth, whichever is present
func ResolveCredential(provider, explicit string) (cred, method string, err error) {
	cred, method, _, err = ResolveCredentialFull(provider, explicit)
	return cred, method, err
}

// ResolveCredentialFull is like ResolveCredential but also returns a
// provider-specific accountID when the credential is an OpenAI OAuth
// token (the ChatGPT account id extracted from the stored id_token).
// accountID is "" for API-Key auth and for anthropic.
func ResolveCredentialFull(provider, explicit string) (cred, method, accountID string, err error) {
	if explicit != "" {
		return explicit, "apikey", "", nil
	}
	// Environment lookup, driven by the provider registry's apiKeyEnv
	// list (provider_registry.go). Two providers need handling beyond
	// a flat ordered Key list:
	//   - anthropic: ANTHROPIC_OAUTH_TOKEN yields subscription ("oauth")
	//     auth and takes precedence over its API Key.
	//   - amazon-bedrock: credentials come from the AWS chain (profile,
	//     IAM keys, container creds, bearer token); we surface a
	//     sentinel so Resolve doesn't error on a "missing" Key.
	if provider == "anthropic" {
		if v := os.Getenv("ANTHROPIC_OAUTH_TOKEN"); v != "" {
			return v, "oauth", "", nil
		}
	}
	if spec, ok := specFor(provider); ok {
		for _, ev := range spec.apiKeyEnv {
			if v := os.Getenv(ev); v != "" {
				return v, "apikey", "", nil
			}
		}
	}
	if provider == "amazon-bedrock" {
		if os.Getenv("AWS_ACCESS_KEY_ID") != "" || os.Getenv("AWS_PROFILE") != "" ||
			os.Getenv("AWS_BEARER_TOKEN_BEDROCK") != "" {
			return "<aws>", "apikey", "", nil
		}
	}
	c, err := config.AuthStoreFor().Load()
	if err != nil {
		return "", "", "", err
	}
	if pc, ok := c.AdditionalAPIKeyCreds[provider]; ok && pc.APIKey != "" {
		return pc.APIKey, "apikey", "", nil
	}
	switch provider {
	case "anthropic":
		if c.Anthropic.APIKey != "" {
			return c.Anthropic.APIKey, "apikey", "", nil
		}
		if c.Anthropic.OAuth != nil && c.Anthropic.OAuth.AccessToken != "" {
			tok, _ := config.RefreshIfExpired("anthropic", c.Anthropic.OAuth)
			return tok.AccessToken, "oauth", "", nil
		}
	case "openai":
		if c.OpenAI.APIKey != "" {
			return c.OpenAI.APIKey, "apikey", "", nil
		}
	case "openai-codex":
		if c.OpenAI.OAuth != nil && c.OpenAI.OAuth.AccessToken != "" {
			tok, _ := config.RefreshIfExpired("openai", c.OpenAI.OAuth)
			return tok.AccessToken, "oauth", tok.AccountID, nil
		}
	case "kimi":
		if c.Kimi.APIKey != "" {
			return c.Kimi.APIKey, "apikey", "", nil
		}
		if c.Kimi.OAuth != nil && c.Kimi.OAuth.AccessToken != "" {
			tok, _ := config.RefreshIfExpired("kimi", c.Kimi.OAuth)
			return tok.AccessToken, "oauth", "", nil
		}
		if config.KimiCLIFallbackDisabled() {
			break
		}
		if tok := config.LoadKimiCodeCLIToken(); tok != nil && tok.AccessToken != "" {
			tok, _ = config.RefreshIfExpired("kimi", tok)
			return tok.AccessToken, "oauth", "", nil
		}
	case "deepseek":
		if c.DeepSeek.APIKey != "" {
			return c.DeepSeek.APIKey, "apikey", "", nil
		}
	case "google":
		// Google is API-Key only — no OAuth path. We still load
		// auth.json so /login api-Key flows work without exporting
		// an env var.
		if c.Google.APIKey != "" {
			return c.Google.APIKey, "apikey", "", nil
		}
	case "github-copilot":
		if c.GithubCopilot.APIKey != "" {
			return c.GithubCopilot.APIKey, "apikey", "", nil
		}
		if c.GithubCopilot.OAuth != nil && c.GithubCopilot.OAuth.AccessToken != "" {
			return c.GithubCopilot.OAuth.AccessToken, "oauth", "", nil
		}
	}
	return "", "", "", fmt.Errorf("no credential for %s", provider)
}
