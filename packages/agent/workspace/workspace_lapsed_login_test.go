package workspace

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider/auth"
	"terva.sh/terva/packages/testsupport"
)

// lapsedConfiguredProvider stages the exact shape that made terva unstartable:
// config.json names a provider whose subscription has lapsed, and a DIFFERENT
// provider still has a working credential.
//
// The second credential is what makes this subtle. Without it the boot resolve
// reports CredentialErr, the host defers to /login, and everything works. With
// it, boot's auto-fallback finds the live provider and reports no error at all —
// so the host believes it is logged in, and the failure surfaces one layer
// later, on a session, where config.json's provider has been pinned onto the
// args and the fallback can no longer run.
func lapsedConfiguredProvider(t *testing.T) {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	// A real HOME, redirected: the Kimi CLI fallback reads one, and the ambient
	// developer's would decide the test.
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, k := range []string{
		"ANTHROPIC_API_KEY", "ANTHROPIC_OAUTH_TOKEN", "OPENAI_API_KEY",
		"KIMI_API_KEY", "MOONSHOT_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY",
		"DEEPSEEK_API_KEY", "AWS_ACCESS_KEY_ID", "AWS_PROFILE", "AWS_BEARER_TOKEN_BEDROCK",
	} {
		t.Setenv(k, "")
	}
	if err := config.SetKimiCLIFallbackDisabled(true); err != nil {
		t.Fatal(err)
	}
	// The lapsed subscription. No refresh_token, so RefreshIfExpired refuses
	// without a network call — that is what keeps this hermetic.
	if err := config.AuthStoreFor().SetOAuth("anthropic", auth.OAuthToken{
		AccessToken: "expired-access-token",
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(-24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	// The provider that still works, and hides the problem at boot.
	if err := config.AuthStoreFor().SetAPIKey("openai", "live-openai-key"); err != nil {
		t.Fatal(err)
	}
	if err := config.MutateConfig(func(c *config.Config) {
		c.Provider, c.Model = "anthropic", "claude-opus-5"
	}); err != nil {
		t.Fatal(err)
	}
}

// A session that cannot resolve a credential must say so with
// CodeNoCredential, not CodeInternal.
//
// This is the whole recovery path. The TUI defers a credential failure and
// opens /login; it can only do that if it can TELL a credential failure from a
// daemon that broke. Reported as CodeInternal, the interactive entry returned
// the error straight out of main — so terva printed "anthropic login expired
// and could not be refreshed … sign in again with /login" and exited to the
// shell, naming as the remedy the one thing the user could no longer reach.
//
// 🪤 The classification has to survive the wire, so it is a CODE and not a
// wrapped error: ctrlproto.Error is {code, message} with nothing to errors.As
// through.
func TestSessionOnALapsedLoginIsNotAnInternalError(t *testing.T) {
	lapsedConfiguredProvider(t)

	// Boot with NO provider pinned, exactly as a plain `terva` does.
	w, err := NewWorkspace(build.Args{CWD: testsupport.TempDir(t), NoExt: true, NoMCP: true}, "test")
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	defer w.Close()

	// Guard the guard: if boot reported the credential error, the host would
	// already defer to /login and this test would prove nothing about the gap.
	if err := w.CredentialErr(); err != nil {
		t.Fatalf("boot reported CredentialErr = %v; the fallback did not run, so this no longer reproduces", err)
	}

	_, cerr := w.CreateSession(context.Background(), ctrlproto.CreateOpts{})
	if cerr == nil {
		t.Fatal("CreateSession succeeded on a lapsed anthropic subscription")
	}
	var wire *ctrlproto.Error
	if !errors.As(cerr, &wire) {
		t.Fatalf("error is not a *ctrlproto.Error: %v", cerr)
	}
	if wire.Code != ctrlproto.CodeNoCredential {
		t.Errorf("code = %q, want %q — a host cannot tell this from a broken daemon",
			wire.Code, ctrlproto.CodeNoCredential)
	}
	// The message still has to name the remedy; the code is for the machine.
	if !strings.Contains(wire.Message, "/login") {
		t.Errorf("message does not name the remedy: %q", wire.Message)
	}
	// And it must not be flattened into "no credential for anthropic" — the
	// credential is on disk, it is the GRANT that lapsed.
	if !strings.Contains(wire.Message, "expired") {
		t.Errorf("message does not describe a lapsed login: %q", wire.Message)
	}
}

// A genuinely broken resolve must stay CodeInternal. Without this, the fix
// above could have been "call everything no_credential", which would send every
// real daemon fault to the login dialog.
func TestNonCredentialResolveFailureStaysInternal(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t), NoExt: true, NoMCP: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// A persona that does not exist fails Resolve for a reason that has nothing
	// to do with credentials.
	_, cerr := w.CreateSession(context.Background(), ctrlproto.CreateOpts{Persona: "no-such-persona-anywhere"})
	if cerr == nil {
		t.Fatal("CreateSession succeeded with an unknown persona")
	}
	var wire *ctrlproto.Error
	if !errors.As(cerr, &wire) {
		t.Fatalf("error is not a *ctrlproto.Error: %v", cerr)
	}
	if wire.Code == ctrlproto.CodeNoCredential {
		t.Errorf("an unknown persona was reported as %q; every real fault would route to /login", wire.Code)
	}
}
