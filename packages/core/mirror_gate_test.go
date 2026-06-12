package core

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"terva.sh/terva/packages/provider"
)

// mirrorFakeClient speaks a chat-completions-shaped wire: it opts into
// the tool-image mirror (MirrorsToolImages), asks for one tool call,
// then finishes.
type mirrorFakeClient struct{ calls int32 }

func (c *mirrorFakeClient) Name() string            { return "mirror-fake" }
func (c *mirrorFakeClient) MirrorsToolImages() bool { return true }

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
