package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// runEdit applies one edit op via the real tool and returns the
// resulting file content (or the error).
func runEdit(t *testing.T, content string, edit map[string]any) (string, error) {
	t.Helper()
	dir := testsupport.TempDir(t)
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &EditTool{CWD: dir}
	_, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"path":  "f.txt",
		"edits": []map[string]any{edit},
	}), nil)
	if err != nil {
		return "", err
	}
	b, rerr := os.ReadFile(p)
	if rerr != nil {
		t.Fatal(rerr)
	}
	return string(b), nil
}

func TestEditTolerantIndentAdded(t *testing.T) {
	// File block is indented one tab deeper than the model's oldText.
	// The match must land AND the replacement must pick up the file's
	// real indentation, not the model's.
	content := "func f() {\n\t\tx := 1\n\t\ty := 2\n}\n"
	got, err := runEdit(t, content, map[string]any{
		"oldText": "\tx := 1\n\ty := 2",
		"newText": "\tx := 10\n\ty := 20",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "func f() {\n\t\tx := 10\n\t\ty := 20\n}\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEditTolerantIndentRemoved(t *testing.T) {
	// Model over-indented its oldText relative to the file.
	content := "a := 1\nb := 2\n"
	got, err := runEdit(t, content, map[string]any{
		"oldText": "    a := 1\n    b := 2",
		"newText": "    a := 9\n    b := 9",
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "a := 9\nb := 9\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEditTolerantTrailingWhitespace(t *testing.T) {
	// File has trailing spaces inside the block that the model didn't
	// copy, so no exact match exists. The tolerant span must swallow
	// them, not leave them dangling after the replacement.
	content := "hello   \nworld\n"
	got, err := runEdit(t, content, map[string]any{
		"oldText": "hello\nworld",
		"newText": "goodbye\nworld",
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "goodbye\nworld\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEditTolerantBlankLinesMatchAnyIndent(t *testing.T) {
	content := "\tif x {\n\n\t\treturn\n\t}\n"
	got, err := runEdit(t, content, map[string]any{
		"oldText": "if x {\n\n\treturn\n}",
		"newText": "if y {\n\n\treturn\n}",
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "\tif y {\n\n\t\treturn\n\t}\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEditTolerantAmbiguousErrors(t *testing.T) {
	// Two blocks match under *different* indent shifts and there is
	// no exact match (the lines are never literally adjacent the way
	// oldText writes them). The tolerant pass must refuse to pick.
	content := "\ta()\n\tb()\n\t\ta()\n\t\tb()\n"
	_, err := runEdit(t, content, map[string]any{
		"oldText": "a()\nb()",
		"newText": "c()\nd()",
	})
	if err == nil {
		t.Fatal("want ambiguity error")
	}
	if !strings.Contains(err.Error(), "whitespace-tolerant") {
		t.Errorf("error should name the tolerant pass: %v", err)
	}
}

func TestEditTolerantMixedShiftRejected(t *testing.T) {
	// Two lines shifted by different amounts is not a uniform shift;
	// matching that would silently mangle structure.
	content := "\ta\n\t\t\tb\n"
	_, err := runEdit(t, content, map[string]any{
		"oldText": "a\n\tb",
		"newText": "a\n\tc",
	})
	if err == nil {
		t.Fatal("want not-found error for non-uniform shift")
	}
}

func TestEditReplaceAllExact(t *testing.T) {
	content := "x = old\ny = old\nz = old\n"
	got, err := runEdit(t, content, map[string]any{
		"oldText":    "old",
		"newText":    "new",
		"replaceAll": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "x = new\ny = new\nz = new\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEditAmbiguousErrorListsLines(t *testing.T) {
	content := "x\na\nx\n"
	_, err := runEdit(t, content, map[string]any{
		"oldText": "x",
		"newText": "y",
	})
	if err == nil {
		t.Fatal("want ambiguity error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "lines 1, 3") {
		t.Errorf("want occurrence line numbers in error, got: %v", msg)
	}
	if !strings.Contains(msg, "replaceAll") {
		t.Errorf("error should mention the replaceAll escape hatch: %v", msg)
	}
}

func TestEditDidYouMeanShowsActualBlock(t *testing.T) {
	content := "func g() {\n\tcount += 2\n}\n"
	_, err := runEdit(t, content, map[string]any{
		"oldText": "func g() {\n\tcount += 1\n}",
		"newText": "func g() {\n\tcount += 3\n}",
	})
	if err == nil {
		t.Fatal("want not-found error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "file line 1") {
		t.Errorf("want anchor line number, got: %v", msg)
	}
	if !strings.Contains(msg, "count += 2") {
		t.Errorf("want the file's actual content in the error, got: %v", msg)
	}
}

// Nothing in the file resembles the text. The error must not invent an anchor
// or suggest an unrelated block — but it must still be actionable, which the
// bare "oldText not found in <path>" was not.
func TestEditNotFoundWithNothingSimilarSaysSo(t *testing.T) {
	_, err := runEdit(t, "alpha\nbeta\n", map[string]any{
		"oldText": "gamma",
		"newText": "delta",
	})
	if err == nil {
		t.Fatal("want not-found error")
	}
	msg := err.Error()
	if strings.Contains(msg, "diverges") || strings.Contains(msg, "closest block") {
		t.Errorf("no anchor and nothing similar; the error must not suggest one: %v", msg)
	}
	if !strings.Contains(msg, "no block in the file resembles it") {
		t.Errorf("error should say nothing resembles the text: %v", msg)
	}
	if !strings.Contains(msg, "re-read the file") {
		t.Errorf("error should tell the model what to do next: %v", msg)
	}
}

func TestEditTolerantCRLFFile(t *testing.T) {
	// CRLF body + indent shift at once: normalization happens before
	// matching, and the file's CRLF endings must survive the rewrite.
	content := "if (a) {\r\n        doIt();\r\n}\r\n"
	got, err := runEdit(t, content, map[string]any{
		"oldText": "    doIt();",
		"newText": "    doBetter();",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "if (a) {\r\n        doBetter();\r\n}\r\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEditExactStillPreferredOverTolerant(t *testing.T) {
	// When an exact match exists, the tolerant pass must not widen it
	// into an ambiguity with a same-shape, differently-indented twin.
	content := "x := 1\n\tx := 1\n"
	got, err := runEdit(t, content, map[string]any{
		"oldText": "\tx := 1",
		"newText": "\tx := 2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "x := 1\n\tx := 2\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEditTolerantMultiEditMixed(t *testing.T) {
	// One exact edit and one tolerant edit in a single call still
	// resolve against the original body and apply atomically.
	content := "alpha\n\tbeta\n"
	dir := testsupport.TempDir(t)
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &EditTool{CWD: dir}
	_, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{
			{"oldText": "alpha", "newText": "ALPHA"},
			{"oldText": "beta", "newText": "BETA"}, // tolerant: file has \tbeta
		},
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if want := "ALPHA\n\tBETA\n"; string(b) != want {
		t.Errorf("got %q, want %q", string(b), want)
	}
}

// The formatter case, and the whole of finding B3: the block IS in the file,
// but a formatter realigned it after the model read it. gofmt aligning struct
// fields is INTRA-line, so the tolerant matcher's uniform indent shift cannot
// see it — the edit fails and the old error said only "oldText not found",
// which reads as "you got the code wrong" when the code was right and the bytes
// were stale.
func TestEditNotFoundNamesWhitespaceOnlyDivergence(t *testing.T) {
	// What a formatter produces: the same tokens, realigned within the line.
	content := "type T struct {\n\tID        string\n\tOrigin    string\n}\n"
	_, err := runEdit(t, content, map[string]any{
		"oldText": "\tID string\n\tOrigin string",
		"newText": "\tID string\n\tOrigin string\n\tExtra string",
	})
	if err == nil {
		t.Fatal("want not-found error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "apart from whitespace") {
		t.Errorf("error does not name the whitespace-only divergence: %v", msg)
	}
	if !strings.Contains(msg, "formatter") {
		t.Errorf("error does not name the likely cause: %v", msg)
	}
	if !strings.Contains(msg, "Origin    string") {
		t.Errorf("error does not show the bytes to copy: %v", msg)
	}
	// It must name WHERE, or the model cannot find the block it just failed on.
	if !strings.Contains(msg, "lines 2-3") {
		t.Errorf("error does not locate the block: %v", msg)
	}
}

// The bare "not found" tier from the reviewed session: no line of oldText
// matches verbatim, so the anchor misses — but a block clearly corresponds, and
// showing it is the difference between one recovery step and a blind re-read.
func TestEditNotFoundShowsTheNearestBlock(t *testing.T) {
	content := "func parse(s string) (int, error) {\n\tn, err := strconv.Atoi(s)\n\tif err != nil {\n\t\treturn 0, err\n\t}\n\treturn n, nil\n}\n"
	_, err := runEdit(t, content, map[string]any{
		// The model's copy has drifted, INCLUDING its first line — so the
		// exact anchor cannot fire and the nearest-block tier is the only
		// thing standing between the model and a blind re-read.
		"oldText": "func parse(s string) (int64, error) {\n\tn, err := strconv.ParseInt(s)\n\tif err != nil {\n\t\treturn 0, err\n\t}\n\treturn n, nil\n}",
		"newText": "// replaced",
	})
	if err == nil {
		t.Fatal("want not-found error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "closest block") {
		t.Errorf("error does not offer the nearest block: %v", msg)
	}
	if !strings.Contains(msg, "strconv.Atoi") {
		t.Errorf("error does not show the file's actual content: %v", msg)
	}
	if !strings.Contains(msg, "line 1") {
		t.Errorf("error does not locate the block: %v", msg)
	}
}

// A suggestion that resembles nothing is worse than no suggestion: a model
// given one acts on it. The threshold is what stops that.
func TestEditNearestBlockRefusesWeakMatches(t *testing.T) {
	body := "alpha one\nbravo two\ncharlie three\ndelta four\n"
	// One line in four collapses equal — well under the threshold.
	if got := nearestBlock(body, "alpha one\nzulu\nyankee\nxray"); got != "" {
		t.Errorf("a 25%% match was offered as the closest block: %q", got)
	}
	// Three in four is worth showing.
	if got := nearestBlock(body, "alpha one\nbravo two\ncharlie three\nxray"); got == "" {
		t.Error("a 75% match should be offered as the closest block")
	}
}

// The scan is bounded: a huge oldText against a huge file must not turn a failed
// edit into a stall. Past the budget it reports nothing rather than grinding.
func TestEditNearestBlockIsBounded(t *testing.T) {
	body := strings.Repeat("a line of text\n", 3000)
	old := strings.Repeat("a different line\n", 2000)
	done := make(chan string, 1)
	go func() { done <- nearestBlock(body, old) }()
	select {
	case got := <-done:
		if got != "" {
			t.Errorf("over-budget scan produced a suggestion: %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nearestBlock did not return within 5s — the budget is not bounding the scan")
	}
}

// The good tier from the reviewed session must survive the new ladder: an exact
// first-line anchor still wins over the nearest-block fallback, because "your
// block starts here and diverges" is more precise than a similarity score.
func TestEditAnchorStillBeatsNearest(t *testing.T) {
	content := "func g() {\n\tcount += 2\n\tother()\n}\n"
	_, err := runEdit(t, content, map[string]any{
		"oldText": "func g() {\n\tcount += 1\n\tother()\n}",
		"newText": "func g() {\n\tcount += 3\n}",
	})
	if err == nil {
		t.Fatal("want not-found error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "diverges") {
		t.Errorf("the exact-anchor tier should have fired: %v", msg)
	}
	if strings.Contains(msg, "closest block") {
		t.Errorf("nearest-block fired over an exact anchor: %v", msg)
	}
}
