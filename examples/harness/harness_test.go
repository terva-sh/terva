package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// A whole turn against the scripted client: the gate allows one call and
// refuses the other, the allowed tool runs, the refusal reaches the model as
// an error result, and the model's answer arrives. This is the check that the
// stable packages are enough to run an agent.
func TestATurnRunsOnTheStablePackagesAlone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	notes := &notebook{}
	agent := mustAgent(t, hostCatalog(), notes, core.NewMemoryTranscriptStore())

	var text strings.Builder
	results := map[string]core.ToolResult{}
	calls := map[string]string{}
	err := agent.Run(ctx, core.PromptInput{Text: "What is 2 + 3? Also remember my password is hunter2"}, func(ev core.AgentEvent) {
		switch e := ev.(type) {
		case core.EvTextDelta:
			text.WriteString(e.Delta)
		case core.EvToolCall:
			calls[e.ID] = e.Name
		case core.EvToolResult:
			results[calls[e.ID]] = e.Result
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if r, ok := results["add"]; !ok || r.IsError || resultOf(r) != "5" {
		t.Errorf("add: got %+v, want a result of 5", r)
	}
	if r, ok := results["remember"]; !ok || !r.IsError || !strings.Contains(resultOf(r), "may not mention a password") {
		t.Errorf("remember: got %+v, want the gate's refusal as an error result", r)
	}
	if got := notes.all(); len(got) != 0 {
		t.Errorf("the refused note was kept: %q", got)
	}
	// The fake prefixes a result with "error: " only when the block it was
	// sent has IsError set, so this checks the flag reached the provider and
	// not just the event stream.
	if !strings.Contains(text.String(), "5;") || !strings.Contains(text.String(), "error: refused by the host") {
		t.Errorf("answer %q does not carry the sum and the refusal as an error", text.String())
	}
}

// The script reads the prompt, so a turn only calls the tools it asks for,
// and a note the gate allows is kept.
func TestTheScriptCallsOnlyTheToolsThePromptAsksFor(t *testing.T) {
	for _, c := range []struct {
		prompt, want string
		kept         []string
	}{
		{"what is 2 + 3.5?", "The tools said: 5.5.", nil},
		{"remember to buy 2 apples", "The tools said: kept note 1.", []string{"buy 2 apples"}},
		{"hello", "I have no tool for that", nil},
	} {
		notes := &notebook{}
		var text strings.Builder
		agent := mustAgent(t, hostCatalog(), notes, core.NewMemoryTranscriptStore())
		err := agent.Run(context.Background(), core.PromptInput{Text: c.prompt}, func(ev core.AgentEvent) {
			if e, ok := ev.(core.EvTextDelta); ok {
				text.WriteString(e.Delta)
			}
		})
		if err != nil {
			t.Fatalf("%q: %v", c.prompt, err)
		}
		if !strings.Contains(text.String(), c.want) {
			t.Errorf("%q: answer %q, want it to contain %q", c.prompt, text.String(), c.want)
		}
		if got := notes.all(); strings.Join(got, "|") != strings.Join(c.kept, "|") {
			t.Errorf("%q: kept %q, want %q", c.prompt, got, c.kept)
		}
	}
}

// The gate is the host's policy, so it is tested as one: a note is kept
// unless it looks like a credential.
func TestTheGateKeepsCredentialsOutOfNotes(t *testing.T) {
	for _, c := range []struct {
		note string
		want bool
	}{
		{"buy milk", true},
		{"my Password is x", false},
	} {
		call := callBlock("remember", `{"note":`+quote(c.note)+`}`)
		if got, _, _ := gate(context.Background(), call, rememberTool{}); got != c.want {
			t.Errorf("gate(%q) = %v, want %v", c.note, got, c.want)
		}
	}
	if ok, _, _ := gate(context.Background(), callBlock("add", `{"numbers":[1]}`), addTool{}); !ok {
		t.Error("the gate refused add, which it has no rule for")
	}
}

// mustAgent builds the example's agent on the scripted client.
func mustAgent(t *testing.T, catalog provider.ModelCatalog, notes *notebook, store core.TranscriptStore) *core.Agent {
	t.Helper()
	agent, err := newAgent(scriptedClient{}, "scripted", catalog, notes, store)
	if err != nil {
		t.Fatal(err)
	}
	return agent
}

func resultOf(r core.ToolResult) string {
	return resultText(toolResultBlock(r))
}

func toolResultBlock(r core.ToolResult) provider.ToolResultBlock {
	return provider.ToolResultBlock{Content: r.Content, IsError: r.IsError}
}

func callBlock(name, args string) provider.ToolCallBlock {
	return provider.ToolCallBlock{ID: "t", Name: name, Arguments: json.RawMessage(args)}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// A second agent resumed from the store continues the conversation: it starts
// with the first agent's messages and cost, and its own turn lands in the same
// store after them.
func TestAResumedAgentContinuesFromTheStore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	notes := &notebook{}
	store := core.NewMemoryTranscriptStore()

	first := mustAgent(t, hostCatalog(), notes, store)
	if err := first.Run(ctx, core.PromptInput{Text: "remember to buy milk"}, nil); err != nil {
		t.Fatal(err)
	}
	before := len(first.Messages())

	second, err := resume(scriptedClient{}, "scripted", hostCatalog(), notes, store)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(second.Messages()); got != before {
		t.Fatalf("resumed with %d messages, want the first agent's %d", got, before)
	}
	if second.Cost() != first.Cost() {
		t.Errorf("resumed cost %+v, want %+v", second.Cost(), first.Cost())
	}
	if err := second.Run(ctx, core.PromptInput{Text: "what is 1 + 1?"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(store.Transcript().Messages); got <= before {
		t.Errorf("the store holds %d messages after the second turn, want more than %d", got, before)
	}
}

// The agent reads the catalog the host passed. The scripted model is in the
// host's catalog alone, so its window can come from nowhere else; an agent
// given no catalog knows only the built-in list, and gauges nothing.
func TestTheAgentReadsTheHostCatalog(t *testing.T) {
	agent := mustAgent(t, hostCatalog(), &notebook{}, core.NewMemoryTranscriptStore())
	if _, window := agent.ContextUsage(); window != 32000 {
		t.Errorf("window = %d, want the host catalog's 32000", window)
	}
	bare := mustAgent(t, nil, &notebook{}, core.NewMemoryTranscriptStore())
	if _, window := bare.ContextUsage(); window != 0 {
		t.Errorf("an agent given no catalog gauged the scripted model at %d; only the host's catalog holds it", window)
	}
}
