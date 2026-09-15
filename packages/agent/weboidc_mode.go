//go:build terva_web

package agent

import (
	"context"
	"fmt"
	"os"
	"strings"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/oidc"
	"terva.sh/terva/packages/agent/web"
)

// resolveWebOIDC turns config.json's web_oidc block into a discovered provider.
//
// Returns (nil, nil, nil) when no block is present — single sign-on is opt-in,
// and a daemon without it keeps exactly the auth modes it had.
//
// 🚨 config.LoadConfig reads $TERVA_HOME — the USER layer, never LoadProjectConfig. A project's .terva/config.json naming an issuer
// would let repo-controlled bytes decide who terva trusts to log in, which is
// the "project-scope launders trust" category error arriving through the front
// door rather than through a redirected home.
func resolveWebOIDC(ctx context.Context) (*oidc.Provider, *web.OIDCState, error) {
	cfg, err := config.LoadConfig()
	if err != nil {
		// A daemon that cannot read its own config should not silently come up
		// with single sign-on disabled — that is the shape of an outage that
		// looks like a permissions bug for an hour.
		return nil, nil, fmt.Errorf("terva web: reading config for single sign-on: %w", err)
	}
	oc := cfg.WebOIDC
	if oc == nil {
		return nil, nil, nil
	}
	if strings.TrimSpace(oc.RedirectURL) == "" {
		return nil, nil, fmt.Errorf("terva web: web_oidc needs a redirect_url — the absolute callback URL registered with your provider, e.g. https://terva.example.com%s", web.OIDCCallbackPath)
	}
	// Opened at the point of USE, not when the config loads — the same rule the
	// image backend and extension delivery paths follow. A sealed secret that
	// could not be opened must fail the daemon with a named reason rather than
	// travel onward as ciphertext and come back as an opaque "invalid_client"
	// from the provider an hour later.
	clientSecret, err := config.DecryptFieldValue(config.WebOIDCClientSecretPath, oc.ClientSecret)
	if err != nil {
		return nil, nil, fmt.Errorf("terva web: single sign-on client secret: %w", err)
	}
	prov, err := oidc.Discover(ctx, oidc.Config{
		Issuer:              oc.Issuer,
		ClientID:            oc.ClientID,
		ClientSecret:        clientSecret,
		Scopes:              oc.Scopes,
		GroupsClaim:         oc.GroupsClaim,
		RoleMap:             oc.RoleMap,
		AllowInsecureIssuer: oc.AllowInsecureIssuer,
	}, oc.RedirectURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("terva web: single sign-on: %w", err)
	}
	if len(oc.RoleMap) == 0 {
		// Not fatal: an operator may be staging the config. But every login will
		// authenticate and then be refused, and that is worth saying once at
		// startup rather than leaving it to be discovered one user at a time.
		fmt.Fprintln(os.Stderr, "terva web: single sign-on has an empty role_map — every sign-in will succeed and then be refused access")
	}
	fmt.Fprintf(os.Stderr, "terva web: single sign-on enabled (issuer %s, %d group mapping(s))\n", oc.Issuer, len(oc.RoleMap))
	return prov, web.NewOIDCState(), nil
}
