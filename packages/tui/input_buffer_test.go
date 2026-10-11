package tui

import (
	"bufio"
	"bytes"
	"io"
	"testing"
)

func TestBufferedInputDrainsTranslatedSequenceBeforeReadinessProbe(t *testing.T) {
	wire := []byte("\x1b[A\xc3\xa9")
	source := bytes.NewReader(wire)
	reader := bufio.NewReaderSize(source, 64*1024)
	b, err := reader.ReadByte()
	if err != nil || b != wire[0] || source.Len() != 0 {
		t.Fatalf("initial read: byte=%q err=%v pending=%d", b, err, source.Len())
	}
	probes := 0
	ready := func() (bool, error) { probes++; return false, nil }
	for _, want := range wire[1:] {
		b, ok, err := peekBufferedInput(reader, ready)
		if b != want || !ok || err != nil || probes != 0 {
			t.Fatalf("buffered byte=%q want=%q ok=%v err=%v probes=%d", b, want, ok, err, probes)
		}
	}
	if _, ok, err := peekBufferedInput(reader, ready); ok || err != nil || probes != 1 {
		t.Fatalf("empty buffer: ok=%v err=%v probes=%d", ok, err, probes)
	}
}

func TestBufferedInputChecksReadinessAndErrors(t *testing.T) {
	reader := bufio.NewReader(bytes.NewReader([]byte("x")))
	if b, ok, err := peekBufferedInput(reader, func() (bool, error) { return true, nil }); b != 'x' || !ok || err != nil {
		t.Fatalf("ready input: byte=%q ok=%v err=%v", b, ok, err)
	}
	if _, ok, err := peekBufferedInput(reader, func() (bool, error) { return false, io.EOF }); ok || err != io.EOF {
		t.Fatalf("readiness error: ok=%v err=%v", ok, err)
	}
}
