package build

import (
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/testsupport"
)

func TestEngineFeatureOnResolvesOverridesOverDefaults(t *testing.T) {
	f, ok := EngineFeatureByID("activation_continuation")
	if !ok {
		t.Fatal("activation_continuation must be a declared engine feature")
	}
	if !f.Default || !f.RequiresLazyTools {
		t.Errorf("activation_continuation should default on and nest under lazy tools, got %+v", f)
	}
	if !EngineFeatureOn(nil, f) {
		t.Error("no overrides must resolve to the default")
	}
	if EngineFeatureOn(map[string]bool{"activation_continuation": false}, f) {
		t.Error("an override must win over the default")
	}
	if _, ok := EngineFeatureByID("nope"); ok {
		t.Error("unknown ids must not resolve")
	}
}

// The NewAgent funnel applies every engine feature — default unless the config
// override says otherwise — so all hosts and headless runs resolve identically.
func TestEngineFeaturesApplyAtNewAgent(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	if err := config.MutateConfig(func(c *config.Config) { c.LazyTools = true }); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !r.NewAgent().ActivationContinuationEnabled() {
		t.Error("activation continuation should default on under lazy tools")
	}

	if err := config.MutateConfig(func(c *config.Config) {
		c.EngineFeatures = map[string]bool{"activation_continuation": false}
	}); err != nil {
		t.Fatalf("override config: %v", err)
	}
	r, err = Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve with override: %v", err)
	}
	if r.NewAgent().ActivationContinuationEnabled() {
		t.Error("the engine_features override must switch the feature off at build")
	}
}
