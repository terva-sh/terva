package workspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/testsupport"
)

// seedCreds writes an auth.json into a scratch TERVA_HOME and points the process
// at it, so AuthProviders reads what the test wrote rather than the developer's
// real credentials.
func seedCreds(t *testing.T, body string) {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if body != "" {
		if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func providers(t *testing.T) ctrlproto.ProvidersView {
	t.Helper()
	w := &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}
	v, err := w.AuthProviders(context.Background())
	if err != nil {
		t.Fatalf("AuthProviders: %v", err)
	}
	return v
}

func find(t *testing.T, v ctrlproto.ProvidersView, id string) ctrlproto.ProviderInfo {
	t.Helper()
	for _, p := range v.Providers {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("provider %q absent from the view", id)
	return ctrlproto.ProviderInfo{}
}

// The whole point of the pane: an expired subscription is VISIBLE, instead of
// surfacing as a turn that fails for no stated reason.
func TestAnExpiredSubscriptionIsReported(t *testing.T) {
	seedCreds(t, `{"anthropic":{"oauth":{"access_token":"stale","expiry":"2020-01-01T00:00:00Z"}}}`)

	p := find(t, providers(t), "anthropic")
	if p.Method != "oauth" {
		t.Errorf("method = %q, want oauth", p.Method)
	}
	if !p.Expired {
		t.Error("a long-dead token does not report as expired; the panel cannot warn anyone")
	}
	if p.Expiry == "" {
		t.Error("no expiry timestamp to show")
	}
}

// The other half of the point: the model picker silently omits every provider
// terva was never logged into, and looks broken. The pane lists them all, logged
// in or not, so the absence can be explained rather than merely observed.
func TestEveryLoggableProviderIsListedEvenWithNothingStored(t *testing.T) {
	seedCreds(t, "")

	v := providers(t)
	if len(v.Providers) < 20 {
		t.Fatalf("only %d providers listed; the pane cannot explain what is missing", len(v.Providers))
	}
	for _, p := range v.Providers {
		if p.Method != "" {
			t.Errorf("%s reports %q on an empty store", p.ID, p.Method)
		}
		if p.Label == "" {
			t.Errorf("%s has no label to render", p.ID)
		}
	}
	// openai-codex is a LOGIN id, not a storage id, and must still be offered.
	find(t, v, "openai-codex")
}

// Logged-in providers sort first — that is what the reader came for — and the
// order is stable, because a map has none and a pane that reshuffles itself
// between fetches is a bug.
func TestLoggedInProvidersComeFirstAndTheOrderIsStable(t *testing.T) {
	seedCreds(t, `{"anthropic":{"api_key":"sk-a"},"kimi":{"api_key":"sk-k"}}`)

	first := providers(t)
	if first.Providers[0].Method == "" || first.Providers[1].Method == "" {
		t.Errorf("logged-in providers are not first: %q, %q",
			first.Providers[0].ID, first.Providers[1].ID)
	}
	second := providers(t)
	for i := range first.Providers {
		if first.Providers[i].ID != second.Providers[i].ID {
			t.Fatalf("the order changed between two fetches (%q then %q at %d)",
				first.Providers[i].ID, second.Providers[i].ID, i)
		}
	}
}

// This view goes on a screen — possibly a phone on a shared network, possibly a
// screen share. It must never carry credential material.
func TestTheProvidersViewCarriesNoSecret(t *testing.T) {
	seedCreds(t, `{
		"anthropic":{"api_key":"sk-ant-SUPERSECRET"},
		"openai":{"oauth":{"access_token":"TOKENSECRET","refresh_token":"REFRESHSECRET"}}
	}`)

	blob, err := json.Marshal(providers(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sk-ant-SUPERSECRET", "TOKENSECRET", "REFRESHSECRET"} {
		if strings.Contains(string(blob), secret) {
			t.Errorf("the providers view leaked %q onto the wire:\n%s", secret, blob)
		}
	}
}

// A provider terva stores no credential for at all (bedrock, vertex, azure,
// cloudflare) is configured by environment variable. The pane must say so — a
// blank row for a provider you cannot sign into looks like a bug, and a paste
// box for one would be a dead end that looks like a way in.
func TestEnvProvidersCarryTheirSetupGuidance(t *testing.T) {
	seedCreds(t, "")

	p := find(t, providers(t), "amazon-bedrock")
	if len(p.Note) == 0 {
		t.Fatal("amazon-bedrock carries no setup guidance; the pane would show an empty row")
	}
	if got := strings.Join(p.Note, "\n"); !strings.Contains(got, "AWS_BEARER_TOKEN_BEDROCK") {
		t.Errorf("guidance does not name the variable to set:\n%s", got)
	}
	if len(p.Offers) != 1 || p.Offers[0] != "env" {
		t.Errorf("offers = %v, want [env] — it has no key for terva to take", p.Offers)
	}
}

// Until the auth group exists, the pane must not pretend it can sign you in.
func TestThePaneDoesNotOfferALoginItCannotPerform(t *testing.T) {
	seedCreds(t, "")
	if providers(t).CanLogin {
		t.Error("CanLogin is true, but no daemon serves a login yet — the pane would show a control that does nothing")
	}
}
