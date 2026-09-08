package tools

import "testing"

// resolveSpawnRoute pins the (provider, model) pair: explicit both passes
// through, omitting both inherits the host route (a tier picks a cheaper
// model for the host provider, pinned to it), and a lone model/provider is
// rejected.
func TestResolveSpawnRoute(t *testing.T) {
	// Deterministic tier override so resolution doesn't depend on the live
	// catalog: anthropic/weak -> a fixed id.
	// The medium rung moves the EFFORT and not the model, which is the shape a
	// provider with one good model has to use.
	tiers := SwarmTierMap{"anthropic": {
		"weak":   {Model: "claude-weak-test"},
		"medium": {Model: "claude-medium-test", Reasoning: "low"},
	}}

	cases := []struct {
		name                    string
		args                    swarmSpawnArgs
		hostProvider, hostModel string
		wantModel, wantProvider string
		wantReasoning           string
		wantInherited, wantTier bool
		wantErr                 bool
	}{
		{
			name:         "omit both inherits host pair",
			args:         swarmSpawnArgs{Task: "x"},
			hostProvider: "anthropic", hostModel: "claude-opus-test",
			wantModel: "claude-opus-test", wantProvider: "anthropic", wantInherited: true,
		},
		{
			name:         "tier resolves a host-provider model and pins the provider",
			args:         swarmSpawnArgs{Task: "x", Tier: "weak"},
			hostProvider: "anthropic", hostModel: "claude-opus-test",
			wantModel: "claude-weak-test", wantProvider: "anthropic", wantInherited: true, wantTier: true,
		},
		{
			name:         "unresolved tier falls back to the host model, still pinned",
			args:         swarmSpawnArgs{Task: "x", Tier: "weak"},
			hostProvider: "customgw", hostModel: "gw-model", // no tier table/override
			wantModel: "gw-model", wantProvider: "customgw", wantInherited: true,
		},
		{
			name:         "explicit model+provider passes through",
			args:         swarmSpawnArgs{Task: "x", Model: "gpt-5", Provider: "openai"},
			hostProvider: "anthropic", hostModel: "claude-opus-test",
			wantModel: "gpt-5", wantProvider: "openai",
		},
		{
			name:    "lone model is rejected",
			args:    swarmSpawnArgs{Task: "x", Model: "gpt-5"},
			wantErr: true,
		},
		{
			name:    "lone provider is rejected",
			args:    swarmSpawnArgs{Task: "x", Provider: "openai"},
			wantErr: true,
		},
		{
			name:    "provider with tier (no model) is rejected",
			args:    swarmSpawnArgs{Task: "x", Provider: "openai", Tier: "weak"},
			wantErr: true,
		},
		{
			name:         "a tier carries its own effort",
			args:         swarmSpawnArgs{Task: "x", Tier: "medium"},
			hostProvider: "anthropic", hostModel: "claude-opus-test",
			wantModel: "claude-medium-test", wantProvider: "anthropic",
			wantReasoning: "low", wantInherited: true, wantTier: true,
		},
		{
			// The reason this field exists: a pinned route used to be the one
			// shape that could carry no effort at all.
			name:         "an explicit effort rides a pinned model and provider",
			args:         swarmSpawnArgs{Task: "x", Model: "gpt-5", Provider: "openai", Reasoning: "HIGH"},
			hostProvider: "anthropic", hostModel: "claude-opus-test",
			wantModel: "gpt-5", wantProvider: "openai", wantReasoning: "high",
		},
		{
			name:         "an explicit effort beats the tier's own",
			args:         swarmSpawnArgs{Task: "x", Tier: "medium", Reasoning: "maximum"},
			hostProvider: "anthropic", hostModel: "claude-opus-test",
			wantModel: "claude-medium-test", wantProvider: "anthropic",
			wantReasoning: "maximum", wantInherited: true, wantTier: true,
		},
		{
			// "off" must reach the child as the word, not as the empty string,
			// because empty means "resolve your own" one layer down.
			name:         "off is an effort, not an absent one",
			args:         swarmSpawnArgs{Task: "x", Reasoning: "off"},
			hostProvider: "anthropic", hostModel: "claude-opus-test",
			wantModel: "claude-opus-test", wantProvider: "anthropic",
			wantReasoning: "off", wantInherited: true,
		},
		{
			name:    "an unknown effort word is rejected",
			args:    swarmSpawnArgs{Task: "x", Reasoning: "banana"},
			wantErr: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			route, errMsg := resolveSpawnRoute(c.args, c.hostProvider, c.hostModel, tiers)
			if c.wantErr {
				if errMsg == "" {
					t.Fatalf("expected an error, got route %+v", route)
				}
				return
			}
			if errMsg != "" {
				t.Fatalf("unexpected error: %s", errMsg)
			}
			if route.Model != c.wantModel || route.Provider != c.wantProvider {
				t.Errorf("route = (%q,%q), want (%q,%q)", route.Model, route.Provider, c.wantModel, c.wantProvider)
			}
			if route.Inherited != c.wantInherited {
				t.Errorf("inherited = %v, want %v", route.Inherited, c.wantInherited)
			}
			if !route.Tier.IsZero() != c.wantTier {
				t.Errorf("tier pick = %+v, want tier-resolved=%v", route.Tier, c.wantTier)
			}
			if route.Reasoning != c.wantReasoning {
				t.Errorf("reasoning = %q, want %q", route.Reasoning, c.wantReasoning)
			}
		})
	}
}
