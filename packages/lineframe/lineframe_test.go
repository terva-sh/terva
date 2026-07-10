package lineframe

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

func TestReadFrameNormalLines(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("one\ntwo\n"))
	for _, want := range []string{"one", "two"} {
		line, tooLong, err := ReadFrame(r, DefaultMaxBytes)
		if err != nil || tooLong {
			t.Fatalf("ReadFrame(%q): tooLong=%v err=%v", want, tooLong, err)
		}
		if string(line) != want {
			t.Fatalf("ReadFrame = %q, want %q", line, want)
		}
	}
	if _, _, err := ReadFrame(r, DefaultMaxBytes); err != io.EOF {
		t.Fatalf("want io.EOF at end, got %v", err)
	}
}

// The whole point of ReadFrame over bufio.Scanner: an over-limit frame is
// drained and flagged, and the NEXT frame reads normally — one oversized
// payload from a peer must not kill the stream.
func TestReadFrameRecoversFromOversized(t *testing.T) {
	huge := strings.Repeat("x", DefaultMaxBytes+1)
	r := bufio.NewReader(strings.NewReader(huge + "\nafter\n"))

	line, tooLong, err := ReadFrame(r, DefaultMaxBytes)
	if err != nil {
		t.Fatalf("oversized frame returned err %v", err)
	}
	if !tooLong || line != nil {
		t.Fatalf("oversized frame: tooLong=%v line-len=%d, want tooLong with nil line", tooLong, len(line))
	}

	line, tooLong, err = ReadFrame(r, DefaultMaxBytes)
	if err != nil || tooLong {
		t.Fatalf("frame after oversized: tooLong=%v err=%v", tooLong, err)
	}
	if string(line) != "after" {
		t.Fatalf("frame after oversized = %q, want %q", line, "after")
	}
}

// A final unterminated line surfaces with io.EOF; exactly at the limit is not
// over it.
func TestReadFrameEdges(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("tail-no-newline"))
	line, tooLong, err := ReadFrame(r, DefaultMaxBytes)
	if err != io.EOF || tooLong || string(line) != "tail-no-newline" {
		t.Fatalf("unterminated tail: line=%q tooLong=%v err=%v", line, tooLong, err)
	}

	exact := strings.Repeat("y", DefaultMaxBytes)
	r = bufio.NewReader(strings.NewReader(exact + "\n"))
	line, tooLong, err = ReadFrame(r, DefaultMaxBytes)
	if err != nil || tooLong || len(line) != DefaultMaxBytes {
		t.Fatalf("exact-limit frame: len=%d tooLong=%v err=%v", len(line), tooLong, err)
	}
}

// The limit is a parameter: a small custom max rejects a frame the default
// would accept, and still recovers for the next frame.
func TestReadFrameCustomLimit(t *testing.T) {
	const max = 8
	r := bufio.NewReader(strings.NewReader("123456789\nok\n")) // 9 bytes then a short frame
	line, tooLong, err := ReadFrame(r, max)
	if err != nil || !tooLong || line != nil {
		t.Fatalf("9 bytes over max=%d: line=%q tooLong=%v err=%v", max, line, tooLong, err)
	}
	line, tooLong, err = ReadFrame(r, max)
	if err != nil || tooLong || string(line) != "ok" {
		t.Fatalf("frame after over-limit: line=%q tooLong=%v err=%v", line, tooLong, err)
	}
}

// Reader is the carrier policy: skip-and-warn on oversized, deliver the final
// unterminated line before EOF, strip one trailing CR.
func TestReaderSkipsOversizedAndWarns(t *testing.T) {
	huge := strings.Repeat("x", DefaultMaxBytes+1)
	var warns []string
	fr := NewReader(strings.NewReader("first\n"+huge+"\nsecond\n"), DefaultMaxBytes, func(m string) { warns = append(warns, m) })

	for _, want := range []string{"first", "second"} {
		line, err := fr.Read()
		if err != nil {
			t.Fatalf("Read(%q): %v", want, err)
		}
		if string(line) != want {
			t.Fatalf("Read = %q, want %q", line, want)
		}
	}
	if _, err := fr.Read(); err != io.EOF {
		t.Fatalf("want io.EOF at end, got %v", err)
	}
	if len(warns) != 1 {
		t.Fatalf("warns = %v, want exactly one oversized-frame warning", warns)
	}
}

func TestReaderScannerParity(t *testing.T) {
	// CRLF tolerance: one trailing \r is stripped, like bufio.ScanLines.
	fr := NewReader(strings.NewReader("crlf\r\nplain\n"), DefaultMaxBytes, nil)
	line, err := fr.Read()
	if err != nil || string(line) != "crlf" {
		t.Fatalf("CRLF line = %q err=%v, want %q", line, err, "crlf")
	}
	line, err = fr.Read()
	if err != nil || string(line) != "plain" {
		t.Fatalf("plain line = %q err=%v", line, err)
	}

	// A final unterminated line is delivered, THEN the stream error.
	fr = NewReader(strings.NewReader("tail"), DefaultMaxBytes, nil)
	line, err = fr.Read()
	if err != nil || string(line) != "tail" {
		t.Fatalf("unterminated tail = %q err=%v, want delivered with nil err", line, err)
	}
	if _, err := fr.Read(); err != io.EOF {
		t.Fatalf("want io.EOF after delivered tail, got %v", err)
	}
}

// NewReader with max <= 0 falls back to DefaultMaxBytes rather than rejecting
// every frame.
func TestNewReaderZeroMaxDefaults(t *testing.T) {
	fr := NewReader(strings.NewReader("hello\n"), 0, nil)
	line, err := fr.Read()
	if err != nil || string(line) != "hello" {
		t.Fatalf("zero-max Read = %q err=%v, want %q with a default limit", line, err, "hello")
	}
}
