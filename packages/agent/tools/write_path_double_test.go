package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// blockTexts returns the text of every TextBlock in a result, in order. Order
// matters here: both warnings prepend, so their relative position is a claim
// the code makes and a test has to hold.
func blockTexts(t *testing.T, res core.ToolResult) []string {
	t.Helper()
	var out []string
	for _, c := range res.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			out = append(out, tb.Text)
		}
	}
	return out
}

func writeDetail(t *testing.T, res core.ToolResult, key string) any {
	t.Helper()
	d, ok := res.Details.(map[string]any)
	if !ok {
		t.Fatalf("Details is %T, want map[string]any", res.Details)
	}
	return d[key]
}

// F11. read fails loudly on a doubled path; write CREATES it. The write still
// happens — the tool cannot tell a mistake from a deliberately nested tree —
// but it no longer happens silently.
func TestWriteWarnsOnADoubledPath(t *testing.T) {
	_, cwd := doubledLayout(t)
	tool := &WriteTool{CWD: cwd}

	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"path":    doubledPath,
		"content": "shadow\n",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}

	if got := writeDetail(t, res, "path_doubled"); got != true {
		t.Fatalf(`Details["path_doubled"] = %v, want true`, got)
	}
	texts := blockTexts(t, res)
	if len(texts) == 0 || !strings.Contains(texts[0], "warning:") {
		t.Fatalf("no warning led the result body:\n%v", texts)
	}
	if !strings.Contains(texts[0], "README.md already exists") {
		t.Fatalf("the warning does not name the file the caller probably meant:\n%s", texts[0])
	}

	// Behaviour is deliberately unchanged: the write went through, exactly
	// where the caller asked. Warning and refusing are different things, and
	// this fix is the first.
	if _, err := os.Stat(filepath.Join(cwd, doubledPath)); err != nil {
		t.Fatalf("the write did not happen: %v", err)
	}
	// And the file it was confused with is untouched.
	orig, err := os.ReadFile(filepath.Join(cwd, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(orig) != "# readme\n" {
		t.Fatalf("the undoubled file was modified: %q", string(orig))
	}
}

// Creating a genuinely new directory is what write is FOR, and it must stay
// silent. This is the case that rules out refusing outright.
func TestWriteStaysQuietOnAnOrdinaryNewTree(t *testing.T) {
	_, cwd := doubledLayout(t)
	tool := &WriteTool{CWD: cwd}

	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"path":    "docs/notes.md",
		"content": "notes\n",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := writeDetail(t, res, "path_doubled"); got != nil {
		t.Fatalf(`Details["path_doubled"] = %v on an ordinary new tree`, got)
	}
	for _, s := range blockTexts(t, res) {
		if strings.Contains(s, "repeats the working directory") {
			t.Fatalf("an ordinary new directory was warned about:\n%s", s)
		}
	}
}

// The existence proof is what keeps this quiet. A path may repeat the working
// directory and still be the only place that file has ever lived, and then
// there is nothing to point the caller at.
func TestWriteStaysQuietWhenTheUndoubledFileIsAbsent(t *testing.T) {
	_, cwd := doubledLayout(t)
	tool := &WriteTool{CWD: cwd}

	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"path":    "projects/simple-vllm-monitoring-dashboard/NOTES.md",
		"content": "notes\n",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := writeDetail(t, res, "path_doubled"); got != nil {
		t.Fatalf(`Details["path_doubled"] = %v with no undoubled file to point at`, got)
	}
	for _, s := range blockTexts(t, res) {
		if strings.Contains(s, "repeats the working directory") {
			t.Fatalf("warned without an undoubled file to name:\n%s", s)
		}
	}
}

// Both warnings prepend, so the last one written reads first. A file in the
// wrong place matters more than one merely invisible to git, and the ordering
// in the source is deliberate rather than incidental.
func TestWriteDoublingWarningReadsBeforeTheGitignoreWarning(t *testing.T) {
	_, cwd := doubledLayout(t)
	// Ignored() needs no .git dir, only a .gitignore. This hides the doubled
	// path, so both warnings fire on one write.
	if err := os.WriteFile(filepath.Join(cwd, ".gitignore"), []byte("projects/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &WriteTool{CWD: cwd}

	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"path":    doubledPath,
		"content": "shadow\n",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := writeDetail(t, res, "gitignored"); got != true {
		t.Fatalf("setup: the doubled path should be gitignored, got %v", got)
	}
	if got := writeDetail(t, res, "path_doubled"); got != true {
		t.Fatalf("setup: the path should be detected as doubled, got %v", got)
	}

	texts := blockTexts(t, res)
	if len(texts) < 2 {
		t.Fatalf("want both warnings, got %d block(s):\n%v", len(texts), texts)
	}
	if !strings.Contains(texts[0], "repeats the working directory") {
		t.Fatalf("the doubling warning does not read first:\n%s", texts[0])
	}
	if !strings.Contains(texts[1], ".gitignore") {
		t.Fatalf("the gitignore warning does not read second:\n%s", texts[1])
	}
}
