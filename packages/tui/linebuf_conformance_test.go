package tui

import (
	"fmt"
	"testing"
)

// terva has two text fields, and they are not going to become one. Editor is
// the composer: many lines, wrapping, ghost text, pastes collapsed to
// placeholders, submit. LineBuf is a form row: one line, rendered into a column
// the dialog formats, handing back the keys it does not claim.
//
// They do share a key map, and that is where they can drift. Both already call
// the same isWordSep, graphemeLeft and graphemeRight, so the motions themselves
// cannot diverge; only the dispatch can. That is exactly what happened once:
// LineBuf bound ctrl+left to word motion and Editor did not, and nobody noticed
// because the key decoder was dropping the ctrl bit, so neither branch ran.
//
// This walks the keys they both claim and holds them to the same result. Keys
// where they differ on purpose are out of scope and listed in
// TestEditorAndLineBufDifferOnPurpose.

// sharedMotionKeys is every key both fields claim to handle the same way.
func sharedMotionKeys() []Key {
	return []Key{
		{Kind: KeyLeft},
		{Kind: KeyLeft, Alt: true},
		{Kind: KeyLeft, Ctrl: true},
		{Kind: KeyRight},
		{Kind: KeyRight, Alt: true},
		{Kind: KeyRight, Ctrl: true},
		{Kind: KeyHome},
		{Kind: KeyEnd},
		{Kind: KeyCtrlA},
		{Kind: KeyCtrlE},
		{Kind: KeyCtrlU},
		{Kind: KeyCtrlK},
		{Kind: KeyCtrlW},
		{Kind: KeyDelete},
		{Kind: KeyBackspace},
		{Kind: KeyBackspace, Alt: true},
		{Kind: KeyRune, Rune: 'x'},
	}
}

func keyName(k Key) string {
	name := map[KeyKind]string{
		KeyLeft: "left", KeyRight: "right", KeyHome: "home", KeyEnd: "end",
		KeyCtrlA: "ctrl+a", KeyCtrlE: "ctrl+e", KeyCtrlU: "ctrl+u",
		KeyCtrlK: "ctrl+k", KeyCtrlW: "ctrl+w", KeyDelete: "delete",
		KeyBackspace: "backspace", KeyRune: "rune",
	}[k.Kind]
	if k.Alt {
		name = "alt+" + name
	}
	if k.Ctrl {
		name = "ctrl+" + name
	}
	if k.Kind == KeyRune {
		name = fmt.Sprintf("%s(%c)", name, k.Rune)
	}
	return name
}

// singleLineEditor is an Editor holding one line with the caret at pos, which
// is the shape a form row would have if it used one.
func singleLineEditor(text string, pos int) *Editor {
	e := NewEditor("")
	e.SetValue(text)
	e.CursorR = 0
	e.CursorC = pos
	return e
}

func lineBufAt(text string, pos int) *LineBuf {
	b := &LineBuf{}
	b.SetValue(text)
	b.cursor = pos // same package, so the test can place the caret directly
	return b
}

func TestEditorAndLineBufAgreeOnSharedMotions(t *testing.T) {
	const text = "alpha beta gamma"
	// Caret positions worth testing: both ends, a word boundary, mid-word, and
	// the space between two words.
	positions := []int{0, 5, 6, 8, len(text)}

	for _, pos := range positions {
		for _, k := range sharedMotionKeys() {
			e := singleLineEditor(text, pos)
			b := lineBufAt(text, pos)

			e.HandleKey(k)
			b.HandleKey(k)

			if got, want := b.Value(), e.Lines[0]; got != want {
				t.Errorf("%s at %d: LineBuf text %q, Editor text %q",
					keyName(k), pos, got, want)
			}
			if got, want := b.Cursor(), e.CursorC; got != want {
				t.Errorf("%s at %d: LineBuf cursor %d, Editor cursor %d",
					keyName(k), pos, got, want)
			}
		}
	}
}

// The same walk over text that is not plain ASCII words. A grapheme step and a
// word rule are easy to agree on for "abc" and easy to diverge on here.
func TestEditorAndLineBufAgreeOnAwkwardText(t *testing.T) {
	texts := []string{
		"minimal,low,medium,high",  // the efforts list: punctuation, no spaces
		"https://api.acme.dev/v1",  // a base url
		"e\u0301clair na\u00efve",  // a combining accent and a precomposed one
		"  leading and trailing  ", // runs of spaces at both ends
		"",                         // empty: every key is an edge case
	}
	for _, text := range texts {
		n := runeLen(text)
		for pos := 0; pos <= n; pos++ {
			for _, k := range sharedMotionKeys() {
				e := singleLineEditor(text, pos)
				b := lineBufAt(text, pos)

				e.HandleKey(k)
				b.HandleKey(k)

				if got, want := b.Value(), e.Lines[0]; got != want {
					t.Fatalf("%q %s at %d: LineBuf text %q, Editor text %q",
						text, keyName(k), pos, got, want)
				}
				if got, want := b.Cursor(), e.CursorC; got != want {
					t.Fatalf("%q %s at %d: LineBuf cursor %d, Editor cursor %d",
						text, keyName(k), pos, got, want)
				}
			}
		}
	}
}

// The divergences are deliberate, so name them. A future reader who finds one
// of these keys behaving differently in the two fields should find it here
// rather than conclude the conformance test has a hole.
func TestEditorAndLineBufDifferOnPurpose(t *testing.T) {
	// Enter submits the composer and is not the buffer's key at all: the
	// dialog hosting a LineBuf decides what committing a row means.
	e := singleLineEditor("done", 4)
	if submit := e.HandleKey(Key{Kind: KeyEnter}); !submit {
		t.Error("Editor: a bare enter on a non-empty buffer should submit")
	}
	b := lineBufAt("done", 4)
	if consumed, _ := b.HandleKey(Key{Kind: KeyEnter}); consumed {
		t.Error("LineBuf: enter must fall through to the dialog")
	}

	// A paste with newlines becomes several lines in the composer and one
	// flattened line in a form row, which has nowhere to put the second.
	e = singleLineEditor("", 0)
	e.HandleKey(Key{Kind: KeyPaste, Paste: "one\ntwo"})
	if len(e.Lines) != 2 {
		t.Errorf("Editor: multi-line paste gave %d lines, want 2", len(e.Lines))
	}
	b = lineBufAt("", 0)
	b.HandleKey(Key{Kind: KeyPaste, Paste: "one\ntwo"})
	if got := b.Value(); got != "one two" {
		t.Errorf("LineBuf: multi-line paste = %q, want it flattened", got)
	}

	// Up and Down move between visual rows in the composer. A one-line field
	// leaves them to the dialog, where they move the selection.
	b = lineBufAt("text", 2)
	for _, k := range []Key{{Kind: KeyUp}, {Kind: KeyDown}, {Kind: KeyTab}} {
		if consumed, _ := b.HandleKey(k); consumed {
			t.Errorf("LineBuf consumed %v, which belongs to the dialog", k.Kind)
		}
	}
}
