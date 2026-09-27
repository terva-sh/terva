package build

import (
	"sort"
	"strings"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/auth"
	"terva.sh/terva/packages/provider"
)

// LoggedInProviders returns every provider a model picker may offer: those with
// a resolvable credential, plus the ones that need no credential at all —
// ollama, the shared compatible slots once they have a base URL, and each
// named endpoint from config.
//
// "Logged in" cannot be spelled ResolveCredential alone, and that is the point.
// Most local OpenAI-compatible servers want no key, so a keyless endpoint has no
// credential to resolve; asking only the credential store would report it as a
// provider the user is signed out of, when in fact it is the one backend they
// are certain to be able to reach.
//
// It lives here, in the package that owns the provider registry and ResolveCredential,
// because it used to live in three places at once — the TUI's picker, the web
// daemon's, and ACP's. Each copy's doc comment claimed to mirror the next. When
// named endpoints arrived, only the TUI's copy learned about them, so the web
// listed an endpoint in its provider pane and then refused to show a single one
// of its models. One predicate, three callers.
func LoggedInProviders() []string {
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if seen[p] {
			return
		}
		out = append(out, p)
		seen[p] = true
	}

	for _, p := range ProviderIDs() {
		if _, _, err := ResolveCredential(p, ""); err == nil {
			add(p)
		}
	}
	// Ollama needs no auth at all.
	add("ollama")
	// Each shared compatible slot is reachable as soon as it has a base URL.
	// Their keys are optional, so the ResolveCredential sweep above will not
	// have surfaced them.
	store := config.AuthStoreFor()
	for _, id := range []string{provider.OpenAICompatProvider, provider.AnthropicCompatProvider} {
		if store.CompatEndpointFor(id).Configured() {
			add(id)
		}
	}
	// Each named endpoint, for exactly the same reason. Sorted, because a picker
	// that reshuffles its providers between reads is a bug, and a map is not an
	// order.
	if cfg, err := config.LoadConfig(); err == nil {
		ids := make([]string, 0, len(cfg.Endpoints))
		for id, ep := range cfg.Endpoints {
			// A half-written entry is not a backend: no base URL, nothing to reach.
			if strings.TrimSpace(ep.BaseURL) != "" {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		for _, id := range ids {
			add(id)
		}
	}
	return out
}

// ProviderConfigured reports whether terva holds anything that reaches
// provider p: a credential that EXISTS, or a backend that needs none. It is
// the presence twin of [LoggedInProviders], and it differs in one way.
//
// An expired login counts. hasCredential never refreshes a token, so this
// makes no network call and writes nothing to auth.json. The caller that
// needs a usable credential resolves one later, and that resolve reports the
// lapse with its own error.
func ProviderConfigured(p string) bool {
	switch {
	case p == "ollama":
		return true
	case isCompatProvider(p):
		return config.AuthStoreFor().CompatEndpointFor(p).Configured()
	}
	if cfg, err := config.LoadConfig(); err == nil {
		if ep, ok := cfg.Endpoints[p]; ok {
			return strings.TrimSpace(ep.BaseURL) != ""
		}
	}
	creds, _ := config.AuthStoreFor().Load()
	return hasCredential(p, creds, auth.Describe(creds))
}

// LoggedInProviderSet is [LoggedInProviders] keyed for membership tests.
func LoggedInProviderSet() map[string]bool {
	list := LoggedInProviders()
	out := make(map[string]bool, len(list))
	for _, p := range list {
		out[p] = true
	}
	return out
}

// LoggedInProviderAuth reports HOW each credentialed provider authenticates:
// "oauth" for a subscription token (a Claude/ChatGPT/Kimi plan) or "apikey" for
// a metered key. A picker can then say which of two rows offering the same model
// id spends a subscription and which bills per token — the one thing the model
// id alone can never tell you.
//
// Keyless backends carry NO entry, deliberately. ollama, the shared
// compatible slots and named endpoints are reachable without a credential
// (see [LoggedInProviders]), so there is no method to report and guessing one
// would put a confident, wrong badge on a row. An absent entry means "unknown",
// and the caller should render nothing rather than invent a label.
func LoggedInProviderAuth() map[string]string {
	out := map[string]string{}
	for _, p := range ProviderIDs() {
		if _, method, err := ResolveCredential(p, ""); err == nil && method != "" {
			out[p] = method
		}
	}
	return out
}
