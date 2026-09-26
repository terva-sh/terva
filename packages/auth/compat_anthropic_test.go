package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// The Anthropic slot's wire settings survive a write/read cycle. They are the
// difference between a working gateway and one that 401s every turn, so losing
// them on the way to disk is indistinguishable from never having set them.
func TestStoreAnthropicCompatRoundTrip(t *testing.T) {
	store := NewStore(filepath.Join(testsupport.TempDir(t), "auth.json"))
	want := CompatEndpoint{
		BaseURL:        "http://gw.box:4000",
		Model:          "claude-sonnet-4.5",
		ContextWindow:  200000,
		APIVersion:     "2099-01-01",
		Beta:           "context-1m-2025-08-07",
		AuthStyle:      AuthStyleBearer,
		DisableCaching: true,
	}
	if err := store.SetCompatEndpoint(anthropicCompatProvider, "sk-gw", want); err != nil {
		t.Fatal(err)
	}
	if got := store.CompatEndpointFor(anthropicCompatProvider); got != want {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
	// A keyless endpoint is still "logged in": that is the whole point of
	// pointing terva at your own server.
	creds, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !creds.Has(anthropicCompatProvider) || creds.Method(anthropicCompatProvider) != "apikey" {
		t.Errorf("Has/Method = %v/%q", creds.Has(anthropicCompatProvider), creds.Method(anthropicCompatProvider))
	}
}

// The four fields are additive: an auth.json written before they existed must
// load unchanged and mean "the defaults". Anything else turns an upgrade into a
// broken login.
func TestStoreAnthropicCompatReadsAPreExistingFile(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "auth.json")
	legacy := `{"additional_api_key_creds":{"anthropic-compatible":{` +
		`"api_key":"sk","base_url":"http://gw.box:4000","model":"m","context_window":123}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	ep := NewStore(path).CompatEndpointFor(anthropicCompatProvider)
	if ep.BaseURL != "http://gw.box:4000" || ep.Model != "m" || ep.ContextWindow != 123 {
		t.Fatalf("pre-existing fields lost: %+v", ep)
	}
	if !ep.AnthropicOptions().IsZero() {
		t.Errorf("absent wire settings = %+v, want the defaults", ep.AnthropicOptions())
	}
}

// Writing the whole endpoint — not merging — is deliberate: these fields
// describe ONE server, and carrying a previous endpoint's beta header or auth
// style across to a new base URL would be a setting the operator never chose
// and cannot see.
func TestSetCompatEndpointReplacesRatherThanMerges(t *testing.T) {
	store := NewStore(filepath.Join(testsupport.TempDir(t), "auth.json"))
	if err := store.SetCompatEndpoint(anthropicCompatProvider, "", CompatEndpoint{
		BaseURL: "http://old:4000", Model: "m", Beta: "old-beta", AuthStyle: AuthStyleBearer,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCompatEndpoint(anthropicCompatProvider, "", CompatEndpoint{
		BaseURL: "http://new:4000", Model: "m",
	}); err != nil {
		t.Fatal(err)
	}
	got := store.CompatEndpointFor(anthropicCompatProvider)
	if got.Beta != "" || got.AuthStyle != "" {
		t.Errorf("settings from the previous endpoint survived: %+v", got)
	}
}

// Rendering to the wire options is where auth_style stops being a word and
// starts being a header choice.
func TestCompatEndpointAnthropicOptions(t *testing.T) {
	for _, tc := range []struct {
		style      string
		wantBearer bool
	}{
		{"", false},
		{AuthStyleAPIKey, false},
		{AuthStyleBearer, true},
		{"Bearer", true}, // a browser select or a hand-edited file may capitalise
	} {
		got := CompatEndpoint{AuthStyle: tc.style}.AnthropicOptions().BearerAuth
		if got != tc.wantBearer {
			t.Errorf("auth style %q -> BearerAuth %v, want %v", tc.style, got, tc.wantBearer)
		}
	}
}

// An unrecognised auth style is REFUSED, not ignored.
//
// Ignoring is the trap: AnthropicOptions folds anything it does not recognise to
// x-api-key, so a typo would produce a login that succeeds, a probe that passes
// against a keyless server, and a 401 on the first real turn — with the setting
// visibly present in auth.json and visibly not in effect.
func TestValidateCompatEndpointRejectsAnUnknownAuthStyle(t *testing.T) {
	err := ValidateCompatEndpoint(anthropicCompatProvider, CompatEndpoint{
		BaseURL: "http://gw:4000", Model: "m", AuthStyle: "Bearer-token",
	})
	if err == nil {
		t.Fatal("an unknown auth style was accepted")
	}
	for _, ok := range AuthStyles() {
		if err := ValidateCompatEndpoint(anthropicCompatProvider, CompatEndpoint{AuthStyle: ok}); err != nil {
			t.Errorf("ValidateCompatEndpoint(%q) = %v, want accepted", ok, err)
		}
	}
}

// Header values may not carry a line break. Go's http.Header.Set would reject it
// at send time with an error naming neither the setting nor the login that
// stored it, which is a long way from the field the operator typed into.
func TestValidateCompatEndpointRejectsHeaderInjection(t *testing.T) {
	for _, ep := range []CompatEndpoint{
		{APIVersion: "2023-06-01\r\nx-evil: 1"},
		{Beta: "beta\nx-evil: 1"},
	} {
		if err := ValidateCompatEndpoint(anthropicCompatProvider, ep); err == nil {
			t.Errorf("a line break was accepted in %+v", ep)
		}
	}
}

// The OpenAI slot has no wire settings, so it has nothing to validate — the
// fields are inert there rather than an error worth refusing a login over.
func TestValidateCompatEndpointIgnoresTheOpenAISlot(t *testing.T) {
	if err := ValidateCompatEndpoint(compatProvider, CompatEndpoint{AuthStyle: "nonsense"}); err != nil {
		t.Errorf("openai-compatible validation = %v, want it to ignore the anthropic fields", err)
	}
}

// The on/off field's default is ON, so anything unrecognised must mean on:
// a value nobody understands must not quietly disable prompt caching.
func TestIsOffValue(t *testing.T) {
	for _, s := range []string{"off", "OFF", " false ", "no", "0", "disabled"} {
		if !IsOffValue(s) {
			t.Errorf("IsOffValue(%q) = false", s)
		}
	}
	for _, s := range []string{"", "on", "true", "yes", "1", "sometimes", "nope"} {
		if IsOffValue(s) {
			t.Errorf("IsOffValue(%q) = true — an unrecognised value must not disable caching", s)
		}
	}
}

// Nothing in the endpoint may reach a client as a secret by accident: the key is
// the key, and everything else is configuration. This pins that the settings
// serialize under their own names rather than being folded into the api_key
// field by a future refactor.
func TestAnthropicCompatFieldsAreNotStoredAsTheKey(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "auth.json")
	store := NewStore(path)
	if err := store.SetCompatEndpoint(anthropicCompatProvider, "sk-secret", CompatEndpoint{
		BaseURL: "http://gw:4000", Model: "m", Beta: "b", AuthStyle: AuthStyleBearer,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	if err := json.Unmarshal(raw, &f); err != nil {
		// An encrypted-at-rest home is a valid outcome here; the round trip
		// above already covers the values, so there is nothing to assert.
		t.Skip("auth.json is not plain JSON on this home (secrets at rest)")
	}
	creds, _ := f["additional_api_key_creds"].(map[string]any)
	row, _ := creds[anthropicCompatProvider].(map[string]any)
	if row["auth_style"] != AuthStyleBearer || row["beta"] != "b" {
		t.Errorf("settings not persisted under their own keys: %v", row)
	}
}
