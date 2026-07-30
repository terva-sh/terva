package tui

import "testing"

// Shift+Enter and Alt+Enter only reach the editor on terminals that speak
// the kitty keyboard protocol or xterm modifyOtherKeys. iTerm2 and
// Terminal.app send a bare CR for both, so the editor needs chords that
// survive a terminal with no enhanced keyboard reporting at all: ctrl+j
// (a plain 0x0a byte) and a trailing backslash before Enter.

func TestReaderMapsLFToCtrlEnter(t *testing.T) {
	// Raw mode clears ICRNL, so Enter is always CR. A lone LF is ctrl+j
	// (or a ctrl+enter that the terminal folded onto the same byte).
	r := NewReader(func() (byte, error) { return '\n', nil })
	k, err := r.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if k.Kind != KeyEnter || !k.Ctrl {
		t.Fatalf("Read(LF) = {kind:%v ctrl:%v}, want {KeyEnter true}", k.Kind, k.Ctrl)
	}
}

func TestReaderKeepsCRUnmodified(t *testing.T) {
	r := NewReader(func() (byte, error) { return '\r', nil })
	k, err := r.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if k.Kind != KeyEnter || k.Ctrl || k.Shift || k.Alt {
		t.Fatalf("Read(CR) = %+v, want a bare KeyEnter", k)
	}
}

func TestEditorCtrlEnterInsertsNewline(t *testing.T) {
	e := NewEditor("> ")
	e.HandleKey(Key{Kind: KeyRune, Rune: 'a'})
	if submit := e.HandleKey(Key{Kind: KeyEnter, Ctrl: true}); submit {
		t.Fatal("Ctrl+Enter submitted; want newline")
	}
	e.HandleKey(Key{Kind: KeyRune, Rune: 'b'})
	if got, want := e.Value(), "a\nb"; got != want {
		t.Fatalf("Value() = %q, want %q", got, want)
	}
}

func TestEditorBackslashEnterInsertsNewline(t *testing.T) {
	e := NewEditor("> ")
	for _, r := range "one\\" {
		e.HandleKey(Key{Kind: KeyRune, Rune: r})
	}
	if submit := e.HandleKey(Key{Kind: KeyEnter}); submit {
		t.Fatal(`"\" + Enter submitted; want newline`)
	}
	for _, r := range "two" {
		e.HandleKey(Key{Kind: KeyRune, Rune: r})
	}
	// The backslash is consumed, shell-style.
	if got, want := e.Value(), "one\ntwo"; got != want {
		t.Fatalf("Value() = %q, want %q", got, want)
	}
}

func TestEditorEscapedBackslashStillSubmits(t *testing.T) {
	// "\\" is a literal backslash, not a continuation — text that really
	// ends in a backslash must stay reachable.
	e := NewEditor("> ")
	for _, r := range `path\\` {
		e.HandleKey(Key{Kind: KeyRune, Rune: r})
	}
	if submit := e.HandleKey(Key{Kind: KeyEnter}); !submit {
		t.Fatal(`"\\" + Enter did not submit`)
	}
	if got, want := e.Value(), `path\\`; got != want {
		t.Fatalf("Value() = %q, want %q", got, want)
	}
}

func TestEditorBackslashMidLineDoesNotContinue(t *testing.T) {
	// The backslash only counts when it sits directly before the cursor.
	e := NewEditor("> ")
	for _, r := range `a\b` {
		e.HandleKey(Key{Kind: KeyRune, Rune: r})
	}
	if submit := e.HandleKey(Key{Kind: KeyEnter}); !submit {
		t.Fatal("Enter after a mid-line backslash did not submit")
	}
	if got, want := e.Value(), `a\b`; got != want {
		t.Fatalf("Value() = %q, want %q", got, want)
	}
}
