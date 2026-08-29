package core

import (
	"context"
	"testing"

	"terva.sh/terva/packages/provider"
)

// Not every provider waits to be asked.
//
// The display/record split was built around Codex, where a summary exists only
// because terva requested one — so "recording off and display off" means there
// is nothing on hand to strip. Two providers break that premise: chat backends
// that emit `reasoning_content` (DeepSeek, Kimi, and kin — provider/openai.go)
// and Gemini's thought summaries (provider/gemini.go) both arrive whether or
// not anyone asked for them, exactly as Anthropic's thinking does.
//
// Anthropic is caught by dropUnrecordableThinking, which keys on its shape.
// These two were caught by nothing, so with BOTH switches off their readable
// text reached the session file — the setting saying one thing and the file
// saying another.

// unbiddenReasoningClient answers with a readable summary nobody requested.
type unbiddenReasoningClient struct {
	shape     string
	encrypted string
	lastReq   provider.Request
	// replyIsReasoningOnly finishes the turn with reasoning and nothing else,
	// the shape a chat backend produces when its thinking channel never closes.
	replyIsReasoningOnly bool
}

func (c *unbiddenReasoningClient) Name() string { return "unbidden-fake" }

func (c *unbiddenReasoningClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.lastReq = req
	out := make(chan provider.Event, 8)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: c.Name(), Model: req.Model}
		out <- provider.EventReasoningDelta{Delta: "the btree is narrower"}
		content := []provider.Content{}
		if !c.replyIsReasoningOnly {
			out <- provider.EventTextDelta{Delta: "done"}
			content = append(content, provider.TextBlock{Text: "done"})
		}
		content = append(content, provider.ReasoningBlock{
			Summary:   "the btree is narrower",
			Encrypted: c.encrypted,
			Shape:     c.shape,
		})
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: content,
		}}
	}()
	return out, nil
}

func runUnbidden(t *testing.T, c *unbiddenReasoningClient, record string) provider.ReasoningBlock {
	t.Helper()
	a := NewAgent(c, "fake-model", "system", Registry{})
	if record != "" {
		a.SetReasoningSummary(record)
	}
	if err := a.Prompt(context.Background(), "go", nil, func(AgentEvent) {}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	return reasoningBlockOf(t, a)
}

// The leak, on a chat backend. Nothing asked for this text and nothing was
// going to display it, so nothing should have written it down.
func TestUnrequestedChatReasoningIsNotRecordedWithBothSwitchesOff(t *testing.T) {
	c := &unbiddenReasoningClient{shape: provider.ReasoningShapeOpenAIChat}
	rb := runUnbidden(t, c, "")

	if got := c.lastReq.ReasoningSummary; got != "" {
		t.Fatalf("precondition: the request asked for a summary (%q) — this test is about UNASKED text", got)
	}
	if rb.Summary != "" {
		t.Errorf("readable reasoning persisted with Record thinking off: %q", rb.Summary)
	}
}

// The same leak on Gemini, where blanking must be surgical: the thought
// SIGNATURE is the replay half and lives in the same block.
//
// 🪤 gemini.go:423 replays only blocks carrying Encrypted, so losing the
// signature here would silently cost thought continuity on every following
// turn — a privacy fix paying for itself in fidelity is not the trade.
func TestUnrequestedGeminiSummaryIsBlankedButKeepsItsSignature(t *testing.T) {
	c := &unbiddenReasoningClient{
		shape:     provider.ReasoningShapeGeminiThoughtSummary,
		encrypted: "THOUGHT-SIG",
	}
	rb := runUnbidden(t, c, "")

	if rb.Summary != "" {
		t.Errorf("readable reasoning persisted with Record thinking off: %q", rb.Summary)
	}
	if rb.Encrypted != "THOUGHT-SIG" {
		t.Errorf("blanking the summary cost the thought signature: %+v", rb)
	}
	if rb.Shape != provider.ReasoningShapeGeminiThoughtSummary {
		t.Errorf("shape must survive, or replay cannot tell the wire apart: %+v", rb)
	}
}

// The exemption, and the reason a blanket strip is wrong.
//
// A chat backend whose thinking channel never closes puts the whole REPLY in
// reasoning_content and leaves content empty. openai.go promotes that summary
// back to visible text on replay; with it blanked there is nothing to promote,
// and the serializer drops a message carrying neither text nor tool calls. The
// turn then vanishes from the conversation — "a hole in the history exactly
// where the answer had been", which is the regression that comment records.
//
// 🪤 So the rule is not "blank every chat summary". It is "blank it where the
// message has substance of its own". Here it has none, so the summary IS the
// answer and it stays, recording off or not.
func TestChatReasoningThatIsTheWholeReplySurvivesRecordingOff(t *testing.T) {
	c := &unbiddenReasoningClient{shape: provider.ReasoningShapeOpenAIChat}
	a := NewAgent(c, "fake-model", "system", Registry{})
	// A turn whose ONLY substance is reasoning: no text, no tool calls.
	c.replyIsReasoningOnly = true
	if err := a.Prompt(context.Background(), "go", nil, func(AgentEvent) {}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if rb := reasoningBlockOf(t, a); rb.Summary != "the btree is narrower" {
		t.Errorf("blanking deleted the turn's only content: %q", rb.Summary)
	}
}

// The counterweight. An over-broad "always blank it" would satisfy both tests
// above while deleting the very thing Record thinking is turned on to keep.
func TestUnrequestedReasoningIsKeptWhenRecordingIsOn(t *testing.T) {
	for _, shape := range []string{
		provider.ReasoningShapeOpenAIChat,
		provider.ReasoningShapeGeminiThoughtSummary,
	} {
		t.Run(shape, func(t *testing.T) {
			c := &unbiddenReasoningClient{shape: shape, encrypted: "SIG"}
			if rb := runUnbidden(t, c, "detailed"); rb.Summary != "the btree is narrower" {
				t.Errorf("summary lost with recording on: %q", rb.Summary)
			}
		})
	}
}
