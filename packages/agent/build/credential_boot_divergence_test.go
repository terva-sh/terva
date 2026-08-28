package build

import (
	"errors"
	"testing"

	"terva.sh/terva/packages/agent/config"
)

// The boot resolve and the per-session resolve do not see the same Args, and
// that difference is a whole class of "terva will not start".
//
// Boot resolves with NO explicit provider, so the auto-fallback (see Resolve)
// is free to walk past a lapsed login and land on whichever provider is
// actually usable — CredentialErr comes back nil and the host concludes it does
// not need a login. A session resolves with args.Provider PINNED, because
// buildSession replays the session's stored meta onto the args, and an explicit
// provider is exactly what disables that fallback. So the same auth.json
// resolves clean at boot and hard-fails one layer later.
//
// This is not hypothetical: config.json's provider/model IS the seed for a
// fresh session (effectiveDefaultModel), so a user whose configured provider's
// subscription lapsed hits it on a plain `terva` with no flags and no resume.
//
// The two resolves are allowed to disagree — pinning a provider SHOULD defeat a
// fallback, or a session could silently change model mid-transcript. What is
// not allowed is for the host to read only the boot verdict and treat the
// session failure as fatal. This test pins the divergence so the hosts' handling
// of it stays deliberate.
func TestBootFallbackHidesAPinnedProvidersLapsedLogin(t *testing.T) {
	isolate(t)
	if err := config.SetKimiCLIFallbackDisabled(true); err != nil {
		t.Fatal(err)
	}
	// The lapsed subscription the user configured...
	if err := config.AuthStoreFor().SetOAuth("anthropic", deadToken()); err != nil {
		t.Fatal(err)
	}
	// ...and a second provider that still works, which is what makes the boot
	// resolve look healthy.
	if err := config.AuthStoreFor().SetAPIKey("google", "live-google-key"); err != nil {
		t.Fatal(err)
	}

	// Boot: no explicit provider. The fallback rescues it.
	boot, err := Resolve(Args{}, false)
	if err != nil {
		t.Fatalf("boot Resolve: %v", err)
	}
	if boot.CredentialErr != nil {
		t.Fatalf("boot CredentialErr = %v, want nil (the fallback should have found google)", boot.CredentialErr)
	}
	if boot.Provider == "anthropic" {
		t.Fatalf("boot stayed on anthropic; the fallback did not run, so this test no longer reproduces the divergence")
	}

	// The session: provider pinned, exactly as buildSession replays it from
	// session meta. requireCred=true is what buildSession asks for today.
	_, serr := Resolve(Args{Provider: "anthropic"}, true)
	if serr == nil {
		t.Fatal("session Resolve succeeded on a dead anthropic subscription")
	}
	var credErr *CredentialError
	if !errors.As(serr, &credErr) {
		t.Fatalf("session error is not a *CredentialError, so a host cannot tell it apart from a genuine internal failure: %v", serr)
	}
	var expired *ExpiredLoginError
	if !errors.As(serr, &expired) {
		t.Fatalf("session error is not an *ExpiredLoginError: %v", serr)
	}
}
