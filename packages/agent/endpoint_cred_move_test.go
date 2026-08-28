package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/provider/auth"
	"terva.sh/terva/packages/testsupport"
)

// writeEndpointConfig lays down a config.json pinning one named endpoint.
func writeEndpointConfig(t *testing.T, home, id string) {
	t.Helper()
	b, _ := json.Marshal(config.Config{
		Provider: id,
		Model:    "qwen3.8-27b",
		Endpoints: map[string]config.EndpointConfig{
			id: {BaseURL: "http://vllm.example:8000/v1"},
		},
	})
	if err := os.WriteFile(filepath.Join(home, "config.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func additionalCreds(t *testing.T) map[string]auth.ProviderCreds {
	t.Helper()
	creds, err := config.AuthStoreFor().Load()
	if err != nil {
		t.Fatalf("load auth: %v", err)
	}
	return creds.AdditionalAPIKeyCreds
}

// A keyed endpoint must keep its key across the rename. Leaving it behind
// strands the secret under an id nothing resolves any more, and the operator
// meets a keyless endpoint against a server that wants a key — a 401 whose
// cause is two renames away.
func TestEndpointRenameCarriesTheCredential(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	if err := config.AuthStoreFor().SetAPIKey("NeoT", "sk-endpoint-live"); err != nil {
		t.Fatal(err)
	}
	writeEndpointConfig(t, home, "NeoT")
	if err := build.RegisterOrReplaceEndpoint("NeoT", config.EndpointConfig{
		BaseURL: "http://vllm.example:8000/v1",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { build.UnregisterEndpoint("neot") })

	ValidateAndRepairConfig()

	got := additionalCreds(t)
	if c, ok := got["neot"]; !ok || c.APIKey != "sk-endpoint-live" {
		t.Errorf("credential not carried to %q: %+v", "neot", got)
	}
	if _, ok := got["NeoT"]; ok {
		t.Errorf("credential left behind under the old id %q", "NeoT")
	}
}

// The case that was silently destructive: a credential already under the
// canonical name — an orphan from a removed endpoint, say. The rename must not
// overwrite it, and must not delete the live one either.
//
// 🪤 The first version copied conditionally and deleted unconditionally, so the
// live key vanished with no message at all.
func TestEndpointRenameStrandsNoCredentialWhenTheTargetIsTaken(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	if err := config.AuthStoreFor().SetAPIKey("NeoT", "sk-live"); err != nil {
		t.Fatal(err)
	}
	if err := config.AuthStoreFor().SetAPIKey("neot", "sk-orphan"); err != nil {
		t.Fatal(err)
	}

	moveEndpointCredential("NeoT", "neot")

	got := additionalCreds(t)
	if c, ok := got["NeoT"]; !ok || c.APIKey != "sk-live" {
		t.Errorf("the live key was destroyed rather than left in place: %+v", got)
	}
	if c, ok := got["neot"]; !ok || c.APIKey != "sk-orphan" {
		t.Errorf("the existing key under the canonical id was overwritten: %+v", got)
	}
}

// A keyless endpoint is the common case (most local servers want no key), and
// the mover must be a clean no-op rather than writing an empty entry.
func TestEndpointRenameWithNoCredentialIsANoOp(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	moveEndpointCredential("NeoT", "neot")

	if got := additionalCreds(t); len(got) != 0 {
		t.Errorf("a keyless rename wrote credentials: %+v", got)
	}
}
