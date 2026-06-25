package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestEditNotFoundNoAnchorStaysPlain(t *testing.T) {
	_, err := runEdit(t, "alpha\nbeta\n", map[string]any{
		"oldText": "gamma",
		"newText": "delta",
	})
	if err == nil {
		t.Fatal("want not-found error")
	}
	if strings.Contains(err.Error(), "diverges") {
		t.Errorf("no anchor exists; error should stay plain: %v", err)
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
