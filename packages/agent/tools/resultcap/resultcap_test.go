package resultcap

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// useTempDir points os.TempDir at a fresh directory for the test, so a spill
// lands where the test can see it and is removed afterwards.
func useTempDir(t *testing.T) string {
	t.Helper()
	dir := testsupport.TempDir(t)
	t.Setenv("TMPDIR", dir)
	if runtime.GOOS == "windows" {
		t.Setenv("TMP", dir)
		t.Setenv("TEMP", dir)
	}
	return dir
}

// numbered returns n lines of the form "line 000017 ...", each width bytes
// with its newline, so a test can name the line a cut fell on.
func numbered(n, width int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		head := fmt.Sprintf("line %06d ", i)
		b.WriteString(head + strings.Repeat("x", width-len(head)-1) + "\n")
	}
	return b.String()
}

func textOf(t *testing.T, c provider.Content) string {
	t.Helper()
	tb, ok := c.(provider.TextBlock)
	if !ok {
		t.Fatalf("want a text block, got %T", c)
	}
	return tb.Text
}

func inlineBytes(content []provider.Content) int {
	n := 0
	for _, c := range content[:len(content)-1] { // the last block is the pointer
		if tb, ok := c.(provider.TextBlock); ok {
			n += len(tb.Text)
		}
	}
	return n
}

var offsetRe = regexp.MustCompile(`Continue with offset=(\d+)\.`)

// continueLine reads the line number the pointer gives.
func continueLine(t *testing.T, pointer string) int {
	t.Helper()
	m := offsetRe.FindStringSubmatch(pointer)
	if m == nil {
		t.Fatalf("pointer gives no offset: %q", pointer)
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// checkSpill asserts the spill file holds want, sits in dir, and is private.
func checkSpill(t *testing.T, path, dir, want string) {
	t.Helper()
	if filepath.Dir(path) != dir || !strings.HasPrefix(filepath.Base(path), "terva-test-") || !strings.HasSuffix(path, ".log") {
		t.Fatalf("spill path %q is not terva-test-<hex>.log in %s", path, dir)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("spill file unreadable: %v", err)
	}
	if string(got) != want {
		t.Fatalf("spill file holds %d bytes, want the full %d", len(got), len(want))
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("spill file mode = %o, want 600", mode)
		}
	}
}

func TestCapLeavesSmallResultAlone(t *testing.T) {
	dir := useTempDir(t)
	in := []provider.Content{
		provider.TextBlock{Text: strings.Repeat("a", MaxBytes/2)},
		provider.ImageBlock{MimeType: "image/png", Data: make([]byte, 4*MaxBytes)},
		provider.TextBlock{Text: strings.Repeat("b", MaxBytes/2)},
	}
	out, path := Cap(in, "test")
	if path != "" {
		t.Fatalf("a result at the limit spilled to %q", path)
	}
	if len(out) != len(in) || textOf(t, out[0]) != textOf(t, in[0]) || textOf(t, out[2]) != textOf(t, in[2]) {
		t.Fatalf("a result at the limit changed: %d blocks", len(out))
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a result at the limit wrote %d file(s)", len(entries))
	}
}

func TestCapCutsAtALineAndSpillsTheRest(t *testing.T) {
	dir := useTempDir(t)
	full := numbered(4000, 64) // 256000 bytes, 800 lines per 50 KiB
	out, path := Cap([]provider.Content{provider.TextBlock{Text: full}}, "test")
	if path == "" {
		t.Fatal("an oversized result did not spill")
	}
	checkSpill(t, path, dir, full)
	if len(out) != 2 {
		t.Fatalf("want the cut text and a pointer, got %d blocks", len(out))
	}
	kept := textOf(t, out[0])
	if len(kept) > MaxBytes || !strings.HasPrefix(full, kept) || !strings.HasSuffix(kept, "\n") {
		t.Fatalf("kept %d bytes; want a whole-line prefix of at most %d", len(kept), MaxBytes)
	}
	pointer := textOf(t, out[1])
	// The path ends its line, so no punctuation can ride into a read call.
	if !strings.Contains(pointer, "\nfull output: "+path+"\n") || !strings.Contains(pointer, "50 KiB limit") || !strings.Contains(pointer, "250 KiB") {
		t.Fatalf("pointer does not name the limit, the size and the file: %q", pointer)
	}
	// The offset must name the first line the model has not seen.
	line := continueLine(t, pointer)
	if want := strings.Count(kept, "\n") + 1; line != want {
		t.Fatalf("offset = %d, want %d", line, want)
	}
	lines := strings.Split(full, "\n")
	if !strings.HasPrefix(lines[line-1], fmt.Sprintf("line %06d ", line)) {
		t.Fatalf("spill line %d is %q", line, lines[line-1][:20])
	}
}

// A single long line, the usual shape of a JSON result, cuts at the limit and
// never inside a rune.
func TestCapCutsALongLineOnARuneBoundary(t *testing.T) {
	useTempDir(t)
	full := "x" + strings.Repeat("é", MaxBytes) // two bytes a rune, so MaxBytes falls inside one
	out, path := Cap([]provider.Content{provider.TextBlock{Text: full}}, "test")
	if path == "" {
		t.Fatal("an oversized result did not spill")
	}
	kept := textOf(t, out[0])
	if !utf8.ValidString(kept) || len(kept) != MaxBytes-1 {
		t.Fatalf("kept %d bytes (valid UTF-8: %v), want %d", len(kept), utf8.ValidString(kept), MaxBytes-1)
	}
	// A line offset would send the reader back to byte 0, so the pointer must
	// name the first byte left out instead.
	pointer := textOf(t, out[1])
	if strings.Contains(pointer, "offset=1.") && !strings.Contains(pointer, "byte_offset=") {
		t.Fatalf("a cut inside the only line continues by line: %q", pointer)
	}
	if off := continueByte(t, pointer); off != len(kept) {
		t.Fatalf("byte_offset = %d, want %d", off, len(kept))
	}
}

var byteOffsetRe = regexp.MustCompile(`Continue with byte_offset=(\d+)\.`)

// continueByte reads the byte position the pointer gives.
func continueByte(t *testing.T, pointer string) int {
	t.Helper()
	m := byteOffsetRe.FindStringSubmatch(pointer)
	if m == nil {
		t.Fatalf("pointer gives no byte_offset: %q", pointer)
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// A cut inside a line of a later block counts the earlier blocks and the
// newline that joins them in the spill, so the position is a spill position.
func TestCapCountsEarlierBlocksInTheBytePosition(t *testing.T) {
	useTempDir(t)
	first := "head line\n"
	second := strings.Repeat("{\"k\":1}", MaxBytes/4) // one line, far over the budget
	out, path := Cap([]provider.Content{provider.TextBlock{Text: first}, provider.TextBlock{Text: second}}, "test")
	if path == "" {
		t.Fatal("an oversized result did not spill")
	}
	kept := textOf(t, out[1])
	off := continueByte(t, textOf(t, out[2]))
	if want := len(first) + 1 + len(kept); off != want {
		t.Fatalf("byte_offset = %d, want %d", off, want)
	}
	spilled, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(spilled[:off]) != first+"\n"+kept {
		t.Fatal("the spill bytes before byte_offset are not the text the model saw")
	}
}

// A cut that ends a line still continues by line: offset is what read pages
// by, and a byte here would hand the model the unfamiliar form for no gain.
func TestContinuation(t *testing.T) {
	for _, tc := range []struct{ seen, want string }{
		{"", "offset=1"},
		{"abc\n", "offset=2"},
		{"a\nb\n", "offset=3"},
		{"abc", "byte_offset=3"},
		{"a\nbc", "byte_offset=4"},
		{"é", "byte_offset=2"},
	} {
		if got := Continuation(tc.seen); got != tc.want {
			t.Errorf("Continuation(%q) = %q, want %q", tc.seen, got, tc.want)
		}
	}
}

func TestRuneCut(t *testing.T) {
	invalid := "ab" + strings.Repeat("\x80", 8) // continuation bytes with no start
	for _, tc := range []struct {
		s    string
		n    int
		want int
	}{
		{"abc", -1, 0},
		{"abc", 0, 0},
		{"abc", 2, 2},
		{"abc", 3, 3},
		{"abc", 9, 3},
		{"aé", 2, 1},           // inside a two-byte rune
		{"a€", 3, 1},           // inside a three-byte rune, at its last byte
		{"a𝄞", 4, 1},           // inside a four-byte rune, at its last byte
		{"a𝄞b", 5, 5},          // after the rune: no move
		{"€", 2, 0},            // a rune at the start moves the cut to 0
		{invalid, 7, 7},        // no rune start within three bytes: cut at n
		{invalid, 4, 1},        // the b three bytes back is a rune start: move to it
		{"\x80\x80\x80", 2, 0}, // the start of the text is always a cut point
	} {
		if got := RuneCut(tc.s, tc.n); got != tc.want {
			t.Errorf("RuneCut(%q, %d) = %d, want %d", tc.s, tc.n, got, tc.want)
		}
	}
}

// Images keep their place, count for nothing, and survive past the cut. Text
// blocks after the cut are dropped from the inline view, and the spill joins
// every text block with one newline so the offset still counts true.
func TestCapKeepsImagesAndJoinsBlocksInTheSpill(t *testing.T) {
	dir := useTempDir(t)
	img1 := provider.ImageBlock{MimeType: "image/png", Data: []byte{1, 2, 3}}
	img2 := provider.ImageBlock{MimeType: "image/jpeg", Data: make([]byte, 2*MaxBytes)}
	first := numbered(10, 32)
	second := numbered(2000, 64)
	third := "after the cut\n"
	in := []provider.Content{img1, provider.TextBlock{Text: first}, img2, provider.TextBlock{Text: second}, provider.TextBlock{Text: third}}

	out, path := Cap(in, "test")
	checkSpill(t, path, dir, first+"\n"+second+"\n"+third)
	if len(out) != 5 {
		t.Fatalf("want image, text, image, cut text, pointer; got %d blocks", len(out))
	}
	if ib, ok := out[0].(provider.ImageBlock); !ok || ib.MimeType != "image/png" || string(ib.Data) != string(img1.Data) {
		t.Fatalf("first image changed: %#v", out[0])
	}
	if ib, ok := out[2].(provider.ImageBlock); !ok || ib.MimeType != "image/jpeg" || len(ib.Data) != len(img2.Data) {
		t.Fatalf("second image changed: %#v", out[2])
	}
	if textOf(t, out[1]) != first {
		t.Fatal("a text block under the budget was changed")
	}
	if n := inlineBytes(out); n > MaxBytes {
		t.Fatalf("inline text is %d bytes, over %d", n, MaxBytes)
	}
	cut := textOf(t, out[3])
	line := continueLine(t, textOf(t, out[4]))
	// first has 10 lines, the separator closes the empty line after it, and
	// the cut block starts on line 12.
	if want := 12 + strings.Count(cut, "\n"); line != want {
		t.Fatalf("offset = %d, want %d", line, want)
	}
	spilled, _ := os.ReadFile(path)
	got := strings.Split(string(spilled), "\n")[line-1]
	if want := strings.Split(second, "\n")[strings.Count(cut, "\n")]; got != want {
		t.Fatalf("spill line %d is %q, want %q", line, got, want)
	}
}

// A spill that cannot be written still cuts the result. The bound is what
// protects the context window, so a full disk must not lift it.
func TestCapStillCutsWhenTheSpillFails(t *testing.T) {
	dir := useTempDir(t)
	missing := filepath.Join(dir, "gone")
	t.Setenv("TMPDIR", missing)
	if runtime.GOOS == "windows" {
		t.Setenv("TMP", missing)
		t.Setenv("TEMP", missing)
	}
	out, path := Cap([]provider.Content{provider.TextBlock{Text: strings.Repeat("z", 2*MaxBytes)}}, "test")
	if path != "" {
		t.Fatalf("a failed spill reported path %q", path)
	}
	if n := inlineBytes(out); n != MaxBytes {
		t.Fatalf("inline text is %d bytes, want %d", n, MaxBytes)
	}
	pointer := textOf(t, out[len(out)-1])
	if !strings.Contains(pointer, "could not save the rest") || strings.Contains(pointer, "full output:") {
		t.Fatalf("pointer after a failed spill: %q", pointer)
	}
}

func TestCutPoint(t *testing.T) {
	for _, tc := range []struct {
		s      string
		budget int
		want   int
	}{
		{"abc", 0, 0},
		{"abc", 5, 3},
		{"aaaa\nbbbbbb", 8, 5},   // the newline is in the second half: cut after it
		{"a\nbbbbbbbbbb", 8, 8},  // the newline is too early: cut at the budget
		{"aaaaaaaé", 8, 7},       // the budget falls inside é: back up to its start
		{"aaaaaa\néé", 8, 7},     // a newline wins over a rune boundary
		{"\nxxxxxxxxxxxx", 1, 1}, // a cut right after a newline at the very start
	} {
		if got := cutPoint(tc.s, tc.budget); got != tc.want {
			t.Errorf("cutPoint(%q, %d) = %d, want %d", tc.s, tc.budget, got, tc.want)
		}
	}
}
