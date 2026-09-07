package core

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

// cutShortClient streams some text and then ends the turn. With fail set it ends
// on a PERMANENT provider error, which is what an interrupted turn looks like
// from the agent's side: runLoop does not retry a 400, and oneTurn keeps the text
// that already arrived. Without it the same reply lands cleanly, so one client
// covers both halves of the mark.
type cutShortClient struct {
	text  string
	fail  bool
	calls int32
}

func (c *cutShortClient) Name() string { return "cut-short-fake" }

func (c *cutShortClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	atomic.AddInt32(&c.calls, 1)
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "cut-short-fake", Model: req.Model}
		out <- provider.EventTextDelta{Delta: c.text}
		msg := provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: c.text}},
		}
		if c.fail {
			out <- provider.EventDone{
				Stop:    provider.StopError,
				Err:     provider.NewHTTPError("cut-short-fake", 400, "", "invalid request"),
				Message: msg,
			}
			return
		}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: msg}
	}()
	return out, nil
}

// TestACutShortReplyIsMarkedIncompleteOnDisk is the point of MetaIncomplete. The
// in-memory check is the cheap half; the durable one is the half that matters,
// because a resume reads the session file and not this process's memory. The
// mark is stamped before fireMessageAppended for exactly that reason, so a
// regression that stamped it afterwards would pass the first assertion and fail
// the second.
func TestACutShortReplyIsMarkedIncompleteOnDisk(t *testing.T) {
	client := &cutShortClient{text: "The answer begins", fail: true}
	a := NewAgent(client, "fake-model", "system", Registry{})
	a.RetryBaseDelay = time.Millisecond

	var persisted []provider.Message
	a.AddMessageObserver(func(m provider.Message) {
		persisted = append(persisted, m)
	})

	if err := a.Prompt(context.Background(), "ask", nil, nil); err == nil {
		t.Fatal("the turn died on a 400; Prompt must surface that error")
	}

	msgs := a.Messages()
	if len(msgs) == 0 {
		t.Fatal("transcript is empty; the partial reply should have been kept")
	}
	last := msgs[len(msgs)-1]
	if last.Role != provider.RoleAssistant {
		t.Fatalf("last message role = %q; want the kept partial assistant reply", last.Role)
	}
	if last.Meta[MetaIncomplete] != "true" {
		t.Errorf("the in-memory reply is not marked incomplete; Meta = %v", last.Meta)
	}

	if len(persisted) == 0 {
		t.Fatal("nothing reached the persistence observer")
	}
	got := persisted[len(persisted)-1]
	if got.Role != provider.RoleAssistant {
		t.Fatalf("last persisted role = %q; want the assistant reply", got.Role)
	}
	if got.Meta[MetaIncomplete] != "true" {
		t.Errorf("the PERSISTED reply lost the incomplete mark (Meta = %v); a resumed session would read this as a finished answer", got.Meta)
	}
}

// TestACleanReplyIsNotMarkedIncomplete is the other half. Without it the mark
// could be stamped unconditionally and the test above would still pass, which
// would strand every healthy session behind a resume prompt.
func TestACleanReplyIsNotMarkedIncomplete(t *testing.T) {
	client := &cutShortClient{text: "A whole answer."}
	a := NewAgent(client, "fake-model", "system", Registry{})

	var persisted []provider.Message
	a.AddMessageObserver(func(m provider.Message) {
		persisted = append(persisted, m)
	})

	if err := a.Prompt(context.Background(), "ask", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	for _, m := range a.Messages() {
		if m.Meta[MetaIncomplete] != "" {
			t.Errorf("a clean turn marked a message incomplete; Meta = %v", m.Meta)
		}
	}
	for _, m := range persisted {
		if m.Meta[MetaIncomplete] != "" {
			t.Errorf("a clean turn persisted an incomplete mark; Meta = %v", m.Meta)
		}
	}
}

// TestARetriedTurnThatRecoversIsNotMarkedIncomplete pins the meaning of the mark:
// it says the turn is OVER and the reply is short, not merely that something went
// wrong on the way. partialRetryFakeClient fails the first attempt with a 503
// after streaming "partial", and runLoop drops that partial and retries. Only the
// recovered message is committed, so nothing is marked.
//
// This is the case that made the argument necessary rather than reading the error
// inside oneTurn, which cannot tell a retryable failure from a final one.
func TestARetriedTurnThatRecoversIsNotMarkedIncomplete(t *testing.T) {
	client := &partialRetryFakeClient{}
	a := NewAgent(client, "fake-model", "system", Registry{})
	a.RetryBaseDelay = time.Millisecond

	if err := a.Prompt(context.Background(), "hello", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	for _, m := range a.Messages() {
		if m.Meta[MetaIncomplete] != "" {
			t.Errorf("a turn that recovered on retry was marked incomplete; Meta = %v", m.Meta)
		}
	}
}

// TestIncompleteCrossesToTheWire: a client renders the cut-short state, so the
// mark has to survive the Meta map being dropped at the wire boundary.
func TestIncompleteCrossesToTheWire(t *testing.T) {
	cut := provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: "half a"}},
		Meta:    map[string]string{MetaIncomplete: "true"},
	}
	if w := MessageToWire(cut); !w.Incomplete {
		t.Error("WireMessage.Incomplete is false for a message marked incomplete")
	}
	whole := provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: "all of it"}},
	}
	if w := MessageToWire(whole); w.Incomplete {
		t.Error("WireMessage.Incomplete is true for an ordinary finished reply")
	}
}

// TestIncompleteSurvivesTheWireRoundTrip is the half that was missing when the
// mark was first added, and the half that decides whether any client can act on
// it. Every ctrlproto snapshot rebuilds a transcript through this pair, so a mark
// that travels only outward is one no client ever sees: the cut-short reply
// arrives looking finished and the resume is never offered.
func TestIncompleteSurvivesTheWireRoundTrip(t *testing.T) {
	cut := provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: "half a"}},
		Meta:    map[string]string{MetaIncomplete: "true"},
	}
	back := MessageFromWire(MessageToWire(cut))
	if back.Meta[MetaIncomplete] != "true" {
		t.Errorf("after a wire round trip Meta = %v; the incomplete mark did not come back", back.Meta)
	}
	// And the classifier, which is what every surface actually asks.
	if got := ResumeStateOf([]provider.Message{userMessage("q"), back}); got != ResumeAfterCutShort {
		t.Errorf("ResumeStateOf after the round trip = %s; want after-cut-short", got)
	}

	whole := MessageFromWire(MessageToWire(provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: "all of it"}},
	}))
	if whole.Meta[MetaIncomplete] != "" {
		t.Errorf("a finished reply came back marked incomplete; Meta = %v", whole.Meta)
	}
}
