package build

import (
	"slices"
	"terva.sh/terva/packages/core/lazytools"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/core"
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

	on := true
	if err := config.MutateConfig(func(c *config.Config) { c.LazyTools = &on }); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !lazytools.Of(r.NewAgent(core.AllowAll)).ContinuationEnabled() {
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
	if lazytools.Of(r.NewAgent(core.AllowAll)).ContinuationEnabled() {
		t.Error("the engine_features override must switch the feature off at build")
	}
}

// Cache-aware compaction ships ON, and must be switchable OFF.
//
// The default lives ONLY here — core.New's zero value is off, and every
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
	ag := r.NewAgent(core.AllowAll)
	if !allows(ag, core.CompactWarm) {
		t.Error("cache_aware_compaction must default ON — a compaction that rebuilds its own prefix re-reads the whole conversation at full price")
	}
	if !offers(ag) {
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
	ag = r.NewAgent(core.AllowAll)
	if allows(ag, core.CompactWarm) {
		t.Error("engine_features.cache_aware_compaction=false must switch it off at build")
	}
	// The guard stays declared-on, but is inert without the summarizer that gives
	// its offer something to save — so turning cache-aware off is a complete exit
	// from both behaviors, with no second toggle to hunt for.
	if !offers(ag) {
		t.Error("the guard's own toggle should be untouched by the other feature's override")
	}
}

// offers reports whether ag's policy would offer a compaction before a
// cache-invalidating change.
func offers(ag *core.Agent) bool { return ag.Compaction(core.CompactPrefixChanged).Compact }

// The guard's own way out: prefix_change_guard=false stops the offer at build,
// and the feature's Apply flips a live agent both ways.
func TestPrefixChangeGuardCanBeSwitchedOff(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	if err := config.MutateConfig(func(c *config.Config) {
		c.EngineFeatures = map[string]bool{"prefix_change_guard": false}
	}); err != nil {
		t.Fatalf("override config: %v", err)
	}
	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	ag := r.NewAgent(core.AllowAll)
	if offers(ag) {
		t.Error("engine_features.prefix_change_guard=false must switch the offer off at build")
	}
	f, _ := EngineFeatureByID("prefix_change_guard")
	f.Apply(ag, true)
	if !offers(ag) {
		t.Error("switching the feature on did not reach the live agent's policy")
	}
	f.Apply(ag, false)
	if offers(ag) {
		t.Error("switching the feature off did not reach the live agent's policy")
	}
}

// Stuck-loop detection ships ON and must be switchable OFF. Like cache-aware
// compaction, the shipped default lives ONLY here — a zero stall.Detector is
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
	if !AssemblerOf(r.NewAgent(core.AllowAll)).Stall().Enabled() {
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
	if AssemblerOf(r.NewAgent(core.AllowAll)).Stall().Enabled() {
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
	// flag is armed and rung 3 still has nothing to escalate to.
	if !AssemblerOf(r.NewAgent(core.AllowAll)).Stall().EscalationEnabled() {
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
	if AssemblerOf(r.NewAgent(core.AllowAll)).Stall().EscalationEnabled() {
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
	if AssemblerOf(r.NewAgent(core.AllowAll)).Stall().EscalateAutoEnabled() {
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
	if !AssemblerOf(r.NewAgent(core.AllowAll)).Stall().EscalateAutoEnabled() {
		t.Error("escalation.auto=true must arm auto-escalate at the build funnel")
	}
}

// Prefix-divergence recording ships ON, and that default is the whole design:
// a diagnostic shipped off is never enabled at the moment the rare thing
// happens, and this one's value is entirely retrospective — reading rows back
// out of a session nobody knew would go wrong. Two measured sessions lost 10.5%
// and 32.2% of their spend to zero-cache turns with no trace of why.
//
// Like the others, the shipped default lives ONLY here: core.New's zero
// value is off, so a flip back to default-off would pass every core test.
func TestPrefixDivergenceRecordingShipsOnAndCanBeSwitchedOff(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !AssemblerOf(r.NewAgent(core.AllowAll)).PrefixWatch().Enabled() {
		t.Error("prefix_divergence_recording must default ON — a diagnostic that ships off is never on when the rare event happens")
	}

	if err := config.MutateConfig(func(c *config.Config) {
		c.EngineFeatures = map[string]bool{"prefix_divergence_recording": false}
	}); err != nil {
		t.Fatalf("override config: %v", err)
	}
	r, err = Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve with override: %v", err)
	}
	if AssemblerOf(r.NewAgent(core.AllowAll)).PrefixWatch().Enabled() {
		t.Error("engine_features.prefix_divergence_recording=false must switch it off at build")
	}
}

func TestTransportRecordingShipsOnAndCanBeSwitchedOff(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !AssemblerOf(r.NewAgent(core.AllowAll)).Transport().Enabled() {
		t.Error("transport_recording must default ON — its value is retrospective, and the sessions it exists for are the ones nobody knew would go wrong")
	}

	if err := config.MutateConfig(func(c *config.Config) {
		c.EngineFeatures = map[string]bool{"transport_recording": false}
	}); err != nil {
		t.Fatalf("override config: %v", err)
	}
	r, err = Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve with override: %v", err)
	}
	if AssemblerOf(r.NewAgent(core.AllowAll)).Transport().Enabled() {
		t.Error("engine_features.transport_recording=false must switch it off at build")
	}
}

// Provider compaction ships OFF and must be switchable ON.
//
// The inverse of every test above, and it needs one more assertion than they
// do. A default-ON feature has a test that fails the moment the default flips;
// a default-OFF one has a test that would keep passing if the feature were
// deleted outright, since "off" and "absent" resolve identically at the agent.
// So the declaration is asserted separately from the behavior.
//
// What the default is protecting: this strategy's checkpoint is an encrypted
// blob only the issuing provider can read, and unlike cache_aware_compaction —
// which shipped on with its cost side measured and only summary quality open —
// the saving this exists for has not been measured at all. Nobody gets an
// unportable transcript they did not ask for.
func TestProviderCompactionShipsOffAndCanBeSwitchedOn(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	f, ok := EngineFeatureByID("provider_compaction")
	if !ok {
		t.Fatal("provider_compaction must be a declared engine feature — without the declaration there is no way to turn it on, and the off assertion below would pass vacuously")
	}
	if f.Default {
		t.Error("provider_compaction must default OFF until the cache saving it exists for is measured")
	}

	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if allows(r.NewAgent(core.AllowAll), core.CompactProvider) {
		t.Error("provider_compaction is on at build with no override asking for it")
	}

	if err := config.MutateConfig(func(c *config.Config) {
		c.EngineFeatures = map[string]bool{"provider_compaction": true}
	}); err != nil {
		t.Fatalf("override config: %v", err)
	}
	r, err = Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve with override: %v", err)
	}
	if !allows(r.NewAgent(core.AllowAll), core.CompactProvider) {
		t.Error("engine_features.provider_compaction=true must switch it on at build — an A/B that cannot enable its own arm cannot be run")
	}
}

// allows reports whether ag's compaction policy lets a compaction try st. The
// engine has no strategy switches; the policy's decision is the whole answer.
func allows(ag *core.Agent, st core.CompactStrategy) bool {
	return slices.Contains(ag.Compaction(core.CompactRequested).Strategies, st)
}

// A strategy feature toggled on a live agent, as the settings surface does
// through Apply, changes the next compaction's strategies without a rebuild.
// Cold stays last in every combination: it is the fallback that works on every
// provider.
func TestStrategyFeaturesApplyToALiveAgent(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	ag := r.NewAgent(core.AllowAll)
	warm, _ := EngineFeatureByID("cache_aware_compaction")
	prov, _ := EngineFeatureByID("provider_compaction")

	for _, tc := range []struct {
		warm, provider bool
		want           []core.CompactStrategy
	}{
		{false, false, []core.CompactStrategy{core.CompactCold}},
		{true, false, []core.CompactStrategy{core.CompactWarm, core.CompactCold}},
		{false, true, []core.CompactStrategy{core.CompactProvider, core.CompactCold}},
		{true, true, []core.CompactStrategy{core.CompactProvider, core.CompactWarm, core.CompactCold}},
	} {
		warm.Apply(ag, tc.warm)
		prov.Apply(ag, tc.provider)
		if got := ag.Compaction(core.CompactRequested).Strategies; !slices.Equal(got, tc.want) {
			t.Errorf("warm=%v provider=%v: strategies = %v, want %v", tc.warm, tc.provider, got, tc.want)
		}
	}
}
