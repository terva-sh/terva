package build

import (
	"testing"

	"terva.sh/terva/packages/agent/config"
)

// pinConfig writes the provider/model pair config.json would hold after a
// /login or a /model — the shape that makes provName a CHOICE rather than the
// built-in default.
func pinConfig(t *testing.T, prov, model string) {
	t.Helper()
	if err := config.MutateConfig(func(c *config.Config) {
		c.Provider, c.Model = prov, model
	}); err != nil {
		t.Fatal(err)
	}
}

// Boot falls back off a lapsed pin, and must RECORD that it did.
//
// The fallback itself is not the bug — a host with no login flow has nothing
// better to do than use the provider that works. Doing it SILENTLY is: the
// chrome read "(openai) gpt-5" for someone who chose claude-opus-5, and a turn
// would have billed an account they never selected for this session.
func TestResolveRecordsASwitchOffALapsedPin(t *testing.T) {
	isolate(t)
	if err := config.SetKimiCLIFallbackDisabled(true); err != nil {
		t.Fatal(err)
	}
	pinConfig(t, "anthropic", "claude-opus-5")
	if err := config.AuthStoreFor().SetOAuth("anthropic", deadToken()); err != nil {
		t.Fatal(err)
	}
	if err := config.AuthStoreFor().SetAPIKey("openai", "live-openai-key"); err != nil {
		t.Fatal(err)
	}

	r, err := Resolve(Args{}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// The fallback still APPLIES — that part is unchanged.
	if r.Provider == "anthropic" {
		t.Fatal("stayed on the dead pin; the fallback did not run")
	}
	if r.CredentialErr != nil {
		t.Fatalf("CredentialErr = %v, want nil once a usable provider was found", r.CredentialErr)
	}

	sw := r.ProviderSwitch
	if sw == nil {
		t.Fatal("ProviderSwitch is nil; the switch happened and nothing recorded it")
	}
	if sw.From != "anthropic" || sw.FromModel != "claude-opus-5" {
		t.Errorf("From = %q/%q, want anthropic/claude-opus-5 — the pair the user chose", sw.From, sw.FromModel)
	}
	if sw.To != r.Provider || sw.ToModel != r.Model {
		t.Errorf("To = %q/%q but Resolve returned %q/%q; the notice must name what actually got used",
			sw.To, sw.ToModel, r.Provider, r.Model)
	}
	if sw.Err == nil {
		t.Error("Err is nil; nothing says WHY the pin was unusable")
	}
	// The remedy differs for a lapse ("log in again") and a provider that was
	// never set up, so the host has to be able to tell them apart.
	if !sw.Lapsed() {
		t.Errorf("Lapsed() = false for an expired subscription: %v", sw.Err)
	}
}

// An UNPINNED config must not produce a switch notice. provName is "anthropic"
// only because it is the built-in default — nobody chose it, so there is no
// choice to hand back and no decision to interrupt a first run over.
func TestResolveDoesNotReportASwitchWhenNothingWasPinned(t *testing.T) {
	isolate(t)
	if err := config.SetKimiCLIFallbackDisabled(true); err != nil {
		t.Fatal(err)
	}
	// No pinConfig: config.json names no provider.
	if err := config.AuthStoreFor().SetAPIKey("openai", "live-openai-key"); err != nil {
		t.Fatal(err)
	}

	r, err := Resolve(Args{}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Provider == "anthropic" {
		t.Fatal("did not fall back; this no longer exercises the path")
	}
	if r.ProviderSwitch != nil {
		t.Errorf("ProviderSwitch = %+v on an unpinned config; a first run would be interrupted to confirm a choice nobody made",
			r.ProviderSwitch)
	}
}

// An EXPLICIT --provider is not a switch either: the fallback never runs, so
// the user gets the credential error for the provider they named.
func TestResolveDoesNotSwitchOffAnExplicitFlag(t *testing.T) {
	isolate(t)
	if err := config.SetKimiCLIFallbackDisabled(true); err != nil {
		t.Fatal(err)
	}
	pinConfig(t, "anthropic", "claude-opus-5")
	if err := config.AuthStoreFor().SetOAuth("anthropic", deadToken()); err != nil {
		t.Fatal(err)
	}
	if err := config.AuthStoreFor().SetAPIKey("openai", "live-openai-key"); err != nil {
		t.Fatal(err)
	}

	r, err := Resolve(Args{Provider: "anthropic"}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Provider != "anthropic" {
		t.Fatalf("provider = %q; an explicit --provider was overridden", r.Provider)
	}
	if r.CredentialErr == nil {
		t.Error("CredentialErr is nil on a dead explicit provider")
	}
	if r.ProviderSwitch != nil {
		t.Errorf("ProviderSwitch = %+v; nothing was switched", r.ProviderSwitch)
	}
}

// A pin that was never LOGGED IN is not a lapse. Both are worth reporting, but
// only a lapse makes "sign in again" a real remedy — Lapsed() is what the host
// keys on, so a wrong answer sends the user after the wrong fix.
func TestSwitchOffANeverConfiguredPinIsNotALapse(t *testing.T) {
	isolate(t)
	if err := config.SetKimiCLIFallbackDisabled(true); err != nil {
		t.Fatal(err)
	}
	pinConfig(t, "deepseek", "deepseek-chat")
	if err := config.AuthStoreFor().SetAPIKey("openai", "live-openai-key"); err != nil {
		t.Fatal(err)
	}

	r, err := Resolve(Args{}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	sw := r.ProviderSwitch
	if sw == nil {
		t.Fatal("ProviderSwitch is nil; a pinned provider with no credential was switched away from silently")
	}
	if sw.From != "deepseek" {
		t.Errorf("From = %q, want deepseek", sw.From)
	}
	if sw.Lapsed() {
		t.Error("Lapsed() = true for a provider that was never logged in; the user would be told to re-authenticate an account they never had")
	}
}
