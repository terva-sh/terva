package tui

import "strings"

// LineCaret is the glyph a dialog draws at the cursor. A thin bar sits between
// two characters, which is what a cursor inside a line means; a block would
// read as the character under it being selected.
const LineCaret = "▏"

// LineBuf is one line of editable text with a cursor inside it. The zero value
// is an empty buffer with the cursor at the start.
//
// Editor (editor.go) is the other input in this package, and it is the whole
// composer: many lines, wrapping, ghost text, pastes collapsed to placeholders,
// file-path quoting. A dialog form row cannot use it, because Editor renders
// its own block with a prompt while the row is a column inside a line the
// dialog formats. So each dialog grew a plain string it appended to, and none
// of them could move a cursor: fixing a typo early in a value meant deleting
// back to it. LineBuf is the piece those rows were missing. It reuses Editor's
// word rules and grapheme steps, so both inputs move the same way.
type LineBuf struct {
	text   string
	cursor int // rune index in text, 0..runeLen(text)

	// Accept, when set, decides which runes may be inserted. It filters typed
	// runes and pasted ones alike. A rune it rejects is dropped rather than
	// refused, so a digits-only row swallows a stray letter instead of handing
	// it to the dialog's own key bindings.
	Accept func(r rune) bool
}

// Value returns the text.
func (b *LineBuf) Value() string { return b.text }

// Cursor returns the cursor position as a rune index.
func (b *LineBuf) Cursor() int { return b.cursor }

// SetValue replaces the text and puts the cursor at the end, which is where
// someone who just opened a field to edit it expects to start.
func (b *LineBuf) SetValue(s string) {
	b.text = s
	b.cursor = runeLen(s)
}

// Clear empties the buffer. It leaves Accept in place.
func (b *LineBuf) Clear() {
	b.text = ""
	b.cursor = 0
}

// Insert puts s at the cursor, subject to Accept, and moves the cursor after
// what went in.
func (b *LineBuf) Insert(s string) {
	if s == "" {
		return
	}
	if b.Accept != nil {
		kept := make([]rune, 0, len(s))
		for _, r := range s {
			if b.Accept(r) {
				kept = append(kept, r)
			}
		}
		s = string(kept)
		if s == "" {
			return
		}
	}
	b.text = substringBefore(b.text, b.cursor) + s + substringAfter(b.text, b.cursor)
	b.cursor += runeLen(s)
}

// HandleKey applies k to the buffer.
//
// consumed reports whether the buffer owns the key. A dialog hands every key
// here first and falls through to its own bindings when this is false, so a
// chord LineBuf does not use stays the dialog's.
//
// changed reports whether the text differs afterwards. A filter box needs the
// two answers apart: moving the cursor consumes the key but must not re-run the
// search and reset the selection.
func (b *LineBuf) HandleKey(k Key) (consumed, changed bool) {
	before := b.text
	switch k.Kind {
	case KeyRune:
		// A modified rune belongs to whatever bound the chord, and a control
		// character is not text.
		if k.Alt || k.Ctrl || k.Rune < 0x20 || k.Rune == 0x7f {
			return false, false
		}
		b.Insert(string(k.Rune))
	case KeyPaste:
		b.Insert(flattenToLine(k.Paste))
	case KeyBackspace:
		if k.Alt {
			b.deleteWordLeft()
		} else {
			b.backspace()
		}
	case KeyDelete:
		b.deleteForward()
	case KeyLeft:
		// Ctrl and Alt both jump a word, matching Editor. Ctrl reaches here
		// only because dispatchCSI now reads the ctrl bit out of the modifier
		// mask. While the decoder dropped that bit, this branch could not fire
		// at all, and ctrl+left moved one character.
		if k.Alt || k.Ctrl {
			b.moveWordLeft()
		} else {
			b.moveLeft()
		}
	case KeyRight:
		if k.Alt || k.Ctrl {
			b.moveWordRight()
		} else {
			b.moveRight()
		}
	case KeyHome, KeyCtrlA:
		b.cursor = 0
	case KeyEnd, KeyCtrlE:
		b.cursor = runeLen(b.text)
	case KeyCtrlU:
		b.text = substringAfter(b.text, b.cursor)
		b.cursor = 0
	case KeyCtrlK:
		b.text = substringBefore(b.text, b.cursor)
	case KeyCtrlW:
		b.deleteWordLeft()
	default:
		return false, false
	}
	return true, b.text != before
}

// Render returns the text with the caret drawn at the cursor.
func (b *LineBuf) Render(caret string) string {
	return substringBefore(b.text, b.cursor) + caret + substringAfter(b.text, b.cursor)
}

// RenderMasked is Render for a secret: every rune of the text becomes mask, and
// the caret still marks the cursor, so the count of characters and the position
// within them stay visible while the value does not.
func (b *LineBuf) RenderMasked(mask rune, caret string) string {
	return maskRunes(substringBefore(b.text, b.cursor), mask) + caret +
		maskRunes(substringAfter(b.text, b.cursor), mask)
}

func (b *LineBuf) moveLeft() {
	if b.cursor > 0 {
		b.cursor = graphemeLeft(b.text, b.cursor)
	}
}

func (b *LineBuf) moveRight() {
	if b.cursor < runeLen(b.text) {
		b.cursor = graphemeRight(b.text, b.cursor)
	}
}

// moveWordLeft puts the cursor at the start of the word to its left, using the
// separator rule Editor's word motion uses.
func (b *LineBuf) moveWordLeft() {
	r := []rune(b.text)
	i := b.cursor
	for i > 0 && isWordSep(r[i-1]) {
		i--
	}
	for i > 0 && !isWordSep(r[i-1]) {
		i--
	}
	b.cursor = i
}

// moveWordRight puts the cursor at the start of the word to its right.
func (b *LineBuf) moveWordRight() {
	r := []rune(b.text)
	i := b.cursor
	for i < len(r) && !isWordSep(r[i]) {
		i++
	}
	for i < len(r) && isWordSep(r[i]) {
		i++
	}
	b.cursor = i
}

func (b *LineBuf) backspace() {
	if b.cursor == 0 {
		return
	}
	start := graphemeLeft(b.text, b.cursor)
	b.text = substringBefore(b.text, start) + substringAfter(b.text, b.cursor)
	b.cursor = start
}

func (b *LineBuf) deleteForward() {
	if b.cursor >= runeLen(b.text) {
		return
	}
	b.text = substringBefore(b.text, b.cursor) + substringAfter(b.text, graphemeRight(b.text, b.cursor))
}

func (b *LineBuf) deleteWordLeft() {
	r := []rune(b.text)
	i := b.cursor
	for i > 0 && isWordSep(r[i-1]) {
		i--
	}
	for i > 0 && !isWordSep(r[i-1]) {
		i--
	}
	b.text = string(r[:i]) + string(r[b.cursor:])
	b.cursor = i
}

// flattenToLine turns pasted text into something a one-line field can hold.
// Every newline, tab and other control character becomes a space. A carriage
// return that reached the row would send the terminal back to column zero and
// paint over the label beside it.
func flattenToLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}
