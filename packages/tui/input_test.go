package tui

import "testing"

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
