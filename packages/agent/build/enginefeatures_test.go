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

// Cache-aware compaction ships ON, and must be switchable OFF.
//
// The default lives ONLY here — core.NewAgent's zero value is off, and every
// core test sets the flag explicitly — so without this test a flip back to
// default-off would break nothing and pass everything. The off switch matters
// just as much: the cost side of this feature is measured and settled, but its
// effect on summary QUALITY is not, and shipping something unproven without a
// way back is how a dogfood becomes a regression nobody can escape.
func TestCacheAwareCompactionShipsOnAndCanBeSwitchedOff(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	ag := r.NewAgent()
	if !ag.CacheAwareCompactionEnabled() {
		t.Error("cache_aware_compaction must default ON — a compaction that rebuilds its own prefix re-reads the whole conversation at full price")
	}
	if !ag.PrefixChangeGuardEnabled() {
		t.Error("prefix_change_guard must default ON; it comes alive with cache-aware compaction")
	}

	// The way back. Both are independently escapable: someone who wants the
	// dedicated summarizer's prompt can have it, and someone who wants the cheap
	// compaction without a pre-turn dialog can have that too.
	if err := config.MutateConfig(func(c *config.Config) {
		c.EngineFeatures = map[string]bool{"cache_aware_compaction": false}
	}); err != nil {
		t.Fatalf("override config: %v", err)
	}
	r, err = Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve with override: %v", err)
	}
	ag = r.NewAgent()
	if ag.CacheAwareCompactionEnabled() {
		t.Error("engine_features.cache_aware_compaction=false must switch it off at build")
	}
	// The guard stays declared-on, but is inert without the summarizer that gives
	// its offer something to save — so turning cache-aware off is a complete exit
	// from both behaviors, with no second toggle to hunt for.
	if !ag.PrefixChangeGuardEnabled() {
		t.Error("the guard's own toggle should be untouched by the other feature's override")
	}
}

// Stuck-loop detection ships ON and must be switchable OFF. Like cache-aware
// compaction, the shipped default lives ONLY here — core.NewAgent's zero value is
// off — so without this a flip back to default-off would pass every core test.
// The off switch matters because the nudge is a model-facing intervention: a
// deployment that finds it noisy needs a way to silence it without a rebuild.
func TestStuckLoopDetectionShipsOnAndCanBeSwitchedOff(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !r.NewAgent().StallDetectionEnabled() {
		t.Error("stuck_loop_detection must default ON — detection + a one-turn nudge is safe (in-band, no model swap, no egress)")
	}

	if err := config.MutateConfig(func(c *config.Config) {
		c.EngineFeatures = map[string]bool{"stuck_loop_detection": false}
	}); err != nil {
		t.Fatalf("override config: %v", err)
	}
	r, err = Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve with override: %v", err)
	}
	if r.NewAgent().StallDetectionEnabled() {
		t.Error("engine_features.stuck_loop_detection=false must switch it off at build")
	}
}

// Stuck-loop escalation ships ON but inert: the flag defaults on (so a user who
// configures a target is asked without hunting for a toggle), yet nothing
// escalates until a host binds an Escalator and a target is set. Like the others,
// the shipped default lives only here.
func TestStuckLoopEscalationShipsOnAndCanBeSwitchedOff(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// On by default, but inert here: a plain Resolve binds no Escalator, so the
	// flag is armed and the runLoop driver is still a no-op.
	if !r.NewAgent().StuckLoopEscalationEnabled() {
		t.Error("stuck_loop_escalation must default ON (inert without a bound Escalator + a configured target)")
	}

	if err := config.MutateConfig(func(c *config.Config) {
		c.EngineFeatures = map[string]bool{"stuck_loop_escalation": false}
	}); err != nil {
		t.Fatalf("override config: %v", err)
	}
	r, err = Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve with override: %v", err)
	}
	if r.NewAgent().StuckLoopEscalationEnabled() {
		t.Error("engine_features.stuck_loop_escalation=false must switch it off at build")
	}
}

// Auto-escalate defaults OFF (a persistent loop asks first, because escalating a
// local model to a remote one egresses the transcript), and config escalation.auto
// arms it at the build funnel.
func TestEscalationAutoFromConfig(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.NewAgent().EscalateAutoEnabled() {
		t.Error("auto-escalate must default OFF (ask-first) with no escalation config")
	}

	if err := config.MutateConfig(func(c *config.Config) {
		c.Escalation = &config.EscalationConfig{Provider: "anthropic", Model: "claude-sonnet-5", Auto: true}
	}); err != nil {
		t.Fatalf("override config: %v", err)
	}
	r, err = Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve with override: %v", err)
	}
	if !r.NewAgent().EscalateAutoEnabled() {
		t.Error("escalation.auto=true must arm auto-escalate at the build funnel")
	}
}
