package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/auth"
	"terva.sh/terva/packages/provider"
)

// The login form for an Anthropic-compatible endpoint.
//
// The daemon owns the field list; the TUI and the web panel both render exactly
// what it declares. So the assertions here are the contract for both clients at
// once — which is the whole reason AuthField exists (the two forms ordered
// openai-compatible's fields differently when each decided for itself).

func fieldNames(step ctrlproto.AuthFlowStep) []string {
	out := make([]string, 0, len(step.Fields))
	for _, f := range step.Fields {
		out = append(out, f.Name)
	}
	return out
}

func fieldNamed(t *testing.T, step ctrlproto.AuthFlowStep, name string) ctrlproto.AuthField {
	t.Helper()
	for _, f := range step.Fields {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no field %q; form has %v", name, fieldNames(step))
	return ctrlproto.AuthField{}
}

func startLogin(t *testing.T, w *Workspace, providerID string) ctrlproto.AuthFlowStep {
	t.Helper()
	step, err := w.AuthLoginStart(context.Background(), ctrlproto.AuthLoginStartParams{
		Provider: providerID, Method: "apikey",
	})
	if err != nil {
		t.Fatalf("AuthLoginStart(%s): %v", providerID, err)
	}
	return step
}

// The parity fields come first and in the same order as the OpenAI form, so an
// operator who has connected one recognises the other. The wire settings are
// appended, all optional.
func TestAnthropicCompatFormDeclaresTheWireSettings(t *testing.T) {
	seedConfig(t, "")
	w := endpointWorkspace(t)

	openai := startLogin(t, w, provider.OpenAICompatProvider)
	anthropic := startLogin(t, w, provider.AnthropicCompatProvider)

	// The shared prefix is identical — not merely present, but in order.
	shared := fieldNames(openai)
	got := fieldNames(anthropic)
	if len(got) <= len(shared) {
		t.Fatalf("anthropic form has %v, want the openai fields %v plus the wire settings", got, shared)
	}
	for i, want := range shared {
		if got[i] != want {
			t.Errorf("field %d = %q, want %q — the two compatible forms have drifted", i, got[i], want)
		}
	}

	// Every wire setting is optional: the form is longer than the OpenAI one and
	// nothing in it has to be filled in.
	for _, name := range []string{"anthropic_version", "anthropic_beta", "auth_style", "prompt_caching"} {
		f := fieldNamed(t, anthropic, name)
		if f.Required {
			t.Errorf("%s is required; every wire setting has a working default", name)
		}
		if f.Help == "" {
			t.Errorf("%s has no help text; its accepted values are not discoverable otherwise", name)
		}
	}

	// The two enum-ish fields carry their default, because AuthField has no
	// select type and an empty box would be the only hint of what is accepted.
	if f := fieldNamed(t, anthropic, "auth_style"); f.Default != auth.AuthStyleAPIKey {
		t.Errorf("auth_style default = %q, want %q", f.Default, auth.AuthStyleAPIKey)
	}
	if f := fieldNamed(t, anthropic, "prompt_caching"); f.Default != "on" {
		t.Errorf("prompt_caching default = %q, want on", f.Default)
	}
	if f := fieldNamed(t, anthropic, "anthropic_version"); f.Default != auth.AnthropicDefaultAPIVersion {
		t.Errorf("anthropic_version default = %q, want %q", f.Default, auth.AnthropicDefaultAPIVersion)
	}

	// The model stays optional-when-named on both forms: a named endpoint
	// discovers what its server serves, so demanding one would ask the operator
	// to paste back what terva is about to find for itself.
	if f := fieldNamed(t, anthropic, "model"); f.RequiredUnless != "name" {
		t.Errorf("model.required_unless = %q, want name", f.RequiredUnless)
	}
}

// anthropicStub is a server that answers the Anthropic model list, recording
// how the probe authenticated.
func anthropicStub(t *testing.T) (*httptest.Server, *http.Header) {
	t.Helper()
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Header().Set("content-type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"claude-sonnet-4.5","display_name":"Claude Sonnet 4.5"}],"has_more":false}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func submit(t *testing.T, w *Workspace, flow string, values map[string]string) error {
	t.Helper()
	return w.AuthLoginSubmit(context.Background(), ctrlproto.AuthLoginSubmitParams{Flow: flow, Values: values})
}

// Naming an Anthropic endpoint writes a config.json entry carrying `api:
// "anthropic"` plus the wire settings — the definition, never the key.
func TestNamedAnthropicEndpointIsSavedWithItsWire(t *testing.T) {
	seedConfig(t, "")
	srv, probeHeaders := anthropicStub(t)
	w := endpointWorkspace(t)
	step := startLogin(t, w, provider.AnthropicCompatProvider)

	if err := submit(t, w, step.Flow, map[string]string{
		"name":              "claude-gw",
		"base_url":          srv.URL,
		"api_key":           "sk-gw",
		"context_window":    "200000",
		"anthropic_version": "2099-01-01",
		"anthropic_beta":    "context-1m-2025-08-07",
		"auth_style":        auth.AuthStyleBearer,
		"prompt_caching":    "off",
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	t.Cleanup(func() { build.UnregisterEndpoint("claude-gw") })

	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	ep, ok := cfg.Endpoints["claude-gw"]
	if !ok {
		t.Fatalf("endpoint not written; config has %v", cfg.Endpoints)
	}
	if !ep.IsAnthropic() {
		t.Errorf("api = %q, want the endpoint marked anthropic", ep.API)
	}
	if ep.AnthropicVersion != "2099-01-01" || ep.AnthropicBeta != "context-1m-2025-08-07" {
		t.Errorf("headers not saved: %+v", ep)
	}
	if ep.AuthStyle != auth.AuthStyleBearer || !ep.DisableCaching {
		t.Errorf("auth style / caching not saved: %+v", ep)
	}
	if ep.ContextWindow != 200000 {
		t.Errorf("context window = %d", ep.ContextWindow)
	}

	// The probe authenticated the way the endpoint will. A probe that used the
	// default header would pass here and leave the first turn to 401.
	//
	// 🪤 `authorization: Bearer` is NOT the assertion to make: ProbeOpenAICompatible
	// sends exactly that too, so checking it passes whether or not the Anthropic
	// prober ran. anthropic-version is the header only this wire sends, and the
	// operator's override is a value only their endpoint config carries.
	if got := probeHeaders.Get("anthropic-version"); got != "2099-01-01" {
		t.Errorf("probe anthropic-version = %q, want the endpoint's own override — "+
			"the wrong prober ran, or it used defaults instead of this endpoint's settings", got)
	}
	if got := probeHeaders.Get("authorization"); got != "Bearer sk-gw" {
		t.Errorf("probe authorization = %q, want the endpoint's own bearer style", got)
	}

	// The key is a credential, so it must not be in config.json.
	raw, _ := json.Marshal(cfg.Endpoints)
	if strings.Contains(string(raw), "sk-gw") {
		t.Errorf("the api key leaked into config.json: %s", raw)
	}

	// And it became a usable provider in THIS process, without a restart.
	if !build.IsKnownProvider("claude-gw") {
		t.Error("the endpoint was saved but not registered; every attempt to use it this session would fall through to the default provider")
	}
}

// An unnamed login goes to the shared slot in auth.json, and carries the same
// settings there.
func TestUnnamedAnthropicLoginFillsTheSharedSlot(t *testing.T) {
	seedConfig(t, "")
	srv, _ := anthropicStub(t)
	w := endpointWorkspace(t)
	step := startLogin(t, w, provider.AnthropicCompatProvider)

	if err := submit(t, w, step.Flow, map[string]string{
		"base_url":       srv.URL,
		"model":          "claude-sonnet-4.5",
		"auth_style":     auth.AuthStyleBearer,
		"prompt_caching": "off",
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}

	ep := config.AuthStoreFor().CompatEndpointFor(provider.AnthropicCompatProvider)
	if ep.BaseURL != srv.URL || ep.Model != "claude-sonnet-4.5" {
		t.Fatalf("shared slot = %+v", ep)
	}
	if !ep.AnthropicOptions().BearerAuth || !ep.DisableCaching {
		t.Errorf("wire settings lost on the shared slot: %+v", ep)
	}
	// Nothing was written to config.json: the shared slot is a login, not a
	// provider definition.
	if cfg, err := config.LoadConfig(); err == nil && len(cfg.Endpoints) != 0 {
		t.Errorf("an unnamed login wrote endpoints: %v", cfg.Endpoints)
	}
}

// The daemon is the authority on what these values may be. The form is an
// affordance: a client that sends anything must be refused here, not have it
// silently folded to a default that then does not work.
func TestAnthropicLoginRefusesBadSettings(t *testing.T) {
	for _, tc := range []struct{ name, field, value string }{
		{"unknown auth style", "auth_style", "Bearer-token"},
		{"header injection in version", "anthropic_version", "2023-06-01\r\nx-evil: 1"},
		{"header injection in beta", "anthropic_beta", "b\nx-evil: 1"},
		{"non-numeric context window", "context_window", "lots"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seedConfig(t, "")
			srv, _ := anthropicStub(t)
			w := endpointWorkspace(t)
			step := startLogin(t, w, provider.AnthropicCompatProvider)

			values := map[string]string{
				"name":     "bad-gw",
				"base_url": srv.URL,
			}
			values[tc.field] = tc.value
			if err := submit(t, w, step.Flow, values); err == nil {
				build.UnregisterEndpoint("bad-gw")
				t.Fatalf("%s was accepted", tc.field)
			}
			// Nothing half-written: a refused login must leave no endpoint behind.
			if cfg, err := config.LoadConfig(); err == nil {
				if _, ok := cfg.Endpoints["bad-gw"]; ok {
					t.Error("a refused login still wrote the endpoint to config.json")
				}
			}
		})
	}
}

// Neither shared slot's own name may be taken by an endpoint: it is what named
// endpoints exist to escape, and the collision would shadow the slot in
// config.json, auth.json, and the model picker at once.
func TestValidEndpointNameRefusesBothSharedSlots(t *testing.T) {
	for _, slot := range []string{provider.OpenAICompatProvider, provider.AnthropicCompatProvider} {
		if err := ValidEndpointName(slot); err == nil {
			t.Errorf("ValidEndpointName(%q) = nil, want a refusal", slot)
		}
	}
	if err := ValidEndpointName("claude-gw"); err != nil {
		t.Errorf("ValidEndpointName(claude-gw) = %v, want it accepted", err)
	}
}
