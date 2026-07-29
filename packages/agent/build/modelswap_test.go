package build

import (
	"context"
	"encoding/json"
	"testing"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// oldClient reports a usage snapshot, standing in for the client being
// replaced. swapClient accepts one, standing in for its replacement.
type oldClient struct{ snap provider.UsageSnapshot }

func (c *oldClient) Name() string { return "old" }
func (c *oldClient) Stream(context.Context, provider.Request) (<-chan provider.Event, error) {
	return nil, nil
}
func (c *oldClient) UsageSnapshot() (provider.UsageSnapshot, bool) { return c.snap, true }

type swapClient struct {
	seeded     *provider.UsageSnapshot
	installed  func() bool // reports whether this client is already on the agent
	seenBefore bool        // was the agent still on the OLD client when seeded?
}

func (c *swapClient) Name() string { return "new" }
func (c *swapClient) Stream(context.Context, provider.Request) (<-chan provider.Event, error) {
	return nil, nil
}
func (c *swapClient) SeedUsage(s provider.UsageSnapshot) {
	c.seeded = &s
	if c.installed != nil {
		c.seenBefore = !c.installed()
	}
}

// routedTool is a stand-in for swarm_spawn / actor_spawn / raati_convene: a
// tool that inherits the host's route.
type routedTool struct{ prov, model string }

func (t *routedTool) Name() string               { return "routed_probe" }
func (t *routedTool) Description() string        { return "probe" }
func (t *routedTool) Schema() json.RawMessage    { return json.RawMessage(`{"type":"object"}`) }
func (t *routedTool) SetHost(prov, model string) { t.prov, t.model = prov, model }
func (t *routedTool) Execute(context.Context, json.RawMessage, func(string)) (core.ToolResult, error) {
	return core.ToolResult{}, nil
}

var _ tools.HostRouted = (*routedTool)(nil)

func swapFixture() (*core.Agent, *routedTool, *tools.StatusTool) {
	routed := &routedTool{prov: "old-prov", model: "old-model"}
	status := &tools.StatusTool{Provider: "old-prov", AuthMethod: "apikey", BaseURL: "https://old.example"}
	reg := core.Registry{"routed_probe": routed, "terva_status": status}
	ag := core.NewAgent(&oldClient{snap: provider.UsageSnapshot{Provider: "old-prov"}}, "old-model", "", reg)
	return ag, routed, status
}

// The usage carry-over is the one ordering constraint that is not a matter of
// taste: the snapshot is read from the OLD client and seeded into the NEW one
// while the old one is still installed. Seed after the swap and Agent.Usage()
// reads the fresh client's empty snapshot instead, which is the bug the
// carry-over exists to prevent — the status meters blank until the next turn.
func TestTheUsageSnapshotIsCarriedBeforeTheNewClientIsInstalled(t *testing.T) {
	ag, _, _ := swapFixture()
	nc := &swapClient{}
	nc.installed = func() bool { return ag.Client == provider.Client(nc) }

	ApplyModelSwap(ModelSwap{Agent: ag, Client: nc, Provider: "new-prov", Model: "new-model"})

	if nc.seeded == nil {
		t.Fatal("the new client was never seeded with the old one's usage snapshot")
	}
	if nc.seeded.Provider != "old-prov" {
		t.Errorf("seeded snapshot = %+v, want the OLD client's", *nc.seeded)
	}
	if !nc.seenBefore {
		t.Error("the seed happened after the new client was already installed, so it carried the new " +
			"client's empty snapshot rather than the old one's observation")
	}
	if ag.Client != provider.Client(nc) || ag.Model != "new-model" {
		t.Errorf("agent did not end on the new client/model: %v / %q", ag.Client, ag.Model)
	}
}

// A swap keeps the agent's registry, so terva_status still carries the previous
// provider identity until it is re-bound — and because FindModel(oldProvider,
// newModel) then misses, the tool also loses the context-window size.
func TestAClientSwapRebindsTheStatusIdentity(t *testing.T) {
	ag, _, status := swapFixture()
	ApplyModelSwap(ModelSwap{
		Agent: ag, Client: &swapClient{},
		Provider: "new-prov", Model: "new-model", AuthMethod: "oauth", BaseURL: "https://new.example",
	})
	if status.Provider != "new-prov" || status.AuthMethod != "oauth" || status.BaseURL != "https://new.example" {
		t.Errorf("terva_status identity = %q/%q/%q, want the target's", status.Provider, status.AuthMethod, status.BaseURL)
	}
}

// The same-endpoint id swap must NOT touch that identity. It performs no
// Resolve, so its caller has no truthful AuthMethod or BaseURL to offer — and
// writing the zero values would erase an identity that is still correct,
// turning a no-op into a regression.
func TestAnIDSwapLeavesTheStatusIdentityAlone(t *testing.T) {
	ag, _, status := swapFixture()
	ApplyModelSwap(ModelSwap{Agent: ag, Provider: "old-prov", Model: "new-model"})

	if status.AuthMethod != "apikey" || status.BaseURL != "https://old.example" {
		t.Errorf("an id swap erased the status identity: %q/%q", status.AuthMethod, status.BaseURL)
	}
	if ag.Model != "new-model" {
		t.Errorf("agent model = %q, want the new id", ag.Model)
	}
	if _, isOld := ag.Client.(*oldClient); !isOld {
		t.Error("an id swap replaced the client; it is supposed to keep serving")
	}
}

// Both paths re-point the dispatch tools. This is the step that existed in one
// host out of four: a sub-agent spawned after a swap must inherit the CURRENT
// route, and an id swap changes that route just as much as a rebuild does.
func TestBothPathsRepointTheHostRoutedTools(t *testing.T) {
	for _, tc := range []struct {
		name   string
		client provider.Client
	}{
		{"client rebuild", &swapClient{}},
		{"same-endpoint id swap", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ag, routed, _ := swapFixture()
			ApplyModelSwap(ModelSwap{Agent: ag, Client: tc.client, Provider: "new-prov", Model: "new-model"})
			if routed.prov != "new-prov" || routed.model != "new-model" {
				t.Errorf("dispatch tool still routes to %s/%s — a sub-agent spawned now would run on the "+
					"pre-swap model", routed.prov, routed.model)
			}
		})
	}
}

// Bot mode fans one client across every per-chat agent, so it owns the
// assignment; everything else in the event still applies to it.
func TestSwapOverrideReplacesTheAssignmentAndNothingElse(t *testing.T) {
	ag, routed, status := swapFixture()
	var got string
	ApplyModelSwap(ModelSwap{
		Agent: ag, Client: &swapClient{}, Provider: "new-prov", Model: "new-model", AuthMethod: "oauth",
		Swap: func(_ provider.Client, m string) { got = m },
	})
	if got != "new-model" {
		t.Errorf("the host's Swap was not used (got %q)", got)
	}
	if ag.Model == "new-model" {
		t.Error("ApplyModelSwap assigned the model itself despite the host supplying Swap")
	}
	if status.Provider != "new-prov" || routed.prov != "new-prov" {
		t.Error("a host that owns the assignment still gets the rest of the event")
	}
}

func TestTheHostTailRunsLast(t *testing.T) {
	ag, routed, _ := swapFixture()
	var routeAtTail string
	ApplyModelSwap(ModelSwap{
		Agent: ag, Provider: "new-prov", Model: "new-model",
		After: func() { routeAtTail = routed.prov },
	})
	if routeAtTail != "new-prov" {
		t.Errorf("After ran before the event finished (dispatch route was %q)", routeAtTail)
	}
}

func TestANilAgentIsANoOp(t *testing.T) {
	ApplyModelSwap(ModelSwap{Provider: "p", Model: "m"}) // must not panic
}
