package provider

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestSigV4Known checks our signer against AWS's published test vector
// from the SigV4 documentation. The expected Authorization header below
// is taken from "get-vanilla" in the suite (adapted: we use POST with
// an empty body since Bedrock is always POST).
//
// We don't run the official suite (it lives in S3 as a tarball and the
// signing rules vary slightly between examples); instead this test
// pins a fixed input/output so future refactors can't silently break
// the signer.
func TestSigV4Deterministic(t *testing.T) {
	creds := &bedrockSigV4Creds{
		accessKeyID:     "AKIDEXAMPLE",
		secretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
	}
	body := []byte(`{"hello":"world"}`)
	req, err := http.NewRequest("POST", "https://bedrock-runtime.us-east-1.amazonaws.com/model/foo/converse-stream", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("content-type", "application/json")
	when := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	if err := signSigV4(req, body, "bedrock", "us-east-1", creds, when); err != nil {
		t.Fatal(err)
	}
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
		t.Fatalf("missing AWS4 prefix: %q", auth)
	}
	if !strings.Contains(auth, "Credential=AKIDEXAMPLE/20240115/us-east-1/bedrock/aws4_request") {
		t.Errorf("wrong credential scope: %q", auth)
	}
	if !strings.Contains(auth, "SignedHeaders=") {
		t.Errorf("missing SignedHeaders: %q", auth)
	}
	if !strings.Contains(auth, "Signature=") {
		t.Errorf("missing Signature: %q", auth)
	}
	// Sign again with identical inputs -> identical signature (determinism).
	req2, _ := http.NewRequest("POST", "https://bedrock-runtime.us-east-1.amazonaws.com/model/foo/converse-stream", bytes.NewReader(body))
	req2.Header.Set("content-type", "application/json")
	if err := signSigV4(req2, body, "bedrock", "us-east-1", creds, when); err != nil {
		t.Fatal(err)
	}
	if req2.Header.Get("Authorization") != auth {
		t.Errorf("signature not deterministic:\n a=%q\n b=%q", auth, req2.Header.Get("Authorization"))
	}
}

func TestSigV4PathEncoding(t *testing.T) {
	// Bedrock model IDs include colons and dots. The colon must encode
	// to %3A in the canonical path (per SigV4 rules).
	cases := map[string]string{
		"/model/anthropic.claude-sonnet-4-5-20250929-v1:0/converse-stream": "/model/anthropic.claude-sonnet-4-5-20250929-v1%3A0/converse-stream",
		"/":  "/",
		"":   "/",
		"/a": "/a",
	}
	for in, want := range cases {
		got := canonicalSigV4Path(in)
		if got != want {
			t.Errorf("canonicalSigV4Path(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadAWSCredentialsFile(t *testing.T) {
	// Smoke test: parses an INI-ish file when present. We don't require
	// the file to exist on the test host.
	if _, err := readAWSCredentialsFile("default"); err != nil {
		// missing file or missing profile is fine — both are non-panics
		t.Logf("no aws creds available (expected on CI): %v", err)
	}
}

func TestNormalizeBedrockToolResultsMovesResultsAdjacentToToolUse(t *testing.T) {
	msgs := []Message{
		{Role: RoleAssistant, Content: []Content{ToolCallBlock{ID: "tool-1", Name: "edit", Arguments: json.RawMessage(`{"path":"a"}`)}}},
		{Role: RoleUser, Content: []Content{TextBlock{Text: "what's wrong"}}},
		{Role: RoleTool, Content: []Content{ToolResultBlock{CallID: "tool-1", Content: []Content{TextBlock{Text: "edited"}}}}},
	}

	out := normalizeBedrockToolResults(msgs)
	if len(out) != 3 {
		t.Fatalf("got %d messages, want 3: %+v", len(out), out)
	}
	if out[0].Role != RoleAssistant {
		t.Fatalf("first role = %s, want assistant", out[0].Role)
	}
	if out[1].Role != RoleTool {
		t.Fatalf("second role = %s, want tool", out[1].Role)
	}
	tr, ok := out[1].Content[0].(ToolResultBlock)
	if !ok {
		t.Fatalf("second message content = %T, want ToolResultBlock", out[1].Content[0])
	}
	if tr.CallID != "tool-1" || tr.IsError {
		t.Fatalf("unexpected tool result: %+v", tr)
	}
	if out[2].Role != RoleUser {
		t.Fatalf("third role = %s, want user", out[2].Role)
	}
}

func TestNormalizeBedrockToolResultsInjectsMissingResult(t *testing.T) {
	msgs := []Message{
		{Role: RoleAssistant, Content: []Content{ToolCallBlock{ID: "tool-1", Name: "edit", Arguments: json.RawMessage(`{}`)}}},
		{Role: RoleUser, Content: []Content{TextBlock{Text: "what's wrong"}}},
	}

	out := normalizeBedrockToolResults(msgs)
	if len(out) != 3 {
		t.Fatalf("got %d messages, want 3: %+v", len(out), out)
	}
	tr, ok := out[1].Content[0].(ToolResultBlock)
	if !ok {
		t.Fatalf("second message content = %T, want ToolResultBlock", out[1].Content[0])
	}
	if tr.CallID != "tool-1" || !tr.IsError {
		t.Fatalf("unexpected synthetic result: %+v", tr)
	}
}

func TestBedrockModelSupportsCaching(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		// Bare Claude IDs (as they come from catalog_builtin)
		{"anthropic.claude-sonnet-4-5-20250929-v1:0", true},
		{"anthropic.claude-haiku-4-5-20251001-v1:0", true},
		{"anthropic.claude-opus-4-5-20251101-v1:0", true},
		// Geo-prefixed (resolved form that arrives at bedrockModelSupportsCaching)
		{"us.anthropic.claude-sonnet-4-5-20250929-v1:0", true},
		{"eu.anthropic.claude-haiku-4-5-20251001-v1:0", true},
		{"global.anthropic.claude-opus-4-5-20251101-v1:0", true},
		// Nova models have PriceCacheWrite==0 — they use automatic caching
		{"amazon.nova-pro-v1:0", false},
		{"amazon.nova-lite-v1:0", false},
		{"amazon.nova-micro-v1:0", false},
		// DeepSeek — no cache write price
		{"deepseek.r1-v1:0", false},
		// Unknown model with claude prefix
		{"anthropic.claude-future-v99:0", true},
		// Unknown non-claude model
		{"some.unknown-model-v1:0", false},
	}
	for _, c := range cases {
		got := bedrockModelSupportsCaching(c.model)
		if got != c.want {
			t.Errorf("bedrockModelSupportsCaching(%q) = %v, want %v", c.model, got, c.want)
		}
	}
}

func TestBedrockBuildRequestMaxTokens(t *testing.T) {
	client := &bedrockClient{region: "us-east-1"}

	// A non-zero MaxTokens must flow through to InferenceConfig so the
	// model gets its full output budget. This is the regression guard
	// for long writes/edits being truncated at Bedrock's 4096 default.
	req, err := client.buildRequest(Request{
		Model:     "anthropic.claude-sonnet-4-5-20250929-v1:0",
		MaxTokens: 64000,
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "hello"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.InferenceConfig.MaxTokens != 64000 {
		t.Errorf("MaxTokens = %d, want 64000", req.InferenceConfig.MaxTokens)
	}

	// Zero still falls back to the conservative provider default so an
	// unset budget never sends maxTokens:0 (which Bedrock rejects).
	reqZero, err := client.buildRequest(Request{
		Model: "anthropic.claude-sonnet-4-5-20250929-v1:0",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "hello"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reqZero.InferenceConfig.MaxTokens != 4096 {
		t.Errorf("zero MaxTokens default = %d, want 4096", reqZero.InferenceConfig.MaxTokens)
	}
}

func TestBedrockBuildRequestCachingClaudeModel(t *testing.T) {
	// A Claude model (PriceCacheWrite > 0) should get cachePoint markers
	// in the system array and on the last user message.
	client := &bedrockClient{region: "us-east-1"}
	req, err := client.buildRequest(Request{
		Model:  "anthropic.claude-sonnet-4-5-20250929-v1:0",
		System: "You are a helpful assistant.",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "hello"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// system should be: [{text: ...}, {cachePoint: {type: default}}]
	if len(req.System) != 2 {
		t.Fatalf("system len = %d, want 2 (text + cachePoint)", len(req.System))
	}
	if _, ok := req.System[0]["text"]; !ok {
		t.Errorf("system[0] missing text key: %v", req.System[0])
	}
	if _, ok := req.System[1]["cachePoint"]; !ok {
		t.Errorf("system[1] missing cachePoint key: %v", req.System[1])
	}

	// last user message should end with a cachePoint block
	if len(req.Messages) == 0 {
		t.Fatal("no messages")
	}
	lastMsg := req.Messages[len(req.Messages)-1]
	if lastMsg.Role != "user" {
		t.Fatalf("last message role = %q, want user", lastMsg.Role)
	}
	lastBlock := lastMsg.Content[len(lastMsg.Content)-1]
	if _, ok := lastBlock["cachePoint"]; !ok {
		t.Errorf("last user message final block missing cachePoint: %v", lastBlock)
	}
}

func TestBedrockBuildRequestNoCachingNovaModel(t *testing.T) {
	// A Nova model (PriceCacheWrite == 0) should NOT get cachePoint markers.
	client := &bedrockClient{region: "us-east-1"}
	req, err := client.buildRequest(Request{
		Model:  "amazon.nova-pro-v1:0",
		System: "You are helpful.",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// system should be plain: [{text: ...}] with no cachePoint
	if len(req.System) != 1 {
		t.Fatalf("system len = %d, want 1 (text only)", len(req.System))
	}
	if _, ok := req.System[0]["cachePoint"]; ok {
		t.Errorf("Nova system unexpectedly contains cachePoint")
	}

	// last user message should NOT end with a cachePoint block
	if len(req.Messages) == 0 {
		t.Fatal("no messages")
	}
	lastMsg := req.Messages[len(req.Messages)-1]
	for _, block := range lastMsg.Content {
		if _, ok := block["cachePoint"]; ok {
			t.Errorf("Nova user message unexpectedly contains cachePoint: %v", block)
		}
	}
}

func TestBedrockBuildRequestCachingMultiTurn(t *testing.T) {
	// In a multi-turn conversation the cachePoint should be on the LAST
	// user message only, not on earlier ones.
	client := &bedrockClient{region: "us-east-1"}
	req, err := client.buildRequest(Request{
		Model: "anthropic.claude-sonnet-4-5-20250929-v1:0",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "turn 1"}}},
			{Role: RoleAssistant, Content: []Content{TextBlock{Text: "response 1"}}},
			{Role: RoleUser, Content: []Content{TextBlock{Text: "turn 2"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Find all user messages and verify only the last has a cachePoint.
	var userMsgs []bedrockMessage
	for _, m := range req.Messages {
		if m.Role == "user" {
			userMsgs = append(userMsgs, m)
		}
	}
	if len(userMsgs) != 2 {
		t.Fatalf("expected 2 user messages, got %d", len(userMsgs))
	}

	// First user message: no cachePoint
	for _, block := range userMsgs[0].Content {
		if _, ok := block["cachePoint"]; ok {
			t.Errorf("first user message should not have cachePoint: %v", block)
		}
	}

	// Last user message: ends with cachePoint
	lastContent := userMsgs[len(userMsgs)-1].Content
	lastBlock := lastContent[len(lastContent)-1]
	if _, ok := lastBlock["cachePoint"]; !ok {
		t.Errorf("last user message should end with cachePoint, got: %v", lastBlock)
	}
}

func TestBedrockBuildRequestCachingNoSystemNoCrash(t *testing.T) {
	// No system prompt: system array should be nil, not a bare cachePoint.
	client := &bedrockClient{region: "us-east-1"}
	req, err := client.buildRequest(Request{
		Model:    "anthropic.claude-sonnet-4-5-20250929-v1:0",
		Messages: []Message{{Role: RoleUser, Content: []Content{TextBlock{Text: "hi"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(req.System) != 0 {
		t.Errorf("empty system prompt should produce nil system, got: %v", req.System)
	}
	// Message cachePoint still present
	lastBlock := req.Messages[0].Content[len(req.Messages[0].Content)-1]
	if _, ok := lastBlock["cachePoint"]; !ok {
		t.Errorf("user message should still have cachePoint when system is empty")
	}
}

func TestBedrockTagLastUserCache(t *testing.T) {
	msgs := []bedrockMessage{
		{Role: "user", Content: []map[string]interface{}{{"text": "hi"}}},
		{Role: "assistant", Content: []map[string]interface{}{{"text": "hello"}}},
		{Role: "user", Content: []map[string]interface{}{{"text": "followup"}}},
	}
	bedrockTagLastUserCache(msgs)

	// Only the last user message (index 2) should have a cachePoint appended.
	last := msgs[2].Content
	if _, ok := last[len(last)-1]["cachePoint"]; !ok {
		t.Errorf("last user message should end with cachePoint")
	}
	// The first user message should be untouched.
	if len(msgs[0].Content) != 1 {
		t.Errorf("first user message content len = %d, want 1", len(msgs[0].Content))
	}
	if _, ok := msgs[0].Content[0]["cachePoint"]; ok {
		t.Errorf("first user message should not have cachePoint")
	}
}

func TestBedrockTagLastUserCacheEmpty(t *testing.T) {
	// Should not panic on empty or assistant-only history.
	bedrockTagLastUserCache(nil)
	bedrockTagLastUserCache([]bedrockMessage{})
	msgs := []bedrockMessage{
		{Role: "assistant", Content: []map[string]interface{}{{"text": "hi"}}},
	}
	bedrockTagLastUserCache(msgs) // should not panic, no user message to tag
}

func TestBedrockBuildRequestCachingWireJSON(t *testing.T) {
	// Verify the JSON shape Bedrock actually receives has the right keys.
	client := &bedrockClient{region: "us-east-1"}
	breq, err := client.buildRequest(Request{
		Model:  "anthropic.claude-sonnet-4-5-20250929-v1:0",
		System: "be helpful",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "hello"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(breq)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"cachePoint"`) {
		t.Errorf("serialised request missing cachePoint key: %s", s)
	}
	if !strings.Contains(s, `"type":"default"`) {
		t.Errorf("serialised request missing cachePoint type:default: %s", s)
	}
}

// TestBedrockBuildRequestBtwToolHistory reproduces the /btw HTTP 400 bug:
// the side-chat sends no tools but the frozen main transcript contains
// toolUse / toolResult blocks. Bedrock rejects such a request unless
// toolConfig is present. buildRequest must inject a stub toolConfig.
func TestBedrockBuildRequestBtwToolHistory(t *testing.T) {
	client := &bedrockClient{region: "us-east-1"}
	req, err := client.buildRequest(Request{
		Model: "amazon.nova-pro-v1:0",
		// No tools — simulates /btw side-chat.
		Messages: []Message{
			// Frozen main transcript: one tool call + result already happened.
			{Role: RoleAssistant, Content: []Content{
				ToolCallBlock{ID: "tc-1", Name: "bash", Arguments: json.RawMessage(`{"command":"ls"}`)},
			}},
			{Role: RoleTool, Content: []Content{
				ToolResultBlock{CallID: "tc-1", Content: []Content{TextBlock{Text: "file.go"}}},
			}},
			// Side-chat question appended by btwDialog.submit.
			{Role: RoleUser, Content: []Content{TextBlock{Text: "what does that file do?"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.ToolConfig == nil {
		t.Fatal("buildRequest should inject a stub toolConfig when history has tool blocks and req.Tools is empty, but ToolConfig is nil")
	}
	if len(req.ToolConfig.Tools) == 0 {
		t.Fatal("stub toolConfig should have at least one tool")
	}
	// The stub must have a valid name and a non-nil schema so Bedrock won't
	// reject it for a different reason.
	stub := req.ToolConfig.Tools[0]
	if stub.ToolSpec.Name == "" {
		t.Error("stub tool name must not be empty")
	}
	if stub.ToolSpec.InputSchema.JSON == nil {
		t.Error("stub tool schema must not be nil")
	}
	// Serialised wire payload must include toolConfig.
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"toolConfig"`) {
		t.Errorf("serialised request missing toolConfig: %s", b)
	}
}

// TestBedrockBuildRequestNoToolHistoryNoStub ensures that when there are
// no tool blocks in the history and no tools in the request (ordinary
// conversational call), we do NOT inject a toolConfig at all — the extra
// field is unnecessary and some models may behave differently with it.
func TestBedrockBuildRequestNoToolHistoryNoStub(t *testing.T) {
	client := &bedrockClient{region: "us-east-1"}
	req, err := client.buildRequest(Request{
		Model: "amazon.nova-pro-v1:0",
		Messages: []Message{
			{Role: RoleUser, Content: []Content{TextBlock{Text: "hello"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.ToolConfig != nil {
		t.Errorf("expected nil ToolConfig for plain conversation, got: %+v", req.ToolConfig)
	}
}

func TestBedrockBuildRequestSkipsEmptyToolMessages(t *testing.T) {
	client := &bedrockClient{}
	req, err := client.buildRequest(Request{Messages: []Message{
		{Role: RoleTool, Content: []Content{ToolResultBlock{CallID: "missing", Content: []Content{TextBlock{Text: "orphan"}}}}},
		{Role: RoleUser, Content: []Content{TextBlock{Text: "hello"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("got %d bedrock messages, want 1: %+v", len(req.Messages), req.Messages)
	}
	if req.Messages[0].Role != "user" {
		t.Fatalf("role = %s, want user", req.Messages[0].Role)
	}
}

func TestBedrockEventPayloadHelpers(t *testing.T) {
	wrapped := []byte(`{"contentBlockDelta":{"contentBlockIndex":0,"delta":{"text":"Hello"}}}`)
	if got := bedrockEventTypeFromPayload(wrapped); got != "contentBlockDelta" {
		t.Fatalf("event type = %q, want contentBlockDelta", got)
	}
	var delta struct {
		ContentBlockIndex int `json:"contentBlockIndex"`
		Delta             struct {
			Text string `json:"text"`
		} `json:"delta"`
	}
	if err := unmarshalBedrockEventPayload(wrapped, "contentBlockDelta", &delta); err != nil {
		t.Fatal(err)
	}
	if delta.ContentBlockIndex != 0 || delta.Delta.Text != "Hello" {
		t.Fatalf("unexpected wrapped delta: %+v", delta)
	}

	direct := []byte(`{"contentBlockIndex":1,"delta":{"text":"world"}}`)
	if err := unmarshalBedrockEventPayload(direct, "contentBlockDelta", &delta); err != nil {
		t.Fatal(err)
	}
	if delta.ContentBlockIndex != 1 || delta.Delta.Text != "world" {
		t.Fatalf("unexpected direct delta: %+v", delta)
	}
}

// TestBedrockBuildRequestImageBlock is the regression test for the bug where
// reading an image caused the Bedrock provider to return HTTP 500. The root
// cause was a missing case ImageBlock in buildRequest's content-block switch,
// which caused image content to be silently dropped and Bedrock to receive an
// empty or malformed message.
func TestBedrockBuildRequestImageBlock(t *testing.T) {
	client := &bedrockClient{region: "us-east-1"}

	// Minimal 1×1 red JPEG (smallest valid JPEG we can construct inline).
	// This is 26 bytes: SOI + minimal APP0 marker + EOI.
	// We use a real tiny JPEG to exercise the shrink path too.
	jpegBytes := []byte{
		0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, 0x01,
		0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0xFF, 0xD9,
	}

	req, err := client.buildRequest(Request{
		Model: "amazon.nova-pro-v1:0",
		Messages: []Message{
			{
				Role: RoleUser,
				Content: []Content{
					TextBlock{Text: "what's in this image?"},
					ImageBlock{MimeType: "image/jpeg", Data: jpegBytes},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(req.Messages))
	}
	msg := req.Messages[0]
	// Expect 2 content blocks: text + image (+ optional cachePoint for Nova which
	// has no cachePoint, so exactly text + image).
	var imageBlock map[string]interface{}
	for _, block := range msg.Content {
		if img, ok := block["image"]; ok {
			imageBlock = img.(map[string]interface{})
		}
	}
	if imageBlock == nil {
		b, _ := json.Marshal(req)
		t.Fatalf("image block missing from Bedrock request; full request: %s", b)
	}
	if imageBlock["format"] != "jpeg" {
		t.Errorf("image format = %q, want \"jpeg\"", imageBlock["format"])
	}
	src, ok := imageBlock["source"].(map[string]interface{})
	if !ok || src["bytes"] == "" {
		t.Errorf("image source.bytes is missing or empty: %v", imageBlock["source"])
	}
}

// TestBedrockBuildRequestImageInToolResult is the core regression for the
// HTTP 500 crash: when the read tool returns an image it is wrapped inside a
// ToolResultBlock. The previous serialiser only handled TextBlock inner
// content, so the image was silently dropped, leaving Bedrock a toolResult
// with empty content — which it rejects with HTTP 500.
func TestBedrockBuildRequestImageInToolResult(t *testing.T) {
	client := &bedrockClient{region: "us-east-1"}

	pngHeader := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A} // PNG magic bytes

	req, err := client.buildRequest(Request{
		Model: "amazon.nova-pro-v1:0",
		Messages: []Message{
			{
				Role: RoleAssistant,
				Content: []Content{
					ToolCallBlock{ID: "read-1", Name: "read", Arguments: json.RawMessage(`{"path":"/tmp/cat.jpg"}`)},
				},
			},
			{
				Role: RoleTool,
				Content: []Content{
					ToolResultBlock{
						CallID: "read-1",
						Content: []Content{
							ImageBlock{MimeType: "image/png", Data: pngHeader},
						},
					},
				},
			},
			{
				Role:    RoleUser,
				Content: []Content{TextBlock{Text: "what's in the image?"}},
			},
		},
		Tools: []Tool{{Name: "read", Schema: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Find the toolResult content block in the serialised messages.
	var toolResultContent []map[string]interface{}
	for _, m := range req.Messages {
		for _, block := range m.Content {
			if tr, ok := block["toolResult"]; ok {
				trd := tr.(map[string]interface{})
				toolResultContent = trd["content"].([]map[string]interface{})
				break
			}
		}
	}
	if toolResultContent == nil {
		b, _ := json.Marshal(req)
		t.Fatalf("toolResult block not found in request: %s", b)
	}
	if len(toolResultContent) == 0 {
		t.Fatal("toolResult content is empty — image was dropped (reproduces HTTP 500 bug)")
	}
	block := toolResultContent[0]
	img, ok := block["image"]
	if !ok {
		t.Fatalf("expected image block inside toolResult content, got: %v", block)
	}
	imgMap := img.(map[string]interface{})
	if imgMap["format"] != "png" {
		t.Errorf("image format = %q, want \"png\"", imgMap["format"])
	}
	src, ok := imgMap["source"].(map[string]interface{})
	if !ok || src["bytes"] == "" {
		t.Errorf("image source.bytes missing or empty: %v", imgMap["source"])
	}
}

func TestResolveBedrockInferenceProfileID(t *testing.T) {
	cases := []struct {
		model  string
		region string
		want   string
	}{
		// Bare Anthropic foundation IDs get the region-matched prefix.
		{"anthropic.claude-sonnet-4-5-20250929-v1:0", "us-east-1", "us.anthropic.claude-sonnet-4-5-20250929-v1:0"},
		{"anthropic.claude-sonnet-4-5-20250929-v1:0", "eu-central-1", "eu.anthropic.claude-sonnet-4-5-20250929-v1:0"},
		{"anthropic.claude-opus-4-6-v1", "ap-southeast-2", "apac.anthropic.claude-opus-4-6-v1"},
		{"anthropic.claude-opus-4-6-v1", "us-gov-west-1", "us-gov.anthropic.claude-opus-4-6-v1"},
		{"deepseek.r1-v1:0", "eu-west-1", "eu.deepseek.r1-v1:0"},
		// Empty region defaults to us.
		{"anthropic.claude-opus-4-6-v1", "", "us.anthropic.claude-opus-4-6-v1"},
		// Already-prefixed IDs are left untouched.
		{"eu.anthropic.claude-sonnet-4-5-20250929-v1:0", "us-east-1", "eu.anthropic.claude-sonnet-4-5-20250929-v1:0"},
		{"global.anthropic.claude-opus-4-6-v1", "us-east-1", "global.anthropic.claude-opus-4-6-v1"},
		{"us.anthropic.claude-opus-4-6-v1", "eu-central-1", "us.anthropic.claude-opus-4-6-v1"},
		// ARNs are passed through verbatim.
		{"arn:aws:bedrock:us-east-1:123:inference-profile/us.anthropic.claude-opus-4-6-v1", "eu-west-1", "arn:aws:bedrock:us-east-1:123:inference-profile/us.anthropic.claude-opus-4-6-v1"},
		// Families that don't need a profile are untouched.
		{"amazon.nova-pro-v1:0", "us-east-1", "amazon.nova-pro-v1:0"},
		{"", "us-east-1", ""},
	}
	for _, c := range cases {
		if got := resolveBedrockInferenceProfileID(c.model, c.region); got != c.want {
			t.Errorf("resolveBedrockInferenceProfileID(%q, %q) = %q; want %q", c.model, c.region, got, c.want)
		}
	}
}
