package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// chatRequest is what the binary POSTs to /v1/chat/completions,
// decoded loosely so tests can assert on the raw message shapes.
type chatRequest struct {
	Model    string           `json:"model"`
	Messages []map[string]any `json:"messages"`
	Tools    []map[string]any `json:"tools"`
	Stream   bool             `json:"stream"`
}

// sseScript is one scripted /v1/chat/completions response: chunks are
// emitted in order as `data:` SSE frames, then [DONE] if sendDone is
// set. Leaving sendDone false ends the response without the terminal
// frame — i.e. a mid-stream connection death, which the client must
// surface as an error (the Tier 1 stream-death fix).
//
// When httpStatus is non-zero the response is that HTTP error instead
// of a stream (retryAfter, if set, is sent as a Retry-After header) —
// for exercising the typed-error retry path.
type sseScript struct {
	chunks     []map[string]any
	sendDone   bool
	httpStatus int
	retryAfter string
}

// fakeProvider is a scripted openai-compatible endpoint. Each
// chat/completions call consumes the next script in order and records
// the request for later assertions; calls beyond the script list get
// HTTP 500, so an unexpected extra request (e.g. a surprise retry
// loop) fails the turn instead of hanging it.
type fakeProvider struct {
	t   *testing.T
	srv *httptest.Server

	mu      sync.Mutex
	scripts []sseScript
	reqs    []chatRequest
	auths   []string
}

func newFakeProvider(t *testing.T, scripts ...sseScript) *fakeProvider {
	t.Helper()
	f := &fakeProvider{t: t, scripts: scripts}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", f.handleChat)
	mux.HandleFunc("/v1/models", f.handleModels)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeProvider) url() string { return f.srv.URL }

// requests returns a snapshot of the chat requests seen so far.
func (f *fakeProvider) requests() []chatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]chatRequest(nil), f.reqs...)
}

// authHeaders returns the Authorization header of each chat request.
func (f *fakeProvider) authHeaders() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.auths...)
}

func (f *fakeProvider) handleChat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.auths = append(f.auths, r.Header.Get("Authorization"))
	if len(f.scripts) == 0 {
		f.mu.Unlock()
		// 418, deliberately: it marks "script exhausted" unmistakably
		// AND is not in the client's transient-retry status set, so an
		// unexpected extra request (e.g. the agent retrying a failed
		// stream) fails the turn immediately instead of burning
		// seconds in a retry-backoff loop against this handler.
		http.Error(w, "fake provider: no scripted response left", http.StatusTeapot)
		return
	}
	script := f.scripts[0]
	f.scripts = f.scripts[1:]
	f.mu.Unlock()

	if script.httpStatus != 0 {
		if script.retryAfter != "" {
			w.Header().Set("Retry-After", script.retryAfter)
		}
		http.Error(w, `{"error":{"message":"scripted provider error"}}`, script.httpStatus)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flusher", http.StatusInternalServerError)
		return
	}
	for _, c := range script.chunks {
		b, err := json.Marshal(c)
		if err != nil {
			f.t.Errorf("fake provider: marshal chunk: %v", err)
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", b)
		fl.Flush()
	}
	if script.sendDone {
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
	}
	// Returning without [DONE] closes the connection mid-stream.
}

func (f *fakeProvider) handleModels(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data": []map[string]any{{"id": "test-model"}},
	})
}

// ----- chunk builders (openai chat-completions stream shapes) -----

func textChunk(text string) map[string]any {
	return map[string]any{
		"choices": []any{map[string]any{
			"index": 0,
			"delta": map[string]any{"content": text},
		}},
	}
}

func toolCallChunk(id, name, argsJSON string) map[string]any {
	return map[string]any{
		"choices": []any{map[string]any{
			"index": 0,
			"delta": map[string]any{
				"tool_calls": []any{map[string]any{
					"index": 0,
					"id":    id,
					"type":  "function",
					"function": map[string]any{
						"name":      name,
						"arguments": argsJSON,
					},
				}},
			},
		}},
	}
}

// finishChunk carries the finish_reason ("stop", "tool_calls", …) and
// final usage numbers, like a real server's last pre-[DONE] chunk.
func finishChunk(reason string, promptTokens, completionTokens int) map[string]any {
	return map[string]any{
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": reason,
		}},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
		},
	}
}
