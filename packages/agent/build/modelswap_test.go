package build

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
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

// The session record is part of the event, not a host chore. It was a host
// chore, and bot mode never did it: a bot that switched model wrote every later
// turn under the old route and resumed onto it.
func TestTheSwapRecordsTheNewRouteOnTheSession(t *testing.T) {
	ag, _, _ := swapFixture()
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	sess, err := core.NewSessionAtPath(path, "/ws", "old-prov", "old-model", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	ApplyModelSwap(ModelSwap{
		Agent: ag, Provider: "new-prov", Model: "new-model", Session: sess,
	})

	if sess.Meta.Provider != "new-prov" || sess.Meta.Model != "new-model" {
		t.Errorf("session meta = %s/%s, want new-prov/new-model",
			sess.Meta.Provider, sess.Meta.Model)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"model":"new-model"`) {
		t.Error("the new route never reached the file, so a resume would restore the old one")
	}
}

// Recorded only once the agent is actually on the new route: a file naming a
// model the agent had not moved to would resume onto the wrong one.
func TestTheRouteIsRecordedAfterTheAgentHasMoved(t *testing.T) {
	ag, routed, _ := swapFixture()
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	sess, err := core.NewSessionAtPath(path, "/ws", "old-prov", "old-model", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	_ = routed
	// Read the file's idea of the model at the instant the client is installed.
	// The record must not have landed yet: a swap that fails partway would
	// otherwise leave a session naming a model nothing is running.
	var modelAtSwap string
	ApplyModelSwap(ModelSwap{
		Agent: ag, Client: &swapClient{}, Provider: "new-prov", Model: "new-model", Session: sess,
		Swap: func(c provider.Client, m string) {
			modelAtSwap = sess.Meta.Model
			ag.SetClientAndModel(c, m)
		},
	})
	if modelAtSwap != "old-model" {
		t.Errorf("the session already said %q while the client was still being installed; "+
			"the record ran before the agent moved", modelAtSwap)
	}
	if sess.Meta.Model != "new-model" {
		t.Errorf("session meta model = %q, want new-model", sess.Meta.Model)
	}
}

// A host with no durable session (bot mode before it had one, an ephemeral
// per-chat agent) must still swap.
func TestASwapWithNoSessionIsFine(t *testing.T) {
	ag, routed, _ := swapFixture()
	ApplyModelSwap(ModelSwap{Agent: ag, Provider: "new-prov", Model: "new-model"})
	if routed.prov != "new-prov" {
		t.Errorf("dispatch route = %q, want new-prov", routed.prov)
	}
}

// Re-applying the same route writes nothing. This is what makes the record safe
// on the resume path, where the swap is onto the model the file already names —
// otherwise every resume would append a meta row saying nothing changed.
func TestReRecordingTheSameRouteWritesNothing(t *testing.T) {
	ag, _, _ := swapFixture()
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	sess, err := core.NewSessionAtPath(path, "/ws", "same-prov", "same-model", "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ApplyModelSwap(ModelSwap{
		Agent: ag, Provider: "same-prov", Model: "same-model", Session: sess,
	})
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("an unchanged route appended %d bytes; resume would grow the file every open",
			len(after)-len(before))
	}
}
