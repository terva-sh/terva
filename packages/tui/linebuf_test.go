package tui

import "testing"

func lbKey(kind KeyKind) Key { return Key{Kind: kind} }

func lbType(b *LineBuf, s string) {
	for _, r := range s {
		b.HandleKey(Key{Kind: KeyRune, Rune: r})
	}
}

// The reported bug: a value typed into a form row could only be edited at its
// end. Insertion in the middle is the whole point of the type.
func TestLineBufInsertsAtTheCursor(t *testing.T) {
	var b LineBuf
	b.SetValue("minimal,high")
	for i := 0; i < len("high"); i++ {
		b.HandleKey(lbKey(KeyLeft))
	}
	lbType(&b, "medium,")
	if got := b.Value(); got != "minimal,medium,high" {
		t.Fatalf("value = %q, want %q", got, "minimal,medium,high")
	}
	if got, want := b.Cursor(), len("minimal,medium,"); got != want {
		t.Fatalf("cursor = %d, want %d", got, want)
	}
}

func TestLineBufSetValueParksCursorAtEnd(t *testing.T) {
	var b LineBuf
	b.SetValue("low")
	lbType(&b, "!")
	if got := b.Value(); got != "low!" {
		t.Fatalf("value = %q, want %q", got, "low!")
	}
}

func TestLineBufBackspaceAndDeleteActAroundTheCursor(t *testing.T) {
	var b LineBuf
	b.SetValue("abcd")
	b.HandleKey(lbKey(KeyLeft))
	b.HandleKey(lbKey(KeyLeft)) // between b and c
	b.HandleKey(lbKey(KeyBackspace))
	if got := b.Value(); got != "acd" {
		t.Fatalf("after backspace = %q, want %q", got, "acd")
	}
	b.HandleKey(lbKey(KeyDelete))
	if got := b.Value(); got != "ad" {
		t.Fatalf("after delete = %q, want %q", got, "ad")
	}
	if got := b.Cursor(); got != 1 {
		t.Fatalf("cursor = %d, want 1", got)
	}
}

func TestLineBufEdgesDoNotMoveOrDelete(t *testing.T) {
	var b LineBuf
	b.SetValue("ab")
	b.HandleKey(lbKey(KeyHome))
	b.HandleKey(lbKey(KeyLeft))
	b.HandleKey(lbKey(KeyBackspace))
	if got, cur := b.Value(), b.Cursor(); got != "ab" || cur != 0 {
		t.Fatalf("at start: value %q cursor %d, want %q 0", got, cur, "ab")
	}
	b.HandleKey(lbKey(KeyEnd))
	b.HandleKey(lbKey(KeyRight))
	b.HandleKey(lbKey(KeyDelete))
	if got, cur := b.Value(), b.Cursor(); got != "ab" || cur != 2 {
		t.Fatalf("at end: value %q cursor %d, want %q 2", got, cur, "ab")
	}
}

func TestLineBufWordMotionAndDeletion(t *testing.T) {
	var b LineBuf
	b.SetValue("alpha beta gamma")
	b.HandleKey(Key{Kind: KeyLeft, Alt: true})
	if got, want := b.Cursor(), len("alpha beta "); got != want {
		t.Fatalf("alt+left cursor = %d, want %d", got, want)
	}
	b.HandleKey(Key{Kind: KeyLeft, Ctrl: true})
	if got, want := b.Cursor(), len("alpha "); got != want {
		t.Fatalf("ctrl+left cursor = %d, want %d", got, want)
	}
	b.HandleKey(lbKey(KeyCtrlW))
	if got := b.Value(); got != "beta gamma" {
		t.Fatalf("after ctrl+w = %q, want %q", got, "beta gamma")
	}
	b.HandleKey(Key{Kind: KeyRight, Alt: true})
	if got, want := b.Cursor(), len("beta "); got != want {
		t.Fatalf("alt+right cursor = %d, want %d", got, want)
	}
}

func TestLineBufKillsToStartAndEnd(t *testing.T) {
	var b LineBuf
	b.SetValue("keep this")
	b.HandleKey(Key{Kind: KeyLeft, Alt: true}) // before "this"
	b.HandleKey(lbKey(KeyCtrlK))
	if got := b.Value(); got != "keep " {
		t.Fatalf("after ctrl+k = %q, want %q", got, "keep ")
	}
	b.HandleKey(lbKey(KeyCtrlE))
	b.HandleKey(lbKey(KeyCtrlU))
	if got, cur := b.Value(), b.Cursor(); got != "" || cur != 0 {
		t.Fatalf("after ctrl+u: value %q cursor %d, want empty at 0", got, cur)
	}
}

func TestLineBufCtrlAAndCtrlEMatchHomeAndEnd(t *testing.T) {
	var b LineBuf
	b.SetValue("abc")
	b.HandleKey(lbKey(KeyCtrlA))
	if got := b.Cursor(); got != 0 {
		t.Fatalf("ctrl+a cursor = %d, want 0", got)
	}
	b.HandleKey(lbKey(KeyCtrlE))
	if got := b.Cursor(); got != 3 {
		t.Fatalf("ctrl+e cursor = %d, want 3", got)
	}
}

// A filter box re-runs its search on changed and not on consumed. Reporting a
// cursor move as a change would reset the selection under the user.
func TestLineBufReportsConsumedAndChangedApart(t *testing.T) {
	var b LineBuf
	b.SetValue("ab")
	if consumed, changed := b.HandleKey(Key{Kind: KeyRune, Rune: 'c'}); !consumed || !changed {
		t.Fatalf("typing: consumed %v changed %v, want both true", consumed, changed)
	}
	if consumed, changed := b.HandleKey(lbKey(KeyLeft)); !consumed || changed {
		t.Fatalf("left: consumed %v changed %v, want true and false", consumed, changed)
	}
	if consumed, changed := b.HandleKey(lbKey(KeyHome)); !consumed || changed {
		t.Fatalf("home: consumed %v changed %v, want true and false", consumed, changed)
	}
	// Backspace at the start consumes the key and changes nothing.
	if consumed, changed := b.HandleKey(lbKey(KeyBackspace)); !consumed || changed {
		t.Fatalf("backspace at start: consumed %v changed %v, want true and false", consumed, changed)
	}
}

// A chord the buffer does not use has to fall through, or adopting it would
// steal keys the dialogs already bound.
func TestLineBufLeavesForeignKeysAlone(t *testing.T) {
	var b LineBuf
	b.SetValue("x")
	for _, k := range []Key{
		lbKey(KeyUp), lbKey(KeyDown), lbKey(KeyEnter), lbKey(KeyEsc),
		lbKey(KeyTab), lbKey(KeyPageUp), lbKey(KeyPageDown),
		lbKey(KeyCtrlT), lbKey(KeyCtrlY), lbKey(KeyCtrlR),
		{Kind: KeyRune, Rune: 's', Alt: true},
		{Kind: KeyRune, Rune: 'r', Ctrl: true},
	} {
		if consumed, _ := b.HandleKey(k); consumed {
			t.Fatalf("key %+v was consumed, want it left to the dialog", k)
		}
	}
	if got := b.Value(); got != "x" {
		t.Fatalf("value = %q, want unchanged %q", got, "x")
	}
}

func TestLineBufAcceptFiltersTypedAndPastedRunes(t *testing.T) {
	b := LineBuf{Accept: func(r rune) bool { return r >= '0' && r <= '9' }}
	lbType(&b, "12a3")
	if got := b.Value(); got != "123" {
		t.Fatalf("typed value = %q, want %q", got, "123")
	}
	b.HandleKey(Key{Kind: KeyPaste, Paste: "45x6"})
	if got := b.Value(); got != "123456" {
		t.Fatalf("pasted value = %q, want %q", got, "123456")
	}
	// A rejected rune is still the buffer's key. Letting it fall through would
	// hand a letter to a dialog shortcut while the user was typing a number.
	if consumed, changed := b.HandleKey(Key{Kind: KeyRune, Rune: 'q'}); !consumed || changed {
		t.Fatalf("rejected rune: consumed %v changed %v, want true and false", consumed, changed)
	}
}

func TestLineBufPasteLandsOnOneLine(t *testing.T) {
	var b LineBuf
	b.HandleKey(Key{Kind: KeyPaste, Paste: "one\r\ntwo\tthree"})
	if got := b.Value(); got != "one  two three" {
		t.Fatalf("value = %q, want %q", got, "one  two three")
	}
}

func TestLineBufMovesOverAGraphemeAtATime(t *testing.T) {
	var b LineBuf
	b.SetValue("aé\u0301b") // e + combining acute is one grapheme
	b.HandleKey(lbKey(KeyHome))
	b.HandleKey(lbKey(KeyRight))
	b.HandleKey(lbKey(KeyRight))
	b.HandleKey(lbKey(KeyBackspace))
	if got := b.Value(); got != "ab" {
		t.Fatalf("value = %q, want %q", got, "ab")
	}
}

func TestLineBufRendersTheCaretAtTheCursor(t *testing.T) {
	var b LineBuf
	b.SetValue("ab")
	if got, want := b.Render(LineCaret), "ab"+LineCaret; got != want {
		t.Fatalf("render at end = %q, want %q", got, want)
	}
	b.HandleKey(lbKey(KeyLeft))
	if got, want := b.Render(LineCaret), "a"+LineCaret+"b"; got != want {
		t.Fatalf("render mid-string = %q, want %q", got, want)
	}
	if got, want := b.RenderMasked('•', LineCaret), "•"+LineCaret+"•"; got != want {
		t.Fatalf("masked render = %q, want %q", got, want)
	}
}

func TestLineBufClearKeepsAccept(t *testing.T) {
	b := LineBuf{Accept: func(r rune) bool { return r != 'z' }}
	lbType(&b, "abc")
	b.Clear()
	if got, cur := b.Value(), b.Cursor(); got != "" || cur != 0 {
		t.Fatalf("after clear: value %q cursor %d", got, cur)
	}
	lbType(&b, "az")
	if got := b.Value(); got != "a" {
		t.Fatalf("value = %q, want %q", got, "a")
	}
}
