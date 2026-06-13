package e2e

import (
	"context"
	"encoding/json"
	"testing"

	"terva.sh/terva/packages/agent/sdk"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// TestSDKSmoke is the first test of the embedding SDK: construct a
// Runtime against the fake provider (in-process, no subprocess),
// run a prompt, and check the event stream, transcript, state, and
// close semantics that examples/sdk demonstrates.
func TestSDKSmoke(t *testing.T) {
	fp := newFakeProvider(t, sseScript{
		chunks:   []map[string]any{textChunk("hello "), textChunk("sdk"), finishChunk("stop", 8, 2)},
		sendDone: true,
	})
	ws := t.TempDir()
	// Isolate from the developer's real config/credentials: the SDK
	// resolves through TervaHome() like every other mode.
	t.Setenv("TERVA_HOME", t.TempDir())

	rt, err := sdk.New(sdk.Config{
		Provider: "openai-compatible",
		Model:    "test-model",
		APIKey:   "e2e-test-key",
		BaseURL:  fp.url(),
		CWD:      ws,
	})
	if err != nil {
		t.Fatalf("sdk.New: %v", err)
	}
	defer rt.Close()

	if rt.Provider() != "openai-compatible" || rt.Model() != "test-model" {
		t.Fatalf("runtime identity: provider=%q model=%q", rt.Provider(), rt.Model())
	}

	events, err := rt.Prompt(context.Background(), "say hi", nil)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	var (
		text       string
		sawTurnEnd bool
	)
	for ev := range events {
		switch ev.Type {
		case "text_delta":
			text += ev.Delta
		case "turn_end":
			sawTurnEnd = true
			if ev.Stop != "end" {
				t.Errorf("turn_end stop = %q, want end", ev.Stop)
			}
		case "error":
			t.Errorf("unexpected error event: %s", ev.Error)
		}
	}
	if !sawTurnEnd {
		t.Errorf("no turn_end event")
	}
	if text != "hello sdk" {
		t.Errorf("streamed text = %q, want %q", text, "hello sdk")
	}

	msgs := rt.Messages()
	if len(msgs) != 2 {
		t.Fatalf("Messages() = %d entries, want 2 (user + assistant)", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Errorf("roles = %q,%q; want user,assistant", msgs[0].Role, msgs[1].Role)
	}

	st := rt.State()
	if st.Busy {
		t.Errorf("State() reports busy after prompt completed")
	}
	if st.MessageCount != 2 {
		t.Errorf("State().MessageCount = %d, want 2", st.MessageCount)
	}

	// Usage from the fake's final chunk must reach Cost().
	if c := rt.Cost(); c.Input <= 0 || c.Output <= 0 {
		t.Errorf("Cost() lost usage: %+v", c)
	}

	if err := rt.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if _, err := rt.Prompt(context.Background(), "again", nil); err == nil {
		t.Errorf("Prompt after Close should fail")
	}
}

// recordingTool is an embedder-supplied core.Tool injected via
// sdk.Config.ExtraTools. It records that it ran and returns a fixed
// string, proving the dispatch reaches an in-process tool.
type recordingTool struct{ ran *bool }

func (recordingTool) Name() string        { return "echo_extra" }
func (recordingTool) Description() string { return "echo a fixed marker" }
func (recordingTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t recordingTool) Execute(_ context.Context, _ json.RawMessage, _ func(string)) (core.ToolResult, error) {
	*t.ran = true
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: "EXTRA-OK"}}}, nil
}

// An ExtraTool is registered, advertised to the model, callable, and
// its result flows back into the turn.
func TestSDKExtraTools(t *testing.T) {
	// Request 1: the model calls echo_extra. Request 2 (after the
	// tool result is fed back): it finishes with text.
	fp := newFakeProvider(t,
		sseScript{
			chunks:   []map[string]any{toolCallChunk("call-1", "echo_extra", `{}`), finishChunk("tool_calls", 6, 2)},
			sendDone: true,
		},
		sseScript{
			chunks:   []map[string]any{textChunk("done"), finishChunk("stop", 4, 1)},
			sendDone: true,
		},
	)
	t.Setenv("TERVA_HOME", t.TempDir())

	ran := false
	rt, err := sdk.New(sdk.Config{
		Provider:   "openai-compatible",
		Model:      "test-model",
		APIKey:     "e2e-test-key",
		BaseURL:    fp.url(),
		CWD:        t.TempDir(),
		ExtraTools: []core.Tool{recordingTool{ran: &ran}},
	})
	if err != nil {
		t.Fatalf("sdk.New: %v", err)
	}
	defer rt.Close()

	events, err := rt.Prompt(context.Background(), "use the tool", nil)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	var sawResult bool
	for ev := range events {
		if ev.Type == "tool_result" && ev.ID == "call-1" {
			sawResult = true
		}
	}
	if !ran {
		t.Error("ExtraTool was never executed; dispatch didn't reach it")
	}
	if !sawResult {
		t.Error("no tool_result event for the ExtraTool call")
	}
}
