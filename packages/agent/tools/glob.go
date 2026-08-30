package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
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
	`"pattern":{"type":"string","description":"A glob pattern. The tool compares it with the path of each file, relative to the search root. Two stars match any number of directories. Use \"**/*.go\" to search all the subdirectories. Use \"*.go\" to search the top level only."},` +
	`"path":{"type":"string","description":"The directory to search, relative to the working directory. The default is the working directory."},` +
	`"include_ignored":{"type":"boolean","description":"Also search the files that .gitignore usually removes. The tool always ignores the .git directory."},` +
	`"max_results":{"type":"integer","description":"The maximum number of paths to return. The default is 1000."},` +
	`"offset":{"type":"integer","description":"The number of matches to skip before the tool returns results. To get the next page, use the next_offset value from a result that the tool cut short."}` +
	`},"required":["pattern"]}`

func (t *GlobTool) Name() string { return "glob" }
func (t *GlobTool) Description() string {
	return i18n.D("tool.glob.description", "Find the files with a path that agrees with a glob pattern. Examples of a pattern are \"**/*.go\" and \"src/**/test_*.py\". The tool returns the paths relative to the working directory, in lexical order. The tool obeys .gitignore. Use this tool instead of the `find` or `ls` commands in `bash`.")
}
func (t *GlobTool) Schema() json.RawMessage { return json.RawMessage(globSchema) }

func (t *GlobTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var a globArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(a.Pattern) == "" {
		return core.ToolResult{}, fmt.Errorf("pattern is required%s", argHint(raw, globSchema))
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
		return core.ToolResult{}, notFoundError(t.CWD, a.Path, t.Sandbox.DisplayPath(root, a.Path), err)
	}
	// Read-side check: also allows registered read-only roots.
	if err := t.Sandbox.CheckPathRead(root); err != nil {
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
