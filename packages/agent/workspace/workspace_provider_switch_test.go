package workspace

import (
	"context"
	"errors"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider/auth"
	"terva.sh/terva/packages/testsupport"
)

// lapsedPinWithWorkingAlternative stages the shape the whole feature exists
// for: config pins a provider whose subscription lapsed, and a different one
// still works — so boot has both a problem and a way around it.
func lapsedPinWithWorkingAlternative(t *testing.T) {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
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
	if err := config.AuthStoreFor().SetOAuth("anthropic", auth.OAuthToken{
		AccessToken: "expired-access-token",
		TokenType:   "Bearer",
		Expiry:      time.Now().Add(-24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := config.AuthStoreFor().SetAPIKey("openai", "live-openai-key"); err != nil {
		t.Fatal(err)
	}
	if err := config.MutateConfig(func(c *config.Config) {
		c.Provider, c.Model = "anthropic", "claude-opus-5"
	}); err != nil {
		t.Fatal(err)
	}
}

// "continue on X for this session" must stay CONFINED to that session.
//
// This is the promise the whole design rests on: the user is agreeing to spend
// on another account once, to get unblocked. If it silently became their
// default, every later launch would keep billing it — which is precisely the
// silent switch this feature was built to refuse, just deferred by one run.
func TestUsingTheOfferedProviderDoesNotChangeTheDefault(t *testing.T) {
	lapsedPinWithWorkingAlternative(t)

	w, err := NewWorkspace(build.Args{CWD: testsupport.TempDir(t), NoExt: true, NoMCP: true}, "test")
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	defer w.Close()

	sw := w.ProviderSwitch()
	if sw == nil {
		t.Fatal("ProviderSwitch is nil; boot switched providers and told nobody")
	}

	// The host's CarrierUseProvider does exactly this.
	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{
		Provider: sw.To, Model: sw.ToModel,
	})
	if err != nil {
		t.Fatalf("creating a session on the offered pair failed: %v", err)
	}
	if info.Provider != sw.To {
		t.Errorf("session provider = %q, want the offered %q", info.Provider, sw.To)
	}

	// The pin is untouched on disk.
	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "anthropic" || cfg.Model != "claude-opus-5" {
		t.Errorf("config.json moved to %q/%q; taking the offer must not rewrite the default",
			cfg.Provider, cfg.Model)
	}
}

// The offered pair must be one that actually RUNS. Offering a provider whose
// credential is as dead as the pinned one would replace a clear refusal with a
// second failure one keystroke later.
func TestTheOfferedPairCanActuallyOpenASession(t *testing.T) {
	lapsedPinWithWorkingAlternative(t)

	w, err := NewWorkspace(build.Args{CWD: testsupport.TempDir(t), NoExt: true, NoMCP: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	sw := w.ProviderSwitch()
	if sw == nil {
		t.Fatal("no switch recorded")
	}
	if sw.To == "" || sw.ToModel == "" {
		t.Fatalf("offer is incomplete: %+v — a half-named pair cannot be created", sw)
	}
	if sw.To == sw.From {
		t.Fatalf("offered the same provider that just failed: %+v", sw)
	}
	if _, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{
		Provider: sw.To, Model: sw.ToModel,
	}); err != nil {
		t.Fatalf("the offered pair does not open a session: %v", err)
	}
}

// A fresh session with NO explicit pair still refuses, and says why.
//
// The seed returning the config pin is not an oversight — it is what makes the
// refusal happen at all. Were it to hand back the fallback, the session would
// open on another account with nothing asked and nothing said, which is the
// behaviour this whole change exists to remove. Guarded so a later "fix" to the
// divergence cannot quietly reintroduce it.
func TestADefaultSessionStillRefusesRatherThanSwitchingItself(t *testing.T) {
	lapsedPinWithWorkingAlternative(t)

	w, err := NewWorkspace(build.Args{CWD: testsupport.TempDir(t), NoExt: true, NoMCP: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{})
	if err == nil {
		t.Fatalf("a default session opened on %q/%q without asking; the pinned provider is dead",
			info.Provider, info.Model)
	}
	var wire *ctrlproto.Error
	if !errors.As(err, &wire) || wire.Code != ctrlproto.CodeNoCredential {
		t.Fatalf("err = %v, want %q so the host knows to offer the switch", err, ctrlproto.CodeNoCredential)
	}
}
