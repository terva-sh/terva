//go:build windows

package tui

import (
	"os"
	"testing"
	"time"
)

func TestWindowsPeekPipeTimeoutAndRead(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	start := time.Now()
	if _, ok, err := peekStdin(r, 15*time.Millisecond); ok || err != nil {
		t.Fatalf("empty pipe: ok=%v err=%v", ok, err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("input timeout exceeded one second")
	}
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if b, ok, err := peekStdin(r, time.Second); b != 'x' || !ok || err != nil {
		t.Fatalf("ready pipe: byte=%q ok=%v err=%v", b, ok, err)
	}
}

func TestWindowsProcInputRetainsSequenceAndUTF8Bytes(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	p := &ProcTerm{in: r}
	wire := []byte("\x1b[A\xc3\xa9")
	if _, err := w.Write(wire); err != nil {
		t.Fatal(err)
	}
	if b, err := p.ReadByte(); err != nil || b != wire[0] {
		t.Fatalf("initial byte=%q err=%v", b, err)
	}
	for _, want := range wire[1:] {
		if b, ok, err := p.PeekByteTimeout(0); b != want || !ok || err != nil {
			t.Fatalf("buffered byte=%q want=%q ok=%v err=%v", b, want, ok, err)
		}
	}
	if _, ok, err := p.PeekByteTimeout(0); ok || err != nil {
		t.Fatalf("empty pipe: ok=%v err=%v", ok, err)
	}
}
