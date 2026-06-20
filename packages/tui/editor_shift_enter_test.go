package tui

import "testing"

func TestEditorShiftEnterInsertsNewline(t *testing.T) {
	e := NewEditor("> ")
	e.HandleKey(Key{Kind: KeyRune, Rune: 'a'})
	if submit := e.HandleKey(Key{Kind: KeyEnter, Shift: true}); submit {
		t.Fatal("Shift+Enter submitted; want newline")
	}
	e.HandleKey(Key{Kind: KeyRune, Rune: 'b'})

	if got, want := e.Value(), "a\nb"; got != want {
		t.Fatalf("Value() = %q, want %q", got, want)
	}
}

func TestEditorPlainEnterSubmits(t *testing.T) {
	e := NewEditor("> ")
	e.HandleKey(Key{Kind: KeyRune, Rune: 'a'})
	if submit := e.HandleKey(Key{Kind: KeyEnter}); !submit {
		t.Fatal("Enter did not submit")
	}
}
