// Package tools implements terva's built-in tools: read, write, edit, bash.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

const (
	maxReadLines = 2000
	maxReadBytes = 50 * 1024
	// maxReadFileBytes bounds how much of a file we pull into memory
	// before line-splitting and applying offset/limit. It is much larger
	// than maxReadBytes so offset-based paging can reach content past the
	// 50KiB result cap, but still finite so a multi-GB file cannot OOM us.
	maxReadFileBytes = 10 * 1024 * 1024
	// maxImageBytes caps inline image reads. Images are returned whole
	// (no paging), so an oversized image would otherwise be loaded
	// entirely into memory and shipped to the model verbatim.
	maxImageBytes = 5 * 1024 * 1024
)

// ReadTool reads file contents from disk.
type ReadTool struct {
	CWD     string
	Sandbox *Sandbox // when jailed, confines reads to the sandbox root

	// SupportsVision reports whether the active model can consume image
	// pixels. When true, reading an image file returns an inline
	// ImageBlock for the vision model. When false, the image branch
	// returns a text result explaining the file is an image whose pixels
	// were NOT sent (the provider would silently drop the block) and how
	// to enable vision — rather than shipping a block that vanishes.
	SupportsVision bool
}

type readArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

const readSchema = `{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer"},"limit":{"type":"integer"}},"required":["path"]}`

func (t *ReadTool) Name() string { return "read" }
func (t *ReadTool) Description() string {
	return "Read a file from disk. This is also THE way to inspect or analyze a LOCAL image: pass an image path (png/jpg/gif/webp) and its pixels are returned inline for a vision-capable model to see."
}
func (t *ReadTool) Schema() json.RawMessage { return json.RawMessage(readSchema) }

func (t *ReadTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var a readArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	if a.Path == "" {
		return core.ToolResult{}, fmt.Errorf("path is required")
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

	// Image handling.
	if mime := imageMIME(path); mime != "" {
		if info.Size() > maxImageBytes {
			return core.ToolResult{}, fmt.Errorf("%s is %d bytes; image reads are capped at %d bytes", a.Path, info.Size(), maxImageBytes)
		}
		// Non-vision model: an ImageBlock would be silently dropped at
		// serialization, so the model would "see" nothing and not know
		// why. Return a successful TEXT result instead — it names the
		// file as an image and tells the agent how to actually inspect
		// it (switch models, or tag a vision-capable local model in
		// models.json). Not an error: a text result lets the agent act.
		if !t.SupportsVision {
			return core.ToolResult{
				Content: []provider.Content{provider.TextBlock{Text: fmt.Sprintf(
					"%s is an image (%s, %d bytes). Its pixels were NOT sent: the active model isn't marked vision-capable, so an inline image block would be dropped and the model would see nothing.\n"+
						"To inspect it: switch to a vision-capable model with /model, or — if this model DOES support images (e.g. a local one) — mark it in models.json with \"capabilities\": {\"image-input\": true} and re-run.",
					a.Path, mime, info.Size())}},
				Details: map[string]any{
					"path":           path,
					"image":          true,
					"mime":           mime,
					"bytes":          info.Size(),
					"vision_dropped": true,
				},
			}, nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return core.ToolResult{}, err
		}
		return core.ToolResult{
			Content: []provider.Content{provider.ImageBlock{MimeType: mime, Data: data}},
		}, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return core.ToolResult{}, err
	}
	defer f.Close()

	// Pull the file into memory up to a generous hard cap so offset-based
	// paging can reach content beyond the 50KiB *result* cap. The 50KiB
	// cap (maxReadBytes) is applied LATER, to the selected slice, not here
	// — applying it before offset/limit would make read(path, offset=N)
	// return nothing for any N past 50KiB.
	limited := io.LimitReader(f, int64(maxReadFileBytes)+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return core.ToolResult{}, err
	}
	truncFile := len(data) > maxReadFileBytes
	if truncFile {
		data = data[:maxReadFileBytes]
	}

	if looksBinary(data) {
		return core.ToolResult{}, fmt.Errorf("%s looks binary; refusing to read as text", a.Path)
	}

	lines := strings.Split(string(data), "\n")
	// Trim trailing empty line from final \n.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}

	start := 0
	if a.Offset > 0 {
		start = a.Offset - 1
		if start > len(lines) {
			start = len(lines)
		}
	}
	end := len(lines)
	if a.Limit > 0 && start+a.Limit < end {
		end = start + a.Limit
	}
	selected := lines[start:end]

	truncLines := false
	if len(selected) > maxReadLines {
		selected = selected[:maxReadLines]
		truncLines = true
	}

	// Apply the byte cap to the SELECTED slice (post offset/limit), so a
	// large window is trimmed but paging still works. We count bytes
	// including the trailing newline we emit per line. We always keep at
	// least one line so a single over-cap line still returns content
	// (truncated) rather than an empty result.
	truncBytes := false
	byteCount := 0
	for i, line := range selected {
		byteCount += len(line) + 1 // +1 for the '\n' we emit below
		if byteCount > maxReadBytes && i > 0 {
			selected = selected[:i]
			truncBytes = true
			break
		}
	}

	// Raw file contents go to the model. We deliberately DON'T
	// prepend line numbers here: they'd inflate the token count by
	// ~15-20% on typical source files (7 bytes per line, every
	// line, every time the file gets re-sent as context on later
	// turns) and the model doesn't need them — edit goes through
	// exact-match text replacement, not line ranges.
	//
	// The TUI renders its own gutter using the start offset stored
	// in Details, so the on-screen view still looks like cat -n.
	var sb strings.Builder
	for _, line := range selected {
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	if truncLines || truncBytes || truncFile {
		sb.WriteString("\n")
	}
	if truncLines {
		sb.WriteString(fmt.Sprintf("... [truncated at %d lines]\n", maxReadLines))
	}
	if truncBytes {
		sb.WriteString(fmt.Sprintf("... [truncated at %d bytes]\n", maxReadBytes))
	}
	if truncFile {
		sb.WriteString(fmt.Sprintf("... [file exceeds %d bytes; tail not loaded — page with offset]\n", maxReadFileBytes))
	}

	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: sb.String()}},
		Details: map[string]any{
			"path":            path,
			"start_line":      start + 1, // 1-indexed; TUI draws the gutter
			"lines_truncated": truncLines,
			"bytes_truncated": truncBytes,
			"file_truncated":  truncFile,
			"total_lines":     len(lines),
		},
	}, nil
}

func resolvePath(cwd, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	return filepath.Join(cwd, p)
}

func imageMIME(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	}
	return ""
}

// looksBinary returns true if the buffer contains a NUL byte in its first 8 KiB.
func looksBinary(b []byte) bool {
	n := len(b)
	if n > 8192 {
		n = 8192
	}
	for i := 0; i < n; i++ {
		if b[i] == 0 {
			return true
		}
	}
	return false
}
