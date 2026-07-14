package workspace

import (
	"context"
	"sort"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/identity"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/provider/auth"
)

// The Providers pane, read-only.
//
// terva has always been able to establish a model-provider credential in exactly
// one place — the TUI's /login dialog — and every other surface has been blind to
// the result. The model picker silently omits every provider you never logged
// into, with no way to say why; a subscription whose refresh token died surfaces
// as a turn that fails, rather than as "anthropic needs a re-login". This is the
// read half: it explains what the panel can see, and changes nothing.
//
// The verbs that CHANGE a credential are a separate, higher-authority group and
// do not exist yet (docs/proposals/web-provider-login.md). Until they do,
// CanLogin is false and the pane says so, rather than offering a control that
// would do nothing.

// AuthProviders reports the workspace's model-provider credential state.
//
// Never a secret: auth.Describe returns state (which provider, which method,
// expired or not) and this adds only labels and setup guidance. Everything here
// is safe on a screen someone else is looking at — which, for a web panel, is the
// operating assumption rather than a hypothetical.
func (w *Workspace) AuthProviders(_ context.Context) (ctrlproto.ProvidersView, error) {
	// A failed load is not an error to the caller: auth.Describe on the zero
	// value is a complete, correct "nobody is logged in", which is exactly what a
	// panel should show when the credential file cannot be read. Erroring here
	// would blank the pane instead of explaining it.
	creds, _ := config.AuthStoreFor().Load()

	states := auth.Describe(creds)
	out := make([]ctrlproto.ProviderInfo, 0, len(states))
	for id, st := range states {
		info := ctrlproto.ProviderInfo{
			ID:      id,
			Label:   provider.ProviderLabel(id),
			Method:  st.Method,
			Expired: st.Expired,
			BaseURL: st.BaseURL,
			Model:   st.Model,
			Offers:  offersFor(id),
		}
		if !st.Expiry.IsZero() {
			info.Expiry = st.Expiry.Format(time.RFC3339)
		}
		// The env-var providers are not a form: there is no key for terva to
		// take. Carry the guidance so the pane can say "set these variables"
		// instead of showing an empty row that looks broken.
		if env, ok := auth.EnvProviderInfo(id); ok {
			info.Note = append(append([]string(nil), env.Lines...),
				"", "Docs:", "  "+identity.RawDocURL(env.DocPath))
		}
		out = append(out, info)
	}
	// The operator's own openai-compatible servers (config.json "endpoints"). Each
	// is already its own provider with its own /v1/models discovery — but nothing
	// here could SEE one: this list is auth.Describe's, which enumerates the static
	// catalog and overlays stored credentials, and a named endpoint is in neither
	// (not built in, and a local server usually needs no key). So terva shipped the
	// feature and then hid it. These rows are what make it real.
	for id, ep := range configEndpoints() {
		info := ctrlproto.ProviderInfo{
			ID:       id,
			Label:    id, // the operator named it; do not second-guess them
			Endpoint: true,
			BaseURL:  ep.BaseURL,
		}
		// Ask the credentials directly, not auth.Describe: Describe enumerates the
		// static catalog, and this id is by definition not in it, so its state would
		// always come back empty. A key is optional here anyway (most local servers
		// ignore one), so this reports only whether there IS one.
		info.Method = creds.Method(id)
		out = append(out, info)
	}

	// A map has no order, and a pane that reshuffles itself between fetches is a
	// bug. Logged-in providers first — that is what the reader came for — then by
	// label.
	sort.Slice(out, func(a, b int) bool {
		ia, ib := out[a], out[b]
		if (ia.Method != "") != (ib.Method != "") {
			return ia.Method != ""
		}
		return ia.Label < ib.Label
	})

	return ctrlproto.ProvidersView{
		Providers: out,
		// Whether this daemon will actually serve a login. False unless the
		// composition root called EnableAuth, so a client that sees false renders
		// no controls rather than controls that answer CodeUnsupported.
		CanLogin: w.canLogin(),
	}, nil
}

// configEndpoints returns the operator's named openai-compatible endpoints,
// skipping any with no base URL — a half-written entry is not a backend, and a
// row for it would only offer to remove something that never worked.
//
// A failed config load yields none rather than an error, for the same reason the
// caller tolerates an unreadable credential file: a pane that explains the
// providers it CAN see beats a pane that blanks itself.
func configEndpoints() map[string]config.EndpointConfig {
	c, err := config.LoadConfig()
	if err != nil {
		return nil
	}
	out := make(map[string]config.EndpointConfig, len(c.Endpoints))
	for id, ep := range c.Endpoints {
		if strings.TrimSpace(ep.BaseURL) == "" {
			continue
		}
		out[id] = ep
	}
	return out
}

// ValidEndpointName reports whether id may name a new endpoint, and says why not
// when it may not.
//
// The name becomes a PROVIDER ID — it is what /model groups by, what auth.json
// keys a credential under, and what config.json records. So it may not collide
// with a provider terva ships: an endpoint called "anthropic" would shadow the
// real one in every one of those places, and the operator would be debugging a
// machine they own against a name they did not choose. Nor may it be
// "openai-compatible", which is the single shared slot this exists to escape.
func ValidEndpointName(id string) error {
	id = strings.TrimSpace(id)
	switch {
	case id == "":
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "name this endpoint, or leave the name empty to use the shared openai-compatible slot")
	case id == compatSharedSlot:
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "%q is the shared slot's own name; choose another", compatSharedSlot)
	}
	// A provider id travels through config keys, a credential file, and a model
	// picker. Keep it to something that cannot be mistaken for punctuation in any
	// of them.
	for _, r := range id {
		if !(r == '-' || r == '_' || r == '.' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest,
				"an endpoint name may use letters, digits, dash, underscore and dot only")
		}
	}
	for _, p := range append(auth.APIKeyProviders(), auth.OAuthProviders()...) {
		if p == id {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest,
				"%q is a provider terva already ships; choose another name", id)
		}
	}
	return nil
}

// compatSharedSlot is the single openai-compatible entry in auth.json — the one
// a second login overwrites, and the reason named endpoints exist.
const compatSharedSlot = "openai-compatible"

// offersFor reports how a provider can be logged into.
//
// It describes the PROVIDER, not the client: whether a given flow can actually
// complete from where the user is sitting is a separate question (a browser on a
// phone can never reach the daemon's loopback), and it belongs to the flow
// verbs, not to a read-only list.
func offersFor(id string) []string {
	if _, ok := auth.EnvProviderInfo(id); ok {
		return []string{"env"}
	}
	var offers []string
	for _, p := range auth.APIKeyProviders() {
		if p == id {
			offers = append(offers, "apikey")
			break
		}
	}
	for _, p := range auth.OAuthProviders() {
		if p == id {
			offers = append(offers, "oauth")
			break
		}
	}
	return offers
}
