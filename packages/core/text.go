package core

import "strings"

// CleanOneLine collapses a value to a single safe display line: newlines, tabs,
// and other control characters (including ANSI escapes) are dropped or turned
// into spaces, runs of whitespace are collapsed, the result is trimmed, and it
// is truncated to max runes with an ellipsis. This defuses display-injection via
// model-authored fields and bounds output size. max <= 0 means no length limit.
//
// It lives here rather than in one of its callers because two independent
// agent-facing stores need exactly it — the task board and memory — and the
// second one was about to copy it. A duplicate of a sanitizer is the worst kind:
// a fix to the live copy silently leaves the dead twin accepting what it was
// meant to reject.
func CleanOneLine(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1 // drop other control chars (e.g. ESC)
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if max > 0 {
		r := []rune(s)
		if len(r) > max {
			return strings.TrimSpace(string(r[:max])) + "…"
		}
	}
	return s
}
