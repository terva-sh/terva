//go:build terva_scripting

package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// End-to-end for F2 through a real script. The untagged tests prove the read
// tool refuses; these prove the refusal actually reaches the program as a
// catchable throw, and that the way out the message names really works.

// TestCodeExecutionReadTruncationThrows: the script sees an exception, not a
// prefix with an English sentence stapled to the end of it.
func TestCodeExecutionReadTruncationThrows(t *testing.T) {
	dir := testsupport.TempDir(t)
	bigLineFile(t, dir, "big.txt")
	tool, _ := scriptOverRealRead(dir)

	out := runScript(t, tool, `
		try {
			const s = read("big.txt");
			print("NO_THROW len:" + s.length);
		} catch (e) {
			print("CAUGHT: " + e);
		}
	`)

	if strings.Contains(out, "NO_THROW") {
		t.Fatalf("the script got a cut result instead of a throw:\n%s", out)
	}
	if !strings.Contains(out, "CAUGHT:") {
		t.Fatalf("the refusal should reach the script as a catchable error:\n%s", out)
	}
	// The message the program can print must name the limit and the way on.
	if !strings.Contains(out, "50 KiB") {
		t.Errorf("the thrown message should name the limit:\n%s", out)
	}
	if !strings.Contains(out, "read(path,") {
		t.Errorf("the thrown message should name the way to page:\n%s", out)
	}
}

// bigJSONFile writes a valid JSON document spread over enough lines that the
// byte cap engages. One line per element: the cap counts whole lines, and a
// single over-long line is returned intact rather than cut.
func bigJSONFile(t *testing.T, dir, name string) {
	t.Helper()
	pad := strings.Repeat("x", 200)
	var b strings.Builder
	b.WriteString("[\n")
	for i := 0; i < 2000; i++ {
		if i > 0 {
			b.WriteString(",\n")
		}
		fmt.Fprintf(&b, `{"i":%d,"pad":%q}`, i, pad)
	}
	b.WriteString("\n]\n")
	if err := os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The failure that cost the recorded session 30 turns, in the form that still
// reaches a script: JSON.parse of a document read past the cap. It used to
// fail with "invalid character" pointing at the English marker, which reads as
// a bug in the FILE. Now it fails at the read, and says why.
//
// (The session's other form, eval() of a cut program, can no longer happen at
// all: the binding pre-check refuses a script containing eval before it runs.)
func TestCodeExecutionJSONParseOfATruncatedReadFailsAtTheRead(t *testing.T) {
	dir := testsupport.TempDir(t)
	bigJSONFile(t, dir, "big.json")
	tool, _ := scriptOverRealRead(dir)

	out := runScript(t, tool, `
		try {
			const data = JSON.parse(read("big.json"));
			print("PARSED count:" + data.length);
		} catch (e) {
			print("ERR: " + e);
		}
	`)
	if strings.Contains(out, "PARSED") {
		t.Fatalf("a cut document must not reach JSON.parse:\n%s", out)
	}
	if !strings.Contains(out, "50 KiB") {
		t.Errorf("the failure should name the cap, not look like bad JSON in the file:\n%s", out)
	}
	// The old failure blamed the document. The new one must not read that way.
	if strings.Contains(out, "invalid character") {
		t.Errorf("the script still sees a parse error, so it will blame the file:\n%s", out)
	}
}

// The workaround the message and both descriptions promise has to work from
// inside a script: an explicit limit that fits returns content, not an error.
func TestCodeExecutionReadPagesWithAnExplicitLimit(t *testing.T) {
	dir := testsupport.TempDir(t)
	bigLineFile(t, dir, "big.txt")
	tool, _ := scriptOverRealRead(dir)

	out := runScript(t, tool, `
		const first = read("big.txt", 1, 10);
		const next = read("big.txt", 11, 10);
		print("first:" + first.split("\n")[0].slice(0, 4));
		print("next:" + next.split("\n")[0].slice(0, 4));
		print("lines:" + (first.trim().split("\n").length));
	`)

	if !strings.Contains(out, "first:0000") {
		t.Errorf("a bounded read should return the head of the file:\n%s", out)
	}
	if !strings.Contains(out, "next:0010") {
		t.Errorf("paging with offset should land on the next page:\n%s", out)
	}
	if !strings.Contains(out, "lines:10") {
		t.Errorf("the limit should be honoured:\n%s", out)
	}
}

// A script whose reads fit is untouched by any of this.
func TestCodeExecutionReadUnderTheCapIsUnaffected(t *testing.T) {
	dir := testsupport.TempDir(t)
	tool, _ := scriptOverRealRead(dir)
	if err := os.WriteFile(filepath.Join(dir, "small.txt"), []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runScript(t, tool, `print("body:" + read("small.txt").trim().split("\n").join("|"));`)
	if !strings.Contains(out, "body:alpha|beta") {
		t.Fatalf("an in-cap read should behave exactly as before:\n%s", out)
	}
}
