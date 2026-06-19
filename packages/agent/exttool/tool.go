// Package exttool wraps an extension-registered tool as a core.Tool for
// the agent's tool registry. It is the one place the extproto tool-result
// shape is converted into core.ToolResult / provider.Content, shared by
// the live host (agent.extToolAdapter) and the acp wire harness so both
// exercise the identical conversion.
//
// It depends only on core + provider + extproto and reaches the owning
// extension through the small Invoker interface, so it never imports the
// extensions host layer (keeping that layer free of core/provider and
// avoiding an import cycle with packages that the agent package itself
// imports, e.g. acp).
package exttool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// Invoker is the slice of the extension manager this wrapper needs: it
// round-trips a tool call to the owning subprocess and returns the
// extproto result. Both *extensions.Manager and *extdriver.Driver
// satisfy it structurally.
type Invoker interface {
	InvokeTool(ctx context.Context, name string, args json.RawMessage, timeout time.Duration) (extproto.ToolResultFromExt, error)
}

// Info identifies the extension tool to wrap. It mirrors the
// (extension, name, description, schema) slice of the manager's ToolInfo
// the wrapper needs, declared here so this package depends on neither
// the extensions host layer nor extdriver.
type Info struct {
	Extension   string
	Name        string
	Description string
	Schema      json.RawMessage
}

// extensionTool wraps a single extension-registered tool as a
// core.Tool. The agent's tool registry contains one of these per
// extension tool; Execute round-trips through the invoker to the
// owning subprocess.
//
// One concrete type instead of a closure-driven anonymous tool
// keeps the schema, name, and ownership inspectable for logs and
// dialogs.
type extensionTool struct {
	name        string
	description string
	schema      json.RawMessage
	extension   string
	invoker     Invoker
	timeout     time.Duration
}

// New returns a core.Tool that round-trips invocations through inv to
// the extension that registered (name, schema). The default per-call
// timeout is 60 seconds.
func New(inv Invoker, info Info) core.Tool {
	return &extensionTool{
		name:        info.Name,
		description: info.Description,
		schema:      info.Schema,
		extension:   info.Extension,
		invoker:     inv,
		timeout:     60 * time.Second,
	}
}

func (t *extensionTool) Name() string            { return t.name }
func (t *extensionTool) Description() string     { return t.description }
func (t *extensionTool) Schema() json.RawMessage { return t.schema }
func (t *extensionTool) Extension() string       { return t.extension }

// Execute is what the agent calls when the LLM invokes the tool. It
// hands args to the owning extension, waits up to t.timeout for the
// reply, and converts the response into a core.ToolResult.
func (t *extensionTool) Execute(ctx context.Context, args json.RawMessage, _ func(string)) (core.ToolResult, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	resp, err := t.invoker.InvokeTool(ctx, t.name, args, t.timeout)
	if err != nil {
		return core.ToolResult{
			IsError: true,
			Content: []provider.Content{provider.TextBlock{Text: fmt.Sprintf("extension %s/%s failed: %v", t.extension, t.name, err)}},
		}, nil
	}
	out := core.ToolResult{IsError: resp.IsError}
	for _, b := range resp.Content {
		switch b.Type {
		case "text":
			if b.Text != "" {
				out.Content = append(out.Content, provider.TextBlock{Text: b.Text})
			}
		case "image":
			data, dErr := decodeBase64(b.Data)
			if dErr != nil {
				out.IsError = true
				out.Content = append(out.Content, provider.TextBlock{Text: fmt.Sprintf("extension %s/%s returned undecodable image: %v", t.extension, t.name, dErr)})
				continue
			}
			out.Content = append(out.Content, provider.ImageBlock{
				MimeType: b.MimeType,
				Data:     data,
			})
		default:
			// Unknown block type: stringify for debug visibility.
			out.Content = append(out.Content, provider.TextBlock{Text: fmt.Sprintf("[unknown block type %q from extension %s]", b.Type, t.extension)})
		}
	}
	if len(out.Content) == 0 {
		// Defensive: an empty content slice would confuse the model.
		out.Content = []provider.Content{provider.TextBlock{Text: "(extension returned no content)"}}
	}
	out.Details = map[string]any{"extension": t.extension, "tool": t.name}
	return out, nil
}

// decodeBase64 validates the extension's image data in one place. An
// empty string is treated as no data rather than an error.
func decodeBase64(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(s)
}
