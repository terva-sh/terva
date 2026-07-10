package connproto

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

func TestReadFrameNormalLines(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("one\ntwo\n"))
	for _, want := range []string{"one", "two"} {
		line, tooLong, err := ReadFrame(r)
		if err != nil || tooLong {
			t.Fatalf("ReadFrame(%q): tooLong=%v err=%v", want, tooLong, err)
		}
		if string(line) != want {
			t.Fatalf("ReadFrame = %q, want %q", line, want)
		}
	}
	if _, _, err := ReadFrame(r); err != io.EOF {
		t.Fatalf("want io.EOF at end, got %v", err)
	}
}

// The whole point of ReadFrame over bufio.Scanner: an over-limit frame
// is drained and flagged, and the NEXT frame reads normally — one
// oversized connector payload must not kill the stream.
func TestReadFrameRecoversFromOversized(t *testing.T) {
	huge := strings.Repeat("x", MaxFrameBytes+1)
	r := bufio.NewReader(strings.NewReader(huge + "\nafter\n"))

	line, tooLong, err := ReadFrame(r)
	if err != nil {
		t.Fatalf("oversized frame returned err %v", err)
	}
	if !tooLong || line != nil {
		t.Fatalf("oversized frame: tooLong=%v line-len=%d, want tooLong with nil line", tooLong, len(line))
	}

	line, tooLong, err = ReadFrame(r)
	if err != nil || tooLong {
		t.Fatalf("frame after oversized: tooLong=%v err=%v", tooLong, err)
	}
	if string(line) != "after" {
		t.Fatalf("frame after oversized = %q, want %q", line, "after")
	}
}

// A final unterminated line surfaces with io.EOF, exactly at the limit
// is not over it.
func TestReadFrameEdges(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("tail-no-newline"))
	line, tooLong, err := ReadFrame(r)
	if err != io.EOF || tooLong || string(line) != "tail-no-newline" {
		t.Fatalf("unterminated tail: line=%q tooLong=%v err=%v", line, tooLong, err)
	}

	exact := strings.Repeat("y", MaxFrameBytes)
	r = bufio.NewReader(strings.NewReader(exact + "\n"))
	line, tooLong, err = ReadFrame(r)
	if err != nil || tooLong || len(line) != MaxFrameBytes {
		t.Fatalf("exact-limit frame: len=%d tooLong=%v err=%v", len(line), tooLong, err)
	}
}

// FrameReader is the carrier policy: skip-and-warn on oversized, deliver
// the final unterminated line before EOF, strip one trailing CR.
func TestFrameReaderSkipsOversizedAndWarns(t *testing.T) {
	huge := strings.Repeat("x", MaxFrameBytes+1)
	var warns []string
	fr := NewFrameReader(strings.NewReader("first\n"+huge+"\nsecond\n"), func(m string) { warns = append(warns, m) })

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

func TestFrameReaderScannerParity(t *testing.T) {
	// CRLF tolerance: one trailing \r is stripped, like bufio.ScanLines.
	fr := NewFrameReader(strings.NewReader("crlf\r\nplain\n"), nil)
	line, err := fr.Read()
	if err != nil || string(line) != "crlf" {
		t.Fatalf("CRLF line = %q err=%v, want %q", line, err, "crlf")
	}
	line, err = fr.Read()
	if err != nil || string(line) != "plain" {
		t.Fatalf("plain line = %q err=%v", line, err)
	}

	// A final unterminated line is delivered, THEN the stream error.
	fr = NewFrameReader(strings.NewReader("tail"), nil)
	line, err = fr.Read()
	if err != nil || string(line) != "tail" {
		t.Fatalf("unterminated tail = %q err=%v, want delivered with nil err", line, err)
	}
	if _, err := fr.Read(); err != io.EOF {
		t.Fatalf("want io.EOF after delivered tail, got %v", err)
	}
}
