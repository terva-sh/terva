package core

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// advanceFakeClient captures every request it is handed so a test can assert the
// shape of the advance turn (the stage cue on the ephemeral tail).
type advanceFakeClient struct {
	reply string
	reqs  []provider.Request
}

func (c *advanceFakeClient) Name() string { return "advance-fake" }

func (c *advanceFakeClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.reqs = append(c.reqs, req)
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "advance-fake", Model: req.Model}
		out <- provider.EventTextDelta{Delta: c.reply}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: c.reply}},
		}}
	}()
	return out, nil
}

// directedScene is the state Stage leaves a scene in after the user authors lines
// into it: the transcript ends in a RUN of assistant messages, the last of which
// the user wrote, not the model.
func directedScene() []provider.Message {
	return []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "I sign both copies."}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "She takes the pen."}}},
		{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "*Mistress Elira reads the note.*"}},
			Meta:    map[string]string{MetaSource: MetaDirected, MetaActor: "Mistress Elira"},
		},
		{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "*The fitting passed in a blur.*"}},
			Meta:    map[string]string{MetaSource: MetaDirected},
		},
	}
}

// TestAdvanceAlwaysSendsAStageCue is the whole point of the primitive, and it
// guards a SILENT corruption rather than an error.
//
// A scene whose transcript ends in authored (directed) lines ends in assistant
// messages. Dispatching a bare turn there sends a request whose last message is an
// assistant one — which Anthropic reads as a PREFILL and extends mid-sentence,
// appending the result as a new message (the in-place merge only fires under
// continuePrefill). The user gets a bubble that starts mid-thought.
//
// The cue rides the ephemeral tail so the request always ends in a user block. The
// case that matters is exactly this one: NO ContextProvider, so nothing else would
// populate the tail — a plain chat with no lore, note, or user persona.
func TestAdvanceAlwaysSendsAStageCue(t *testing.T) {
	client := &advanceFakeClient{reply: "Kobeni finally looked up."}
	a := NewAgent(client, "fake-model", "system", Registry{})
	// Deliberately NO ContextProvider: without the cue the tail would be empty.
	a.SetMessages(directedScene())

	if err := a.Advance(context.Background(), nil); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if len(client.reqs) == 0 {
		t.Fatal("no request reached the provider")
	}
	eph := client.reqs[0].EphemeralContext
	if eph == "" {
		t.Fatal("advance sent an EMPTY ephemeral tail: the request then ends on a bare assistant " +
			"message, which Anthropic continues as a prefill — the model extends the user's own " +
			"authored line mid-sentence instead of writing the next beat. The cue is what prevents that.")
	}
	if !strings.Contains(eph, "[Advance]") {
		t.Errorf("ephemeral tail carries no advance cue: %q", eph)
	}
}

// TestAdvanceAppendsANewMessage — advance writes the NEXT beat, it does not extend
// the trailing message. That is the difference from turn.continue, and the reason
// both verbs exist.
func TestAdvanceAppendsANewMessage(t *testing.T) {
	client := &advanceFakeClient{reply: "Kobeni finally looked up."}
	a := NewAgent(client, "fake-model", "system", Registry{})
	a.SetMessages(directedScene())
	before := len(a.Messages())

	if err := a.Advance(context.Background(), nil); err != nil {
		t.Fatalf("Advance: %v", err)
	}

	msgs := a.Messages()
	if len(msgs) != before+1 {
		t.Fatalf("message count = %d; want %d (advance appends a new beat, it does not merge in place)", len(msgs), before+1)
	}
	last := msgs[len(msgs)-1]
	if last.Role != provider.RoleAssistant {
		t.Errorf("last message role = %q; want assistant", last.Role)
	}
	// The new beat is the MODEL's, so it must not be tagged as user-authored —
	// otherwise retry would refuse to regenerate it (see directedCount).
	if last.Meta[MetaSource] == MetaDirected {
		t.Error("the generated beat is tagged as user-directed; it is the model's own turn")
	}
}

// TestAdvanceCueIsOneTurn — the cue is scoped to the advance turn. A later plain
// Continue must not inherit it, or every subsequent turn would be nagged to
// "continue the scene".
func TestAdvanceCueIsOneTurn(t *testing.T) {
	client := &advanceFakeClient{reply: "..."}
	a := NewAgent(client, "fake-model", "system", Registry{})
	a.SetMessages(directedScene())

	if err := a.Advance(context.Background(), nil); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if err := a.Continue(context.Background(), nil); err != nil {
		t.Fatalf("Continue: %v", err)
	}
	if len(client.reqs) < 2 {
		t.Fatalf("expected 2 requests, got %d", len(client.reqs))
	}
	if strings.Contains(client.reqs[1].EphemeralContext, "[Advance]") {
		t.Errorf("the advance cue leaked into the following turn: %q", client.reqs[1].EphemeralContext)
	}
}
