package tui

import (
	"strings"
	"unicode/utf8"
)

// SanitizeLabel reduces untrusted text to something safe to paint as one row of
// a terminal, for the short labels that come from outside this process: a
// shared file's name, the caption an agent wrote for it.
//
// Two things make those different from the text this package already sanitizes.
// They are not the user's own paste and not a local script's output — a caption
// is authored by the MODEL, so a prompt-injected agent chooses it, and on
// `terva attach` both cross a machine boundary from a daemon this client does
// not control. And they are drawn INSIDE a row that has its own styling and
// alignment, so a value that moves the cursor does not merely garble itself, it
// takes the frame with it.
//
// Every escape sequence goes, SGR included. That is the difference from
// sanitizeStatusScriptLine, which deliberately passes colour through because a
// user's own status script is entitled to it. A filename is not: a name that
// can emit SGR can repaint the row it sits in, and an unterminated sequence
// bleeds into everything painted after it.
//
// Newlines and tabs become a space rather than vanishing, so a two-line name
// reads as two words instead of one fused one. Everything else in the C0 and
// C1 control ranges is dropped, along with the bidi overrides — those are the
// classic filename spoof, where "report‮gnp.exe" renders as "reportexe.png"
// and the extension you agree to is not the one you get.
func SanitizeLabel(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == 0x1b:
			// Drop the whole sequence, not just the ESC: leaving "[31m"
			// behind would print the parameters as text.
			i = skipEscapeSequence(s, i)
			continue
		case c == '\n' || c == '\r' || c == '\t':
			b.WriteByte(' ')
			i++
			continue
		case c < 0x20 || c == 0x7f:
			i++
			continue
		case c < utf8.RuneSelf:
			b.WriteByte(c)
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// An invalid byte. Dropped rather than replaced: the width
			// accounting downstream counts runes, and a replacement character
			// would claim a column this text never had.
			i++
			continue
		}
		if unsafeLabelRune(r) {
			i += size
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	return strings.TrimSpace(b.String())
}

// unsafeLabelRune reports a rune that is invisible or that reorders what
// follows it. Both let a label claim to be something it is not.
func unsafeLabelRune(r rune) bool {
	switch {
	case r >= 0x80 && r <= 0x9f:
		// C1 controls. A terminal in UTF-8 mode should not act on these, but
		// "should not" is a property of the terminal, not of this string, and
		// nothing legitimate in a name or caption is here.
		return true
	case r == 0x200e || r == 0x200f:
		// LEFT-TO-RIGHT and RIGHT-TO-LEFT MARK.
		return true
	case r >= 0x202a && r <= 0x202e:
		// The embedding and override pair, including RIGHT-TO-LEFT OVERRIDE.
		return true
	case r >= 0x2066 && r <= 0x2069:
		// The isolates, which do the same job as the overrides.
		return true
	}
	return false
}
