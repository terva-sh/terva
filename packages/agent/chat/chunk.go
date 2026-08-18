package chat

import (
	"strings"
	"unicode/utf8"
)

// runeSafeCut returns how many bytes of s to take for one chunk of at most
// limit bytes, without ending inside a rune.
//
// The limit is a BYTE ceiling (connsdk.Capabilities.MaxTextLen), so the cut
// walks back from it to the nearest rune start rather than rounding up.
//
// The one case with no correct answer is a rune wider than the whole limit.
// Nothing valid fits, so it emits the rune and exceeds the limit by a few
// bytes: a service that rejects a 4-byte message on a 2-byte limit was never
// going to work, and returning 0 here would spin the caller's loop forever.
func runeSafeCut(s string, limit int) int {
	if len(s) <= limit {
		return len(s)
	}
	i := limit
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	if i == 0 {
		_, w := utf8.DecodeRuneInString(s)
		return w
	}
	return i
}

// ChunkMessage splits s into chunks no larger than limit bytes, on
// line boundaries when possible. limit <= 0 means no limit.
//
// A chunk always ends on a rune boundary. The hard-split branch used to slice
// raw bytes, so any line over the limit was cut mid-rune and the two halves
// reached the user as replacement characters — on the single outbound chokepoint
// for both Loop and Bridge.
func ChunkMessage(s string, limit int) []string {
	if limit <= 0 || len(s) <= limit {
		return []string{s}
	}
	var out []string
	lines := strings.Split(s, "\n")
	var cur strings.Builder
	for _, l := range lines {
		if cur.Len()+len(l)+1 > limit && cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
		if len(l) > limit {
			// Line itself too long; hard-split, on rune boundaries.
			for len(l) > limit {
				cut := runeSafeCut(l, limit)
				out = append(out, l[:cut])
				l = l[cut:]
			}
		}
		if cur.Len() > 0 {
			cur.WriteString("\n")
		}
		cur.WriteString(l)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// IsStopCommand reports whether text should abort the active turn.
// Chat users often type plain "stop" rather than bot-style "/stop";
// keep this intentionally narrow so normal prompts like "stop doing
// X" still go to the agent.
func IsStopCommand(text string) bool {
	return strings.EqualFold(strings.TrimSpace(text), "stop")
}
