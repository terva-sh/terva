package build

import (
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// The precedence, stated once as a table: a provider override wins, an unset
// provider inherits, and a word this does not understand inherits rather than
// resolving to off. A hand-edited config with a typo must degrade to the
// default, never to the quieter behavior.
func TestActivationContinuationPrecedence(t *testing.T) {
	globalOn := map[string]bool{}
	globalOff := map[string]bool{ActivationContinuationFeatureID: false}

	cases := []struct {
		name    string
		global  map[string]bool
		keyword string
		want    bool
	}{
		{"unset inherits a global that is on", globalOn, "", true},
		{"unset inherits a global that is off", globalOff, "", false},
		{"off overrides a global that is on", globalOn, ActivationContinuationOff, false},
		{"on overrides a global that is off", globalOff, ActivationContinuationOn, true},
		{"off agrees with a global that is off", globalOff, ActivationContinuationOff, false},
		{"on agrees with a global that is on", globalOn, ActivationContinuationOn, true},
		{"a typo inherits rather than resolving to off", globalOn, "offf", true},
		{"a typo inherits an off global too", globalOff, "yes", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ActivationContinuationFor(tc.global, tc.keyword); got != tc.want {
				t.Errorf("ActivationContinuationFor(%v, %q) = %v, want %v", tc.global, tc.keyword, got, tc.want)
			}
		})
	}
}

// ValidActivationContinuation is what the settings write validates against, so
// a typo is refused at the boundary instead of silently inheriting.
func TestOnlyTheThreeKeywordsAreValid(t *testing.T) {
	for _, ok := range []string{"", "on", "off"} {
		if !ValidActivationContinuation(ok) {
			t.Errorf("%q must be accepted", ok)
		}
	}
	for _, bad := range []string{"offf", "true", "false", "ON", "inherit", "yes"} {
		if ValidActivationContinuation(bad) {
			t.Errorf("%q must be refused, so a typo cannot reach the config", bad)
		}
	}
}

// seedActivationConfig writes a config with a global engine-feature value and
// an optional per-provider override, then resolves a route on that provider.
func seedActivationConfig(t *testing.T, globalOn bool, providerID, keyword string) Resolved {
	t.Helper()
	lazy := true
	if err := config.MutateConfig(func(c *config.Config) {
		c.LazyTools = &lazy
		c.EngineFeatures = map[string]bool{ActivationContinuationFeatureID: globalOn}
		if keyword != "" {
			c.Providers = map[string]config.ProviderSettings{
				providerID: {ActivationContinuation: keyword},
			}
		} else {
			c.Providers = nil
		}
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5"}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return r
}

// The override reaches a built agent, in both directions. This is the criterion
// the ticket leads with: a session on a provider that overrides gets the
// override, not the global.
func TestProviderOverrideReachesTheBuiltAgent(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	r := seedActivationConfig(t, true, "openai", ActivationContinuationOff)
	if r.ActivationContinuation != ActivationContinuationOff {
		t.Fatalf("Resolve must carry the provider keyword, got %q", r.ActivationContinuation)
	}
	if r.NewAgent().ActivationContinuationEnabled() {
		t.Error("a provider override of off must switch the feature off, against a global that is on")
	}

	r = seedActivationConfig(t, false, "openai", ActivationContinuationOn)
	if !r.NewAgent().ActivationContinuationEnabled() {
		t.Error("a provider override of on must switch the feature on, against a global that is off")
	}
}

// The override is scoped: setting it on one provider must not move a session on
// another. Without this the setting would be a second global with extra steps.
func TestOverrideOnAnotherProviderDoesNotMoveThisOne(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	// The route resolves on openai; the override names a different provider.
	r := seedActivationConfig(t, true, "anthropic", ActivationContinuationOff)
	if r.ActivationContinuation != "" {
		t.Fatalf("a route on openai must not pick up anthropic's keyword, got %q", r.ActivationContinuation)
	}
	if !r.NewAgent().ActivationContinuationEnabled() {
		t.Error("an override on another provider must leave this session on the global")
	}
}

// The no-op criterion, and the one worth the most: a provider with no override
// must behave exactly as before this change, for BOTH global values. This
// touches the turn loop for every provider, so the valuable proof is that
// nothing moved rather than that the new path works.
func TestNoOverrideIsIdenticalToTheGlobal(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	for _, globalOn := range []bool{true, false} {
		r := seedActivationConfig(t, globalOn, "openai", "")
		if r.ActivationContinuation != "" {
			t.Fatalf("no override configured, but Resolve carried %q", r.ActivationContinuation)
		}
		// The two ways of asking must agree: what the funnel applies, and what
		// the feature alone would have applied before this change existed.
		f, ok := EngineFeatureByID(ActivationContinuationFeatureID)
		if !ok {
			t.Fatal("activation_continuation must still be a declared engine feature")
		}
		want := EngineFeatureOn(r.EngineFeatures, f)
		if want != globalOn {
			t.Fatalf("seeded global %v did not survive Resolve, got %v", globalOn, want)
		}
		if got := r.NewAgent().ActivationContinuationEnabled(); got != want {
			t.Errorf("global %v with no override: agent got %v, want %v — an unconfigured provider must be untouched", globalOn, got, want)
		}
	}
}

// ActivationContinuationEffective is what a swap hands ModelSwap, so it must
// apply the same precedence the build funnel does rather than a second copy of
// it that can drift.
func TestEffectiveMatchesTheFunnel(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	for _, keyword := range []string{"", ActivationContinuationOn, ActivationContinuationOff} {
		for _, globalOn := range []bool{true, false} {
			r := seedActivationConfig(t, globalOn, "openai", keyword)
			eff := r.ActivationContinuationEffective()
			if eff == nil {
				t.Fatal("a resolved route always has an answer, so this must never be nil")
			}
			if got := r.NewAgent().ActivationContinuationEnabled(); got != *eff {
				t.Errorf("keyword %q global %v: funnel says %v, swap would say %v", keyword, globalOn, got, *eff)
			}
		}
	}
}

// A cross-provider swap re-resolves, which is the whole point of scoping the
// setting per provider: a session that moves onto a provider with a different
// answer must take that answer, and one that moves onto a provider with no
// override must go back to the global.
func TestModelSwapReResolvesActivationContinuation(t *testing.T) {
	newAgent := func() *core.Agent {
		a := core.NewAgent(nil, "m", "sys", core.Registry{})
		a.EnableLazyTools()
		return a
	}

	off := false
	on := true

	a := newAgent()
	if !a.ActivationContinuationEnabled() {
		t.Fatal("the fixture must start on, or the assertions below prove nothing")
	}
	ApplyModelSwap(ModelSwap{Agent: a, Provider: "openai-codex", Model: "m2", ActivationContinuation: &off})
	if a.ActivationContinuationEnabled() {
		t.Error("a swap onto a provider that overrides off must switch the agent off")
	}
	ApplyModelSwap(ModelSwap{Agent: a, Provider: "anthropic", Model: "m3", ActivationContinuation: &on})
	if !a.ActivationContinuationEnabled() {
		t.Error("a swap onto a provider with no override must restore the global rather than keep the last provider's answer")
	}
}

// Nil is "no opinion", which the same-provider id-swap paths rely on: they are
// guarded on the provider being unchanged, so re-resolving would be busywork
// and must not be required of them.
func TestModelSwapWithNoOpinionLeavesTheSettingAlone(t *testing.T) {
	a := core.NewAgent(nil, "m", "sys", core.Registry{})
	a.EnableLazyTools()
	a.SetActivationContinuation(false)

	ApplyModelSwap(ModelSwap{Agent: a, Provider: "openai", Model: "m2"})
	if a.ActivationContinuationEnabled() {
		t.Error("a swap with no ActivationContinuation must not change the setting")
	}
}
