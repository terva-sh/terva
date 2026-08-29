package tui

// Terminal escape-sequence scanning shared by the width, wrap, and
// truncate paths.
//
// Every one of those used to recognise CSI (ESC '[' … final byte) and
// nothing else, which was fine while CSI was the only escape terva
// emitted into a measured line. OSC 8 hyperlinks changed that: an OSC
// sequence carries a URL that occupies no cells, so a scanner that does
// not know it counts a hundred-odd bytes of link target toward the
// visible column and shreds the layout around it. These two helpers are
// the single definition of "an escape starts here, and it is this long",
// so the CSI and OSC rules cannot drift between callers again.

// escSeqLen reports the byte length of the terminal escape sequence
// beginning at s[i], or 0 when s[i] does not begin one.
//
// Recognises CSI (ESC '[' … final byte 0x40-0x7E) and OSC (ESC ']' …
// terminated by BEL or ST, i.e. ESC '\'). An unterminated sequence runs
// to the end of s, matching what a terminal would do with the bytes and
// what the CSI-only scanners this replaces already did.
func escSeqLen(s string, i int) int {
	if i >= len(s) || s[i] != 0x1b || i+1 >= len(s) {
		return 0
	}
	switch s[i+1] {
	case '[':
		j := i + 2
		for j < len(s) {
			c := s[j]
			j++
			if c >= 0x40 && c <= 0x7e {
				break
			}
		}
		return j - i
	case ']':
		j := i + 2
		for j < len(s) {
			if s[j] == 0x07 { // BEL terminator
				return j + 1 - i
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' { // ST terminator
				return j + 2 - i
			}
			j++
		}
		return len(s) - i
	}
	return 0
}

// escSeqLenRunes is escSeqLen over a rune slice, returning a length in
// runes. Escape sequences are pure ASCII, so the two agree on every
// input; the rune form exists because the wrap paths walk []rune to keep
// their column arithmetic honest about multi-byte glyphs.
func escSeqLenRunes(r []rune, i int) int {
	if i >= len(r) || r[i] != 0x1b || i+1 >= len(r) {
		return 0
	}
	switch r[i+1] {
	case '[':
		j := i + 2
		for j < len(r) {
			c := r[j]
			j++
			if c >= 0x40 && c <= 0x7e {
				break
			}
		}
		return j - i
	case ']':
		j := i + 2
		for j < len(r) {
			if r[j] == 0x07 {
				return j + 1 - i
			}
			if r[j] == 0x1b && j+1 < len(r) && r[j+1] == '\\' {
				return j + 2 - i
			}
			j++
		}
		return len(r) - i
	}
	return 0
}
