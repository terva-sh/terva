package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// ChatSender is the small affordance the chat-send tools call into.
// The real implementation lives in the interactive runtime and
// forwards to the active chat bridge (Telegram today; any connector
// tomorrow); tests can pass any stub.
//
// SendImage delivers a compressed inline image preview; SendDocument
// delivers the raw file (no recompression). Services like Telegram
// resize inline images to a preview format, which loses detail but
// renders in the conversation; documents preserve the original bytes
// but show up as a file the recipient downloads.
type ChatSender interface {
	// SendImage uploads path as an inline-rendered photo with an
	// optional caption. Returns an error if the bridge is not
	// active or the upload fails.
	SendImage(ctx context.Context, path, caption string) error
	// SendDocument uploads path as a raw attachment.
	SendDocument(ctx context.Context, path, caption string) error
	// Active reports whether a paired chat is currently
	// reachable. Tools surface a clear error to the model when it
	// tries to send without a connected bridge.
	Active() bool
}

// ChatSendImageTool exposes the bridge's image-send affordance to
// the model so a turn that comes in over a chat service can produce
// a real image reply (a screenshot, a generated chart, a downloaded
// asset) instead of a textual description of one. Only registered
// while a bridge is connected; deregistered on disconnect.
type ChatSendImageTool struct {
	CWD     string
	Sandbox *Sandbox
	Sender  ChatSender
}

type chatSendImageArgs struct {
	Path    string `json:"path"`
	Caption string `json:"caption,omitempty"`
}

const chatSendImageSchema = `{"type":"object","properties":{"path":{"type":"string","description":"absolute or cwd-relative path to a local image file (png/jpg/gif/webp)"},"caption":{"type":"string","description":"optional caption sent alongside the image"}},"required":["path"]}`

func (t *ChatSendImageTool) Name() string { return "chat_send_image" }
func (t *ChatSendImageTool) Description() string {
	return "Send a local image file to the paired chat (e.g. Telegram) as an inline image. Use when the user, chatting remotely, asks to see an image rather than have it described."
}
func (t *ChatSendImageTool) Schema() json.RawMessage {
	return json.RawMessage(chatSendImageSchema)
}

func (t *ChatSendImageTool) Execute(ctx context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	var a chatSendImageArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	if a.Path == "" {
		return core.ToolResult{}, fmt.Errorf("path is required")
	}
	if t.Sender == nil || !t.Sender.Active() {
		return core.ToolResult{
			IsError: true,
			Content: []provider.Content{provider.TextBlock{Text: "chat bridge is not connected; cannot send image"}},
		}, nil
	}
	path := resolvePath(t.CWD, a.Path)
	if err := t.Sandbox.CheckPath(path); err != nil {
		return core.ToolResult{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return core.ToolResult{}, err
	}
	if info.IsDir() {
		return core.ToolResult{}, fmt.Errorf("%s is a directory", path)
	}
	if mime := imageMIME(path); mime == "" {
		return core.ToolResult{
			IsError: true,
			Content: []provider.Content{provider.TextBlock{Text: fmt.Sprintf("%s is not a recognised image format (png/jpg/gif/webp); use chat_send_file for arbitrary attachments", path)}},
		}, nil
	}
	if err := t.Sender.SendImage(ctx, path, a.Caption); err != nil {
		return core.ToolResult{}, fmt.Errorf("send: %w", err)
	}
	kb := info.Size() / 1024
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: fmt.Sprintf("sent %s to the chat (%d KB)", path, kb)}},
	}, nil
}

// ChatSendFileTool uploads any local file to the paired chat as a
// document attachment. Use this for non-image files or when the model
// needs the recipient to receive the original bytes (no service-side
// compression). For images you usually want chat_send_image.
type ChatSendFileTool struct {
	CWD     string
	Sandbox *Sandbox
	Sender  ChatSender
}

type chatSendFileArgs struct {
	Path    string `json:"path"`
	Caption string `json:"caption,omitempty"`
}

const chatSendFileSchema = `{"type":"object","properties":{"path":{"type":"string","description":"absolute or cwd-relative path to any local file"},"caption":{"type":"string","description":"optional caption sent alongside the file"}},"required":["path"]}`

func (t *ChatSendFileTool) Name() string { return "chat_send_file" }
func (t *ChatSendFileTool) Description() string {
	return "Send a local file to the paired chat (e.g. Telegram) as a document attachment (no compression). Use for non-image files or when the recipient needs the original bytes."
}
func (t *ChatSendFileTool) Schema() json.RawMessage {
	return json.RawMessage(chatSendFileSchema)
}

func (t *ChatSendFileTool) Execute(ctx context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	var a chatSendFileArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	if a.Path == "" {
		return core.ToolResult{}, fmt.Errorf("path is required")
	}
	if t.Sender == nil || !t.Sender.Active() {
		return core.ToolResult{
			IsError: true,
			Content: []provider.Content{provider.TextBlock{Text: "chat bridge is not connected; cannot send file"}},
		}, nil
	}
	path := resolvePath(t.CWD, a.Path)
	if err := t.Sandbox.CheckPath(path); err != nil {
		return core.ToolResult{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return core.ToolResult{}, err
	}
	if info.IsDir() {
		return core.ToolResult{}, fmt.Errorf("%s is a directory", path)
	}
	if err := t.Sender.SendDocument(ctx, path, a.Caption); err != nil {
		return core.ToolResult{}, fmt.Errorf("send: %w", err)
	}
	kb := info.Size() / 1024
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: fmt.Sprintf("sent %s to the chat (%d KB)", path, kb)}},
	}, nil
}
