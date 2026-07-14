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
