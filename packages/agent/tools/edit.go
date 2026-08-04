package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// EditTool applies one or more exact-match replacements to a file.
type EditTool struct {
	CWD     string
	Sandbox *Sandbox
	// Files records what the model has seen of each path (see ReadTool.Files).
	// Read on a failed match to say whether the file moved underneath the edit.
	Files *FileState
}

type editOp struct {
	OldText    string `json:"oldText"`
	NewText    string `json:"newText"`
	ReplaceAll bool   `json:"replaceAll,omitempty"`
}

type editArgs struct {
	Path  string   `json:"path"`
	Edits []editOp `json:"edits"`
}

const editSchema = `{"type":"object","properties":{"path":{"type":"string"},"edits":{"type":"array","items":{"type":"object","properties":{"oldText":{"type":"string"},"newText":{"type":"string"},"replaceAll":{"type":"boolean","description":"Replace all the occurrences of oldText. If you set this, oldText does not have to be unique."}},"required":["oldText","newText"]}}},"required":["path","edits"]}`

func (t *EditTool) Name() string { return "edit" }
func (t *EditTool) Description() string {
	return i18n.D("tool.edit.description", "Change a file with exact-match replacements. First, read the file. Then make sure that each oldText value agrees exactly with the bytes on the disk. Each oldText value must occur one time only in the file. To change all the occurrences, set replaceAll to true.\n\nIf no text agrees exactly, the tool tries a match that permits different whitespace. This match applies only when the lines are the same and the indent moves by one equal amount. If the result is ambiguous, the tool does not change the file.")
}
func (t *EditTool) Schema() json.RawMessage { return json.RawMessage(editSchema) }

func (t *EditTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var a editArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	if a.Path == "" {
		return core.ToolResult{}, fmt.Errorf("path is required")
	}
	if len(a.Edits) == 0 {
		return core.ToolResult{}, fmt.Errorf("at least one edit is required")
	}
	path := resolvePath(t.CWD, a.Path)
	if err := t.Sandbox.CheckPath(path); err != nil {
		return core.ToolResult{}, err
	}

	orig, err := os.ReadFile(path)
	if err != nil {
		return core.ToolResult{}, err
	}

	// Detect BOM and line endings.
	var bom []byte
	if bytes.HasPrefix(orig, []byte{0xEF, 0xBB, 0xBF}) {
		bom = orig[:3]
		orig = orig[3:]
	}
	nl := detectLineEnding(orig)
	// Normalize to \n for matching.
	body := string(bytes.ReplaceAll(orig, []byte("\r\n"), []byte("\n")))

	// Normalize the model-supplied oldText/newText line endings to \n as
	// well. A model that copied bytes out of a `read` of a CRLF file may
	// hand us \r\n in oldText; without this the literal match against the
	// \n-normalized body would fail with a confusing "not found". We work
	// on \n-only copies throughout and re-apply the file's native ending
	// once, at the end, so newText that already contains \r\n does not get
	// double-converted into \r\r\n.
	edits := make([]editOp, len(a.Edits))
	for i, e := range a.Edits {
		edits[i] = editOp{
			OldText:    normalizeNewlines(e.OldText),
			NewText:    normalizeNewlines(e.NewText),
			ReplaceAll: e.ReplaceAll,
		}
	}

	// Resolve every edit against the original content (not
	// sequentially) so all-or-nothing semantics hold: any failure
	// leaves the file untouched. resolveSpans owns the matching
	// ladder — exact, whitespace-tolerant, did-you-mean error.
	spans := make([]editSpan, 0, len(edits))
	for i, e := range edits {
		if e.OldText == "" {
			return core.ToolResult{}, fmt.Errorf("edit %d: oldText must not be empty", i+1)
		}
		if e.OldText == e.NewText {
			return core.ToolResult{}, fmt.Errorf("edit %d: oldText equals newText", i+1)
		}
		s, err := resolveSpans(body, e, i+1, a.Path)
		if err != nil {
			// The match failed. Whether the file MOVED since the model last saw
			// it is the one thing the diagnostics above cannot derive from the
			// bytes alone, and it changes what the model should do: re-copy the
			// block (stale bytes, correct intent) rather than re-derive the edit
			// (wrong intent). Appended, never substituted — the did-you-mean
			// evidence is still the actionable part.
			return core.ToolResult{}, fmt.Errorf("%w%s", err, t.stalenessNote(path, orig))
		}
		spans = append(spans, s...)
	}
	// Check for overlaps.
	for i := 0; i < len(spans); i++ {
		for j := i + 1; j < len(spans); j++ {
			if spans[i].start < spans[j].end && spans[j].start < spans[i].end {
				return core.ToolResult{}, fmt.Errorf("edits overlap; merge them into one edit")
			}
		}
	}
	// Sort ascending.
	for i := 1; i < len(spans); i++ {
		for j := i; j > 0 && spans[j-1].start > spans[j].start; j-- {
			spans[j-1], spans[j] = spans[j], spans[j-1]
		}
	}
	var out strings.Builder
	prev := 0
	for _, s := range spans {
		out.WriteString(body[prev:s.start])
		out.WriteString(s.replacement)
		prev = s.end
	}
	out.WriteString(body[prev:])
	newBody := out.String()

	// Restore line endings. newBody is guaranteed \n-only at this point
	// (both the file body and every replacement were normalized above),
	// so a single \n->\r\n pass is exact and idempotent — it cannot turn
	// an existing \r\n into \r\r\n.
	if nl == "\r\n" {
		newBody = strings.ReplaceAll(newBody, "\n", "\r\n")
	}
	final := append([]byte{}, bom...)
	final = append(final, []byte(newBody)...)

	if err := os.WriteFile(path, final, 0o644); err != nil {
		return core.ToolResult{}, err
	}
	// The model now knows this file exactly — it just produced it.
	t.Files.Record(path, final, "edit")

	diff := unifiedDiff(a.Path, string(orig), strings.ReplaceAll(newBody, "\r\n", "\n"))
	// The tool-call header renders the path above the result, so the
	// result body is just the context diff — no "applied N edit(s)"
	// prose prefix. The Details map carries the edit count for
	// programmatic consumers (json mode, rpc clients) that might
	// want it.
	added, removed := countDiffLines(diff)
	return core.ToolResult{
		Content:      []provider.Content{provider.TextBlock{Text: diff}},
		Details:      map[string]any{"path": path, "edits": len(a.Edits), "diff": diff},
		LinesAdded:   added,
		LinesRemoved: removed,
	}, nil
}

// countDiffLines tallies a unified diff's content changes (+/- lines, file
// headers excluded) — the first-class line counts UIs show without having to
// re-parse the diff out of Details.
func countDiffLines(diff string) (added, removed int) {
	for _, l := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
			// file headers, not content
		case strings.HasPrefix(l, "+"):
			added++
		case strings.HasPrefix(l, "-"):
			removed++
		}
	}
	return added, removed
}

func detectLineEnding(b []byte) string {
	if bytes.Contains(b, []byte("\r\n")) {
		return "\r\n"
	}
	return "\n"
}

// normalizeNewlines collapses \r\n (and bare \r) to \n so model-supplied
// oldText/newText compare and splice consistently against the
// \n-normalized file body.
func normalizeNewlines(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// diffContextLines is the number of unchanged lines kept on each
// side of an edit when rendering the diff. 3 is the git-diff
// default and balances readability with transcript size.
const diffContextLines = 3

// unifiedDiff emits a context diff for the edit tool's result.
//
// Shape: each output row is either
//   - " <line>"       unchanged context
//   - "-<line>"       deletion (from a)
//   - "+<line>"       addition (to b)
//   - "..."           context break between hunks
//
// The legacy "--- name / +++ name" header is omitted because the
// tool-call header above the result already shows the path. Only
// lines within diffContextLines of a +/- row are kept; longer
// runs of unchanged content collapse into a single "..." row so
// a one-line edit in a thousand-line file produces a short
// transcript.
func unifiedDiff(name, a, b string) string {
	if a == b {
		return ""
	}
	aLines := strings.Split(a, "\n")
	bLines := strings.Split(b, "\n")
	ops := diffLines(aLines, bLines)

	// Mark ops that sit within diffContextLines of any +/- op.
	keep := make([]bool, len(ops))
	for i, op := range ops {
		if op.kind == '+' || op.kind == '-' {
			keep[i] = true
			for d := 1; d <= diffContextLines; d++ {
				if i-d >= 0 {
					keep[i-d] = true
				}
				if i+d < len(ops) {
					keep[i+d] = true
				}
			}
		}
	}

	var sb strings.Builder
	prevKept := false
	anyOutput := false
	for i, op := range ops {
		if !keep[i] {
			if prevKept {
				sb.WriteString("...\n")
				prevKept = false
			}
			continue
		}
		if !prevKept && anyOutput {
			sb.WriteString("...\n")
		}
		switch op.kind {
		case ' ':
			fmt.Fprintf(&sb, " %s\n", op.line)
		case '-':
			fmt.Fprintf(&sb, "-%s\n", op.line)
		case '+':
			fmt.Fprintf(&sb, "+%s\n", op.line)
		}
		prevKept = true
		anyOutput = true
	}
	_ = name // header dropped; kept in signature for call-site stability
	return sb.String()
}

type diffOp struct {
	kind byte
	line string
}

// diffLines returns a context-diff op stream for a -> b.
//
// The full LCS table is O(m*n) in both time and memory, which is fatal
// for large files: a 50k-line file with a one-line change would allocate
// ~(50000)^2 ints (~20GB) and OOM. Real edits touch a small contiguous
// region and leave long identical prefixes and suffixes, so we strip the
// common head and tail lines first and run the quadratic LCS only over
// the differing middle. The trimmed prefix/suffix are re-attached as
// plain context ops, producing byte-for-byte the same op stream the full
// LCS would have for these (identical-prefix/suffix) inputs.
func diffLines(a, b []string) []diffOp {
	// Common prefix.
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	// Common suffix (not overlapping the prefix).
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}

	midA := a[p : len(a)-s]
	midB := b[p : len(b)-s]

	ops := make([]diffOp, 0, p+len(midA)+len(midB)+s)
	for i := 0; i < p; i++ {
		ops = append(ops, diffOp{' ', a[i]})
	}
	ops = append(ops, lcsDiff(midA, midB)...)
	for i := len(a) - s; i < len(a); i++ {
		ops = append(ops, diffOp{' ', a[i]})
	}
	return ops
}

// lcsDiff is the classic O(m*n) LCS diff. Callers must trim common
// prefix/suffix lines first (see diffLines) so m and n stay bounded by
// the size of the actual change, not the whole file.
func lcsDiff(a, b []string) []diffOp {
	m, n := len(a), len(b)
	if m == 0 && n == 0 {
		return nil
	}
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 0; i < m; i++ {
		for j := 0; j < n; j++ {
			if a[i] == b[j] {
				dp[i+1][j+1] = dp[i][j] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i+1][j+1] = dp[i+1][j]
			} else {
				dp[i+1][j+1] = dp[i][j+1]
			}
		}
	}
	// Backtrack.
	var ops []diffOp
	i, j := m, n
	for i > 0 && j > 0 {
		if a[i-1] == b[j-1] {
			ops = append([]diffOp{{' ', a[i-1]}}, ops...)
			i--
			j--
		} else if dp[i][j-1] >= dp[i-1][j] {
			ops = append([]diffOp{{'+', b[j-1]}}, ops...)
			j--
		} else {
			ops = append([]diffOp{{'-', a[i-1]}}, ops...)
			i--
		}
	}
	for i > 0 {
		ops = append([]diffOp{{'-', a[i-1]}}, ops...)
		i--
	}
	for j > 0 {
		ops = append([]diffOp{{'+', b[j-1]}}, ops...)
		j--
	}
	return ops
}

// stalenessAgeFloor is how recent a "changed since you saw it" gets reported
// without an age. Under it the age is noise — the model saw the file moments
// ago and the duration adds nothing — and printing "0s ago" reads like a bug.
const stalenessAgeFloor = 2 * time.Second

// stalenessNote explains a failed match in terms of what happened to the file,
// when the harness knows. Three cases, three different instructions:
//
//   - never seen: the model is editing a file it did not read. That is the
//     likeliest reason an exact-match edit misses, and no amount of
//     did-you-mean evidence says it.
//   - seen and unchanged: the file is exactly as the model left it, so the
//     mismatch is in the model's text. Worth saying, because it rules OUT the
//     explanation it would otherwise reach for.
//   - seen and changed: something rewrote the file — a formatter, a build step,
//     the user, a sub-agent. The model's text was probably right when it was
//     written; re-copy rather than re-derive.
//
// Returns "" rather than guessing when there is nothing to say.
func (t *EditTool) stalenessNote(path string, current []byte) string {
	changed, since, how, ok := t.Files.Changed(path, current)
	if !ok {
		return "\n(this session has not read " + filepath.Base(path) + " — read it first so oldText matches the bytes on disk)"
	}
	if !changed {
		return "\n(the file is byte-identical to what you last saw, so the difference is in oldText, not on disk)"
	}
	verb := "read"
	if how == "write" || how == "edit" {
		verb = "wrote"
	}
	if since < stalenessAgeFloor {
		return fmt.Sprintf("\n(the file has CHANGED since you %s it — something else rewrote it; re-copy the block rather than re-deriving the edit)", verb)
	}
	return fmt.Sprintf("\n(the file has CHANGED since you %s it %s ago — something else rewrote it; re-copy the block rather than re-deriving the edit)",
		verb, humanDuration(since.Round(time.Second)))
}
