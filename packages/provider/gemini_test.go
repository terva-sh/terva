package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// TestGeminiStreamHappyPath drives the gemini client end-to-end against
// a fake SSE server speaking the Gemini Generative Language wire format.
// We assert text deltas accumulate, usage rolls in from usageMetadata,
// and the final EventDone carries StopEnd.
func TestGeminiStreamHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-goog-api-key"); got != "k" {
			t.Errorf("missing api key header: %q", got)
		}
		if !strings.Contains(r.URL.RawQuery, "alt=sse") {
			t.Errorf("missing alt=sse: %q", r.URL.RawQuery)
		}
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = w.Write([]byte(s))
			if fl != nil {
				fl.Flush()
			}
		}
		// Two text chunks, then a usage-only finish chunk.
		write("data: " + `{"candidates":[{"content":{"role":"model","parts":[{"text":"hel"}]}}]}` + "\n\n")
		write("data: " + `{"candidates":[{"content":{"role":"model","parts":[{"text":"lo"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":2,"totalTokenCount":14}}` + "\n\n")
	}))
	defer srv.Close()

	c := NewGemini("k", srv.URL)
	evs, err := c.Stream(context.Background(), Request{Model: "gemini-2.5-pro"})
	if err != nil {
		t.Fatal(err)
	}
	var gotText string
	var done EventDone
	var usage Usage
	for ev := range evs {
		switch e := ev.(type) {
		case EventTextDelta:
			gotText += e.Delta
		case EventUsage:
			usage = e.Usage
		case EventDone:
			done = e
		}
	}
	if gotText != "hello" {
		t.Fatalf("text=%q", gotText)
	}
	if done.Stop != StopEnd {
		t.Fatalf("stop=%v", done.Stop)
	}
	if usage.InputTokens != 12 || usage.OutputTokens != 2 {
		t.Fatalf("usage=%+v", usage)
	}
}

func TestGeminiStreamInlineImage(t *testing.T) {
	dir := testsupport.TempDir(t)
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	img := base64.StdEncoding.EncodeToString([]byte("png-bytes"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + `{"candidates":[{"content":{"role":"model","parts":[{"text":"here"},{"inlineData":{"mimeType":"image/png","data":"` + img + `"}}]},"finishReason":"STOP"}]}` + "\n\n"))
	}))
	defer srv.Close()

	c := NewGemini("k", srv.URL)
	evs, err := c.Stream(context.Background(), Request{Model: "gemini-2.5-flash-image"})
	if err != nil {
		t.Fatal(err)
	}
	var done EventDone
	for ev := range evs {
		if e, ok := ev.(EventDone); ok {
			done = e
		}
	}
	if len(done.Message.Content) != 3 {
		t.Fatalf("content count=%d: %+v", len(done.Message.Content), done.Message.Content)
	}
	if tb, ok := done.Message.Content[0].(TextBlock); !ok || tb.Text != "here" {
		t.Fatalf("text block=%T %+v", done.Message.Content[0], done.Message.Content[0])
	}
	ib, ok := done.Message.Content[1].(ImageBlock)
	if !ok {
		t.Fatalf("want image block, got %T", done.Message.Content[1])
	}
	if ib.MimeType != "image/png" || string(ib.Data) != "png-bytes" {
		t.Fatalf("image=%q %q", ib.MimeType, string(ib.Data))
	}
	saved, ok := done.Message.Content[2].(TextBlock)
	if !ok || !strings.Contains(saved.Text, "terva-gemini-image-") || !strings.Contains(saved.Text, ".png") {
		t.Fatalf("saved path block=%T %+v", done.Message.Content[2], done.Message.Content[2])
	}
	path := strings.TrimPrefix(saved.Text, "Saved image: `")
	path = strings.TrimSuffix(path, "`")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("saved image missing at %q: %v", path, err)
	}
}

// TestGeminiToolCall covers the tool-call branch: a single
// functionCall part should produce ToolStart/Args/End and the final
// stop reason should be StopToolUse.
func TestGeminiToolCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = w.Write([]byte(s))
			if fl != nil {
				fl.Flush()
			}
		}
		write("data: " + `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"a"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":7,"totalTokenCount":10}}` + "\n\n")
	}))
	defer srv.Close()

	c := NewGemini("k", srv.URL)
	evs, err := c.Stream(context.Background(), Request{Model: "gemini-2.5-pro"})
	if err != nil {
		t.Fatal(err)
	}
	var startName, argsBuf string
	var done EventDone
	for ev := range evs {
		switch e := ev.(type) {
		case EventToolStart:
			startName = e.Name
		case EventToolArgs:
			argsBuf += e.Delta
		case EventDone:
			done = e
		}
	}
	if startName != "read" {
		t.Fatalf("tool name=%q", startName)
	}
	if !strings.Contains(argsBuf, `"path":"a"`) {
		t.Fatalf("tool args=%q", argsBuf)
	}
	if done.Stop != StopToolUse {
		t.Fatalf("stop=%v", done.Stop)
	}
	// The assembled message should contain the tool call as the first content block.
	if len(done.Message.Content) != 1 {
		t.Fatalf("message content count=%d", len(done.Message.Content))
	}
	tc, ok := done.Message.Content[0].(ToolCallBlock)
	if !ok {
		t.Fatalf("expected ToolCallBlock, got %T", done.Message.Content[0])
	}
	if tc.Name != "read" {
		t.Fatalf("tool block name=%q", tc.Name)
	}
}

// TestGeminiBuildRequestSystemAndTools confirms the wire payload puts
// the system prompt under systemInstruction and tool defs under tools[0].
func TestGeminiBuildRequestSystemAndTools(t *testing.T) {
	c := NewGemini("k", "https://example.invalid").(*geminiClient)
	wire, _, err := c.buildRequest(Request{
		Model:  "gemini-2.5-pro",
		System: "you are terva",
		Tools: []Tool{
			{Name: "read", Description: "read a file", Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)},
		},
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if wire.SystemInstruction == nil || wire.SystemInstruction.Parts[0].Text != "you are terva" {
		t.Fatalf("system: %+v", wire.SystemInstruction)
	}
	if len(wire.Tools) != 1 || len(wire.Tools[0].FunctionDeclarations) != 1 || wire.Tools[0].FunctionDeclarations[0].Name != "read" {
		t.Fatalf("tools: %+v", wire.Tools)
	}
	if len(wire.Contents) != 1 || wire.Contents[0].Role != "user" {
		t.Fatalf("contents: %+v", wire.Contents)
	}
}

// TestGeminiBuildRequestImageModelOmitsTools confirms image-generation
// models receive direct multimodal prompts without function declarations.
func TestGeminiBuildRequestStripsUnsupportedSchemaFields(t *testing.T) {
	c := NewGemini("k", "https://example.invalid").(*geminiClient)
	wire, _, err := c.buildRequest(Request{
		Model: "gemini-2.5-pro",
		Tools: []Tool{{
			Name:        "edit",
			Description: "edit a file",
			Schema: json.RawMessage(`{
				"$schema":"http://json-schema.org/draft-07/schema#",
				"type":"object",
				"additionalProperties":false,
				"properties":{
					"edits":{
						"type":"array",
						"items":{
							"type":"object",
							"additionalProperties":false,
							"properties":{"oldText":{"type":"string"},"newText":{"type":"string"}}
						}
					}
				}
			}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(wire.Tools[0].FunctionDeclarations[0].Parameters)
	if strings.Contains(got, "additionalProperties") || strings.Contains(got, "$schema") {
		t.Fatalf("Gemini schema should strip unsupported fields, got %s", got)
	}
	if !strings.Contains(got, `"oldText"`) || !strings.Contains(got, `"newText"`) {
		t.Fatalf("Gemini schema lost nested properties, got %s", got)
	}
}

func TestGeminiBuildRequestImageModelOmitsTools(t *testing.T) {
	c := NewGemini("k", "https://example.invalid").(*geminiClient)
	wire, _, err := c.buildRequest(Request{
		Model: "gemini-2.5-flash-image",
		Tools: []Tool{
			{Name: "read", Description: "read a file", Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)},
		},
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "edit this image"}, ImageBlock{MimeType: "image/png", Data: []byte("png")}}},
			{Role: RoleAssistant, Content: []Content{TextBlock{Text: "checking"}, ToolCallBlock{ID: "1", Name: "read", Arguments: json.RawMessage(`{"path":"x"}`)}}},
			{Role: RoleTool, Content: []Content{ToolResultBlock{CallID: "1", Content: []Content{TextBlock{Text: "tool output"}}}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(wire.Tools) != 0 {
		t.Fatalf("image model should omit tools, got %+v", wire.Tools)
	}
	if len(wire.Contents) != 2 {
		t.Fatalf("contents count=%d: %+v", len(wire.Contents), wire.Contents)
	}
	if len(wire.Contents[0].Parts) != 2 || wire.Contents[0].Parts[1].InlineData == nil {
		t.Fatalf("user image parts not preserved: %+v", wire.Contents[0].Parts)
	}
	if len(wire.Contents[1].Parts) != 1 || wire.Contents[1].Parts[0].FunctionCall != nil {
		t.Fatalf("assistant tool call not stripped: %+v", wire.Contents[1].Parts)
	}
}

// TestGeminiErrorStatus confirms HTTP error bodies bubble up.
func TestGeminiErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":401,"message":"bad key"}}`)
	}))
	defer srv.Close()

	c := NewGemini("k", srv.URL)
	_, err := c.Stream(context.Background(), Request{Model: "gemini-2.5-pro"})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("want 401 err, got %v", err)
	}
}

// TestGeminiThinkingConfig spot-checks the level → wire mapping for
// representative model families. The full table is exercised via
// integration; this guards the routing logic.
func TestGeminiThinkingConfig(t *testing.T) {
	cases := []struct {
		modelID string
		level   string
		wantLvl string
		wantBud int
	}{
		{"gemini-3-pro", "low", "LOW", 0},
		{"gemini-3-pro", "medium", "HIGH", 0}, // Pro can't go below LOW; medium → HIGH
		{"gemini-3-flash", "medium", "MEDIUM", 0},
		{"gemini-2.5-pro", "high", "", 16384},
		{"gemini-2.5-pro", "maximum", "", 32768},
		{"gemini-2.5-flash", "minimum", "", 1024},
		{"gemini-2.5-flash", "low", "", 2048},
		{"gemini-2.0-flash", "high", "", 0}, // 2.0 has no thinking → nil

		// Google's rolling aliases name no generation, so a
		// Contains(id, "gemini-3") test missed them and they fell through
		// the 2.5 switch to nil — terva sent NO thinkingConfig and
		// --reasoning was a silent no-op on two catalogued models that
		// advertise Reasoning: true. Measured live 2026-08-14:
		// gemini-flash-latest ran 107 thought tokens with no config, 134 at
		// LOW and 345 at HIGH; gemini-flash-lite-latest ran 0 by default and
		// 407 at HIGH, so the knob is what enables thinking there at all.
		{"gemini-flash-latest", "low", "LOW", 0},
		{"gemini-flash-latest", "medium", "MEDIUM", 0},
		{"gemini-flash-latest", "high", "HIGH", 0},
		{"gemini-flash-lite-latest", "medium", "MEDIUM", 0},
		// An alias that names an older generation keeps the budget knob.
		{"gemini-2.5-flash-latest", "low", "", 2048},
	}
	for _, tc := range cases {
		got := geminiThinkingConfig(tc.modelID, tc.level)
		if tc.wantLvl == "" && tc.wantBud == 0 {
			if got != nil {
				t.Errorf("%s/%s: want nil, got %+v", tc.modelID, tc.level, got)
			}
			continue
		}
		if got == nil {
			t.Errorf("%s/%s: got nil", tc.modelID, tc.level)
			continue
		}
		if tc.wantLvl != "" && got.ThinkingLevel != tc.wantLvl {
			t.Errorf("%s/%s: level=%q want %q", tc.modelID, tc.level, got.ThinkingLevel, tc.wantLvl)
		}
		if tc.wantBud != 0 {
			if got.ThinkingBudget == nil || *got.ThinkingBudget != tc.wantBud {
				t.Errorf("%s/%s: budget=%v want %d", tc.modelID, tc.level, got.ThinkingBudget, tc.wantBud)
			}
		}
	}
}

// TestDiscoverGoogle exercises the discovery helper against a fake
// /v1beta/models endpoint, confirming pagination plus filtering of
// non-chat ids (embedding, aqa).
func TestDiscoverGoogle(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "k" {
			t.Errorf("missing api key")
		}
		calls++
		if calls == 1 {
			_, _ = io.WriteString(w, `{
				"models": [
					{"name":"models/gemini-2.5-pro","displayName":"Gemini 2.5 Pro","supportedGenerationMethods":["generateContent","streamGenerateContent"],"inputTokenLimit":1048576,"outputTokenLimit":65536},
					{"name":"models/text-embedding-004","displayName":"Text Embedding","supportedGenerationMethods":["embedContent"]},
					{"name":"models/aqa","displayName":"AQA","supportedGenerationMethods":["generateAnswer"]}
				],
				"nextPageToken": "p2"
			}`)
		} else {
			_, _ = io.WriteString(w, `{
				"models": [
					{"name":"models/gemini-2.5-flash","displayName":"Gemini 2.5 Flash","supportedGenerationMethods":["generateContent"]}
				]
			}`)
		}
	}))
	defer srv.Close()

	got, err := DiscoverGoogle(context.Background(), "k", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("want 2 calls, got %d", calls)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 models, got %d: %+v", len(got), got)
	}
	if got[0].ID != "gemini-2.5-pro" || got[0].Provider != "google" {
		t.Errorf("first model wrong: %+v", got[0])
	}
	if got[1].ID != "gemini-2.5-flash" {
		t.Errorf("second model wrong: %+v", got[1])
	}
}
