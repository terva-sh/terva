package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// maxGlobResults caps how many paths a single glob call returns. Paging
// past it uses the offset argument; the result reports the next offset.
const maxGlobResults = 1000

// GlobTool lists files whose path matches a glob pattern. Read-only and
// jailed to the same cwd as the file tools, it replaces `bash find`/`ls`
// for the common "what files match X" question with structured, capped,
// deterministically-ordered output.
type GlobTool struct {
	CWD     string
	Sandbox *Sandbox
}

type globArgs struct {
	Pattern        string `json:"pattern"`
	Path           string `json:"path,omitempty"`
	IncludeIgnored bool   `json:"include_ignored,omitempty"`
	MaxResults     int    `json:"max_results,omitempty"`
	Offset         int    `json:"offset,omitempty"`
}

const globSchema = `{"type":"object","properties":{` +
	`"pattern":{"type":"string","description":"Glob pattern matched against each file's path relative to the search root. ** matches any number of directories: use \"**/*.go\" to recurse, \"*.go\" for top-level only."},` +
	`"path":{"type":"string","description":"Directory to search under, relative to cwd. Defaults to cwd."},` +
	`"include_ignored":{"type":"boolean","description":"Include files normally pruned by .gitignore (.git is always skipped)."},` +
	`"max_results":{"type":"integer","description":"Max paths to return (default 1000)."},` +
	`"offset":{"type":"integer","description":"Skip this many matches before returning; use the next_offset from a truncated result to page."}` +
	`},"required":["pattern"]}`

func (t *GlobTool) Name() string { return "glob" }
func (t *GlobTool) Description() string {
	return "Find files whose path matches a glob pattern (e.g. \"**/*.go\", \"src/**/test_*.py\"). Returns paths relative to cwd in lexical order, honoring .gitignore. Prefer this over `bash find`/`ls`."
}
func (t *GlobTool) Schema() json.RawMessage { return json.RawMessage(globSchema) }

func (t *GlobTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var a globArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(a.Pattern) == "" {
		return core.ToolResult{}, fmt.Errorf("pattern is required")
	}
	if a.Offset < 0 {
		a.Offset = 0
	}
	limit := a.MaxResults
	if limit <= 0 || limit > maxGlobResults {
		limit = maxGlobResults
	}

	root, isDir, err := resolveSearchRoot(t.CWD, a.Path)
	if err != nil {
		return core.ToolResult{}, err
	}
	if err := t.Sandbox.CheckPath(root); err != nil {
		return core.ToolResult{}, err
	}
	if !isDir {
		return core.ToolResult{}, fmt.Errorf("%s is not a directory; glob searches under a directory", root)
	}

	cwd := t.CWD
	if cwd == "" {
		cwd = root
	}

	// Collect one extra past offset+limit so we can report whether more
	// remain without a second pass.
	want := a.Offset + limit + 1
	var matches []string
	walkErr := walkFiles(root, !a.IncludeIgnored, func(abs, rel string) error {
		if !matchGlob(a.Pattern, rel) {
			return nil
		}
		out := abs
		if r, err := filepath.Rel(cwd, abs); err == nil {
			out = filepath.ToSlash(r)
		}
		matches = append(matches, out)
		if len(matches) >= want {
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil && walkErr != filepath.SkipAll {
		return core.ToolResult{}, walkErr
	}

	total := len(matches)
	page := matches
	if a.Offset < len(page) {
		page = page[a.Offset:]
	} else {
		page = nil
	}
	truncated := false
	if len(page) > limit {
		page = page[:limit]
		truncated = true
	}

	var sb strings.Builder
	for _, p := range page {
		sb.WriteString(p)
		sb.WriteByte('\n')
	}
	if len(page) == 0 {
		sb.WriteString("(no files matched)\n")
	}
	nextOffset := 0
	if truncated {
		nextOffset = a.Offset + limit
		fmt.Fprintf(&sb, "\n... more matches; pass offset=%d to continue\n", nextOffset)
	}

	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: sb.String()}},
		Details: map[string]any{
			"matches":     len(page),
			"truncated":   truncated,
			"next_offset": nextOffset,
			"scanned":     total,
		},
	}, nil
}
