package core

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// agentCaptureTool records the agent the dispatch context carried, so
// tests can prove each dispatch identifies its CALLING agent even when
// several agents share one registry (the bot-mode per-chat shape).
type agentCaptureTool struct{ got *Agent }

func (c *agentCaptureTool) Name() string            { return "cap" }
func (c *agentCaptureTool) Description() string     { return "captures the ctx agent" }
func (c *agentCaptureTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }

func (c *agentCaptureTool) Execute(ctx context.Context, _ json.RawMessage, _ func(string)) (ToolResult, error) {
	c.got = AgentFromContext(ctx)
	return ToolResult{Content: []provider.Content{provider.TextBlock{Text: "ok"}}}, nil
}

// TestAgentFromContextOutsideDispatch: a context that didn't come from
// an agent dispatch yields nil, never a stale agent.
func TestAgentFromContextOutsideDispatch(t *testing.T) {
	if got := AgentFromContext(context.Background()); got != nil {
		t.Fatalf("expected nil agent from a bare context, got %v", got)
	}
}

// TestDispatchCarriesCallingAgent: two agents sharing ONE registry (bot
// mode mints an agent per chat over the same Resolved registry) — each
// dispatch must carry its own agent, not whichever was built last.
func TestDispatchCarriesCallingAgent(t *testing.T) {
	cap := &agentCaptureTool{}
	reg := Registry{"cap": cap}
	a1 := NewAgent(nil, "m1", "", reg)
	a2 := NewAgent(nil, "m2", "", reg)

	tc := provider.ToolCallBlock{ID: "T1", Name: "cap", Arguments: json.RawMessage(`{}`)}
	sink := func(AgentEvent) {}

	a1.runOneTool(context.Background(), tc, a1.Tools, sink)
	if cap.got != a1 {
		t.Fatalf("dispatch from a1 carried %p, want %p", cap.got, a1)
	}
	a2.runOneTool(context.Background(), tc, a2.Tools, sink)
	if cap.got != a2 {
		t.Fatalf("dispatch from a2 carried %p, want %p", cap.got, a2)
	}
	// And ContextWithAgent round-trips for direct callers.
	if got := AgentFromContext(ContextWithAgent(context.Background(), a1)); got != a1 {
		t.Fatalf("ContextWithAgent round-trip returned %p, want %p", got, a1)
	}
}

// TestTranscriptEpochsNeverCollideAcrossAgents: epochs are salted per
// agent (high 32 bits), so tools that key per-transcript state on the
// epoch (read-dedup) can never confuse one agent's transcript state
// with another's — even though every agent's bump counter starts at 0.
func TestTranscriptEpochsNeverCollideAcrossAgents(t *testing.T) {
	a1 := NewAgent(nil, "m", "", nil)
	a2 := NewAgent(nil, "m", "", nil)
	if a1.TranscriptEpoch() == a2.TranscriptEpoch() {
		t.Fatalf("two fresh agents share transcript epoch %d", a1.TranscriptEpoch())
	}
	base := a1.TranscriptEpoch() >> 32
	// A wholesale transcript replacement bumps the epoch but stays
	// inside the agent's own salt band.
	a1.SetMessages(nil)
	if a1.TranscriptEpoch()>>32 != base {
		t.Fatalf("epoch bump left the agent's salt band: %d -> %d", base, a1.TranscriptEpoch()>>32)
	}
	if a1.TranscriptEpoch() == a2.TranscriptEpoch() {
		t.Fatal("bumped epoch collided with another agent's")
	}
}

// TestAdoptSessionIdentity: the agent records the transcript file's
// basename (what --resume accepts) and path; nil clears both.
func TestAdoptSessionIdentity(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSession(dir, dir, "prov", "model", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	a := NewAgent(nil, "m", "", nil)
	if id, path := a.SessionIdentity(); id != "" || path != "" {
		t.Fatalf("fresh agent should have no session identity, got %q %q", id, path)
	}

	a.AdoptSessionIdentity(s)
	id, path := a.SessionIdentity()
	if path != s.Path {
		t.Fatalf("session path = %q, want %q", path, s.Path)
	}
	want := strings.TrimSuffix(filepath.Base(s.Path), ".jsonl")
	if id != want || id == "" {
		t.Fatalf("session id = %q, want %q", id, want)
	}

	a.AdoptSessionIdentity(nil)
	if id, path := a.SessionIdentity(); id != "" || path != "" {
		t.Fatalf("nil adoption should clear identity, got %q %q", id, path)
	}
}
