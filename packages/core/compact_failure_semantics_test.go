package core

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

// The pre-turn threshold compaction is a PRECAUTION, and a precaution that
// fails must not take the user's message down with it.
//
// It was the only compaction site that could: it runs before PromptExtra
// appends the message, so aborting left the text nowhere — not in the
// transcript, not in the queue. Every other site fails after the append and the
// transcript keeps it.

// compactFailsClient answers the cold summarization request (the flattened
// "<conversation>" block) with a transient overload and every ordinary turn
// request with plain text.
type compactFailsClient struct {
	mu           sync.Mutex
	turnReqs     int
	compactCalls int
}

func (c *compactFailsClient) Name() string { return "compact-fails" }

func (c *compactFailsClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	isCompact := len(req.Messages) == 1 && len(req.Messages[0].Content) == 1 &&
		func() bool {
			tb, ok := req.Messages[0].Content[0].(provider.TextBlock)
			return ok && strings.Contains(tb.Text, "<conversation>")
		}()
	c.mu.Lock()
	if isCompact {
		c.compactCalls++
	} else {
		c.turnReqs++
	}
	c.mu.Unlock()

	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "compact-fails", Model: req.Model}
		if isCompact {
			out <- provider.EventDone{Stop: provider.StopError, Err: provider.NewAPIError(
				"openai-codex", "Our servers are currently overloaded. Please try again later.", true)}
			return
		}
		out <- provider.EventUsage{Usage: provider.Usage{InputTokens: 8_000}}
		out <- provider.EventTextDelta{Delta: "done"}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "done"}},
		}}
	}()
	return out, nil
}

func (c *compactFailsClient) counts() (turns, compacts int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.turnReqs, c.compactCalls
}

// primed builds an agent whose gauge already reads over the threshold, so the
// pre-turn compaction fires on the next PromptWithPolicy.
func primedOverThreshold(client provider.Client) *Agent {
	a := NewAgent(client, "claude-sonnet-4-5", "system", Registry{})
	a.RetryBaseDelay = time.Millisecond
	seedSmallTranscript(a, 8)
	a.SeedLastTurnUsage(provider.Usage{InputTokens: 190_000})
	return a
}

func TestFailedPreTurnCompactionStillSendsTheMessage(t *testing.T) {
	client := &compactFailsClient{}
	a := primedOverThreshold(client)

	var compactErr string
	err := a.PromptWithPolicy(context.Background(), "the message that must survive", nil, func(ev AgentEvent) {
		if e, ok := ev.(EvCompactEnd); ok && e.Err != "" {
			compactErr = e.Err
		}
	})
	if err != nil {
		t.Fatalf("PromptWithPolicy returned %v; a failed precautionary compaction must not fail the turn", err)
	}

	turns, compacts := client.counts()
	if compacts == 0 {
		t.Fatal("the pre-turn compaction never ran; the fixture is not exercising the path under test")
	}
	if turns == 0 {
		t.Fatal("the turn was never dispatched — the message was dropped, which is the whole defect")
	}

	// The failure is reported, just not fatally. Silence would be its own bug:
	// the user paid for a compaction that did not happen.
	if compactErr == "" {
		t.Error("no EvCompactEnd carried the error; a non-fatal failure still has to be visible")
	}

	// And the message is really in the transcript, not merely "not an error".
	var found bool
	for _, m := range a.Messages() {
		if m.Role != provider.RoleUser {
			continue
		}
		for _, c := range m.Content {
			if tb, ok := c.(provider.TextBlock); ok && strings.Contains(tb.Text, "the message that must survive") {
				found = true
			}
		}
	}
	if !found {
		t.Error("the user's message is not in the transcript after a failed pre-turn compaction")
	}
}

// A cancellation is not a provider failure. Esc during the compaction means
// stop, so the turn the user just interrupted must not then be dispatched.
func TestCancelDuringPreTurnCompactionDoesNotDispatch(t *testing.T) {
	client := &compactFailsClient{}
	a := primedOverThreshold(client)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := a.PromptWithPolicy(ctx, "should not be sent", nil, nil)
	if err == nil {
		t.Fatal("a cancelled context must surface, not be swallowed as a non-fatal compaction failure")
	}
	if turns, _ := client.counts(); turns != 0 {
		t.Errorf("the turn was dispatched %d time(s) after cancellation", turns)
	}
}

// The wire-text path. A host that rebuilds a turn error from the wire — the TUI
// carrier does exactly errors.New(ev.Error) — has no *provider.ProviderError
// left, so classification falls to prose. "Overloaded" is how both Anthropic
// and codex phrase capacity pressure and it matched no needle, which meant the
// single most common recoverable provider failure was shown as a dead end
// instead of opening the model-switch rescue.
func TestOverloadedIsRecoverableFromWireTextAlone(t *testing.T) {
	for _, msg := range []string{
		"openai-codex: Our servers are currently overloaded. Please try again later.",
		"anthropic: overloaded_error: Overloaded",
	} {
		ok, reason := ClassifyRecoverable(errStr(msg))
		if !ok {
			t.Errorf("ClassifyRecoverable(%q) = false; want recoverable — the type is gone over the wire, prose is all that is left", msg)
		}
		if !strings.Contains(reason, "provider unavailable") {
			t.Errorf("reason for %q = %q; want it classed as provider unavailable", msg, reason)
		}
	}

	// The needle must not swallow errors that switching models cannot fix.
	for _, msg := range []string{
		"openai: 400 invalid_request_error: bad tool schema",
		"anthropic: prompt is too long: 208500 tokens",
	} {
		if ok, _ := ClassifyRecoverable(errStr(msg)); ok {
			t.Errorf("ClassifyRecoverable(%q) = true; a validation/oversize error is not fixed by a model switch", msg)
		}
	}
}

type errStr string

func (e errStr) Error() string { return string(e) }
