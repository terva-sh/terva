package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/ignore"
	"terva.sh/terva/packages/provider"
)

// WriteTool writes content to a file, creating parent directories.
type WriteTool struct {
	CWD     string
	Sandbox *Sandbox
	// Files records what the model has seen of each path (see ReadTool.Files).
	Files *FileState
}

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    string `json:"mode"`
}

const writeSchema = `{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"},"mode":{"type":"string","description":"Optional octal permission bits. For example, use \"0755\" to make an executable script. The permitted range is 0o000 to 0o777. The tool does not permit the setuid, setgid, or sticky bits. If you omit this, the tool keeps the secure default: a new file obeys the umask, and a file that exists keeps its mode."}},"required":["path","content"]}`

func (t *WriteTool) Name() string { return "write" }
func (t *WriteTool) Description() string {
	return i18n.D("tool.write.description", "Write a file. The tool makes the parent directories if they do not exist. This tool replaces all the content of the file.\n\nTo change part of a file that exists, use the `edit` tool. Use `write` only to make a new file, or to replace all of a file.\n\nTo set the permission bits in the same step, give `mode` as an octal value. For example, use \"0755\" to make an executable script, instead of a `chmod` command later. If you omit `mode`, the tool keeps the secure default. A new file obeys the umask, and a file that exists keeps its mode.")
}
func (t *WriteTool) Schema() json.RawMessage { return json.RawMessage(writeSchema) }

func (t *WriteTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var a writeArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	if a.Path == "" {
		return core.ToolResult{}, fmt.Errorf("path is required%s", argHint(raw, writeSchema))
	}
	path := resolvePath(t.CWD, a.Path)
	if err := t.Sandbox.CheckPath(path); err != nil {
		return core.ToolResult{}, err
	}

	// An explicit mode is parsed before any write, so a bad value fails without
	// touching the filesystem. Omitted, the secure default stands: a new file
	// honors the umask (private under a 0077 umask) and an existing file keeps
	// its mode — so a write can never silently broaden a secret file.
	perm := os.FileMode(0o644)
	modeSet := a.Mode != ""
	if modeSet {
		p, err := parseFileMode(a.Mode)
		if err != nil {
			return core.ToolResult{}, err
		}
		perm = p
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return core.ToolResult{}, err
	}
	if err := os.WriteFile(path, []byte(a.Content), perm); err != nil {
		return core.ToolResult{}, err
	}
	// The model now knows this file exactly — it just authored it.
	t.Files.Record(path, []byte(a.Content), "write")
	// os.WriteFile applies perm only when it creates the file, and even then the
	// umask masks it; on an overwrite it changes nothing. Chmod pins the exact
	// bits so an explicit mode is honored under any umask and on an overwrite.
	// Creating with perm first means the file is never looser than requested,
	// even in the window before this Chmod.
	if modeSet {
		if err := os.Chmod(path, perm); err != nil {
			return core.ToolResult{}, fmt.Errorf("set mode %s: %w", a.Mode, err)
		}
	}

	// Return the file content as the result body, just like `read`
	// does. The TUI renders it with a syntax-highlighted gutter so
	// the on-screen view after a `write` matches the pre-write
	// streaming preview seamlessly. The model also sees the written
	// content in its tool_result, which is useful on follow-up turns
	// where it wants to reference what it just wrote without a
	// second `read` call.
	totalLines := strings.Count(a.Content, "\n")
	if len(a.Content) > 0 && !strings.HasSuffix(a.Content, "\n") {
		totalLines++ // count the last unterminated line
	}
	content := []provider.Content{provider.TextBlock{Text: a.Content}}
	details := map[string]any{
		"path":        path,
		"bytes":       len(a.Content),
		"total_lines": totalLines,
		"start_line":  1,
	}
	if modeSet {
		details["mode"] = fmt.Sprintf("%#o", perm) // e.g. 0755 — recorded so the mode change is reviewable
	}
	// A newly written file under a .gitignore rule is a silent trap: the write
	// succeeds, but the file won't show up in workspace diffs, grep/glob, or git
	// status, so a later "why isn't my change there?" has no visible cause. Warn
	// in the model-visible body — without changing ignore semantics or the file.
	if ignore.Ignored(t.CWD, path) {
		details["gitignored"] = true
		content = append([]provider.Content{provider.TextBlock{
			Text: fmt.Sprintf("warning: %s is ignored by .gitignore — it will not appear in workspace diffs, grep/glob, or git status.", a.Path),
		}}, content...)
	}
	return core.ToolResult{
		Content: content,
		Details: details,
		// A whole-file write counts as added lines (overwrites don't subtract
		// the old content — unknown here, and "wrote N lines" is the honest
		// claim either way).
		LinesAdded: totalLines,
	}, nil
}

// parseFileMode parses an octal permission string ("0755", "755", or "0o755")
// into permission bits. It rejects anything outside 0o777: setuid, setgid, and
// the sticky bit are deliberately not settable through a reviewable write —
// those carry real escalation risk and need the bash tool and explicit intent.
func parseFileMode(s string) (os.FileMode, error) {
	t := strings.TrimSpace(s)
	t = strings.TrimPrefix(t, "0o")
	t = strings.TrimPrefix(t, "0O")
	v, err := strconv.ParseUint(t, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("mode %q must be octal permission bits, e.g. \"0755\"", s)
	}
	if v > 0o777 {
		return 0, fmt.Errorf("mode %q sets bits outside 0o777; set setuid/setgid/sticky with the bash tool", s)
	}
	return os.FileMode(v), nil
}
