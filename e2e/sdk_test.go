package e2e

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/sdk"
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
