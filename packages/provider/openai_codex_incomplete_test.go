package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The Responses API ends a reply that stopped at max_output_tokens, or at a
// content filter, with response.incomplete instead of response.completed. The
// reader used to skip that frame, so the close after it read as a dropped
// connection: StopError with a transient error, which the retry layer re-sends
// into the same cap. These tests hold the mapping and the usage the frame
// carries, on both providers that share the reader.

// streamResponsesEvents serves frames, one SSE event each, to every client
// built on the Responses reader, and collects what each one emits.
func streamResponsesEvents(t *testing.T, frames ...string) map[string][]Event {
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

	clients := map[string]Client{
		"openai-codex":     NewOpenAICodex("token", "acct", srv.URL),
		"openai-responses": NewOpenAIResponses("key", srv.URL),
	}
	got := map[string][]Event{}
	for name, c := range clients {
		evs, err := c.Stream(context.Background(), Request{Model: "gpt-5.6-sol"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for ev := range evs {
			got[name] = append(got[name], ev)
		}
	}
	return got
}

func usageOf(t *testing.T, evs []Event) Usage {
	t.Helper()
	for _, ev := range evs {
		if u, ok := ev.(EventUsage); ok {
			return u.Usage
		}
	}
	t.Fatal("stream emitted no EventUsage")
	return Usage{}
}

// incompleteFrame is a response.incomplete frame in the documented shape, with
// the usage of a reply that spent most of its output cap reasoning.
func incompleteFrame(details string) string {
	return `{"type":"response.incomplete","sequence_number":9,"response":{"id":"resp_1","object":"response",` +
		`"status":"incomplete"` + details + `,` +
		`"usage":{"input_tokens":10000,"input_tokens_details":{"cached_tokens":6000},` +
		`"output_tokens":256,"output_tokens_details":{"reasoning_tokens":240},"total_tokens":10256}}}`
}

const incompleteMaxOutput = `,"incomplete_details":{"reason":"max_output_tokens"}`

// A text reply cut at the output cap is StopLength, keeps the text streamed so
// far, and reports the usage the incomplete frame carries.
func TestCodexIncompleteAtOutputCapIsStopLength(t *testing.T) {
	runs := streamResponsesEvents(t,
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress"}}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
		`{"type":"response.output_text.delta","sequence_number":2,"output_index":0,"content_index":0,"delta":"The answer"}`,
		`{"type":"response.output_text.delta","sequence_number":3,"output_index":0,"content_index":0,"delta":" is"}`,
		`{"type":"response.output_item.done","sequence_number":4,"output_index":0,"item":{"id":"msg_1","type":"message","status":"incomplete","role":"assistant"}}`,
		incompleteFrame(incompleteMaxOutput),
	)
	for name, evs := range runs {
		t.Run(name, func(t *testing.T) {
			done := doneOf(t, evs)
			if done.Stop != StopLength {
				t.Fatalf("stop = %v (err %v), want StopLength", done.Stop, done.Err)
			}
			if done.Err != nil {
				t.Fatalf("err = %v, want nil: an output cap is not a dropped connection", done.Err)
			}
			if got := textOf(t, done.Message); got != "The answer is" {
				t.Errorf("text = %q, want the text streamed before the cap", got)
			}
			u := usageOf(t, evs)
			if u.OutputTokens != 256 {
				t.Errorf("OutputTokens = %d, want 256", u.OutputTokens)
			}
			if !u.ReasoningTokensKnown || u.ReasoningTokens != 240 {
				t.Errorf("reasoning = %d (known %v), want 240 known", u.ReasoningTokens, u.ReasoningTokensKnown)
			}
			// The same disjoint split response.completed gets.
			if u.CacheReadTokens != 6000 || u.InputTokens != 4000 {
				t.Errorf("in=%d read=%d, want 4000/6000", u.InputTokens, u.CacheReadTokens)
			}
		})
	}
}

// The function_call frames for a call the stop cut: announced, arguments half
// written, and never closed by response.output_item.done.
var cutCallFrames = []string{
	`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"call_1","name":"write","arguments":""}}`,
	`{"type":"response.function_call_arguments.delta","sequence_number":2,"output_index":0,"item_id":"fc_1","delta":"{\"path\":\"a.txt\",\"content\":\"lo"}`,
}

// A reply that stopped early ends without StopToolUse, whatever it carries, so
// core does not run a call whose arguments the stop may have cut. Only the
// output cap is StopLength. Every other reason ends as the Chat Completions
// reader ends a content_filter finish: StopEnd, with no error to retry.
func TestCodexIncompleteReasons(t *testing.T) {
	cases := []struct {
		name    string
		details string
		want    StopReason
	}{
		{"max_output_tokens", incompleteMaxOutput, StopLength},
		{"content_filter", `,"incomplete_details":{"reason":"content_filter"}`, StopEnd},
		{"a reason this reader does not know", `,"incomplete_details":{"reason":"some_future_reason"}`, StopEnd},
		{"no incomplete_details", ``, StopEnd},
		{"null incomplete_details", `,"incomplete_details":null`, StopEnd},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frames := append(append([]string{}, cutCallFrames...), incompleteFrame(tc.details))
			for name, evs := range streamResponsesEvents(t, frames...) {
				t.Run(name, func(t *testing.T) {
					done := doneOf(t, evs)
					if done.Stop != tc.want {
						t.Fatalf("stop = %v (err %v), want %v", done.Stop, done.Err, tc.want)
					}
					if done.Err != nil {
						t.Fatalf("err = %v, want nil, so the retry layer has nothing to re-send", done.Err)
					}
					// The cut call stays in the message. This test checks
					// only that. Core answers it under StopLength, and the
					// next request pairs it with the repair stub under
					// StopEnd. packages/core/cut_off_calls_test.go holds both.
					if calls := toolCallsOf(done.Message); len(calls) != 1 || calls[0].ID != "call_1" {
						t.Errorf("tool calls = %+v, want the one cut call", calls)
					}
					if u := usageOf(t, evs); u.OutputTokens != 256 {
						t.Errorf("OutputTokens = %d, want 256 from the incomplete frame", u.OutputTokens)
					}
				})
			}
		})
	}
}

// A function call the model finished still runs when the reply completes. The
// incomplete arm must not have changed the response.completed path it shares.
func TestCodexCompletedFunctionCallStillRuns(t *testing.T) {
	runs := streamResponsesEvents(t,
		cutCallFrames[0],
		`{"type":"response.function_call_arguments.delta","sequence_number":2,"output_index":0,"item_id":"fc_1","delta":"{\"path\":\"a.txt\"}"}`,
		`{"type":"response.output_item.done","sequence_number":3,"output_index":0,"item":{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"write","arguments":"{\"path\":\"a.txt\"}"}}`,
		`{"type":"response.completed","sequence_number":4,"response":{"id":"resp_1","status":"completed","usage":{"input_tokens":10,"output_tokens":5}}}`,
	)
	for name, evs := range runs {
		t.Run(name, func(t *testing.T) {
			if done := doneOf(t, evs); done.Stop != StopToolUse || done.Err != nil {
				t.Fatalf("stop = %v, err = %v, want StopToolUse and nil", done.Stop, done.Err)
			}
		})
	}
}
