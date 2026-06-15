package core

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"terva.sh/terva/packages/provider"
)

// mirrorFakeClient speaks a chat-completions-shaped wire: it opts into
// the tool-image mirror (MirrorsToolImages), asks for one tool call,
// then finishes.
type mirrorFakeClient struct{ calls int32 }

func (c *mirrorFakeClient) Name() string { return "mirror-fake" }
func (c *mirrorFakeClient) Capabilities() provider.ClientCapabilities {
	return provider.ClientCapabilities{MirrorsToolImages: true}
}

func (c *mirrorFakeClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	call := atomic.AddInt32(&c.calls, 1)
	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "mirror-fake", Model: req.Model}
		if call == 1 {
			out <- provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
				Role: provider.RoleAssistant,
				Content: []provider.Content{
					provider.ToolCallBlock{ID: "t1", Name: "shot", Arguments: json.RawMessage(`{}`)},
				},
			}}
			return
		}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "done"}},
		}}
	}()
	return out, nil
}

// imageTool returns a screenshot-shaped result.
type imageTool struct{}

func (imageTool) Name() string            { return "shot" }
func (imageTool) Description() string     { return "returns an image" }
func (imageTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (imageTool) Execute(ctx context.Context, args json.RawMessage, progress func(string)) (ToolResult, error) {
	return ToolResult{Content: []provider.Content{
		provider.ImageBlock{MimeType: "image/png", Data: []byte("bytes")},
	}}, nil
}

// The tool-image mirror runs only when BOTH the client's wire format
// needs it (MirrorsToolImages) and the model can see images at all
// (the image-input capability). Mirroring screenshots to a vision-less
// model wastes tokens at best and 400s at worst; unknown models keep
// the capability's default (true) so old behavior is preserved.
func TestToolImageMirrorGatedOnModelCapability(t *testing.T) {
	t.Cleanup(provider.ResetCatalogLayers)
	provider.ResetCatalogLayers()
	provider.RegisterExtraModel(provider.Model{
		Provider: "test", ID: "blind-model",
		Caps: map[provider.Capability]bool{provider.CapImageInput: false},
	})
	provider.RegisterExtraModel(provider.Model{Provider: "test", ID: "vision-model"})

	mirrored := func(model string) bool {
		a := NewAgent(&mirrorFakeClient{}, model, "sys", Registry{"shot": imageTool{}})
		if err := a.Prompt(context.Background(), "take a screenshot", nil, func(AgentEvent) {}); err != nil {
			t.Fatalf("Prompt(%s): %v", model, err)
		}
		for _, m := range a.Messages() {
			if m.Role != provider.RoleUser {
				continue
			}
			for _, c := range m.Content {
				if _, ok := c.(provider.ImageBlock); ok {
					return true
				}
			}
		}
		return false
	}

	if !mirrored("vision-model") {
		t.Error("mirror skipped for a vision model")
	}
	if !mirrored("model-nobody-registered") {
		t.Error("mirror skipped for an unknown model; unknown must keep the default (mirror)")
	}
	if mirrored("blind-model") {
		t.Error("mirror ran for an image-input:false model")
	}
}

// A mirror must be synthesized ONLY when the tool result actually carried an
// image. A text-only result (the common case — bash/read/write/edit output)
// must NOT produce one, else the model gets plain text wrongly prefixed
// "Tool output included the following image content:" (the codex round-trip
// the ACP/Zed capability probe surfaced).
func TestMirrorToolImagesOnlyWhenImagePresent(t *testing.T) {
	textOnly := provider.Message{
		Role: provider.RoleUser,
		Content: []provider.Content{provider.ToolResultBlock{
			CallID:  "t1",
			Content: []provider.Content{provider.TextBlock{Text: "ok\n--- short output ---"}},
		}},
	}
	if m := mirrorToolImagesAsUser(textOnly); len(m.Content) != 0 {
		t.Errorf("text-only tool result produced a mirror: %+v", m.Content)
	}

	withImage := provider.Message{
		Role: provider.RoleUser,
		Content: []provider.Content{provider.ToolResultBlock{
			CallID: "t1",
			Content: []provider.Content{
				provider.TextBlock{Text: "screenshot"},
				provider.ImageBlock{MimeType: "image/png", Data: []byte("bytes")},
			},
		}},
	}
	m := mirrorToolImagesAsUser(withImage)
	if !IsToolImageMirror(m) {
		t.Fatalf("image-bearing tool result did not produce a recognized mirror: %+v", m)
	}
	sawImage := false
	for _, c := range m.Content {
		if _, ok := c.(provider.ImageBlock); ok {
			sawImage = true
		}
	}
	if !sawImage {
		t.Error("mirror is missing the image block")
	}
}

// The synthetic mirror message carries a structural meta marker so
// consumers (TUI display, compaction) identify it without matching the
// prefix string. IsToolImageMirror checks the marker, with a fallback
// to the prefix for mirrors persisted before the marker existed.
func TestIsToolImageMirror(t *testing.T) {
	a := NewAgent(&mirrorFakeClient{}, "vision-model", "sys", Registry{"shot": imageTool{}})
	t.Cleanup(provider.ResetCatalogLayers)
	provider.ResetCatalogLayers()
	provider.RegisterExtraModel(provider.Model{Provider: "test", ID: "vision-model"})
	if err := a.Prompt(context.Background(), "shot", nil, func(AgentEvent) {}); err != nil {
		t.Fatal(err)
	}
	var marked bool
	for _, m := range a.Messages() {
		if m.Meta[toolImageMirrorMeta] == "true" {
			marked = true
			if !IsToolImageMirror(m) {
				t.Error("marked message not recognized by IsToolImageMirror")
			}
		}
	}
	if !marked {
		t.Fatal("mirror message was not stamped with the meta marker")
	}

	// Legacy fallback: a mirror from before the marker (no meta) is
	// still recognized by its prefix.
	legacy := provider.Message{Role: provider.RoleUser, Content: []provider.Content{
		provider.TextBlock{Text: ToolImageMirrorPrefix},
		provider.ImageBlock{MimeType: "image/png", Data: []byte("x")},
	}}
	if !IsToolImageMirror(legacy) {
		t.Error("legacy prefix mirror not recognized")
	}
	// A real user message that merely starts with text is not a mirror.
	if IsToolImageMirror(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "hello"}}}) {
		t.Error("ordinary user message misidentified as a mirror")
	}
}

func TestSerializeTranscriptSkipsMirror(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "real question"}}},
		{Role: provider.RoleUser, Meta: map[string]string{toolImageMirrorMeta: "true"}, Content: []provider.Content{
			provider.TextBlock{Text: ToolImageMirrorPrefix},
			provider.ImageBlock{MimeType: "image/png", Data: []byte("x")},
		}},
	}
	got := serializeTranscript(msgs)
	if !strings.Contains(got, "real question") {
		t.Error("real message lost from transcript")
	}
	if strings.Contains(got, ToolImageMirrorPrefix) {
		t.Error("mirror message leaked into the summarization transcript")
	}
}
