package build

import (
	"fmt"
	"os"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider/auth"
)

// Credential resolution reads the provider registry (providerByID) for each
// provider's api-Key env vars, so it sits above the config layer with the
// registry rather than inside it.

// ExpiredLoginError marks a stored subscription whose access token has expired
// and whose refresh was REFUSED: the account is configured, but the grant is
// gone. It is deliberately not the same as "no credential" — the remedy differs
// ("sign in again" vs "configure a provider"), and the two used to be
// indistinguishable because the refresh error was dropped on the floor and the
// dead access token handed back anyway. What the user then saw was a 401 named
// after the WIRE FORMAT ("anthropic: http 401") on a provider they had never
// logged out of.
type ExpiredLoginError struct {
	Provider string
	Err      error
}

func (e *ExpiredLoginError) Error() string {
	return fmt.Sprintf("%s login expired and could not be refreshed (%v); sign in again with /login", e.Provider, e.Err)
}

func (e *ExpiredLoginError) Unwrap() error { return e.Err }

// oauthCredential resolves a stored OAuth token into a usable access token,
// refreshing (and persisting) one that has expired.
//
// A refresh FAILURE returns the error instead of the token. An expired access
// token authenticates nothing, so handing it back buys exactly one thing: a 401
// from the provider, several layers away from the login that lapsed and wearing
// the wire client's name rather than its own.
//
// provider is terva's provider id (what the user picked, what the message
// names); oauthName is the OAuth provider the token was issued by, which is not
// always the same — openai-codex tokens are minted by "openai".
func oauthCredential(provider, oauthName string, tok *auth.OAuthToken) (*auth.OAuthToken, error) {
	next, err := config.RefreshIfExpired(oauthName, tok)
	if err != nil {
		return nil, &ExpiredLoginError{Provider: provider, Err: err}
	}
	return next, nil
}

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
			tok, terr := oauthCredential("anthropic", "anthropic", c.Anthropic.OAuth)
			if terr != nil {
				return "", "", "", terr
			}
			return tok.AccessToken, "oauth", "", nil
		}
	case "openai":
		if c.OpenAI.APIKey != "" {
			return c.OpenAI.APIKey, "apikey", "", nil
		}
	case "openai-codex":
		if c.OpenAI.OAuth != nil && c.OpenAI.OAuth.AccessToken != "" {
			tok, terr := oauthCredential("openai-codex", "openai", c.OpenAI.OAuth)
			if terr != nil {
				return "", "", "", terr
			}
			return tok.AccessToken, "oauth", tok.AccountID, nil
		}
	case "kimi":
		if c.Kimi.APIKey != "" {
			return c.Kimi.APIKey, "apikey", "", nil
		}
		// A dead stored subscription must not SHADOW the CLI's token. Borrowing
		// the official Kimi Code CLI's login is the whole reason terva can run
		// kimi without one of its own, and returning terva's expired token
		// unconditionally stepped in front of it — so a lapsed terva login broke
		// kimi even on a machine where the CLI was signed in and working. Keep
		// the first refusal to report if the fallback has nothing either: it
		// names the login the user actually made.
		var expired error
		if c.Kimi.OAuth != nil && c.Kimi.OAuth.AccessToken != "" {
			tok, terr := oauthCredential("kimi", "kimi", c.Kimi.OAuth)
			if terr == nil {
				return tok.AccessToken, "oauth", "", nil
			}
			expired = terr
		}
		if !config.KimiCLIFallbackDisabled() {
			if cli := config.LoadKimiCodeCLIToken(); cli != nil && cli.AccessToken != "" {
				tok, terr := oauthCredential("kimi", "kimi", cli)
				if terr == nil {
					return tok.AccessToken, "oauth", "", nil
				}
				if expired == nil {
					expired = terr
				}
			}
		}
		if expired != nil {
			return "", "", "", expired
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
