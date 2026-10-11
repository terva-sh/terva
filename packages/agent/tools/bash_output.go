package tools

import (
	"bytes"
	"strings"
	"sync"
	"unicode/utf8"

	"terva.sh/terva/packages/agent/tools/resultcap"
	"terva.sh/terva/packages/i18n"
)

// bashOutput is the bash tool's sink for a runner's output. It keeps two
// bounded windows on the stream: head holds the first maxBashBytes, and tail
// holds at least the last maxBashBytes. spill tees the complete stream to a
// temp file, so the "full output" link is genuinely full and not a relabeled
// copy of a capped buffer. total, newlines and last describe the whole stream.
// The mutex matters for an in-process runner, where a background job and the
// foreground can write at once.
//
// 🔑 The tail is kept because a command's verdict usually comes last: the error
// at the end of a long build, the summary of a test run. A head-only view drops
// exactly that part, and the model then pages the spill file to find it.
type bashOutput struct {
	mu       sync.Mutex
	head     bytes.Buffer
	tail     []byte
	spill    *resultcap.SpillWriter
	total    int64
	newlines int64
	last     byte
	progress func(string)
}

func (o *bashOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(p) > 0 {
		o.total += int64(len(p))
		o.newlines += int64(bytes.Count(p, []byte{'\n'}))
		o.last = p[len(p)-1]
		o.spill.Write(p)
		if room := maxBashBytes - o.head.Len(); room > 0 {
			o.head.Write(p[:min(len(p), room)])
		}
		o.keepTail(p)
	}
	if o.progress != nil {
		o.progress(string(p))
	}
	return len(p), nil
}

// keepTail appends p to the tail window. The window slides once it holds twice
// maxBashBytes, so a long stream costs one copy per maxBashBytes written.
func (o *bashOutput) keepTail(p []byte) {
	if len(p) >= maxBashBytes {
		o.tail = append(o.tail[:0], p[len(p)-maxBashBytes:]...)
		return
	}
	o.tail = append(o.tail, p...)
	if len(o.tail) > 2*maxBashBytes {
		o.tail = o.tail[:copy(o.tail, o.tail[len(o.tail)-maxBashBytes:])]
	}
}

// lines counts the lines of the stream. A last line with no newline counts.
func (o *bashOutput) lines() int64 {
	if o.total > 0 && o.last != '\n' {
		return o.newlines + 1
	}
	return o.newlines
}

// view returns the output text the model sees. Under both inline limits that
// is the whole stream. Over either limit it is the head and the tail, each
// inside half of each limit, with a marker line between them. saved says
// whether the spill file holds the full output, which decides whether the
// marker can point into it.
func (o *bashOutput) view(saved bool) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.total <= maxBashBytes && o.lines() <= maxBashLines {
		return o.head.String()
	}
	c := o.cut()
	var sb strings.Builder
	sb.Write(c.head)
	if len(c.head) > 0 && c.head[len(c.head)-1] != '\n' {
		sb.WriteByte('\n')
	}
	sb.WriteString(bashCutMarker(c.omitted, saved, c.first, c.last, resultcap.Continuation(string(c.head))))
	sb.WriteByte('\n')
	sb.Write(c.tail)
	return sb.String()
}

// bashCut is where view cuts a stream that passed a limit.
type bashCut struct {
	head, tail  []byte // the two parts the model sees
	omitted     int64  // the bytes between them
	first, last int64  // the spill-file lines that hold those bytes
}

// cut splits a stream that passed a limit. The caller holds mu.
func (o *bashOutput) cut() bashCut {
	head := o.head.Bytes()
	head = head[:headEnd(head, maxBashBytes/2, maxBashLines/2)]
	tail := o.tail
	if len(tail) > maxBashBytes {
		tail = tail[len(tail)-maxBashBytes:]
	}
	t := tailStart(tail, maxBashBytes/2, maxBashLines/2)
	c := bashCut{head: head, tail: tail[t:]}

	// The two parts never meet. Over the byte limit each holds at most half of
	// it. Over the line limit each holds at most half of the lines, so at least
	// one line, and so at least one byte, falls between them.
	c.omitted = o.total - int64(len(c.head)) - int64(len(c.tail))

	// The spill file is the stream, so its line numbers are the stream's. The
	// first omitted byte sits on the line after the head's last newline. The
	// last omitted byte sits on the line before the tail, or on the tail's own
	// first line when the tail starts inside a line.
	c.first = int64(bytes.Count(c.head, []byte{'\n'})) + 1
	c.last = o.newlines - int64(bytes.Count(c.tail, []byte{'\n'})) + 1
	if t == 0 || tail[t-1] == '\n' {
		c.last--
	}
	return c
}

// bashCutMarker is the line between the head and the tail of a cut output. It
// names the spill-file lines first to last that hold the cut part. The size is
// in KiB, because a byte count beside an offset is what a model mistakes for
// the offset.
//
// next is the read argument that starts at the first cut byte, from
// resultcap.Continuation over the head: offset=N when the head ends a line, and
// byte_offset=N when it ends inside one. A line offset there would return the
// part of the line the head already shows, and for a cut inside one long line
// it could not reach the cut part at all.
//
// The first page asks for at most maxReadLines, the most one read returns, and
// the last line says where to stop. A limit for the whole range reads as one
// call that read cannot answer. A cut inside one line asks for limit=1 and
// stops at the end of that line, and read's own notes page the line by byte.
//
// 🔑 The marker is the only continuation in the result. The bash footer names
// the file and points back here, so the model never gets two answers.
func bashCutMarker(omitted int64, saved bool, first, last int64, next string) string {
	kib := (omitted + 1023) / 1024
	switch {
	case !saved:
		return i18n.P("bash.cut.nospill",
			"[terva removed %d KiB from the middle of this output. terva could not save the full output to a file.]",
			kib)
	case first == last:
		return i18n.P("bash.cut.line",
			"[terva removed %d KiB from the middle of this output. The full output file has it in line %d. To read it, read that file with %s and limit=1, and continue to the end of that line.]",
			kib, first, next)
	}
	return i18n.P("bash.cut",
		"[terva removed %d KiB from the middle of this output. The full output file has it in lines %d to %d. To read it, read that file with %s and limit=%d, and continue to line %d.]",
		kib, first, last, next, min(last-first+1, maxReadLines), last)
}

// headEnd returns how many bytes of b to show as the head: at most maxBytes,
// and at most maxLines lines. A cut inside a line moves back to the last line
// end when one falls in the second half of the budget, and otherwise to a rune
// start, so the kept text stays valid UTF-8. b is a prefix of the stream.
func headEnd(b []byte, maxBytes, maxLines int) int {
	end := min(len(b), maxBytes)
	if i := nthNewline(b[:end], maxLines); i >= 0 {
		return i + 1
	}
	if end == len(b) || b[end-1] == '\n' {
		return end
	}
	if i := bytes.LastIndexByte(b[:end], '\n'); i >= 0 && i+1 >= maxBytes/2 {
		return i + 1
	}
	for k := 0; k < utf8.UTFMax && end > 0 && !utf8.RuneStart(b[end]); k++ {
		end--
	}
	return end
}

// tailStart returns the offset in b where the shown tail begins: at most the
// last maxBytes of b, and at most its last maxLines lines. A start inside a
// line moves forward to the next line start when one falls in the first half
// of the budget, and otherwise to a rune start. b is a suffix of the stream,
// and either the whole stream or longer than maxBytes, so offset 0 starts a
// line.
func tailStart(b []byte, maxBytes, maxLines int) int {
	start := max(0, len(b)-maxBytes)
	if i := lastLinesStart(b[start:], maxLines); i > 0 {
		return start + i
	}
	if start == 0 || b[start-1] == '\n' {
		return start
	}
	if i := bytes.IndexByte(b[start:], '\n'); i >= 0 && i+1 <= maxBytes/2 {
		return start + i + 1
	}
	for k := 0; k < utf8.UTFMax && start < len(b) && !utf8.RuneStart(b[start]); k++ {
		start++
	}
	return start
}

// nthNewline returns the index of the nth newline in b, or -1 when b has fewer.
func nthNewline(b []byte, n int) int {
	off := 0
	for {
		i := bytes.IndexByte(b[off:], '\n')
		if i < 0 {
			return -1
		}
		off += i
		if n--; n == 0 {
			return off
		}
		off++
	}
}

// lastLinesStart returns the offset in b where its last n lines begin, or 0
// when b holds n lines or fewer. A newline that ends b closes its last line and
// does not start another.
func lastLinesStart(b []byte, n int) int {
	end := len(b)
	if end > 0 && b[end-1] == '\n' {
		end--
	}
	for i := end - 1; i >= 0; i-- {
		if b[i] == '\n' {
			if n--; n == 0 {
				return i + 1
			}
		}
	}
	return 0
}
