package auth

// What a compatible-provider login is allowed to say.
//
// The two compatible providers are the only logins that capture a whole
// ENDPOINT rather than a credential, and the Anthropic one captures wire
// settings on top of that. Three surfaces collect them — the loopback browser
// form, the daemon's form descriptor (TUI and web panel), and the CLI flags —
// so the rules about what those values may be live here, once, below all three.
//
// 🪤 The context-window field is the cautionary tale this file is shaped
// against: it was parsed and range-checked in the TUI dialog AND again in the
// auth manager, and the two sites were free to disagree about what a valid
// window was. Every value below is validated exactly here.

import (
	"strings"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// Auth styles for an Anthropic-compatible endpoint: which header carries the key.
//
// Anthropic's own API takes x-api-key. A gateway fronting it very often wants
// the OpenAI convention on the way in, and answers anything else with a 401
// naming neither header — which is why this is a setting and not a guess.
const (
	AuthStyleAPIKey = "x-api-key"
	AuthStyleBearer = "bearer"
)

// AnthropicDefaultAPIVersion is the `anthropic-version` an endpoint gets when
// the operator names none. Re-exported from the provider package so a form can
// show the default it is departing from without importing the wire client.
const AnthropicDefaultAPIVersion = provider.AnthropicDefaultAPIVersion

// AuthStyles lists the accepted auth_style values, in the order a form should
// offer them. The first is the default.
func AuthStyles() []string { return []string{AuthStyleAPIKey, AuthStyleBearer} }

// AnthropicOptions renders the endpoint's wire settings for the provider
// package. The zero CompatEndpoint yields the zero options, which is exactly
// what api.anthropic.com itself wants.
func (e CompatEndpoint) AnthropicOptions() provider.AnthropicCompatOptions {
	return provider.AnthropicCompatOptions{
		APIVersion:     strings.TrimSpace(e.APIVersion),
		Beta:           strings.TrimSpace(e.Beta),
		BearerAuth:     strings.EqualFold(strings.TrimSpace(e.AuthStyle), AuthStyleBearer),
		DisableCaching: e.DisableCaching,
	}
}

// ValidateCompatEndpoint checks the operator's endpoint settings, naming what
// is wrong rather than silently ignoring it.
//
// Silently ignoring is the failure mode worth spelling out: an unrecognised
// auth_style would fall back to x-api-key through EqualFold above, so a typo
// ("Bearer-token") would produce a login that succeeds, a probe that passes
// against a keyless server, and a 401 on the first real turn — with the setting
// visibly present in auth.json and visibly not in effect.
func ValidateCompatEndpoint(providerID string, e CompatEndpoint) error {
	if providerID != anthropicCompatProvider {
		// The Anthropic knobs on an OpenAI endpoint are not an error worth
		// refusing a login over — no form offers them there, so the only way to
		// arrive here with one set is a hand-edited file. They are inert.
		return nil
	}
	if s := strings.TrimSpace(e.AuthStyle); s != "" {
		ok := false
		for _, v := range AuthStyles() {
			if strings.EqualFold(s, v) {
				ok = true
				break
			}
		}
		if !ok {
			return i18n.Errorf("auth style must be one of: %s", strings.Join(AuthStyles(), ", "))
		}
	}
	// anthropic-version and anthropic-beta go on the wire as header values, so
	// they may not carry anything that could split a header. A newline here
	// would be request smuggling against the operator's own gateway; Go's
	// http.Header.Set would reject it at send time with an error naming neither
	// the setting nor the login that stored it.
	for _, f := range []struct{ label, value string }{
		{i18n.T("anthropic-version"), e.APIVersion},
		{i18n.T("anthropic-beta"), e.Beta},
	} {
		if strings.ContainsAny(f.value, "\r\n") {
			return i18n.Errorf("%s may not contain a line break", f.label)
		}
	}
	return nil
}

// IsOffValue reads a form's on/off field.
//
// AuthField has no boolean or select type — everything crosses the wire as a
// string and the daemon parses, which is the contract that keeps three
// renderers from disagreeing. So "off" has to be recognised in the spellings a
// person actually types, and ANYTHING else means on: the field's default is on,
// and a value nobody understands must not quietly disable prompt caching.
func IsOffValue(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "false", "no", "0", "disabled":
		return true
	}
	return false
}

// OnOffLabel renders a bool back into the spelling a form shows.
func OnOffLabel(on bool) string {
	if on {
		return "on"
	}
	return "off"
}
