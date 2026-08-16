package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

const (
	// maxGrepResults caps matches returned per call; page past it with offset.
	maxGrepResults = 200
	// maxGrepLineLen truncates a single matched/context line so one
	// pathological long line can't dominate the result.
	maxGrepLineLen = 1000
)

// GrepTool searches file contents for a regular expression. Read-only
// and jailed like the file tools, it replaces `bash grep`/`rg` with
// structured, capped, deterministically-ordered output.
type GrepTool struct {
	CWD     string
	Sandbox *Sandbox
}

type grepArgs struct {
	Pattern        string `json:"pattern"`
	Path           string `json:"path,omitempty"`
	Glob           string `json:"glob,omitempty"`
	CaseSensitive  bool   `json:"case_sensitive,omitempty"`
	Context        int    `json:"context,omitempty"`
	MaxResults     int    `json:"max_results,omitempty"`
	Offset         int    `json:"offset,omitempty"`
	IncludeIgnored bool   `json:"include_ignored,omitempty"`
}

const grepSchema = `{"type":"object","properties":{` +
	`"pattern":{"type":"string","description":"The RE2 regular expression to find."},` +
	`"path":{"type":"string","description":"The file or directory to search, relative to the working directory. The default is the working directory."},` +
	`"glob":{"type":"string","description":"Search only the files with a path that agrees with this glob. The path is relative to the search root. An example is \"**/*.go\"."},` +
	`"case_sensitive":{"type":"boolean","description":"Make the match sensitive to letter case. The default is false, and the tool ignores letter case."},` +
	`"context":{"type":"integer","description":"The number of adjacent lines to show before and after each match. This is the same as the -C option of the grep command."},` +
	`"max_results":{"type":"integer","description":"The maximum number of matches to return. The default is 200."},` +
	`"offset":{"type":"integer","description":"The number of matches to skip before the tool returns results. To get the next page, use the next_offset value from a result that the tool cut short."},` +
	`"include_ignored":{"type":"boolean","description":"Also search the files that .gitignore usually removes. The tool always ignores the .git directory."}` +
	`},"required":["pattern"]}`

func (t *GrepTool) Name() string { return "grep" }
func (t *GrepTool) Description() string {
	return i18n.D("tool.grep.description", "Search the content of files for an RE2 regular expression. The tool returns each match as path:line:text, in a constant order. The tool obeys .gitignore and does not search binary files. Use this tool instead of the `grep` or `rg` commands in `bash`.")
}
func (t *GrepTool) Schema() json.RawMessage { return json.RawMessage(grepSchema) }

type grepMatch struct {
	rel  string // path relative to cwd, slash-separated
	line int    // 1-indexed line number of the match
	text string
}

func (t *GrepTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var a grepArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(a.Pattern) == "" {
		return core.ToolResult{}, fmt.Errorf("pattern is required%s", argHint(raw, grepSchema))
	}
	if a.Offset < 0 {
		a.Offset = 0
	}
	if a.Context < 0 {
		a.Context = 0
	}
	limit := a.MaxResults
	if limit <= 0 || limit > maxGrepResults {
		limit = maxGrepResults
	}

	expr := a.Pattern
	if !a.CaseSensitive {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid pattern: %w", err)
	}

	root, isDir, err := resolveSearchRoot(t.CWD, a.Path)
	if err != nil {
		return core.ToolResult{}, err
	}
	// Read-side check: also allows registered read-only roots.
	if err := t.Sandbox.CheckPathRead(root); err != nil {
		return core.ToolResult{}, err
	}

	cwd := t.CWD
	if cwd == "" {
		cwd = root
	}
	relToCWD := func(abs string) string {
		if r, err := filepath.Rel(cwd, abs); err == nil {
			return filepath.ToSlash(r)
		}
		return abs
	}

	// Keep the file's full line set keyed by match index so context can be
	// rendered after paging. We collect matches in walk order and stop once
	// we have a page plus one (to detect "more").
	want := a.Offset + limit + 1
	var matches []grepMatch
	// fileLines caches the lines of files that produced a returned match,
	// so context rendering doesn't re-read.
	fileLines := map[string][]string{}
	filesScanned := 0

	process := func(abs, rel string) error {
		if a.Glob != "" && !matchGlob(a.Glob, rel) {
			return nil
		}
		data, ok := readTextFile(abs)
		if !ok {
			return nil
		}
		filesScanned++
		lines := strings.Split(string(data), "\n")
		if n := len(lines); n > 0 && lines[n-1] == "" {
			lines = lines[:n-1]
		}
		outRel := relToCWD(abs)
		for i, line := range lines {
			if re.MatchString(line) {
				matches = append(matches, grepMatch{rel: outRel, line: i + 1, text: line})
				if len(matches) >= want {
					fileLines[outRel] = lines
					return filepath.SkipAll
				}
			}
		}
		if a.Context > 0 {
			fileLines[outRel] = lines
		}
		return nil
	}

	if isDir {
		walkErr := walkFiles(root, !a.IncludeIgnored, process)
		if walkErr != nil && walkErr != filepath.SkipAll {
			return core.ToolResult{}, walkErr
		}
	} else {
		// A single named file is searched directly, regardless of ignore
		// rules — the user asked for it by name.
		if err := process(root, filepath.Base(root)); err != nil && err != filepath.SkipAll {
			return core.ToolResult{}, err
		}
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

	out := renderGrep(page, fileLines, a.Context)
	nextOffset := 0
	if truncated {
		nextOffset = a.Offset + limit
		out += fmt.Sprintf("\n... more matches; pass offset=%d to continue\n", nextOffset)
	}

	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: out}},
		Details: map[string]any{
			"matches":       len(page),
			"truncated":     truncated,
			"next_offset":   nextOffset,
			"files_scanned": filesScanned,
			"total_matches": total,
		},
	}, nil
}

// renderGrep formats matches as path:line:text lines. With context > 0,
// each match is shown with surrounding lines (path-line-text, dash-
// separated like grep -C) and blocks are divided by "--".
func renderGrep(matches []grepMatch, fileLines map[string][]string, context int) string {
	if len(matches) == 0 {
		return "(no matches)\n"
	}
	var sb strings.Builder
	for idx, m := range matches {
		if context <= 0 {
			fmt.Fprintf(&sb, "%s:%d:%s\n", m.rel, m.line, clip(m.text))
			continue
		}
		if idx > 0 {
			sb.WriteString("--\n")
		}
		lines := fileLines[m.rel]
		start := m.line - context
		if start < 1 {
			start = 1
		}
		end := m.line + context
		if end > len(lines) {
			end = len(lines)
		}
		for ln := start; ln <= end; ln++ {
			sep := "-"
			if ln == m.line {
				sep = ":"
			}
			fmt.Fprintf(&sb, "%s%s%d%s%s\n", m.rel, sep, ln, sep, clip(lines[ln-1]))
		}
	}
	return sb.String()
}

func clip(s string) string {
	if len(s) > maxGrepLineLen {
		return s[:maxGrepLineLen] + "…"
	}
	return s
}

// readTextFile reads a file up to the same hard cap as the read tool and
// reports whether it is usable text (ok=false for read errors or binary
// content). Binary detection reuses the read tool's NUL-byte heuristic.
func readTextFile(path string) (data []byte, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	limited := io.LimitReader(f, int64(maxReadFileBytes))
	b, err := io.ReadAll(limited)
	if err != nil {
		return nil, false
	}
	if looksBinary(b) {
		return nil, false
	}
	return b, true
}
