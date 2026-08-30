package tools

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// The recorded layout: the working directory IS the project subdirectory, and
// the model addresses a file inside it by a path relative to the repository
// root.
const doubledPath = "projects/simple-vllm-monitoring-dashboard/README.md"

func doubledLayout(t *testing.T) (root, cwd string) {
	t.Helper()
	root = testsupport.TempDir(t)
	cwd = filepath.Join(root, "projects", "simple-vllm-monitoring-dashboard")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "README.md"), []byte("# readme\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, cwd
}

// seq 191 of the reviewed session, reduced: read("projects/<proj>/README.md")
// while cwd is already .../projects/<proj>.
func TestPathDoublingSuggestsTheIntendedPath(t *testing.T) {
	_, cwd := doubledLayout(t)
	if got := pathDoublingSuggestion(cwd, doubledPath); got != "README.md" {
		t.Fatalf("pathDoublingSuggestion = %q, want %q", got, "README.md")
	}
}

// Every way the check must decline. A wrong suggestion is worse than none: it
// sends the caller after a second path that is also not the one it wants.
func TestPathDoublingSuggestionBoundaries(t *testing.T) {
	_, cwd := doubledLayout(t)

	cases := []struct {
		name  string
		cwd   string
		given string
	}{
		// Absolute paths never resolve against cwd, so doubling cannot occur.
		{"absolute path", cwd, filepath.Join(cwd, "README.md")},
		// Nothing in common with the tail of cwd.
		{"no overlap", cwd, "docs/README.md"},
		// Whole segments only. cwd ends with the LONGER name, and a suffix of
		// that name must not count as a segment match.
		{"partial segment", cwd, "dashboard/README.md"},
		// One segment leaves no remainder to propose.
		{"nothing would remain", cwd, "README.md"},
		{"empty given", cwd, ""},
		{"empty cwd", "", doubledPath},
		// The overlap is real, but the file it points at is not there. This is
		// the proof step: an unproven suggestion is never made.
		{"overlap but remainder missing", cwd, "projects/simple-vllm-monitoring-dashboard/NOPE.md"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pathDoublingSuggestion(c.cwd, c.given); got != "" {
				t.Fatalf("pathDoublingSuggestion(%q, %q) = %q, want no suggestion", c.cwd, c.given, got)
			}
		})
	}
}

// The error the model reads must carry the display path, not the absolute one
// the operating system was handed. read computes that display path two lines
// above the stat precisely so absolute paths stay out of the context window,
// and then used to discard it.
func TestNotFoundErrorNamesTheDisplayPathNotTheAbsoluteOne(t *testing.T) {
	root, cwd := doubledLayout(t)

	// A real *fs.PathError, carrying the doubled absolute path.
	_, statErr := os.Stat(filepath.Join(cwd, doubledPath))
	if statErr == nil {
		t.Fatal("setup: the doubled path must not exist")
	}
	if !strings.Contains(statErr.Error(), root) {
		t.Fatal("setup: the raw error should carry the absolute path this test is about")
	}

	got := notFoundError(cwd, doubledPath, doubledPath, statErr)
	msg := got.Error()

	if strings.Contains(msg, root) {
		t.Fatalf("the absolute path leaked into the model-visible error:\n%s", msg)
	}
	if !strings.Contains(msg, doubledPath) {
		t.Fatalf("the error does not name the path the caller gave:\n%s", msg)
	}
	if !strings.Contains(msg, "Did you mean README.md?") {
		t.Fatalf("the error does not name the intended path:\n%s", msg)
	}
	// Rebuilding the message must not cost callers the ability to classify it.
	if !errors.Is(got, fs.ErrNotExist) {
		t.Fatal("errors.Is(err, fs.ErrNotExist) broke; the Unwrap contract is load-bearing")
	}
}

// A missing file that is simply missing gets the clean message and no guess.
func TestNotFoundErrorWithoutDoublingMakesNoSuggestion(t *testing.T) {
	_, cwd := doubledLayout(t)

	_, statErr := os.Stat(filepath.Join(cwd, "absent.md"))
	if statErr == nil {
		t.Fatal("setup: the file must not exist")
	}
	msg := notFoundError(cwd, "absent.md", "absent.md", statErr).Error()

	if !strings.Contains(msg, "no such file or directory") {
		t.Fatalf("the error does not say what went wrong:\n%s", msg)
	}
	if strings.Contains(msg, "Did you mean") {
		t.Fatalf("a suggestion was invented for an ordinary missing file:\n%s", msg)
	}
}

// Only a not-exist error is rewritten. A permission problem carries detail the
// caller cannot get anywhere else, so it must survive untouched.
func TestNotFoundErrorLeavesOtherErrorsAlone(t *testing.T) {
	_, cwd := doubledLayout(t)
	perm := &fs.PathError{Op: "open", Path: "/somewhere/secret", Err: fs.ErrPermission}

	if got := notFoundError(cwd, doubledPath, doubledPath, perm); got != error(perm) {
		t.Fatalf("a permission error was rewritten: %v", got)
	}
	if got := notFoundError(cwd, doubledPath, doubledPath, nil); got != nil {
		t.Fatalf("a nil error became %v", got)
	}
}

// End to end through the tool the finding recorded.
func TestReadSuggestsTheUndoubledPath(t *testing.T) {
	root, cwd := doubledLayout(t)
	tool := &ReadTool{CWD: cwd}

	_, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"path": doubledPath,
	}), nil)
	if err == nil {
		t.Fatal("reading a doubled path should fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Did you mean README.md?") {
		t.Fatalf("read did not name the intended path:\n%s", msg)
	}
	if strings.Contains(msg, root) {
		t.Fatalf("read leaked the absolute path:\n%s", msg)
	}
}

// The helper is shared, so the tools that resolve a path the same way report a
// missing one the same way. A fix wired into read alone would leave the same
// confusion unexplained everywhere else.
func TestPathDoublingDiagnosticIsShared(t *testing.T) {
	_, cwd := doubledLayout(t)
	ctx := context.Background()

	t.Run("grep", func(t *testing.T) {
		tool := &GrepTool{CWD: cwd}
		_, err := tool.Execute(ctx, mustJSON(t, map[string]any{
			"pattern": "readme",
			"path":    doubledPath,
		}), nil)
		if err == nil {
			t.Fatal("grep over a doubled path should fail")
		}
		if !strings.Contains(err.Error(), "Did you mean README.md?") {
			t.Fatalf("grep did not name the intended path:\n%s", err.Error())
		}
	})

	t.Run("glob", func(t *testing.T) {
		tool := &GlobTool{CWD: cwd}
		_, err := tool.Execute(ctx, mustJSON(t, map[string]any{
			"pattern": "*.md",
			"path":    doubledPath,
		}), nil)
		if err == nil {
			t.Fatal("glob over a doubled path should fail")
		}
		if !strings.Contains(err.Error(), "Did you mean README.md?") {
			t.Fatalf("glob did not name the intended path:\n%s", err.Error())
		}
	})

	t.Run("edit", func(t *testing.T) {
		tool := &EditTool{CWD: cwd}
		_, err := tool.Execute(ctx, mustJSON(t, map[string]any{
			"path":  doubledPath,
			"edits": []map[string]any{{"oldText": "# readme", "newText": "# hello"}},
		}), nil)
		if err == nil {
			t.Fatal("editing a doubled path should fail")
		}
		if !strings.Contains(err.Error(), "Did you mean README.md?") {
			t.Fatalf("edit did not name the intended path:\n%s", err.Error())
		}
	})
}
