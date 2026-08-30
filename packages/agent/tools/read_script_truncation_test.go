package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// F2 in docs/reviews/2026-08-29-local-model-harness-friction-review.md. A read
// that hits a cap appends an English marker to the bytes it returns. The model
// reads that marker and pages; a script gets it as the return value of read()
// and parses it as content. The recorded session lost 30 turns to a wrong
// theory ("read truncates each line at around 263 characters") because nothing
// in the result named the real limit.

// bigLineFile writes a file whose lines are long enough that the 50 KiB byte
// cap engages well before the 2000 line cap.
func bigLineFile(t *testing.T, dir, name string) {
	t.Helper()
	long := strings.Repeat("x", 200)
	var b strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&b, "%04d%s\n", i, long)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func scriptRead(t *testing.T, tool *ReadTool, args map[string]any) core.ToolResult {
	t.Helper()
	res, err := tool.Execute(withScriptCall(context.Background()), mustJSON(t, args), nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// A script must not receive a cut prefix. It cannot tell that its input is
// short, so it parses the marker as content or computes a confident wrong
// answer over the part it got.
func TestReadScriptTruncationIsAnError(t *testing.T) {
	dir := testsupport.TempDir(t)
	bigLineFile(t, dir, "big.txt")
	tool := &ReadTool{CWD: dir}

	res := scriptRead(t, tool, map[string]any{"path": "big.txt"})
	if !res.IsError {
		t.Fatal("a truncated read must fail in script context, not return a prefix")
	}
	msg := res.Content[0].(provider.TextBlock).Text

	// The message has to name the limit, or the reader theorises about it.
	if !strings.Contains(msg, fmt.Sprintf("%d KiB", maxReadBytes/1024)) {
		t.Errorf("the error should name the byte limit:\n%s", msg)
	}
	// And the range it did return, so the reader knows what it has.
	if !strings.Contains(msg, "lines 1 to") {
		t.Errorf("the error should say which lines came back:\n%s", msg)
	}
	// And the way out.
	if !strings.Contains(msg, "read(path,") {
		t.Errorf("the error should say how to read the next part:\n%s", msg)
	}
	// The prefix itself must not be in the result: a script that catches the
	// throw and inspects the message must not find half a file in it.
	if strings.Contains(msg, strings.Repeat("x", 200)) {
		t.Error("the error must not carry the file content it refused to return")
	}
}

// The 2000-line cap is the same defect with a different trigger, so it takes
// the same route. Short lines keep the byte cap out of it.
func TestReadScriptLineCapTruncationIsAnError(t *testing.T) {
	dir := testsupport.TempDir(t)
	var b strings.Builder
	for i := 0; i < maxReadLines+500; i++ {
		b.WriteString("a\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "many.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ReadTool{CWD: dir}

	res := scriptRead(t, tool, map[string]any{"path": "many.txt"})
	if !res.IsError {
		t.Fatal("the line cap must fail a script read too")
	}
	msg := res.Content[0].(provider.TextBlock).Text
	if !strings.Contains(msg, fmt.Sprintf("%d line limit", maxReadLines)) {
		t.Errorf("the error should name the line limit:\n%s", msg)
	}
}

// The resume offset the error gives has to actually work. An error that names
// a limit but sends the reader to the wrong line is the same trap one step on.
func TestReadScriptTruncationResumeOffsetIsUsable(t *testing.T) {
	dir := testsupport.TempDir(t)
	bigLineFile(t, dir, "big.txt")
	tool := &ReadTool{CWD: dir}

	res := scriptRead(t, tool, map[string]any{"path": "big.txt"})
	next, ok := res.Details.(map[string]any)["next_offset"].(int)
	if !ok || next <= 1 {
		t.Fatalf("the result should carry a usable next_offset, got %v", res.Details)
	}
	if !strings.Contains(res.Content[0].(provider.TextBlock).Text, fmt.Sprintf("read(path, %d,", next)) {
		t.Error("the message and the details should agree on the resume offset")
	}

	// Reading from there, with a limit that fits, returns real content — and
	// it is the line that follows the last one the first read covered.
	page := scriptRead(t, tool, map[string]any{"path": "big.txt", "offset": next, "limit": 5})
	if page.IsError {
		t.Fatalf("the resume offset must not fail: %s", page.Content[0].(provider.TextBlock).Text)
	}
	body := page.Content[0].(provider.TextBlock).Text
	if !strings.HasPrefix(body, fmt.Sprintf("%04d", next-1)) {
		t.Errorf("resume landed on the wrong line; want the file's line %d, got %.20q", next, body)
	}
}

// A read that fits is untouched. The error is for a cut result only, so
// ordinary scripted reads keep working exactly as before.
func TestReadScriptUntruncatedReadIsUnaffected(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "small.txt"), []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ReadTool{CWD: dir}

	res := scriptRead(t, tool, map[string]any{"path": "small.txt"})
	if res.IsError {
		t.Fatalf("a read that fits must not fail: %s", res.Content[0].(provider.TextBlock).Text)
	}
	if got := res.Content[0].(provider.TextBlock).Text; got != "alpha\nbeta\ngamma\n" {
		t.Fatalf("content changed: %q", got)
	}
}

// The model's own read is untouched: it gets the prefix and the marker, which
// it can read and act on. Only the script path changed.
func TestReadConversationalTruncationStillReturnsThePrefix(t *testing.T) {
	dir := testsupport.TempDir(t)
	bigLineFile(t, dir, "big.txt")
	tool := &ReadTool{CWD: dir}

	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"path": "big.txt"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatal("a conversational truncated read must still succeed with a marker")
	}
	got := res.Content[0].(provider.TextBlock).Text
	if !strings.Contains(got, fmt.Sprintf("truncated at %d KiB", maxReadBytes/1024)) {
		t.Error("the conversational marker went missing")
	}
	if !strings.Contains(got, strings.Repeat("x", 200)) {
		t.Error("the conversational read must still return the file prefix")
	}
}
