package widgets

import "strings"

// StripANSIBytes removes ANSI escape sequences from s without pulling in
// the regexp package: CSI (ESC '[' ... final byte) and OSC (ESC ']' ...
// BEL or ST). It mirrors the unexported stripANSI in package tui, which
// is not worth exporting to its own dependents just for this.
//
// OSC is here for the same reason it is there: an OSC 8 hyperlink puts a
// URL in the byte stream that occupies no cells, and a stripper that
// leaves it behind reports a blank row as non-blank and a short row as
// wide.
func StripANSIBytes(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) {
			switch s[i+1] {
			case '[':
				end := i + 2
				for end < len(s) {
					c := s[end]
					end++
					if c >= 0x40 && c <= 0x7e {
						break
					}
				}
				i = end
				continue
			case ']':
				end := i + 2
				for end < len(s) {
					if s[end] == 0x07 {
						end++
						break
					}
					if s[end] == 0x1b && end+1 < len(s) && s[end+1] == '\\' {
						end += 2
						break
					}
					end++
				}
				i = end
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
