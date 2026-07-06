package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/agent/imagegen"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// GenerateImageTool turns a text prompt into an image via a configured imagegen
// backend, returns it inline for display, and — when `path` is given — writes it
// into the workspace through the sandbox so it composes with the rest of the
// tooling (e.g. placeholder assets during project work). It is the read tool's
// image branch in reverse. See docs/proposals/image-generation.md.
//
// It is registered only when an image backend is configured (build.go), and is
// a mutating tool (not in the read-only set), so it is approval-gated and absent
// in plan mode — it spends money and writes files.
type GenerateImageTool struct {
	CWD      string
	Sandbox  *Sandbox
	Registry *imagegen.Registry
}

type generateImageArgs struct {
	Prompt         string `json:"prompt"`
	Path           string `json:"path,omitempty"`
	Size           string `json:"size,omitempty"`
	N              int    `json:"n,omitempty"`
	Backend        string `json:"backend,omitempty"`
	NegativePrompt string `json:"negative_prompt,omitempty"`
}

const generateImageSchema = `{"type":"object","properties":{` +
	`"prompt":{"type":"string","description":"What to generate."},` +
	`"path":{"type":"string","description":"Optional workspace-relative path to save the image to (e.g. assets/hero.png). Omit to only display it inline. With n>1 an index is inserted before the extension."},` +
	`"size":{"type":"string","description":"Optional size like 1024x1024; defaults to the backend's configured size."},` +
	`"n":{"type":"integer","description":"Number of images to generate (default 1)."},` +
	`"backend":{"type":"string","description":"Optional image backend id; defaults to the configured backend."},` +
	`"negative_prompt":{"type":"string","description":"Optional description of what to avoid (only used by backends that support it)."}` +
	`},"required":["prompt"]}`

func (t *GenerateImageTool) Name() string { return "generate_image" }

func (t *GenerateImageTool) Description() string {
	return "Generate an image from a text prompt via the configured image backend. Returns the image inline; if `path` is given, also saves it into the workspace. Uses an external image service (costs money on hosted backends)."
}

func (t *GenerateImageTool) Schema() json.RawMessage { return json.RawMessage(generateImageSchema) }

func (t *GenerateImageTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var a generateImageArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(a.Prompt) == "" {
		return core.ToolResult{}, fmt.Errorf("prompt is required")
	}
	backend, err := t.Registry.Resolve(a.Backend)
	if err != nil {
		return core.ToolResult{}, err
	}
	if progress != nil {
		progress("generating image…")
	}
	res, err := backend.Generate(ctx, imagegen.Request{
		Prompt:         a.Prompt,
		NegativePrompt: a.NegativePrompt,
		Size:           a.Size,
		N:              a.N,
	})
	if err != nil {
		return core.ToolResult{}, err
	}

	// Text caption first so the model always has something to reason about
	// even when it can't see images, then the images themselves.
	content := []provider.Content{}
	var saved []string
	for i, img := range res.Images {
		content = append(content, provider.ImageBlock{MimeType: img.MimeType, Data: img.Data})
		if a.Path == "" {
			continue
		}
		dst := t.savePath(a.Path, i, len(res.Images), img.MimeType)
		if err := t.Sandbox.CheckPath(dst); err != nil {
			return core.ToolResult{}, err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return core.ToolResult{}, err
		}
		if err := os.WriteFile(dst, img.Data, 0o644); err != nil {
			return core.ToolResult{}, err
		}
		saved = append(saved, dst)
	}

	caption := fmt.Sprintf("Generated %d image(s) via %s", len(res.Images), res.Backend)
	if res.Model != "" {
		caption += " (" + res.Model + ")"
	}
	if len(saved) > 0 {
		caption += ", saved to " + strings.Join(saved, ", ")
	}
	return core.ToolResult{
		Content: append([]provider.Content{provider.TextBlock{Text: caption}}, content...),
		Details: map[string]any{
			"backend": res.Backend,
			"model":   res.Model,
			"count":   len(res.Images),
			"saved":   saved,
		},
	}, nil
}

// savePath resolves the destination for image i of total against the CWD,
// giving it an extension matching the MIME type when the caller omitted one and
// inserting a 1-based index before the extension when generating more than one.
func (t *GenerateImageTool) savePath(rel string, i, total int, mime string) string {
	p := resolvePath(t.CWD, rel)
	ext := filepath.Ext(p)
	if ext == "" {
		ext = imageExt(mime)
		p += ext
	}
	if total > 1 {
		p = fmt.Sprintf("%s-%d%s", strings.TrimSuffix(p, ext), i+1, ext)
	}
	return p
}

func imageExt(mime string) string {
	switch mime {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".png"
	}
}
