package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/testsupport"
)

// A config written before endpoint ids were normalised holds whatever the
// operator typed. The repair has to move three things together — the config
// key, the pinned provider, and the stored credential — because a rename that
// moved only some of them trades one broken state for another.
//
// This is the shape that was reported: an endpoint saved as "NeoT" serving
// qwen3.8-27b, where the model list worked but the selection would not stick
// and terva kept resolving to a different provider.
func TestValidateAndRepairConfig_MigratesMixedCaseEndpoint(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	b, _ := json.Marshal(config.Config{
		Provider: "NeoT",
		Model:    "qwen3.8-27b",
		Endpoints: map[string]config.EndpointConfig{
			"NeoT": {BaseURL: "http://vllm.example:8000/v1", ContextWindow: 32768},
		},
	})
	if err := os.WriteFile(filepath.Join(home, "config.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	// The endpoint must be a registered provider, as it is at startup —
	// otherwise the unknown-provider branch is what we would be testing.
	if err := build.RegisterOrReplaceEndpoint("NeoT", config.EndpointConfig{
		BaseURL: "http://vllm.example:8000/v1",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { build.UnregisterEndpoint("neot") })

	ValidateAndRepairConfig()

	out, err := config.LoadConfig()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := out.Endpoints["neot"]; !ok {
		t.Errorf("endpoint not migrated to %q; endpoints = %v", "neot", keysOf(out.Endpoints))
	}
	if _, ok := out.Endpoints["NeoT"]; ok {
		t.Errorf("the original spelling %q survived the migration", "NeoT")
	}
	if out.Provider != "neot" {
		t.Errorf("provider = %q, want %q — the pin must follow the rename", out.Provider, "neot")
	}
	// The whole point: the model the operator chose must still be there. The
	// unknown-provider branch resets Provider AND wipes Model, which is exactly
	// the "it goes back to the default" symptom.
	if out.Model != "qwen3.8-27b" {
		t.Errorf("model = %q, want %q — the selection was lost", out.Model, "qwen3.8-27b")
	}
}

// An operator who already has BOTH spellings must not silently lose one.
func TestValidateAndRepairConfig_EndpointRenameCollisionIsLeftAlone(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	b, _ := json.Marshal(config.Config{
		Endpoints: map[string]config.EndpointConfig{
			"Box": {BaseURL: "http://one.example:8000/v1"},
			"box": {BaseURL: "http://two.example:8000/v1"},
		},
	})
	if err := os.WriteFile(filepath.Join(home, "config.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}

	ValidateAndRepairConfig()

	out, _ := config.LoadConfig()
	if got := out.Endpoints["box"].BaseURL; got != "http://two.example:8000/v1" {
		t.Errorf("the existing canonical entry was overwritten: %q", got)
	}
	if _, ok := out.Endpoints["Box"]; !ok {
		t.Error("the colliding entry was dropped rather than left for the operator")
	}
}

func keysOf(m map[string]config.EndpointConfig) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
