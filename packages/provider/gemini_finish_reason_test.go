package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// geminiFinish drives one SSE response carrying finishReason and returns the
// terminal event.
func geminiFinish(t *testing.T, frame string) EventDone {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + frame + "\n\n"))
	}))
	defer srv.Close()

	evs, err := NewGemini("k", srv.URL).Stream(context.Background(), Request{
		Model:    "gemini-3.1-pro-preview",
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var done EventDone
	for ev := range evs {
		if e, ok := ev.(EventDone); ok {
			done = e
		}
	}
	return done
}

// 🪤 MALFORMED_FUNCTION_CALL carries NO content: the whole response is a single
// empty text part. With no arm for it the switch fell through, `stop` kept its
// StopEnd default and `finalErr` stayed nil, so the turn ended in SUCCESS with
// nothing in it — terva printed a blank line and exited 0.
//
// Caught live 2026-08-14 on gemini-3.1-pro-preview: the identical tool-loop
// prompt returned the right answer twice and this on the third run. A user sees
// an agent that silently does nothing, occasionally, for no stated reason.
func TestGeminiMalformedFunctionCallIsAnError(t *testing.T) {
	done := geminiFinish(t, `{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"MALFORMED_FUNCTION_CALL"}],"usageMetadata":{"promptTokenCount":9213,"thoughtsTokenCount":374}}`)

	if done.Stop != StopError {
		t.Errorf("Stop = %v, want StopError — an empty answer must not read as success", done.Stop)
	}
	if done.Err == nil {
		t.Fatal("Err is nil: the turn ended with no content and no error, which is the silent-empty-turn defect")
	}
	if !strings.Contains(done.Err.Error(), "MALFORMED_FUNCTION_CALL") {
		t.Errorf("Err = %q, want it to name the finish reason", done.Err)
	}
	// Transient: measured 2/3 identical prompts succeeded, so the loop should
	// retry rather than hand the user an empty answer.
	var pe *ProviderError
	if !errors.As(done.Err, &pe) {
		t.Fatalf("Err is %T, want *ProviderError so canRetryError can classify it", done.Err)
	}
	if !pe.Transient {
		t.Error("Transient = false; a retry clears this, so the loop should re-attempt")
	}
}

// The same hole swallows every finish reason Google has not shipped yet. The
// enum keeps growing (UNEXPECTED_TOOL_CALL, TOO_MANY_TOOL_CALLS, LANGUAGE,
// OTHER …) and each new value would otherwise become a silent successful empty
// turn. An unrecognized terminal reason must surface, not vanish.
func TestGeminiUnknownFinishReasonIsAnError(t *testing.T) {
	for _, reason := range []string{"UNEXPECTED_TOOL_CALL", "TOO_MANY_TOOL_CALLS", "OTHER", "SOME_REASON_INVENTED_IN_2027"} {
		done := geminiFinish(t, `{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"`+reason+`"}]}`)
		if done.Stop != StopError || done.Err == nil {
			t.Errorf("%s: stop=%v err=%v, want a surfaced error rather than a silent empty turn",
				reason, done.Stop, done.Err)
			continue
		}
		if !strings.Contains(done.Err.Error(), reason) {
			t.Errorf("%s: Err = %q, want it to name the reason", reason, done.Err)
		}
	}
}

// The normal ends must stay normal — the default arm above must not swallow
// them. STOP with content is a clean turn; MAX_TOKENS is a length stop, not an
// error, because the partial content is still worth keeping.
func TestGeminiNormalFinishReasonsUnaffected(t *testing.T) {
	done := geminiFinish(t, `{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}]}`)
	if done.Stop != StopEnd || done.Err != nil {
		t.Errorf("STOP: stop=%v err=%v, want a clean StopEnd", done.Stop, done.Err)
	}

	done = geminiFinish(t, `{"candidates":[{"content":{"role":"model","parts":[{"text":"trunc"}]},"finishReason":"MAX_TOKENS"}]}`)
	if done.Stop != StopLength || done.Err != nil {
		t.Errorf("MAX_TOKENS: stop=%v err=%v, want StopLength with no error", done.Stop, done.Err)
	}

	// A safety block stays a non-transient error: retrying is pointless.
	done = geminiFinish(t, `{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"SAFETY"}]}`)
	var pe *ProviderError
	if done.Stop != StopError || !errors.As(done.Err, &pe) {
		t.Fatalf("SAFETY: stop=%v err=%v, want a ProviderError", done.Stop, done.Err)
	}
	if pe.Transient {
		t.Error("SAFETY marked transient; a blocked response does not un-block on retry")
	}
}
