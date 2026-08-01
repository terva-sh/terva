package provider

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// The vocabulary itself. transientErrorCode is the in-stream sibling of
// transientHTTPStatus, and every streaming decoder now shares it — so this table
// is the single place the retry policy for a 200-with-an-error-frame is stated.
func TestTransientErrorCode(t *testing.T) {
	cases := []struct {
		name            string
		code, kind, msg string
		want            bool
	}{
		// Transient, by either field: providers populate them inconsistently,
		// which is exactly why both are consulted.
		{"server_error in type", "", "server_error", "boom", true},
		{"server_error in code", "server_error", "", "boom", true},
		{"anthropic's own 500", "", "api_error", "internal", true},
		{"anthropic overload", "", "overloaded_error", "overloaded", true},
		{"service unavailable", "service_unavailable", "", "", true},
		{"internal server error", "internal_server_error", "", "", true},
		{"a timeout", "request_timeout", "", "", true},

		// Every rate-limit spelling the providers use.
		{"rate_limit_error", "", "rate_limit_error", "", true},
		{"rate_limit_exceeded", "rate_limit_exceeded", "", "", true},
		{"rate_limited", "rate_limited", "", "", true},

		// Permanent. Retrying these just fails the same way N more times.
		{"invalid request", "invalid_request_error", "", "bad input", false},
		{"context overflow", "context_length_exceeded", "invalid_request_error", "too long", false},
		{"bad key", "invalid_api_key", "", "", false},
		{"unrecognized", "some_new_thing", "", "who knows", false},
		{"nothing at all", "", "", "", false},

		// The prose fallback: the backend's canonical server error carries no
		// machine-readable field, and says so in words.
		{
			name: "retry advice with no code",
			msg:  "An error occurred while processing your request. You can retry your request, or contact support if the error persists.",
			want: true,
		},
		// ...but a PERMANENT code wins over prose. A 400 that happens to mention
		// retrying must not be retried, and the permanent vocabulary is checked
		// before the words are.
		{
			name: "a permanent code beats retry advice in the message",
			code: "invalid_request_error",
			msg:  "That was invalid. You can retry your request once you fix it.",
			want: false,
		},

		// The ChatGPT backend's overload, verbatim off the wire, with BOTH
		// machine-readable fields empty — the exact shape that classified as
		// permanent and killed three turns in one session. "Please try again
		// later" is the server instructing the client to retry just as plainly
		// as "you can retry your request" does.
		{
			name: "codex overload with no code or type",
			msg:  "Our servers are currently overloaded. Please try again later.",
			want: true,
		},
		// The same guard as above, on the new phrase: a recognized permanent
		// code still wins, so widening the prose list cannot resurrect a client
		// error that happens to end in a polite retry suggestion.
		{
			name: "a permanent code beats try-again-later too",
			kind: "invalid_request_error",
			msg:  "Unsupported parameter. Please try again later.",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := transientErrorCode(tc.code, tc.kind, tc.msg); got != tc.want {
				t.Fatalf("transientErrorCode(%q, %q, %q) = %v, want %v",
					tc.code, tc.kind, tc.msg, got, tc.want)
			}
		})
	}
}

// The openai decoder's in-stream error is the one behind openai, openrouter,
// groq, xai, kimi, openai-compatible AND every named endpoint the operator
// registers. It classified on `type` alone and never parsed `code` — so a server
// that reported its failure the ordinary way, in `code`, was judged on an empty
// string and came out PERMANENT. The turn ended, mid-agent-loop, on a failure the
// server was willing to serve on the next attempt.
func TestOpenAIInStreamErrorTransience(t *testing.T) {
	cases := []struct {
		name    string
		errJSON string
		want    bool
	}{
		{"code-only server error", `{"code":"server_error","message":"upstream blew up"}`, true},
		{"type-only server error", `{"type":"server_error","message":"upstream blew up"}`, true},
		{"code-only overload", `{"code":"rate_limit_exceeded","message":"slow down"}`, true},
		{"invalid request stays permanent", `{"type":"invalid_request_error","message":"bad tool schema"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewOpenAI("k", "https://example.invalid/v1").(*openaiClient)
			body := "data: " + `{"error":` + tc.errJSON + `}` + "\n\n"
			resp := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
			out := make(chan Event, 16)
			go c.runStream(context.Background(), resp, Request{Model: "gpt-4o"}, out)

			var got error
			for ev := range out {
				if done, ok := ev.(EventDone); ok {
					got = done.Err
				}
			}
			pe, ok := got.(*ProviderError)
			if !ok {
				t.Fatalf("error = %T %v, want *ProviderError", got, got)
			}
			if pe.Transient != tc.want {
				t.Fatalf("Transient = %v, want %v (error %v)", pe.Transient, tc.want, pe)
			}
		})
	}
}

// TestCodexInStreamOverloadTransience pins A1 of the 2026-07-30 session-harness
// review, end to end through the decoder that produced it.
//
// Under load the ChatGPT backend sends an error frame carrying prose and NOTHING
// machine-readable — no code, and no type beyond the dispatch key itself. Both
// vocabularies decline it, so classification falls to the prose fallback, which
// recognized only OpenAI's "you can retry your request" and therefore judged
// this PERMANENT. The agent loop does not retry a permanent error, so the turn
// died on its first attempt with the user's message already committed to the
// transcript.
//
// Note the frames below all carry a real top-level "type": this decoder
// dispatches on the type field INSIDE the data payload, not on the SSE
// "event:" name. A fixture without one never reaches the error handler at all —
// it falls through to the stream-death path, which is transient for an
// unrelated reason and will pass this test while proving nothing.
func TestCodexInStreamOverloadTransience(t *testing.T) {
	cases := []struct {
		name string
		data string
		want bool
	}{
		// The flat shape: prose only, no code.
		{"flat error frame, prose only",
			`{"type":"error","message":"Our servers are currently overloaded. Please try again later."}`, true},
		// The GPT-5.6 preview backend nests the same error one level down.
		{"nested error frame, prose only",
			`{"type":"error","error":{"message":"Our servers are currently overloaded. Please try again later."}}`, true},
		// response.failed is the other terminal path to the same classifier.
		{"response.failed, prose only",
			`{"type":"response.failed","response":{"error":{"message":"Our servers are currently overloaded. Please try again later."}}}`, true},
		// A recognized permanent code still wins over the prose, so widening the
		// phrase list cannot resurrect a client error that ends politely.
		{"permanent code beats the prose",
			`{"type":"error","error":{"type":"invalid_request_error","message":"Unsupported parameter. Please try again later."}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewOpenAICodex("k", "acct", "https://example.invalid").(*codexClient)
			resp := &http.Response{Body: io.NopCloser(strings.NewReader("data: " + tc.data + "\n\n"))}
			out := make(chan Event, 16)
			go c.runStream(context.Background(), resp, Request{Model: "gpt-5.6-sol"}, out)

			var got error
			for ev := range out {
				if done, ok := ev.(EventDone); ok {
					got = done.Err
				}
			}
			pe, ok := got.(*ProviderError)
			if !ok {
				t.Fatalf("error = %T %v, want *ProviderError", got, got)
			}
			// Guard against the fall-through above: a fixture that misses the
			// error handler surfaces the stream-death message instead, which is
			// transient for an unrelated reason.
			if strings.Contains(pe.Msg, "stream ended before") {
				t.Fatalf("frame never reached the error handler (got %q) — the fixture's top-level type is wrong", pe.Msg)
			}
			if pe.Transient != tc.want {
				t.Fatalf("Transient = %v, want %v (error %v)", pe.Transient, tc.want, pe)
			}
		})
	}
}

// TestCodexErrorFrameReadsNestedType pins the shadowing bug found while fixing
// A1: on an "error" frame the top-level "type" is the dispatch key, so using it
// as the error kind discarded the nested vocabulary word entirely. Every
// permanent code below arrives ONLY in the nested object — which is where the
// GPT-5.6 backend puts it.
func TestCodexErrorFrameReadsNestedType(t *testing.T) {
	for _, kind := range []string{
		"invalid_request_error", "invalid_api_key", "authentication_error",
		"permission_error", "not_found_error", "context_length_exceeded",
		"content_filter", "invalid_prompt",
	} {
		t.Run(kind, func(t *testing.T) {
			// Prose that WOULD be read as retry advice if the nested type were
			// shadowed, so this fails loudly rather than passing by accident.
			data := `{"type":"error","error":{"type":"` + kind + `","message":"nope. Please try again later."}}`
			c := NewOpenAICodex("k", "acct", "https://example.invalid").(*codexClient)
			resp := &http.Response{Body: io.NopCloser(strings.NewReader("data: " + data + "\n\n"))}
			out := make(chan Event, 16)
			go c.runStream(context.Background(), resp, Request{Model: "gpt-5.6-sol"}, out)

			var got error
			for ev := range out {
				if done, ok := ev.(EventDone); ok {
					got = done.Err
				}
			}
			pe, ok := got.(*ProviderError)
			if !ok {
				t.Fatalf("error = %T %v, want *ProviderError", got, got)
			}
			if pe.Transient {
				t.Fatalf("%s classified transient — the nested error type was shadowed by the dispatch key", kind)
			}
		})
	}
}
