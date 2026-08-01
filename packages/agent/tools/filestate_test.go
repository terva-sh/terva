package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// editWithState runs read-then-edit through real tools sharing one FileState,
// optionally rewriting the file in between — the formatter-behind-your-back
// scenario finding B3 is about.
func editWithState(t *testing.T, initial string, mutate func(path string), edit map[string]any) error {
	t.Helper()
	dir := testsupport.TempDir(t)
	p := filepath.Join(dir, "f.go")
	if err := os.WriteFile(p, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	files := NewFileState()
	rd := &ReadTool{CWD: dir, Files: files}
	if _, err := rd.Execute(context.Background(), mustJSON(t, map[string]any{"path": "f.go"}), nil); err != nil {
		t.Fatalf("read: %v", err)
	}
	if mutate != nil {
		mutate(p)
	}
	ed := &EditTool{CWD: dir, Files: files}
	_, err := ed.Execute(context.Background(), mustJSON(t, map[string]any{
		"path": "f.go", "edits": []map[string]any{edit},
	}), nil)
	return err
}

// The heart of B3: the model read the file, something rewrote it, and the edit
// fails. "oldText not found" reads as "you got the code wrong" — the model
// re-derives. It should re-COPY: its text was right when it was written.
func TestEditNamesAFileThatChangedSinceTheRead(t *testing.T) {
	err := editWithState(t,
		"package p\n\nfunc a() int { return 1 }\n",
		func(path string) {
			// Something else rewrote the file wholesale.
			_ = os.WriteFile(path, []byte("package p\n\nfunc b() int { return 2 }\n"), 0o644)
		},
		map[string]any{"oldText": "func a() int { return 1 }", "newText": "func a() int { return 9 }"},
	)
	if err == nil {
		t.Fatal("want a not-found error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "CHANGED since you read it") {
		t.Errorf("error does not report the file moved under the edit: %v", msg)
	}
	if !strings.Contains(msg, "re-copy") {
		t.Errorf("error does not say what to do about it: %v", msg)
	}
}

// The opposite case, and worth stating because it RULES OUT the explanation the
// model would otherwise reach for: the file is exactly as it was, so the
// mismatch is in oldText.
func TestEditSaysWhenTheFileIsUnchanged(t *testing.T) {
	err := editWithState(t,
		"package p\n\nfunc a() int { return 1 }\n",
		nil,
		map[string]any{"oldText": "func zzz() int { return 1 }", "newText": "x"},
	)
	if err == nil {
		t.Fatal("want a not-found error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "byte-identical to what you last saw") {
		t.Errorf("error does not rule out staleness: %v", msg)
	}
	if strings.Contains(msg, "CHANGED") {
		t.Errorf("an unchanged file must not be reported as changed: %v", msg)
	}
}

// Editing a file this session never read is the likeliest reason an exact-match
// edit misses, and no amount of did-you-mean evidence says it.
func TestEditNamesAnUnreadFile(t *testing.T) {
	dir := testsupport.TempDir(t)
	p := filepath.Join(dir, "f.go")
	if err := os.WriteFile(p, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ed := &EditTool{CWD: dir, Files: NewFileState()}
	_, err := ed.Execute(context.Background(), mustJSON(t, map[string]any{
		"path": "f.go", "edits": []map[string]any{{"oldText": "func a()", "newText": "func b()"}},
	}), nil)
	if err == nil {
		t.Fatal("want a not-found error")
	}
	if !strings.Contains(err.Error(), "has not read f.go") {
		t.Errorf("error does not name the unread file: %v", err)
	}
}

// A successful edit updates what the model has seen, so the NEXT failure is
// judged against the post-edit bytes rather than the pre-edit ones.
func TestEditRecordsItsOwnWrite(t *testing.T) {
	dir := testsupport.TempDir(t)
	p := filepath.Join(dir, "f.go")
	if err := os.WriteFile(p, []byte("package p\n\nold\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := NewFileState()
	ed := &EditTool{CWD: dir, Files: files}
	if _, err := ed.Execute(context.Background(), mustJSON(t, map[string]any{
		"path": "f.go", "edits": []map[string]any{{"oldText": "old", "newText": "new"}},
	}), nil); err != nil {
		t.Fatalf("first edit: %v", err)
	}
	// A second, failing edit must NOT claim the file changed — this tool wrote it.
	_, err := ed.Execute(context.Background(), mustJSON(t, map[string]any{
		"path": "f.go", "edits": []map[string]any{{"oldText": "nothing like this", "newText": "x"}},
	}), nil)
	if err == nil {
		t.Fatal("want a not-found error")
	}
	if strings.Contains(err.Error(), "CHANGED") {
		t.Errorf("the tool's own write was reported as an outside change: %v", err)
	}
}

// A partial read must not record a whole-file digest it does not have. Reading
// a prefix and then digesting it would report every file as changed forever.
func TestFileStateSkipsTruncatedReads(t *testing.T) {
	dir := testsupport.TempDir(t)
	p := filepath.Join(dir, "big.txt")
	big := strings.Repeat("x", maxReadFileBytes+4096)
	if err := os.WriteFile(p, []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	files := NewFileState()
	rd := &ReadTool{CWD: dir, Files: files}
	if _, err := rd.Execute(context.Background(), mustJSON(t, map[string]any{"path": "big.txt"}), nil); err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, _, _, ok := files.Changed(p, []byte(big)); ok {
		t.Error("a truncated read recorded a digest; it only held a prefix")
	}
}

// A paged read DOES record: the question the note answers is "did the file
// change since you looked", and it did look.
func TestFileStateRecordsPagedReads(t *testing.T) {
	dir := testsupport.TempDir(t)
	p := filepath.Join(dir, "f.txt")
	content := "one\ntwo\nthree\nfour\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	files := NewFileState()
	rd := &ReadTool{CWD: dir, Files: files}
	if _, err := rd.Execute(context.Background(), mustJSON(t, map[string]any{
		"path": "f.txt", "offset": 2, "limit": 1,
	}), nil); err != nil {
		t.Fatalf("read: %v", err)
	}
	changed, _, how, ok := files.Changed(p, []byte(content))
	if !ok {
		t.Fatal("a paged read recorded nothing")
	}
	if changed || how != "read" {
		t.Errorf("changed=%v how=%q, want unchanged/read", changed, how)
	}
}

func TestFileStateEvictsAndForgets(t *testing.T) {
	f := &FileState{seen: map[string]fileSeen{}, limit: 3}
	for _, p := range []string{"a", "b", "c", "d"} {
		f.Record(p, []byte(p), "read")
	}
	if len(f.seen) > 3 {
		t.Fatalf("map grew past the limit: %d entries", len(f.seen))
	}
	f.Record("keep", []byte("keep"), "write")
	f.Forget("keep")
	if _, _, _, ok := f.Changed("keep", []byte("keep")); ok {
		t.Error("Forget left the entry behind")
	}
	// A nil receiver must be a no-op on every method, so call sites stay
	// unconditional.
	var nilFS *FileState
	nilFS.Record("x", []byte("y"), "read")
	nilFS.Forget("x")
	if _, _, _, ok := nilFS.Changed("x", []byte("y")); ok {
		t.Error("a nil FileState reported a hit")
	}
}

// Finding B3 in one test: the model writes a file, gofmt realigns it, and the
// model's next edit fails. The review asked for an error that says "changed
// since your write; differs only in whitespace" — which needs BOTH halves,
// because neither alone is actionable. The whitespace evidence without the
// staleness says "you got the spacing wrong"; the staleness without the
// evidence says "something changed" and leaves the model to find out what.
func TestEditComposesStalenessAndWhitespaceEvidence(t *testing.T) {
	err := editWithState(t,
		"type T struct {\n\tID string\n\tOrigin string\n}\n",
		func(path string) {
			// gofmt: same tokens, realigned WITHIN the lines. The tolerant
			// matcher's uniform indent shift cannot see this.
			_ = os.WriteFile(path, []byte("type T struct {\n\tID     string\n\tOrigin string\n}\n"), 0o644)
		},
		map[string]any{
			"oldText": "\tID string\n\tOrigin string",
			"newText": "\tID string\n\tOrigin string\n\tExtra string",
		},
	)
	if err == nil {
		t.Fatal("want a not-found error")
	}
	msg := err.Error()
	// The cause.
	if !strings.Contains(msg, "CHANGED since you read it") {
		t.Errorf("missing the staleness half: %v", msg)
	}
	// The fix.
	if !strings.Contains(msg, "apart from whitespace") {
		t.Errorf("missing the whitespace-divergence half: %v", msg)
	}
	if !strings.Contains(msg, "formatter") {
		t.Errorf("missing the likely culprit: %v", msg)
	}
	// And the bytes to copy, so recovery is one step.
	if !strings.Contains(msg, "ID     string") {
		t.Errorf("missing the actual bytes: %v", msg)
	}
}
