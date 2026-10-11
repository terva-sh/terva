package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"terva.sh/terva/packages/agent/tools/resultcap"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// TKT-01M4K6893G. The byte cap kept the first selected line whole however long
// it was, so one read of a minified file or a compact JSON spill could put
// megabytes into the context. A line longer than the cap is now cut at the cap,
// and the note names the byte to continue from.

// continueRe matches every continuation a read note or a spill pointer gives:
// "continue with offset=N" after whole lines, and "Continue with
// byte_offset=N." after a cut inside a line.
var continueRe = regexp.MustCompile(`(?i)continue with (byte_offset|offset)=(\d+)`)

// readPage is one read result, split into the text before its note and the
// continuation the note names. key is "" when the result has no note.
type readPage struct {
	body string
	key  string
	val  int
}

func readOnce(t *testing.T, tool *ReadTool, args map[string]any) readPage {
	t.Helper()
	res, err := tool.Execute(context.Background(), mustJSON(t, args), nil)
	if err != nil {
		t.Fatalf("read %v: %v", args, err)
	}
	if res.IsError {
		t.Fatalf("read %v failed: %v", args, res.Content)
	}
	text := res.Content[0].(provider.TextBlock).Text
	// The note follows a blank line: the body ends with its own newline, and
	// the tool writes one more before the "... [" marker.
	i := strings.LastIndex(text, "\n... [")
	if i < 0 {
		return readPage{body: text}
	}
	m := continueRe.FindStringSubmatch(text[i:])
	if m == nil {
		t.Fatalf("the note names no continuation: %q", text[i:])
	}
	n, _ := strconv.Atoi(m[2])
	return readPage{body: text[:i], key: strings.ToLower(m[1]), val: n}
}

// lineStart returns the byte position of 1-indexed line n in s.
func lineStart(s string, n int) int {
	pos := 0
	for i := 1; i < n; i++ {
		j := strings.IndexByte(s[pos:], '\n')
		if j < 0 {
			return len(s)
		}
		pos += j + 1
	}
	return pos
}

// pageThrough reads file from args onward, and follows the continuation that
// each note names, until a result has no note. It returns the file bytes of
// each result. It fails the test when a piece does not match the file at the
// position the continuation named, when a continuation does not name the first
// byte left out, or when the pages do not reach the last byte.
func pageThrough(t *testing.T, tool *ReadTool, file string, args map[string]any) []string {
	t.Helper()
	pos := 0
	if b, ok := args["byte_offset"].(int); ok {
		pos = b
	} else if l, ok := args["offset"].(int); ok {
		pos = lineStart(file, l)
	}
	var pieces []string
	for call := 0; ; call++ {
		if call > 200 {
			t.Fatal("paging did not reach the end of the file in 200 reads")
		}
		p := readOnce(t, tool, args)
		piece := p.body
		switch {
		case p.key == "byte_offset":
			// A cut inside a line ends with a newline that the tool writes
			// before the note. That newline is not in the file.
			if !strings.HasSuffix(piece, "\n") {
				t.Fatalf("a cut piece does not end with the newline before its note: %.40q", piece)
			}
			piece = strings.TrimSuffix(piece, "\n")
		case p.key == "" && !strings.HasSuffix(file, "\n"):
			piece = strings.TrimSuffix(piece, "\n")
		}
		if piece == "" {
			t.Fatalf("read %v returned nothing and named %s=%d", args, p.key, p.val)
		}
		if !strings.HasPrefix(file[pos:], piece) {
			t.Fatalf("read %v returned bytes that are not the file at %d: %.40q", args, pos, piece)
		}
		pieces = append(pieces, piece)
		pos += len(piece)
		switch p.key {
		case "":
			if pos != len(file) {
				t.Fatalf("paging stopped at byte %d of %d with no note", pos, len(file))
			}
			return pieces
		case "byte_offset":
			if p.val != pos {
				t.Fatalf("the note names byte_offset=%d, but the first byte left out is %d", p.val, pos)
			}
			args = map[string]any{"path": args["path"], "byte_offset": p.val}
		case "offset":
			if got := lineStart(file, p.val); got != pos {
				t.Fatalf("the note names offset=%d at byte %d, but the first byte left out is %d", p.val, got, pos)
			}
			args = map[string]any{"path": args["path"], "offset": p.val}
		}
	}
}

// useSpillDir points os.TempDir at dir, so a spill lands where the test can
// read it and is removed with the directory.
func useSpillDir(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("TMPDIR", dir)
	if runtime.GOOS == "windows" {
		t.Setenv("TMP", dir)
		t.Setenv("TEMP", dir)
	}
}

// compactJSON builds a one-line JSON document of at least n bytes, with runes
// of two, three and four bytes, so that the cuts land inside some of them.
func compactJSON(n int) string {
	var b strings.Builder
	b.WriteString(`{"items":[`)
	for i := 0; b.Len() < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":%d,"name":"é€𝄞-%d"}`, i, i)
	}
	b.WriteString("]}")
	return b.String()
}

func TestReadCutsALineLongerThanTheCap(t *testing.T) {
	dir := testsupport.TempDir(t)
	line := compactJSON(4 * maxReadBytes)
	if err := os.WriteFile(filepath.Join(dir, "min.json"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ReadTool{CWD: dir}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"path": "min.json"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(provider.TextBlock).Text
	// The whole line came back before the fix: 200 KiB in one result.
	if len(text) > maxReadBytes+200 {
		t.Fatalf("one read returned %d bytes, over the %d byte cap", len(text), maxReadBytes)
	}
	p := readOnce(t, tool, map[string]any{"path": "min.json"})
	piece := strings.TrimSuffix(p.body, "\n")
	if len(piece) > maxReadBytes || !utf8.ValidString(piece) || !strings.HasPrefix(line, piece) {
		t.Fatalf("kept %d bytes (valid UTF-8: %v); want a prefix of at most %d", len(piece), utf8.ValidString(piece), maxReadBytes)
	}
	if p.key != "byte_offset" || p.val != len(piece) {
		t.Fatalf("the note names %s=%d, want byte_offset=%d", p.key, p.val, len(piece))
	}
	if !strings.Contains(text, "inside line 1") || !strings.Contains(text, "50 KiB") {
		t.Fatalf("the note does not name the line and the limit: %q", text[len(text)-120:])
	}
	d := res.Details.(map[string]any)
	if d["bytes_truncated"] != true || d["next_offset"] != 0 || d["next_byte_offset"] != len(piece) {
		t.Fatalf("details disagree with the note: %v", d)
	}
}

// AC4: a single-line spill reads through to its last byte, in pieces no larger
// than the cap. The spill and its pointer come from resultcap.Cap, as an MCP or
// extension result does, so the test also proves AC3 for that pointer.
func TestReadPagesASingleLineSpillToItsLastByte(t *testing.T) {
	dir := testsupport.TempDir(t)
	useSpillDir(t, dir)
	full := compactJSON(5 * resultcap.MaxBytes)

	out, path := resultcap.Cap([]provider.Content{provider.TextBlock{Text: full}}, "mcp")
	if path == "" {
		t.Fatal("an oversized result did not spill")
	}
	kept := out[0].(provider.TextBlock).Text
	m := continueRe.FindStringSubmatch(out[1].(provider.TextBlock).Text)
	if m == nil || m[1] != "byte_offset" {
		t.Fatalf("the spill pointer gives no byte_offset for a cut inside its only line: %q", out[1].(provider.TextBlock).Text)
	}
	start, _ := strconv.Atoi(m[2])
	if start != len(kept) {
		t.Fatalf("the spill pointer names byte_offset=%d, want %d", start, len(kept))
	}

	tool := &ReadTool{CWD: dir}
	pieces := pageThrough(t, tool, full, map[string]any{"path": path, "byte_offset": start})
	if len(pieces) < 4 {
		t.Fatalf("a %d byte line came back in %d pieces; the cap did not hold", len(full), len(pieces))
	}
	for i, p := range pieces {
		if len(p) > resultcap.MaxBytes {
			t.Fatalf("piece %d is %d bytes, over the %d byte cap", i, len(p), resultcap.MaxBytes)
		}
		if !utf8.ValidString(p) {
			t.Fatalf("piece %d splits a rune", i)
		}
	}
	if got := kept + strings.Join(pieces, ""); got != full {
		t.Fatalf("the pieces rebuild %d bytes of %d", len(got), len(full))
	}
}

// A file that mixes long lines with short ones pages through, and each note
// names the form that fits where the cut fell: a line after whole lines, and a
// byte inside a long line.
func TestReadPagesLongAndShortLinesThroughTheFile(t *testing.T) {
	dir := testsupport.TempDir(t)
	var b strings.Builder
	b.WriteString(strings.Repeat("a", 60*1024) + "\n")
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&b, "short line %d\n", i)
	}
	b.WriteString(compactJSON(120*1024) + "\n")
	b.WriteString("tail\n")
	file := b.String()
	if err := os.WriteFile(filepath.Join(dir, "mixed.txt"), []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ReadTool{CWD: dir}
	pieces := pageThrough(t, tool, file, map[string]any{"path": "mixed.txt"})
	for i, p := range pieces {
		// A whole first line may fill the cap and add its newline.
		if len(p) > maxReadBytes+1 {
			t.Fatalf("piece %d is %d bytes, over the cap", i, len(p))
		}
	}
	if strings.Join(pieces, "") != file {
		t.Fatal("the pieces do not rebuild the file")
	}
}

// A line of exactly the cap stays whole. Its newline does not push it over, so
// it is not split into a full piece and a one-byte piece.
func TestReadKeepsALineOfExactlyTheCapWhole(t *testing.T) {
	dir := testsupport.TempDir(t)
	line := strings.Repeat("y", maxReadBytes)
	if err := os.WriteFile(filepath.Join(dir, "edge.txt"), []byte(line+"\nz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := readOnce(t, &ReadTool{CWD: dir}, map[string]any{"path": "edge.txt"})
	if p.body != line+"\n" || p.key != "offset" || p.val != 2 {
		t.Fatalf("got %d bytes and %s=%d; want the whole line and offset=2", len(p.body), p.key, p.val)
	}
}

// A byte_offset inside a rune moves back to the rune's first byte, so the
// result never opens with half a character.
func TestReadByteOffsetInsideARuneMovesBack(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "r.txt"), []byte("aé€\nnext\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ReadTool{CWD: dir}
	for off, want := range map[int]string{2: "é€\nnext\n", 4: "€\nnext\n", 5: "€\nnext\n", 6: "\nnext\n", 7: "next\n"} {
		if got := readOnce(t, tool, map[string]any{"path": "r.txt", "byte_offset": off}).body; got != want {
			t.Errorf("byte_offset=%d returned %q, want %q", off, got, want)
		}
	}
}

// limit still counts lines with byte_offset. The part of the line that holds
// the byte counts as the first one.
func TestReadByteOffsetWithLimitCountsLines(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "l.txt"), []byte("0123456789\nb\nc\nd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := readOnce(t, &ReadTool{CWD: dir}, map[string]any{"path": "l.txt", "byte_offset": 7, "limit": 2}).body
	if got != "789\nb\n" {
		t.Fatalf("got %q, want %q", got, "789\nb\n")
	}
}

// offset and byte_offset both set the start of a read. A call with both is
// refused with a message that says which one to use, not answered from one.
func TestReadRefusesOffsetWithByteOffset(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("alpha\nbravo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ReadTool{CWD: dir}
	for _, tc := range []struct {
		args map[string]any
		want []string
	}{
		{map[string]any{"path": "f.txt", "offset": 2, "byte_offset": 3}, []string{"offset or byte_offset, not both", "give byte_offset alone"}},
		{map[string]any{"path": "f.txt", "byte_offset": -1}, []string{"negative", "first byte is 0"}},
	} {
		_, err := tool.Execute(context.Background(), mustJSON(t, tc.args), nil)
		if err == nil {
			t.Fatalf("read %v was not refused", tc.args)
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("read %v: the refusal %q does not say %q", tc.args, err, w)
			}
		}
	}
}

func TestReadByteOffsetPastTheEndExplains(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "s.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ReadTool{CWD: dir}
	for _, off := range []int{6, 51200} {
		res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{"path": "s.txt", "byte_offset": off}), nil)
		if err != nil {
			t.Fatal(err)
		}
		got := res.Content[0].(provider.TextBlock).Text
		if !strings.Contains(got, "past the end") || !strings.Contains(got, "6 byte(s)") {
			t.Fatalf("byte_offset=%d: want a past-the-end message that names the size, got %q", off, got)
		}
		if d := res.Details.(map[string]any); d["past_eof"] != true || d["total_bytes"] != 6 {
			t.Errorf("byte_offset=%d: details %v", off, d)
		}
	}
}

// A script cannot get a part of one line: every piece of it is a cut result,
// and a cut result throws in a script. The error sends the model to the read
// tool, which can return the line in parts.
func TestReadScriptLongLineNamesTheReadTool(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "min.json"), []byte(compactJSON(2*maxReadBytes)), 0o644); err != nil {
		t.Fatal(err)
	}
	res := scriptRead(t, &ReadTool{CWD: dir}, map[string]any{"path": "min.json"})
	if !res.IsError {
		t.Fatal("a cut inside a line must fail in script context")
	}
	msg := res.Content[0].(provider.TextBlock).Text
	for _, w := range []string{"line 1 is longer than the 50 KiB limit", "outside the script", "offset=1"} {
		if !strings.Contains(msg, w) {
			t.Errorf("the script error does not say %q: %q", w, msg)
		}
	}
}
