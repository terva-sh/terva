package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
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

// feed writes stream into a fresh bashOutput in chunks of size n, the way a
// runner delivers it, and returns the sink. It spills nowhere.
func feed(stream []byte, n int) *bashOutput {
	o := &bashOutput{spill: resultcap.NewSpillWriter("bash-test")}
	for len(stream) > 0 {
		k := min(n, len(stream))
		_, _ = o.Write(stream[:k])
		stream = stream[k:]
	}
	o.spill.Close()
	o.spill.Discard()
	return o
}

// lineOf returns the 1-based line of stream that holds byte i.
func lineOf(stream []byte, i int64) int64 {
	return int64(bytes.Count(stream[:i], []byte{'\n'})) + 1
}

// checkCut holds the invariants of one cut against the stream it came from:
// the head is a prefix, the tail a suffix, nothing in between is shown, each
// part stays inside half of each limit, and the line range names exactly the
// lines that hold the omitted bytes.
func checkCut(t *testing.T, stream []byte, c bashCut) {
	t.Helper()
	total := int64(len(stream))
	if !bytes.HasPrefix(stream, c.head) {
		t.Fatalf("head is not a prefix of the stream")
	}
	if !bytes.HasSuffix(stream, c.tail) {
		t.Fatalf("tail is not a suffix of the stream")
	}
	if c.omitted <= 0 || int64(len(c.head))+c.omitted+int64(len(c.tail)) != total {
		t.Fatalf("head %d + omitted %d + tail %d != total %d", len(c.head), c.omitted, len(c.tail), total)
	}
	for name, part := range map[string][]byte{"head": c.head, "tail": c.tail} {
		if len(part) > maxBashBytes/2 {
			t.Errorf("%s holds %d bytes, over half the byte limit", name, len(part))
		}
		lines := bytes.Count(part, []byte{'\n'})
		if len(part) > 0 && part[len(part)-1] != '\n' {
			lines++
		}
		if lines > maxBashLines/2 {
			t.Errorf("%s holds %d lines, over half the line limit", name, lines)
		}
		if !utf8.Valid(part) && utf8.Valid(stream) {
			t.Errorf("%s cut a rune of valid UTF-8", name)
		}
	}
	firstOmitted := int64(len(c.head))
	lastOmitted := total - int64(len(c.tail)) - 1
	if want := lineOf(stream, firstOmitted); c.first != want {
		t.Errorf("first omitted line = %d, want %d", c.first, want)
	}
	if want := lineOf(stream, lastOmitted); c.last != want {
		t.Errorf("last omitted line = %d, want %d", c.last, want)
	}
}

func numberedLines(n int) []byte {
	var b bytes.Buffer
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.Bytes()
}

// A stream over either limit shows its head and its tail, and the marker names
// the spill-file lines that hold everything between them. The shapes cover the
// byte limit, the line limit, one long line, multi-byte runes, an unterminated
// last line, and chunk sizes from a byte at a time to one write larger than the
// whole window.
func TestBashOutputCutKeepsHeadAndTail(t *testing.T) {
	long := bytes.Repeat([]byte("x"), 200)
	cases := map[string][]byte{
		"byte limit, long lines":   bytes.Repeat(append(append([]byte{}, long...), '\n'), 1000),
		"line limit, short lines":  bytes.Repeat([]byte("y\n"), 5000),
		"one line, no newline":     bytes.Repeat([]byte("z"), 3*maxBashBytes),
		"multi-byte runes":         bytes.Repeat([]byte("€é"), maxBashBytes),
		"cuts inside a rune":       bytes.Repeat([]byte("€"), 40000),
		"numbered, unterminated":   append(numberedLines(9000), "the end"...),
		"just over the line limit": bytes.Repeat([]byte("\n"), maxBashLines+1),
		"long lines with a short line near each cut": bytes.Join([][]byte{
			bytes.Repeat([]byte("a"), maxBashBytes/2-10),
			[]byte("short"),
			bytes.Repeat([]byte("b"), maxBashBytes),
			[]byte("short"),
			bytes.Repeat([]byte("c"), maxBashBytes/2-10),
		}, []byte("\n")),
	}
	for name, stream := range cases {
		for _, chunk := range []int{1, 7, 4096, 3 * maxBashBytes} {
			if chunk == 1 && len(stream) > 64*1024 {
				continue // a byte at a time is slow and proves no more on a big stream
			}
			t.Run(fmt.Sprintf("%s/chunk %d", name, chunk), func(t *testing.T) {
				o := feed(stream, chunk)
				if o.total != int64(len(stream)) {
					t.Fatalf("total = %d, want %d", o.total, len(stream))
				}
				checkCut(t, stream, o.cut())
			})
		}
	}
}

// A stream under both limits comes back whole, with no marker. 2000 lines that
// each end in a newline are 2000 lines, not 2001, so they are not cut.
func TestBashOutputUnderLimitsIsWhole(t *testing.T) {
	for name, stream := range map[string][]byte{
		"empty":                  nil,
		"short":                  []byte("hello\n"),
		"exactly the line limit": bytes.Repeat([]byte("y\n"), maxBashLines),
		"exactly the byte limit": bytes.Repeat([]byte("q"), maxBashBytes),
		"no newline at the end":  []byte("a\nb"),
	} {
		t.Run(name, func(t *testing.T) {
			if got := feed(stream, 4096).view(true); got != string(stream) {
				t.Fatalf("view changed an output under both limits: %d bytes, want %d", len(got), len(stream))
			}
		})
	}
}

// The marker sits on its own line between the head and the tail. A head cut
// inside a line gets a line end first. With no spill file, the marker says the
// rest was not saved and names no offset.
func TestBashOutputViewMarksTheCut(t *testing.T) {
	stream := bytes.Repeat([]byte("z"), 3*maxBashBytes)
	o := feed(stream, 4096)
	c := o.cut()
	got := o.view(false)
	want := string(c.head) + "\n" +
		"[terva removed 100 KiB from the middle of this output. terva could not save the full output to a file.]\n" +
		string(c.tail)
	if got != want {
		t.Fatalf("view = %q...%q, want %q...%q", got[:20], got[len(got)-200:], want[:20], want[len(want)-200:])
	}
	// A cut inside the only line names that line, and starts the read at the
	// first cut byte. A line offset would return the head's part of the line
	// again, and could never reach the middle of it.
	want = fmt.Sprintf("The full output file has it in line 1. To read it, read that file with byte_offset=%d and limit=1, and continue to the end of that line.]", len(c.head))
	if !strings.Contains(o.view(true), want) {
		t.Errorf("a cut inside the only line does not give its byte_offset: %q", want)
	}
}

// The first page of the marker's advice stays inside what one read returns.
func TestBashCutMarkerPagesWithinTheReadLimit(t *testing.T) {
	got := bashCutMarker(300*1024, true, 1001, 8001, "offset=1001")
	want := "with offset=1001 and limit=2000, and continue to line 8001.]"
	if !strings.Contains(got, want) {
		t.Fatalf("marker = %q, want it to contain %q", got, want)
	}
	if got := bashCutMarker(1024, true, 10, 12, "offset=10"); !strings.Contains(got, "with offset=10 and limit=3, and continue to line 12.]") {
		t.Fatalf("a short range is not read in one page: %q", got)
	}
}

// A head that ends inside a line starts the marker's read at the first cut
// byte, not at the start of that line, so the first page repeats nothing the
// head already shows. A head that ends a line keeps the line offset.
func TestBashCutMarkerStartsAtTheFirstCutByte(t *testing.T) {
	for name, tc := range map[string]struct {
		stream []byte
		want   func(c bashCut) string
	}{
		"head ends inside a long first line": {
			stream: append(bytes.Repeat([]byte("a"), 40*1024), append([]byte("\n"), numberedLines(5000)...)...),
			want: func(c bashCut) string {
				return fmt.Sprintf("in lines 1 to %d. To read it, read that file with byte_offset=%d and limit=2000,", c.last, len(c.head))
			},
		},
		"head ends a line": {
			stream: numberedLines(9000),
			want: func(c bashCut) string {
				return fmt.Sprintf("in lines %d to %d. To read it, read that file with offset=%d and limit=2000,", c.first, c.last, c.first)
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			o := feed(tc.stream, 4096)
			c := o.cut()
			if got, want := o.view(true), tc.want(c); !strings.Contains(got, want) {
				t.Fatalf("the marker does not say %q", want)
			}
		})
	}
}

var bashMarkerRE = regexp.MustCompile(`\[terva removed \d+ KiB from the middle of this output\. The full output file has it in lines (\d+) to (\d+)\. To read it, read that file with offset=(\d+) and limit=(\d+), and continue to line (\d+)\.\]`)

var readContinueRE = regexp.MustCompile(`\n\n\.\.\. \[output truncated at [^\]]* continue with offset=(\d+)\]\n$`)

// followMarker does what the marker tells the model to do. It reads the spill
// file from the marker's offset with the marker's limit, follows each
// continuation that read gives, and stops at the marker's last line. It returns
// the text it read and the number of read calls.
func followMarker(t *testing.T, rt *ReadTool, path string, offset, limit, last int) (string, int) {
	t.Helper()
	var got strings.Builder
	calls := 0
	for offset <= last {
		n := min(limit, last-offset+1)
		res, err := rt.Execute(context.Background(), mustJSON(t, map[string]any{"path": path, "offset": offset, "limit": n}), nil)
		if err != nil {
			t.Fatal(err)
		}
		calls++
		text := res.Content[0].(provider.TextBlock).Text
		next := offset + n
		if m := readContinueRE.FindStringSubmatchIndex(text); m != nil {
			next, _ = strconv.Atoi(text[m[2]:m[3]])
			text = text[:m[0]+1]
		}
		if strings.Contains(text, "[output truncated") || next <= offset {
			t.Fatalf("read at offset %d returned a note the walk cannot follow: %q", offset, text[max(0, len(text)-200):])
		}
		got.WriteString(text)
		offset = next
	}
	return got.String(), calls
}

// The remedy the marker names works. The test follows the marker through the
// spill file, page by page, and the head, the pages and the tail put back the
// whole output byte for byte. The error at the end of a long run stays in view,
// above the exit footer. One command hits the line limit and pages by the
// marker's limit. The other hits the byte limit and pages by read's own
// continuation notes.
func TestBashKeepsTheTailAndTheMarkerReadsTheMiddle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell only")
	}
	for _, tc := range []struct {
		name, command string
		detail        string
		minCalls      int
	}{
		{
			name:     "line limit",
			command:  `i=1; while [ $i -le 9000 ]; do echo "line $i"; i=$((i+1)); done; echo "FATAL: the build broke" >&2; exit 2`,
			detail:   "lines_truncated",
			minCalls: 4, // 7001 omitted lines at 2000 a page
		},
		{
			name:     "byte limit",
			command:  `pad=$(printf '%0280d' 0); i=1; while [ $i -le 1500 ]; do echo "line $i $pad"; i=$((i+1)); done; echo "FATAL: the build broke" >&2; exit 2`,
			detail:   "bytes_truncated",
			minCalls: 7, // about 380 KiB omitted at 50 KiB a page
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := testsupport.TempDir(t)
			res, err := (&BashTool{CWD: dir}).Execute(context.Background(), mustJSON(t, map[string]any{
				"command": tc.command,
				"timeout": 60,
			}), nil)
			if err != nil {
				t.Fatal(err)
			}
			got := res.Content[0].(provider.TextBlock).Text
			d := res.Details.(map[string]any)
			if d[tc.detail] != true {
				t.Fatalf("expected %s, details: %v", tc.detail, d)
			}
			path, _ := d["full_output_path"].(string)
			if path == "" {
				t.Fatal("no spill file for a cut output")
			}
			t.Cleanup(func() { _ = os.Remove(path) })
			full, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(got, "truncated at") {
				t.Errorf("a trailing truncation line survived: the marker replaces it")
			}

			// The result is "$ command", a blank line, the head, the marker
			// line, the tail, a blank line, and the exit footer.
			body := strings.TrimPrefix(got, "$ "+tc.command+"\n\n")
			loc := bashMarkerRE.FindStringSubmatchIndex(body)
			if loc == nil {
				t.Fatalf("no cut marker in the output:\n%s", got[:min(len(got), 400)])
			}
			num := func(i int) int { n, _ := strconv.Atoi(body[loc[2*i]:loc[2*i+1]]); return n }
			first, last, offset, limit, stop := num(1), num(2), num(3), num(4), num(5)
			if offset != first || stop != last || limit > maxReadLines {
				t.Fatalf("marker advice offset=%d limit=%d stop=%d does not page lines %d to %d", offset, limit, stop, first, last)
			}
			head := body[:loc[0]]
			footer := strings.Index(body, "\n\n[exit 2]")
			if footer < 0 {
				t.Fatalf("the exit footer is not last:\n%s", body[max(0, len(body)-400):])
			}
			tail := body[loc[1]+1 : footer+1]
			if !strings.HasSuffix(tail, "FATAL: the build broke\n") {
				t.Errorf("the error at the end is not in view above the exit footer")
			}

			middle, calls := followMarker(t, &ReadTool{CWD: dir}, path, offset, limit, last)
			if head+middle+tail != string(full) {
				t.Fatalf("head %d + pages %d + tail %d bytes do not put back the %d-byte output",
					len(head), len(middle), len(tail), len(full))
			}
			if calls < tc.minCalls {
				t.Errorf("the walk took %d reads, want at least %d: it did not page", calls, tc.minCalls)
			}
		})
	}
}

var bashLineMarkerRE = regexp.MustCompile(`\[terva removed \d+ KiB from the middle of this output\. The full output file has it in line (\d+)\. To read it, read that file with byte_offset=(\d+) and limit=1, and continue to the end of that line\.\]`)

// TKT-01M4K6893G. When the cut falls inside one long line, the marker names the
// byte_offset of the first cut byte. The test does what the marker says: it
// reads from there with limit=1, follows each byte_offset that read gives to
// the end of the line, and the head, the pages and the tail put back the whole
// output byte for byte. Paging by line could not do this, because every read
// of the line would start at its first byte.
func TestBashMarkerReadsTheMiddleOfOneLongLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell only")
	}
	const command = `seq 1 40000 | tr '\n' ,; echo; echo "FATAL: the build broke" >&2; exit 2`
	dir := testsupport.TempDir(t)
	res, err := (&BashTool{CWD: dir}).Execute(context.Background(), mustJSON(t, map[string]any{
		"command": command,
		"timeout": 60,
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := res.Content[0].(provider.TextBlock).Text
	path, _ := res.Details.(map[string]any)["full_output_path"].(string)
	if path == "" {
		t.Fatal("no spill file for a cut output")
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	fullBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	full := string(fullBytes)
	lineEnd := strings.IndexByte(full, '\n') + 1 // the end of the long line, after its newline

	body := strings.TrimPrefix(got, "$ "+command+"\n\n")
	loc := bashLineMarkerRE.FindStringSubmatchIndex(body)
	if loc == nil {
		t.Fatalf("no single-line cut marker with a byte_offset in the output:\n%s", got[:min(len(got), 400)])
	}
	if line := body[loc[2]:loc[3]]; line != "1" {
		t.Fatalf("the marker names line %s, want 1", line)
	}
	start, _ := strconv.Atoi(body[loc[4]:loc[5]])
	// The head ends inside the line, so the view adds a line end before the
	// marker. The byte_offset is the head's length in the spill.
	if start+1 != loc[0] || body[start] != '\n' || !strings.HasPrefix(full, body[:start]) {
		t.Fatalf("byte_offset=%d is not the end of the head the model saw", start)
	}
	head := body[:start]
	footer := strings.Index(body, "\n\n[exit 2]")
	if footer < 0 {
		t.Fatalf("the exit footer is not last:\n%s", body[max(0, len(body)-400):])
	}
	tail := body[loc[1]+1 : footer+1]
	if !strings.HasSuffix(tail, "FATAL: the build broke\n") || !strings.HasSuffix(full, tail) {
		t.Fatalf("the tail is not the end of the output with the error in view")
	}
	tailStart := len(full) - len(tail)
	if tailStart < start || tailStart > lineEnd {
		t.Fatalf("the tail starts at %d, outside the cut line %d..%d", tailStart, start, lineEnd)
	}

	rt := &ReadTool{CWD: dir}
	args := map[string]any{"path": path, "byte_offset": start, "limit": 1}
	var middle strings.Builder
	calls := 0
	for {
		if calls++; calls > 50 {
			t.Fatal("the walk did not reach the end of the line in 50 reads")
		}
		p := readOnce(t, rt, args)
		piece := p.body
		if p.key == "byte_offset" {
			piece = strings.TrimSuffix(piece, "\n") // the line end before the note is not in the file
		}
		if len(piece) > maxReadBytes+1 {
			t.Fatalf("read %d returned %d bytes, over the cap", calls, len(piece))
		}
		middle.WriteString(piece)
		pos := start + middle.Len()
		switch p.key {
		case "byte_offset":
			if p.val != pos {
				t.Fatalf("read names byte_offset=%d, but the first byte left out is %d", p.val, pos)
			}
			args = map[string]any{"path": path, "byte_offset": p.val, "limit": 1}
			continue
		case "":
		default:
			t.Fatalf("read inside the line named %s=%d", p.key, p.val)
		}
		break
	}
	if start+middle.Len() != lineEnd || full[start:lineEnd] != middle.String() {
		t.Fatalf("the walk read %d bytes from %d; want the rest of the line up to %d", middle.Len(), start, lineEnd)
	}
	if head+middle.String()[:tailStart-start]+tail != full {
		t.Fatal("the head, the pages and the tail do not put back the output")
	}
	if want := (tailStart-start)/maxReadBytes + 1; calls < want {
		t.Errorf("the walk took %d reads, want at least %d: it did not page", calls, want)
	}
}

// A spill the bash tool keeps belongs to the session in its context, and goes
// when the session removes its spills.
func TestBashSpillBelongsToTheSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell only")
	}
	var spills resultcap.Spills
	ctx := resultcap.WithSpills(context.Background(), &spills)
	tool := &BashTool{CWD: testsupport.TempDir(t)}
	res, err := tool.Execute(ctx, mustJSON(t, map[string]any{
		"command": fmt.Sprintf("yes | head -n %d", maxBashLines+10),
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	path, _ := res.Details.(map[string]any)["full_output_path"].(string)
	if path == "" {
		t.Fatal("no spill file for a cut output")
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the spill is gone before the session ended: %v", err)
	}
	spills.Remove()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the spill outlived its session: %v", err)
	}
}
