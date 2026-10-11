package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// Core runs tools only on StopToolUse. Several chat-completions servers end a
// tool-calling reply with finish_reason "stop", or with none at all: Gemini's
// OpenAI-compatible endpoint when it streams, LiteLLM in front of Ollama,
// OpenVINO Model Server, Open WebUI, and OpenAI when tool_choice names a
// function. The reader mapped that to StopEnd, so the turn ended and the calls
// never ran. These tests hold the promotion and its limits.

// streamChatEvents serves frames, one SSE event each, and collects every event
// the chat reader emits.
func streamChatEvents(t *testing.T, frames ...string) []Event {
	t.Helper()
	var body strings.Builder
	for _, f := range frames {
		fmt.Fprintf(&body, "data: %s\n\n", f)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body.String()))
	}))
	defer srv.Close()

	c := newOpenAICompat("openai-compatible", "k", srv.URL, "")
	evs, err := c.Stream(context.Background(), Request{Model: "qwen3.8-27b"})
	if err != nil {
		t.Fatal(err)
	}
	var got []Event
	for ev := range evs {
		got = append(got, ev)
	}
	return got
}

// toolTrace renders the tool events and the terminal event in order. The order
// is what proves a downstream consumer sees the same sequence whichever finish
// reason the server sent.
func toolTrace(evs []Event) []string {
	var trace []string
	for _, ev := range evs {
		switch e := ev.(type) {
		case EventToolStart:
			trace = append(trace, "start "+e.ID+" "+e.Name)
		case EventToolArgs:
			trace = append(trace, "args "+e.ID+" "+e.Delta)
		case EventToolEnd:
			trace = append(trace, "end "+e.ID)
		case EventDone:
			trace = append(trace, "done")
		}
	}
	return trace
}

func doneOf(t *testing.T, evs []Event) EventDone {
	t.Helper()
	for _, ev := range evs {
		if d, ok := ev.(EventDone); ok {
			return d
		}
	}
	t.Fatal("stream emitted no EventDone")
	return EventDone{}
}

func toolCallsOf(m Message) []ToolCallBlock {
	var calls []ToolCallBlock
	for _, c := range m.Content {
		if tc, ok := c.(ToolCallBlock); ok {
			calls = append(calls, tc)
		}
	}
	return calls
}

// Two calls, each with its arguments split across deltas, and no finish
// reason yet. Each case below appends its own ending.
var twoToolCallFrames = []string{
	`{"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]},"finish_reason":null}]}`,
	`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]},"finish_reason":null}]}`,
	`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.txt\"}"}}]},"finish_reason":null}]}`,
	`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"glob","arguments":"{\"pattern\":\"*.go\"}"}}]},"finish_reason":null}]}`,
}

func withEnding(ending ...string) []string {
	frames := append([]string{}, twoToolCallFrames...)
	frames = append(frames, ending...)
	return append(frames, `{"choices":[],"usage":{"prompt_tokens":8,"completion_tokens":5}}`, "[DONE]")
}

func TestChatToolCallsRunWhicheverCleanFinishReasonArrives(t *testing.T) {
	// The tool_calls finish is the reference: every other clean ending must
	// give the same tool events, in the same order, and the same stop.
	want := []string{
		`start call_1 read`,
		`args call_1 {"path":`,
		`args call_1 "a.txt"}`,
		`start call_2 glob`,
		`args call_2 {"pattern":"*.go"}`,
		`end call_1`,
		`end call_2`,
		`done`,
	}
	cases := []struct {
		name   string
		frames []string
	}{
		{"tool_calls", withEnding(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)},
		{"stop", withEnding(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)},
		{"no finish reason", withEnding()},
		// A trailing stop after tool_calls once reset the stop to StopEnd.
		// The promotion must restore it without a second EventToolEnd.
		{"tool_calls then stop", withEnding(
			`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evs := streamChatEvents(t, tc.frames...)
			done := doneOf(t, evs)
			if done.Stop != StopToolUse {
				t.Fatalf("stop = %v, want StopToolUse: core runs tools on nothing else", done.Stop)
			}
			if done.Err != nil {
				t.Fatalf("err = %v, want nil", done.Err)
			}
			if got := toolTrace(evs); !reflect.DeepEqual(got, want) {
				t.Errorf("tool events =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
			}
			calls := toolCallsOf(done.Message)
			if len(calls) != 2 {
				t.Fatalf("message carries %d tool calls, want 2: %+v", len(calls), done.Message.Content)
			}
			if c := calls[0]; c.ID != "call_1" || c.Name != "read" || string(c.Arguments) != `{"path":"a.txt"}` || c.RawArguments != "" {
				t.Errorf("first call = %+v, want call_1 read {\"path\":\"a.txt\"}", c)
			}
			if c := calls[1]; c.ID != "call_2" || c.Name != "glob" || string(c.Arguments) != `{"pattern":"*.go"}` {
				t.Errorf("second call = %+v, want call_2 glob {\"pattern\":\"*.go\"}", c)
			}
		})
	}
}

// Gemini's OpenAI-compatible endpoint sends the whole call and the stop in one
// chunk, so the finish reason arrives in the same frame that opens the block.
func TestChatToolCallInTheStopChunkRuns(t *testing.T) {
	evs := streamChatEvents(t,
		`{"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"a.txt\"}"}}]},"finish_reason":"stop"}]}`,
		"[DONE]",
	)
	done := doneOf(t, evs)
	if done.Stop != StopToolUse {
		t.Fatalf("stop = %v, want StopToolUse", done.Stop)
	}
	want := []string{`start call_1 read`, `args call_1 {"path":"a.txt"}`, `end call_1`, `done`}
	if got := toolTrace(evs); !reflect.DeepEqual(got, want) {
		t.Errorf("tool events = %q, want %q", got, want)
	}
}

// The promotion is for a reply the model finished. A reply the server cut
// short keeps its stop, and its calls do not run.
func TestChatFinishReasonsThatDoNotRunTools(t *testing.T) {
	cases := []struct {
		name      string
		frames    []string
		want      StopReason
		wantCalls int
	}{
		// A cut-off call has incomplete arguments.
		{"length with tool calls", withEnding(`{"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`), StopLength, 2},
		// The server stopped the reply, not the model.
		{"content_filter with tool calls", withEnding(`{"choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}`), StopEnd, 2},
		{"plain text with stop", []string{
			`{"choices":[{"index":0,"delta":{"content":"hel"},"finish_reason":null}]}`,
			`{"choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}`,
			"[DONE]",
		}, StopEnd, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evs := streamChatEvents(t, tc.frames...)
			done := doneOf(t, evs)
			if done.Stop != tc.want {
				t.Fatalf("stop = %v, want %v", done.Stop, tc.want)
			}
			if got := len(toolCallsOf(done.Message)); got != tc.wantCalls {
				t.Errorf("message carries %d tool calls, want %d", got, tc.wantCalls)
			}
			for _, ev := range evs {
				if e, ok := ev.(EventToolEnd); ok {
					t.Errorf("EventToolEnd{%s} on a reply whose calls do not run", e.ID)
				}
			}
		})
	}
}
