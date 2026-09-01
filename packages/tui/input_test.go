package tui

import (
	"fmt"
	"io"
	"testing"
)

// The enhanced keyboard protocol (kitty CSI-u and xterm modifyOtherKeys)
// must decode modified Enter, Esc, and modified arrows. The Esc case is
// the 1cc654e regression: enabling the protocol made Esc arrive as
// CSI 27 u, which must still map to KeyEsc so it aborts the agent.
func TestReaderParsesEnhancedKeyboard(t *testing.T) {
	cases := []struct {
		seq   string
		kind  KeyKind
		shift bool
		alt   bool
	}{
		{"\x1b[13;2u", KeyEnter, true, false},    // kitty: Shift+Enter
		{"\x1b[13;3u", KeyEnter, false, true},    // kitty: Alt+Enter
		{"\x1b[27u", KeyEsc, false, false},       // kitty: Esc (1cc654e)
		{"\x1b[27;2;13~", KeyEnter, true, false}, // modifyOtherKeys: Shift+Enter
		{"\x1b[1;2A", KeyUp, true, false},        // modified arrow: Shift+Up
	}
	for _, tc := range cases {
		idx := 0
		r := NewReader(func() (byte, error) {
			b := tc.seq[idx]
			idx++
			return b, nil
		})
		k, err := r.Read()
		if err != nil {
			t.Fatalf("Read(%q): %v", tc.seq, err)
		}
		if k.Kind != tc.kind || k.Shift != tc.shift || k.Alt != tc.alt {
			t.Fatalf("Read(%q) = {kind:%v shift:%v alt:%v}, want {kind:%v shift:%v alt:%v}",
				tc.seq, k.Kind, k.Shift, k.Alt, tc.kind, tc.shift, tc.alt)
		}
	}
}

func TestReaderParsesSGRMouseWheel(t *testing.T) {
	cases := []struct {
		seq  string
		want KeyKind
	}{
		{"\x1b[<64;10;20M", KeyMouseWheelUp},
		{"\x1b[<65;10;20M", KeyMouseWheelDown},
	}
	for _, tc := range cases {
		idx := 0
		r := NewReader(func() (byte, error) {
			b := tc.seq[idx]
			idx++
			return b, nil
		})
		k, err := r.Read()
		if err != nil {
			t.Fatalf("Read(%q): %v", tc.seq, err)
		}
		if k.Kind != tc.want {
			t.Fatalf("Read(%q) kind=%v, want %v", tc.seq, k.Kind, tc.want)
		}
	}
}

// readKeySeq feeds seq to a Reader one byte at a time and returns the
// single Key it parses.
func readKeySeq(t *testing.T, seq string) Key {
	t.Helper()
	idx := 0
	r := NewReader(func() (byte, error) {
		if idx >= len(seq) {
			return 0, io.EOF
		}
		b := seq[idx]
		idx++
		return b, nil
	})
	k, err := r.Read()
	if err != nil {
		t.Fatalf("Read(%q): %v", seq, err)
	}
	return k
}

// Every chord the TUI recognises has to decode the same way on all three
// wires a terminal may use: the legacy control byte, kitty's
// CSI <code>;<mod>u, and xterm modifyOtherKeys' CSI 27;<mod>;<code>~.
// terva asks for both enhanced protocols at startup (SeqEnhancedKeyboardOn),
// so on a terminal that honours them — iTerm2 — the control byte never
// arrives and only the escape forms do.
//
// This enumerates ctrlChordKind instead of listing chords, which is the
// whole point: a chord added later is covered without anyone remembering
// to extend a second list. Forgetting that is what left ctrl+s (stash)
// and ctrl+y (copy picker) dead on iTerm2 while they still worked on
// terminals that ignore the protocols.
func TestReaderChordsAgreeAcrossWires(t *testing.T) {
	seen := 0
	for b := byte(1); b <= 26; b++ {
		want, ok := ctrlChordKind(b)
		if !ok {
			continue
		}
		seen++
		letter := int('a') + int(b) - 1
		wires := map[string]string{
			"legacy byte":      string([]byte{b}),
			"kitty CSI u":      fmt.Sprintf("\x1b[%d;5u", letter),
			"modifyOtherKeys":  fmt.Sprintf("\x1b[27;5;%d~", letter),
			"kitty CSI u caps": fmt.Sprintf("\x1b[%d;5u", letter-32),
		}
		for wire, seq := range wires {
			if got := readKeySeq(t, seq).Kind; got != want {
				t.Errorf("ctrl+%c on %s (%q): kind = %v, want %v", rune(letter), wire, seq, got, want)
			}
		}
	}
	if seen == 0 {
		t.Fatal("ctrlChordKind claimed no bytes; the table lookup is not being exercised")
	}
}

// The chord table must never claim a byte that Read decodes as a key in
// its own right: ctrlChordKind runs first, so a chord added for one of
// these would silently swallow Tab, Enter, Esc or Backspace.
func TestCtrlChordKindLeavesDedicatedKeysAlone(t *testing.T) {
	for _, b := range []byte{0x08, 0x09, 0x0a, 0x0d, 0x1b, 0x7f} {
		if kind, ok := ctrlChordKind(b); ok {
			t.Errorf("ctrlChordKind(%#x) = %v, want no claim", b, kind)
		}
	}
	for seq, want := range map[string]KeyKind{
		"\t":   KeyTab,
		"\r":   KeyEnter,
		"\x7f": KeyBackspace,
	} {
		if got := readKeySeq(t, seq).Kind; got != want {
			t.Errorf("Read(%q) = %v, want %v", seq, got, want)
		}
	}
}
