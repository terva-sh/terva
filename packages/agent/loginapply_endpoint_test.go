package agent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// A named endpoint carries no model: the login form deliberately does not ask
// for one, because the server knows what it serves. So the login has to go and
// find out — otherwise it leaves behind a provider the operator can see in
// /model and cannot run, and the session it builds sends an empty model id that
// the server rejects.
func TestApplyLoginSuccessAdoptsAnEndpointsModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[
			{"id":"qwen2.5-coder-32b","max_model_len":65536},
			{"id":"llama-3.3-70b"}]}`)
	}))
	defer srv.Close()

	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	provider.ResetCatalogLayers()
	t.Cleanup(provider.ResetCatalogLayers)
	t.Cleanup(func() { build.UnregisterEndpoint("workshop") })
	// ApplyLoginSuccess fires a background model refresh; join it while this
	// test's scratch home is still live, or it leaks into the tests after us.
	t.Cleanup(waitModelRefresh)

	ep := config.EndpointConfig{BaseURL: srv.URL + "/v1"}
	if err := config.SaveConfig(config.Config{
		Endpoints: map[string]config.EndpointConfig{"workshop": ep},
	}); err != nil {
		t.Fatal(err)
	}
	if err := build.RegisterEndpoint("workshop", ep); err != nil {
		t.Fatal(err)
	}

	var promoted [3]string
	var promotedN int
	ApplyLoginSuccess(config.AuthStoreFor(), "workshop", func(providerName, model, scope string) error {
		promoted = [3]string{providerName, model, scope}
		promotedN++
		return nil
	})

	models := provider.ModelsForProvider("workshop")
	if len(models) != 2 {
		t.Fatalf("catalog has %d models for the endpoint, want the 2 it serves", len(models))
	}
	// Re-stamped onto the endpoint, not left under the shared slot the
	// discoverer labels everything with — otherwise the endpoint's picker is
	// empty and the models show up under a provider the operator never chose.
	for _, m := range models {
		if m.Provider != "workshop" {
			t.Errorf("model %q landed under provider %q", m.ID, m.Provider)
		}
		if m.BaseURL != ep.BaseURL {
			t.Errorf("model %q has base URL %q, want the endpoint's", m.ID, m.BaseURL)
		}
	}
	// The server's own hint wins over the generic floor.
	for _, m := range models {
		if m.ID == "qwen2.5-coder-32b" && m.ContextWindow != 65536 {
			t.Errorf("context window = %d, want the server's 65536", m.ContextWindow)
		}
	}

	if promotedN == 0 {
		t.Fatal("no default was promoted; the operator pointed terva at this server and would still have to pick a model by hand")
	}
	if promoted[0] != "workshop" || promoted[2] != "global" {
		t.Errorf("promoted = %v, want workshop/.../global", promoted)
	}
	if promoted[1] == "" {
		t.Error("promoted an empty model id, which is exactly what the server rejects")
	}
}

// A server that went away between the probe and the model list must not fail the
// login: the endpoint IS saved and registered by then, so the operator keeps a
// provider they can pick a model on once it is back.
func TestApplyLoginSuccessSurvivesAnUnreachableEndpoint(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	provider.ResetCatalogLayers()
	t.Cleanup(provider.ResetCatalogLayers)
	t.Cleanup(func() { build.UnregisterEndpoint("gone-away") })
	// Same join as above: even a refresh that discovers nothing is a leaked
	// goroutine until it finishes.
	t.Cleanup(waitModelRefresh)

	// A port nothing is listening on: connection refused, immediately.
	ep := config.EndpointConfig{BaseURL: "http://127.0.0.1:1/v1"}
	if err := config.SaveConfig(config.Config{
		Endpoints: map[string]config.EndpointConfig{"gone-away": ep},
	}); err != nil {
		t.Fatal(err)
	}
	if err := build.RegisterEndpoint("gone-away", ep); err != nil {
		t.Fatal(err)
	}

	var promotedN int
	ApplyLoginSuccess(config.AuthStoreFor(), "gone-away", func(_, _, _ string) error {
		promotedN++
		return nil
	})

	if promotedN != 0 {
		t.Error("promoted a default from an endpoint that answered nothing")
	}
}
