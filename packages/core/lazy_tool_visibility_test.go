package core

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"terva.sh/terva/packages/provider"
)

// flagTool is a minimal tool that records whether Execute ran, so a test can
// prove a hidden tool still dispatched.
type flagTool struct {
	name     string
	executed bool
}

func (f *flagTool) Name() string            { return f.name }
func (f *flagTool) Description() string     { return "d-" + f.name }
func (f *flagTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f *flagTool) Execute(context.Context, json.RawMessage, func(string)) (ToolResult, error) {
	f.executed = true
	return ToolResult{Content: []provider.Content{provider.TextBlock{Text: "ok"}}}, nil
}

func specNames(specs []provider.Tool) map[string]bool {
	m := make(map[string]bool, len(specs))
	for _, s := range specs {
		m[s.Name] = true
	}
	return m
}

// TestSpecsVisibleFiltersAdvertisedNotCallable pins the registry-level split:
// SpecsVisible narrows the ADVERTISED specs, but the registry — the callable
// surface — is untouched. This is the "daylight" between what the model sees
// and what it can call that lazy tool visibility (H2·b) introduces.
func TestSpecsVisibleFiltersAdvertisedNotCallable(t *testing.T) {
	reg := Registry{
		"alpha": &flagTool{name: "alpha"},
		"bravo": &flagTool{name: "bravo"},
		"charl": &flagTool{name: "charl"},
	}

	// nil predicate advertises everything (Specs's behavior).
	if got := len(reg.Specs()); got != 3 {
		t.Fatalf("Specs() advertised %d tools, want 3", got)
	}

	// Hiding "bravo" removes it from the advertised specs only.
	visible := func(name string) bool { return name != "bravo" }
	adv := specNames(reg.SpecsVisible(visible))
	if adv["bravo"] {
		t.Error("hidden tool must not be advertised")
	}
	if !adv["alpha"] || !adv["charl"] {
		t.Errorf("visible tools must still be advertised, got %v", adv)
	}

	// The registry still resolves the hidden tool: it remains callable.
	if _, err := reg.Get("bravo"); err != nil {
		t.Errorf("hidden tool must remain in the registry (callable), got %v", err)
	}

	// The advertised order stays name-sorted (load-bearing for prompt caching).
	specs := reg.SpecsVisible(visible)
	if len(specs) != 2 || specs[0].Name != "alpha" || specs[1].Name != "charl" {
		t.Errorf("SpecsVisible must stay name-sorted, got %v", specNames(specs))
	}
}

// visibilityCaptureClient records req.Tools per call and drives one tool call:
// call 1 asks for "secret", call 2 ends the turn.
type visibilityCaptureClient struct {
	mu    sync.Mutex
	calls int
	tools [][]provider.Tool
}

func (c *visibilityCaptureClient) Name() string { return "viscap" }

func (c *visibilityCaptureClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	c.calls++
	n := c.calls
	c.tools = append(c.tools, req.Tools)
	c.mu.Unlock()

	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "viscap", Model: req.Model}
		if n == 1 {
			out <- provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.ToolCallBlock{ID: "t1", Name: "secret", Arguments: json.RawMessage(`{}`)}},
			}}
			return
		}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "done"}},
		}}
	}()
	return out, nil
}

// TestVisibleToolHiddenStillDispatchesAndGates is the H2·b load-bearing
// invariant end-to-end: a tool hidden from the model by VisibleTool is (a) NOT
// advertised in the request, yet (b) still dispatches when the model calls it
// anyway, and (c) still passes through the permission gate. Visibility is not
// authority — the advertised list never decides callability or authorization.
func TestVisibleToolHiddenStillDispatchesAndGates(t *testing.T) {
	secret := &flagTool{name: "secret"}
	client := &visibilityCaptureClient{}
	a := NewAgent(client, "fake-model", "system", Registry{"secret": secret})
	a.VisibleTool = func(name string) bool { return name != "secret" }

	gateFired := false
	a.BeforeToolExecute = func(call provider.ToolCallBlock) (bool, string, json.RawMessage) {
		if call.Name == "secret" {
			gateFired = true
		}
		return true, "", nil
	}

	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.tools) == 0 {
		t.Fatal("no request captured")
	}
	// (a) hidden from the advertised list.
	if specNames(client.tools[0])["secret"] {
		t.Error("a hidden tool must not be advertised to the model")
	}
	// (b) still callable — dispatch resolved the full registry.
	if !secret.executed {
		t.Error("a hidden tool must still dispatch when called (visibility != callability)")
	}
	// (c) still gated — the permission hook fired regardless of visibility.
	if !gateFired {
		t.Error("the permission gate must fire for a hidden-but-called tool (visibility != authority)")
	}
}
