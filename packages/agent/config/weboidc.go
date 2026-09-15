package config

// WebOIDCConfig is the single sign-on block of config.json.
//
// 🔑 It is a plain struct here rather than an alias for oidc.Config so the
// `config` package — which every build imports — does not pull the OIDC
// libraries into the min binary. `terva web` is behind a build tag; this makes
// sure its dependencies are too. web_mode.go maps this onto oidc.Config inside
// that tag.
type WebOIDCConfig struct {
	Issuer       string `json:"issuer"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`

	// RedirectURL is the absolute callback URL registered with the provider,
	// e.g. https://terva.example.com/auth/oidc/callback.
	//
	// 🚨 Explicit, never derived from the request's Host. A derived value would
	// let a forged Host header steer the authorize request, and while a
	// correctly configured provider would reject the unregistered redirect_uri,
	// that makes an identity provider's allowlist terva's only defense against
	// its own input handling. Config it once and the question does not arise.
	RedirectURL string `json:"redirect_url"`

	Scopes      []string `json:"scopes,omitempty"`
	GroupsClaim string   `json:"groups_claim,omitempty"`

	// RoleMap maps an identity-provider group to a terva role: owner, operator,
	// member or viewer. A user whose groups map to nothing authenticates and is
	// granted NOTHING, which is the intended answer for someone the provider
	// knows and this daemon was never told to admit.
	RoleMap map[string]string `json:"role_map,omitempty"`

	// AllowInsecureIssuer permits a plaintext http:// issuer. Development only:
	// discovery over plaintext is unauthenticated, so whoever can rewrite it
	// points the key fetch at their own keys and signs any identity they like.
	AllowInsecureIssuer bool `json:"allow_insecure_issuer,omitempty"`
}

// WebOIDCClientSecretPath is the dotted path of the single sign-on client
// secret in config.json.
//
// A named constant rather than a literal because three places must agree on it
// — the at-rest scanner, the sealer, and the census that proves every string
// field was classified — and a typo in any one of them fails OPEN: the field
// simply never gets sealed, and nothing says so.
const WebOIDCClientSecretPath = "web_oidc.client_secret"
