package tools

import "testing"

// resolveSpawnRoute pins the (provider, model) pair: explicit both passes
// through, omitting both inherits the host route (a tier picks a cheaper
// model for the host provider, pinned to it), and a lone model/provider is
// rejected.
func TestResolveSpawnRoute(t *testing.T) {
	// Deterministic tier override so resolution doesn't depend on the live
	// catalog: anthropic/weak -> a fixed id.
	tiers := SwarmTierMap{"anthropic": {"weak": {Model: "claude-weak-test"}, "medium": {Model: "claude-medium-test"}}}

	cases := []struct {
		name                    string
		args                    swarmSpawnArgs
		hostProvider, hostModel string
		wantModel, wantProvider string
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
		})
	}
}
