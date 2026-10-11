package core

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"terva.sh/terva/packages/core/transcriptcodec"
	"terva.sh/terva/packages/provider"
)

// A reply that stops at the output limit keeps its tool calls, and the turn
// ends there without running them. Until this fix nothing answered those calls,
// so the next request filled the gap with the generic interrupt stub. The model
// read "aborted" where the truth was "the output limit cut this call off", and
// it sent the same oversized call again, into the same limit.

// cutOffProbeTool counts its runs. A cut-off call must never reach it.
type cutOffProbeTool struct{ runs atomic.Int32 }

func (t *cutOffProbeTool) Name() string            { return "write" }
func (t *cutOffProbeTool) Description() string     { return "writes a file" }
func (t *cutOffProbeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *cutOffProbeTool) Execute(context.Context, json.RawMessage, func(string)) (ToolResult, error) {
	t.runs.Add(1)
	return ToolResult{Content: []provider.Content{provider.TextBlock{Text: "wrote"}}}, nil
}

// cutOffReply is a reply with two calls that stopped at the output limit: the
// first call arrived whole, and the limit cut the second partway through its
// content, so the provider kept the text it had as RawArguments.
func cutOffReply() []provider.Event {
	msg := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{
		provider.TextBlock{Text: "Writing both files."},
		provider.ToolCallBlock{ID: "whole", Name: "write", Arguments: json.RawMessage(`{"path":"a.txt","content":"x"}`)},
		provider.ToolCallBlock{ID: "cut", Name: "write", Arguments: json.RawMessage(`{}`),
			RawArguments: `{"path":"b.txt","content":"a very long body that the limit cut off mid-`},
	}}
	return []provider.Event{
		provider.EventStart{Provider: "scripted"},
		provider.EventToolStart{ID: "whole", Name: "write"},
		provider.EventToolEnd{ID: "whole"},
		provider.EventToolStart{ID: "cut", Name: "write"},
		provider.EventDone{Stop: provider.StopLength, Message: msg},
	}
}

// resultTexts maps each call id in a tool-role message to its result text and
// error flag.
func resultTexts(t *testing.T, m provider.Message) map[string]provider.ToolResultBlock {
	t.Helper()
	if m.Role != provider.RoleTool {
		t.Fatalf("want a tool-role message, got role %q", m.Role)
	}
	out := map[string]provider.ToolResultBlock{}
	for _, c := range m.Content {
		if tr, ok := c.(provider.ToolResultBlock); ok {
			out[tr.CallID] = tr
		}
	}
	return out
}

func blockText(tr provider.ToolResultBlock) string {
	return resultText(ToolResult{Content: tr.Content})
}

// The whole path: the loop observes StopLength, answers each call without
// running it, and ends the turn. The transcript, the store, the live events and
// the next request then carry the same answers.
func TestACutOffReplyAnswersItsToolCallsWithTheTruth(t *testing.T) {
	tool := &cutOffProbeTool{}
	client := &scriptedClient{name: "scripted", script: func(n int, _ provider.Request) ([]provider.Event, error) {
		if n == 0 {
			return cutOffReply(), nil
		}
		return saidText("ok", 10), nil
	}}
	ag := newTestAgent(client, "m", "", Registry{"write": tool})
	store := NewMemoryTranscriptStore()
	ag.AttachTranscriptStore(store)

	var events []AgentEvent
	if err := ag.Prompt(context.Background(), "write two files", nil, func(ev AgentEvent) {
		events = append(events, ev)
	}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	if n := tool.runs.Load(); n != 0 {
		t.Fatalf("the tool ran %d time(s) for a reply the output limit cut off", n)
	}
	// The turn still ends at the stop. Running on by itself is a separate
	// behaviour change, and this fix must not smuggle it in.
	if n := len(client.calls()); n != 1 {
		t.Fatalf("the cut-off turn sent %d requests, want 1: it must end at the stop", n)
	}

	msgs := ag.Messages()
	got := resultTexts(t, msgs[len(msgs)-1])
	cut, whole := got["cut"], got["whole"]
	if len(got) != 2 {
		t.Fatalf("got results for %d calls, want 2: %v", len(got), got)
	}
	if !cut.IsError || !whole.IsError {
		t.Errorf("a call that did not run came back as a success (cut %v, whole %v)", cut.IsError, whole.IsError)
	}
	for _, want := range []string{"output limit", "arrived incomplete", "did not run", "Do not send the same call again", "smaller parts"} {
		if !strings.Contains(blockText(cut), want) {
			t.Errorf("the cut call's result lacks %q:\n%s", want, blockText(cut))
		}
	}
	// The first call arrived whole. Telling the model its arguments are
	// incomplete would be false, and it would split a call that needs no change.
	if text := blockText(whole); strings.Contains(text, "incomplete") || !strings.Contains(text, "output limit") || !strings.Contains(text, "Send it again") {
		t.Errorf("the whole call's result does not say the reply hit the limit and the call can go again:\n%s", text)
	}
	for _, tr := range got {
		if strings.Contains(blockText(tr), "aborted") {
			t.Errorf("a cut-off call still reads as aborted:\n%s", blockText(tr))
		}
	}

	// The store holds the same tool message as the live transcript, so a resumed
	// session reads these answers, not the repair stub.
	stored := store.Transcript().Messages
	if len(stored) != len(msgs) {
		t.Fatalf("the store holds %d messages and the transcript %d", len(stored), len(msgs))
	}
	for id, tr := range resultTexts(t, stored[len(stored)-1]) {
		if blockText(tr) != blockText(got[id]) {
			t.Errorf("stored result for %s = %q, live = %q", id, blockText(tr), blockText(got[id]))
		}
	}

	// A front end sees each call resolve as failed, after the call itself and
	// before the turn is done.
	seenCall, seenResult := map[string]int{}, map[string]int{}
	done := -1
	for i, ev := range events {
		switch e := ev.(type) {
		case EvToolCall:
			seenCall[e.ID] = i
		case EvToolResult:
			if !e.Result.IsError {
				t.Errorf("EvToolResult for %s is not an error", e.ID)
			}
			seenResult[e.ID] = i
		case EvDone:
			done = i
		}
	}
	for _, id := range []string{"whole", "cut"} {
		c, okC := seenCall[id]
		r, okR := seenResult[id]
		if !okC || !okR || c > r || r > done {
			t.Errorf("call %s: EvToolCall at %d (%v), EvToolResult at %d (%v), EvDone at %d", id, c, okC, r, okR, done)
		}
	}

	// The next request carries the answers as they are, with no stub in place.
	if err := ag.Prompt(context.Background(), "go on", nil, nil); err != nil {
		t.Fatalf("second Prompt: %v", err)
	}
	next := client.calls()[1]
	var sent []provider.Message
	for _, m := range next.Messages {
		if m.Role == provider.RoleTool {
			sent = append(sent, m)
		}
	}
	if len(sent) != 1 {
		t.Fatalf("the next request carries %d tool messages, want 1", len(sent))
	}
	for id, tr := range resultTexts(t, sent[0]) {
		if text := blockText(tr); text == transcriptcodec.DefaultInterruptStub.Text || text != blockText(got[id]) {
			t.Errorf("the next request answers %s with %q, want the recorded result", id, text)
		}
	}
}

// A cut-off reply that ended on text cut no call: every call in it arrived
// whole. None of them is told its arguments are incomplete.
func TestACutOffReplyThatEndedOnTextCutNoCall(t *testing.T) {
	ag := newTestAgent(nil, "m", "", Registry{})
	msg := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{
		provider.ToolCallBlock{ID: "a", Name: "write", Arguments: json.RawMessage(`{"path":"a.txt"}`)},
		provider.TextBlock{Text: "And now I will explain at gre"},
		provider.ReasoningBlock{ID: "r"},
	}}
	ag.answerCutOffCalls(msg, func(AgentEvent) {})

	msgs := ag.Messages()
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want the one tool message", len(msgs))
	}
	tr := resultTexts(t, msgs[0])["a"]
	if text := blockText(tr); strings.Contains(text, "incomplete") || !strings.Contains(text, "arrived complete") {
		t.Errorf("a whole call before trailing text reads as cut:\n%s", text)
	}
}

// A reply whose last call parsed, then stopped at the limit: Gemini sends each
// call whole, and a streamed call can end exactly at the close of its object.
// Nothing was cut, so the call must not be told its arguments are incomplete.
// Position alone said it was cut, which is the false claim this pins.
func TestAWholeLastCallInACutOffReplyIsNotCalledIncomplete(t *testing.T) {
	tool := &cutOffProbeTool{}
	client := &scriptedClient{name: "scripted", script: func(int, provider.Request) ([]provider.Event, error) {
		msg := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.TextBlock{Text: "Writing the file."},
			provider.ToolCallBlock{ID: "g1", Name: "write", Arguments: json.RawMessage(`{"path":"a.txt","content":"x"}`)},
		}}
		return []provider.Event{provider.EventDone{Stop: provider.StopLength, Message: msg}}, nil
	}}
	ag := newTestAgent(client, "m", "", Registry{"write": tool})

	if err := ag.Prompt(context.Background(), "write it", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	if n := tool.runs.Load(); n != 0 {
		t.Fatalf("the tool ran %d time(s) for a reply that stopped at the limit", n)
	}
	msgs := ag.Messages()
	tr, ok := resultTexts(t, msgs[len(msgs)-1])["g1"]
	if !ok || !tr.IsError {
		t.Fatalf("the call has no error result: %+v", msgs[len(msgs)-1])
	}
	text := blockText(tr)
	if strings.Contains(text, "incomplete") || strings.Contains(text, "smaller parts") {
		t.Errorf("a call whose arguments parsed is told they are incomplete:\n%s", text)
	}
	if !strings.Contains(text, "output limit") || !strings.Contains(text, "arrived complete") || !strings.Contains(text, "Send it again") {
		t.Errorf("the whole call's result does not say the reply hit the limit and the call can go again:\n%s", text)
	}
}

// The last call of a cut-off reply carries unparsed arguments for two
// reasons, and they need opposite answers. A buffer that ends early is the
// cut, so the model splits the content. A whole buffer that is malformed is an
// encoding defect, so the model fixes the encoding. Before this, any unparsed
// last call read as cut, so the model split a call it had sent whole.
func TestTheLastCallIsCutOnlyWhenItsArgumentsEndEarly(t *testing.T) {
	const (
		cutText      = "Split large content into smaller parts"
		encodingText = "did not arrive as valid JSON"
	)
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"cut mid-object", `{"path":"b.txt","content":"a very long body`, cutText},
		{"cut after a key", `{"path":"b.txt","content":`, cutText},
		// A coding model types raw tabs into source, and the decoder would
		// report the tab before it reached the cut.
		{"cut in a string that holds a raw tab", "{\"path\":\"b.go\",\"content\":\"func f() {\n\treturn", cutText},
		{"whole, with an invalid escape", `{"path":"a.txt","content":"\q"}`, encodingText},
		{"whole, with a control byte between tokens", "{\"path\":\"a.txt\",\x01\"content\":\"x\"}", encodingText},
		{"whole, with a stray closing brace", `{"path":"a.txt"}}`, encodingText},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ag := newTestAgent(nil, "m", "", Registry{})
			msg := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{
				provider.TextBlock{Text: "Writing the file."},
				provider.ToolCallBlock{ID: "last", Name: "write", Arguments: json.RawMessage(`{}`), RawArguments: c.raw},
			}}
			ag.answerCutOffCalls(msg, func(AgentEvent) {})

			msgs := ag.Messages()
			text := blockText(resultTexts(t, msgs[len(msgs)-1])["last"])
			if !strings.Contains(text, c.want) {
				t.Errorf("the last call lacks %q:\n%s", c.want, text)
			}
			other := map[string]string{cutText: encodingText, encodingText: cutText}[c.want]
			if strings.Contains(text, other) {
				t.Errorf("the last call also carries %q:\n%s", other, text)
			}
		})
	}
}

// The index helper finds the cut call through trailing reasoning. It finds
// none when text ends the reply, none when the last call parsed, and none when
// the last call is whole but malformed.
func TestCutOffCallIndex(t *testing.T) {
	cut := provider.ToolCallBlock{ID: "c", Name: "write", Arguments: json.RawMessage(`{}`), RawArguments: `{"path":"a`}
	whole := provider.ToolCallBlock{ID: "w", Name: "write", Arguments: json.RawMessage(`{"path":"a"}`)}
	malformed := provider.ToolCallBlock{ID: "m", Name: "write", Arguments: json.RawMessage(`{}`), RawArguments: `{"path":"\q"}`}
	cases := []struct {
		name    string
		content []provider.Content
		want    int
	}{
		{"last block is a cut call", []provider.Content{provider.TextBlock{Text: "x"}, cut}, 1},
		{"reasoning after the cut call", []provider.Content{cut, provider.ReasoningBlock{ID: "r"}}, 0},
		{"last call parsed", []provider.Content{cut, whole}, -1},
		{"last call parsed, reasoning after it", []provider.Content{whole, provider.ReasoningBlock{ID: "r"}}, -1},
		{"last call whole but malformed", []provider.Content{whole, malformed}, -1},
		{"text ends the reply", []provider.Content{cut, provider.TextBlock{Text: "x"}}, -1},
		{"empty", nil, -1},
	}
	for _, c := range cases {
		if got := cutOffCallIndex(provider.Message{Content: c.content}); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

// A content filter ends a reply as StopEnd on both OpenAI wires, and a call in
// that reply stays in the message without running. Nothing answers it in the
// turn, which only StopLength does. The next request must still pair it with a
// result, because a provider rejects a call that has none.
func TestACallInAFilteredReplyIsPairedOnTheNextRequest(t *testing.T) {
	tool := &cutOffProbeTool{}
	client := &scriptedClient{name: "scripted", script: func(n int, _ provider.Request) ([]provider.Event, error) {
		if n > 0 {
			return saidText("ok", 10), nil
		}
		msg := provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.ToolCallBlock{ID: "filtered", Name: "write", Arguments: json.RawMessage(`{}`),
				RawArguments: `{"path":"b.txt","content":"the filter stopped this mid-`},
		}}
		return []provider.Event{
			provider.EventStart{Provider: "scripted"},
			provider.EventToolStart{ID: "filtered", Name: "write"},
			provider.EventDone{Stop: provider.StopEnd, Message: msg},
		}, nil
	}}
	ag := newTestAgent(client, "m", "", Registry{"write": tool})

	if err := ag.Prompt(context.Background(), "write it", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if n := tool.runs.Load(); n != 0 {
		t.Fatalf("the tool ran %d time(s) for a reply that ended StopEnd", n)
	}
	if n := len(client.calls()); n != 1 {
		t.Fatalf("the filtered turn sent %d requests, want 1", n)
	}

	if err := ag.Prompt(context.Background(), "go on", nil, nil); err != nil {
		t.Fatalf("second Prompt: %v", err)
	}
	next := client.calls()[1].Messages
	paired := false
	for i, m := range next {
		if m.Role != provider.RoleAssistant || len(toolCallIDs(m)) == 0 {
			continue
		}
		if i+1 >= len(next) {
			t.Fatalf("the call is the last message of the next request, with no result after it")
		}
		tr, ok := resultTexts(t, next[i+1])["filtered"]
		if !ok {
			t.Fatalf("the message after the call holds no result for it: %+v", next[i+1])
		}
		if !tr.IsError || blockText(tr) != transcriptcodec.DefaultInterruptStub.Text {
			t.Errorf("the call is answered with %q (error %v), want the repair stub", blockText(tr), tr.IsError)
		}
		paired = true
	}
	if !paired {
		t.Fatal("the next request does not carry the filtered call at all")
	}
}

func toolCallIDs(m provider.Message) []string {
	var ids []string
	for _, c := range m.Content {
		if tc, ok := c.(provider.ToolCallBlock); ok {
			ids = append(ids, tc.ID)
		}
	}
	return ids
}
