package extproto

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestReadFrameNormalLines(t *testing.T) {
	r := bufio.NewReaderSize(strings.NewReader("alpha\nbeta\n"), 64)
	for _, want := range []string{"alpha", "beta"} {
		line, tooLong, err := ReadFrame(r)
		if err != nil || tooLong {
			t.Fatalf("ReadFrame(%q): tooLong=%v err=%v", want, tooLong, err)
		}
		if string(line) != want {
			t.Errorf("line = %q, want %q", line, want)
		}
	}
	if _, _, err := ReadFrame(r); err != io.EOF {
		t.Errorf("final read err = %v, want io.EOF", err)
	}
}

// An over-limit line is reported tooLong and skipped, and the NEXT
// (valid) frame is still read — the property bufio.Scanner can't give.
func TestReadFrameRecoversFromOversized(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString("ok-before\n")
	buf.Write(bytes.Repeat([]byte("x"), MaxFrameBytes+10)) // one giant line
	buf.WriteString("\n")
	buf.WriteString("ok-after\n")

	// Small bufio buffer to exercise the ErrBufferFull drain path.
	r := bufio.NewReaderSize(&buf, 128)

	line, tooLong, err := ReadFrame(r)
	if tooLong || err != nil || string(line) != "ok-before" {
		t.Fatalf("first frame: line=%q tooLong=%v err=%v", line, tooLong, err)
	}

	line, tooLong, err = ReadFrame(r)
	if !tooLong {
		t.Fatalf("oversized frame not flagged: line=%q err=%v", line, err)
	}
	if line != nil {
		t.Errorf("oversized line should be discarded (nil), got %d bytes", len(line))
	}

	line, tooLong, err = ReadFrame(r)
	if tooLong || err != nil || string(line) != "ok-after" {
		t.Fatalf("recovery frame: line=%q tooLong=%v err=%v", line, tooLong, err)
	}
}

// A line exactly at the limit is accepted; one byte over is not.
func TestReadFrameBoundary(t *testing.T) {
	atLimit := strings.Repeat("a", MaxFrameBytes)
	r := bufio.NewReaderSize(strings.NewReader(atLimit+"\n"), 4096)
	line, tooLong, err := ReadFrame(r)
	if tooLong || err != nil || len(line) != MaxFrameBytes {
		t.Fatalf("at-limit line rejected: len=%d tooLong=%v err=%v", len(line), tooLong, err)
	}

	over := strings.Repeat("a", MaxFrameBytes+1)
	r = bufio.NewReaderSize(strings.NewReader(over+"\n"), 4096)
	if _, tooLong, _ := ReadFrame(r); !tooLong {
		t.Error("one-byte-over line should be tooLong")
	}
}
