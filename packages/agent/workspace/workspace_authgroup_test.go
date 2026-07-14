package workspace

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/provider/auth"
	"terva.sh/terva/packages/testsupport"
)

// authWorkspace is a workspace with the auth group live, wired to a scratch
// credential store so nothing here can touch the developer's real auth.json.
func authWorkspace(t *testing.T) (*Workspace, *auth.Manager) {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	w := &Workspace{ctx: ctx, diag: func(string) {}, sessions: map[string]*wsSession{}}
	m := auth.NewManager(auth.NewStore(filepath.Join(home, "auth.json")))
	t.Cleanup(m.Close)
	w.EnableAuth(m, AuthOptions{}) // no browser, no host hooks
	return w, m
}

// Without EnableAuth, every mutating verb refuses — and says so as "unsupported"
// rather than failing obscurely. A client that sees CanLogin false renders no
// controls, but a client that ignores that must still be told no.
func TestAWorkspaceWithoutTheAuthGroupRefusesEveryLoginVerb(t *testing.T) {
	w := &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}
	ctx := context.Background()

	if _, err := w.AuthLoginStart(ctx, ctrlproto.AuthLoginStartParams{Provider: "anthropic", Method: "oauth"}); err == nil {
		t.Error("login started on a daemon that does not serve logins")
	}
	if err := w.AuthLoginSubmit(ctx, ctrlproto.AuthLoginSubmitParams{Flow: "f1"}); err == nil {
		t.Error("submit accepted on a daemon that does not serve logins")
	}
	if err := w.AuthLogout(ctx, ctrlproto.AuthLogoutParams{Provider: "anthropic"}); err == nil {
		t.Error("logout accepted on a daemon that does not serve logins")
	}
	v, err := w.AuthProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v.CanLogin {
		t.Error("CanLogin is true on a daemon with no auth manager")
	}
}

// The descriptor is the whole point: the daemon says which fields it wants, and
// the client renders them without knowing anything about the provider.
func TestAPIKeyLoginDescribesItsOwnForm(t *testing.T) {
	w, _ := authWorkspace(t)

	step, err := w.AuthLoginStart(context.Background(), ctrlproto.AuthLoginStartParams{
		Provider: "anthropic", Method: "apikey",
	})
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != "form" {
		t.Fatalf("kind = %q, want form", step.Kind)
	}
	if step.Flow == "" {
		t.Error("no flow handle; the submit could not be matched to this attempt")
	}
	if len(step.Fields) != 1 || step.Fields[0].Name != "api_key" {
		t.Fatalf("fields = %+v, want one api_key", step.Fields)
	}
	if step.Fields[0].Type != "secret" {
		t.Error("the api key field is not marked secret; a client would render it in the clear")
	}
	if !step.Fields[0].Required {
		t.Error("the api key is not required, but there is no login without one")
	}
}

// openai-compatible needs four fields, and it is the case that proves the
// descriptor earns its keep: nothing in the wire types names any of them.
func TestACustomEndpointAsksForEverythingItNeeds(t *testing.T) {
	w, _ := authWorkspace(t)

	step, err := w.AuthLoginStart(context.Background(), ctrlproto.AuthLoginStartParams{
		Provider: "openai-compatible", Method: "apikey",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"name":           "text",
		"base_url":       "text",
		"model":          "text",
		"api_key":        "secret",
		"context_window": "integer",
	}
	if len(step.Fields) != len(want) {
		t.Fatalf("got %d fields, want %d: %+v", len(step.Fields), len(want), step.Fields)
	}
	for _, f := range step.Fields {
		typ, ok := want[f.Name]
		if !ok {
			t.Errorf("unexpected field %q", f.Name)
			continue
		}
		if f.Type != typ {
			t.Errorf("field %q has type %q, want %q", f.Name, f.Type, typ)
		}
	}
	// The endpoint and the model are the two without which there is nothing to
	// talk to; the key is optional, because local servers routinely ignore it.
	byName := map[string]ctrlproto.AuthField{}
	for _, f := range step.Fields {
		byName[f.Name] = f
	}
	if !byName["base_url"].Required {
		t.Error("base_url must be required — without it there is nothing to talk to")
	}
	if byName["api_key"].Required {
		t.Error("the key must be optional — lm studio, llama.cpp and ollama all ignore it")
	}
	// The name is what turns this into its own provider instead of overwriting the
	// single shared slot. Optional, because leaving it empty must keep doing
	// exactly what it always did.
	if byName["name"].Required {
		t.Error("the name must be optional — an empty name is the shared openai-compatible slot, and that behaviour cannot change")
	}
	// The model is required for the shared slot and pointless for a named endpoint,
	// which discovers its own. RequiredUnless is what lets the form say which of the
	// two the operator is filling in, instead of disabling the button and going
	// quiet — the exact failure this pane exists to prevent.
	if !byName["model"].Required || byName["model"].RequiredUnless != "name" {
		t.Errorf("model must be required unless the endpoint is named; got required=%v required_unless=%q",
			byName["model"].Required, byName["model"].RequiredUnless)
	}
}

// The env-var providers are not a form. Offering a paste box for one would be a
// dead end that looks like a way in: terva stores nothing for them.
func TestAnEnvProviderIsToldAboutRatherThanAskedFor(t *testing.T) {
	w, _ := authWorkspace(t)

	step, err := w.AuthLoginStart(context.Background(), ctrlproto.AuthLoginStartParams{
		Provider: "amazon-bedrock", Method: "apikey",
	})
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != "info" {
		t.Fatalf("kind = %q, want info — a paste box here would store nothing", step.Kind)
	}
	if len(step.Fields) != 0 {
		t.Errorf("an info step carries fields: %+v", step.Fields)
	}
	if !strings.Contains(strings.Join(step.Lines, "\n"), "AWS_BEARER_TOKEN_BEDROCK") {
		t.Error("the guidance does not name the variable to set")
	}
}

// OAuth over the wire is the paste-back flow, never the loopback one. The
// loopback callback binds a port on the DAEMON, and a browser on a phone can
// never reach it — so what comes back is a URL to visit and a box to paste into.
func TestOAuthOverTheWireIsAPasteBackForm(t *testing.T) {
	w, _ := authWorkspace(t)

	step, err := w.AuthLoginStart(context.Background(), ctrlproto.AuthLoginStartParams{
		Provider: "anthropic", Method: "oauth",
	})
	if err != nil {
		t.Fatal(err)
	}
	if step.Kind != "form" {
		t.Fatalf("kind = %q, want form", step.Kind)
	}
	if step.URL == "" {
		t.Fatal("no authorize URL to visit")
	}
	if strings.Contains(step.URL, "localhost") || strings.Contains(step.URL, "127.0.0.1") {
		t.Errorf("the wire was handed a LOOPBACK authorize URL (%s) — it redirects to a port on the "+
			"daemon, which a remote browser can never reach", step.URL)
	}
	if len(step.Fields) != 1 || step.Fields[0].Name != "code" {
		t.Fatalf("fields = %+v, want one 'code'", step.Fields)
	}
}

// A provider with no subscription product must not be offered one.
func TestAProviderWithNoSubscriptionSaysSo(t *testing.T) {
	w, _ := authWorkspace(t)
	if _, err := w.AuthLoginStart(context.Background(), ctrlproto.AuthLoginStartParams{
		Provider: "deepseek", Method: "oauth",
	}); err == nil {
		t.Error("deepseek offered a subscription login; it has no subscription product")
	}
}

// A submit against a flow that is gone must be refused, not guessed at. This is
// the wire half of the flow handle: the pkce verifier the code was minted against
// is no longer there, and exchanging it would be a silent mix-up of two logins.
func TestASubmitAgainstAVanishedFlowIsRefused(t *testing.T) {
	w, _ := authWorkspace(t)

	err := w.AuthLoginSubmit(context.Background(), ctrlproto.AuthLoginSubmitParams{
		Flow: "f-never-existed", Values: map[string]string{"api_key": "sk-x"},
	})
	if err == nil {
		t.Fatal("a submit against an unknown flow was accepted")
	}
	var werr *ctrlproto.Error
	if !errors.As(err, &werr) || werr.Code != ctrlproto.CodeBusy {
		t.Errorf("error = %v, want a %s — the client did nothing wrong, the flow is simply gone",
			err, ctrlproto.CodeBusy)
	}
}

// The context window is the one field that is not a string, and it is parsed at
// exactly one place: here. A client sends what the user typed.
func TestAContextWindowThatIsNotANumberIsRejectedByTheDaemon(t *testing.T) {
	w, _ := authWorkspace(t)

	step, err := w.AuthLoginStart(context.Background(), ctrlproto.AuthLoginStartParams{
		Provider: "openai-compatible", Method: "apikey",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = w.AuthLoginSubmit(context.Background(), ctrlproto.AuthLoginSubmitParams{
		Flow: step.Flow,
		Values: map[string]string{
			"base_url": "http://localhost:1234/v1", "model": "qwen", "context_window": "lots",
		},
	})
	if err == nil {
		t.Fatal("a non-numeric context window was accepted")
	}
	if !strings.Contains(err.Error(), "whole number") {
		t.Errorf("error = %v, want it to name the problem", err)
	}
}

// Logging out clears the credential AND tells every other client, because a
// logout on the phone changes what the laptop can do.
func TestLogoutClearsTheCredentialAndAnnouncesIt(t *testing.T) {
	w, m := authWorkspace(t)
	if err := m.Store().SetAPIKey("kimi", "sk-kimi"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := w.Subscribe(ctx, ctrlproto.AddrWorkspace)
	if err != nil {
		t.Fatal(err)
	}

	if err := w.AuthLogout(context.Background(), ctrlproto.AuthLogoutParams{Provider: "kimi"}); err != nil {
		t.Fatal(err)
	}

	creds, _ := m.Store().Load()
	if auth.Describe(creds)["kimi"].Method != "" {
		t.Error("the credential survived the logout")
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Type == ctrlproto.EventSurfaceUpdated && ev.SurfaceID == "providers" {
				return // the pane was told
			}
		case <-deadline:
			t.Fatal("the logout was never announced; another client would keep showing a credential that is gone")
		}
	}
}

// openai holds two independent logins in one slot. Revoking the subscription must
// leave the platform API key standing, and vice versa.
func TestLoggingOutOfCodexLeavesThePlatformKeyAlone(t *testing.T) {
	w, m := authWorkspace(t)
	if err := m.Store().SetAPIKey("openai", "sk-platform"); err != nil {
		t.Fatal(err)
	}
	if err := m.Store().SetOAuth("openai", auth.OAuthToken{AccessToken: "codex"}); err != nil {
		t.Fatal(err)
	}

	if err := w.AuthLogout(context.Background(), ctrlproto.AuthLogoutParams{Provider: "openai-codex"}); err != nil {
		t.Fatal(err)
	}

	creds, _ := m.Store().Load()
	states := auth.Describe(creds)
	if states["openai-codex"].Method != "" {
		t.Error("the codex subscription survived its own logout")
	}
	if states["openai"].Method != "apikey" {
		t.Error("logging out of the codex subscription also destroyed the platform API key")
	}
}
