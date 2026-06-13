package tools

import (
	"fmt"
	"strings"
)

// This file is the edit tool's match-resolution layer: exact match
// first, then a whitespace-tolerant fallback, then a did-you-mean
// error. The fallback exists because the dominant real-world edit
// failure is a model copying code with slightly-off indentation or
// trailing whitespace (the field data behind this is Aider's edit-
// format reliability work — see docs/plans/harness-landscape-2026.md
// A5); rejecting those outright costs a full read/retry round-trip
// that a constrained re-match avoids safely.

// editSpan is one resolved splice: replace body[start:end] with
// replacement.
type editSpan struct {
	start, end  int
	replacement string
}

// resolveSpans turns one edit op into splice spans against the
// \n-normalized body. Resolution order:
//
//  1. exact substring match — must be unique unless replaceAll;
//  2. whitespace-tolerant line match (lines equal after right-trim
//     and one uniform indent shift) — the shift is re-applied to
//     newText so the replacement lands at the file's real
//     indentation; must be unambiguous unless replaceAll;
//  3. error carrying a did-you-mean snippet anchored on the first
//     line of oldText, so the model can self-correct without another
//     read round-trip.
func resolveSpans(body string, e editOp, ordinal int, path string) ([]editSpan, error) {
	if n := strings.Count(body, e.OldText); n > 0 {
		if n > 1 && !e.ReplaceAll {
			return nil, fmt.Errorf("edit %d: oldText matches %d times in %s (lines %s); add surrounding context to make it unique, or set replaceAll",
				ordinal, n, path, occurrenceLines(body, e.OldText, 3))
		}
		var out []editSpan
		for off := 0; ; {
			idx := strings.Index(body[off:], e.OldText)
			if idx < 0 {
				break
			}
			start := off + idx
			out = append(out, editSpan{start: start, end: start + len(e.OldText), replacement: e.NewText})
			off = start + len(e.OldText)
			if !e.ReplaceAll {
				break
			}
		}
		return out, nil
	}

	ms := tolerantMatches(body, e.OldText)
	switch {
	case len(ms) == 1 || (e.ReplaceAll && len(ms) > 0):
		out := make([]editSpan, 0, len(ms))
		for _, m := range ms {
			out = append(out, editSpan{start: m.start, end: m.end, replacement: applyShift(e.NewText, m.shift)})
			if !e.ReplaceAll {
				break
			}
		}
		return out, nil
	case len(ms) > 1:
		return nil, fmt.Errorf("edit %d: oldText matches %d locations in %s after whitespace-tolerant matching; add surrounding context, or set replaceAll",
			ordinal, len(ms), path)
	}
	return nil, fmt.Errorf("edit %d: oldText not found in %s%s", ordinal, path, didYouMean(body, e.OldText))
}

// indentShift is the uniform leading-whitespace delta between oldText
// and the matched file block. Exactly one of add/remove is non-empty
// (both empty means the lines differed only by trailing whitespace).
type indentShift struct {
	add    string // prefix present in the file but missing from oldText
	remove string // prefix present in oldText but missing from the file
}

type tolerantMatch struct {
	start, end int
	shift      indentShift
}

// tolerantMatches finds blocks of body whose lines equal oldText's
// lines after right-trimming and one uniform indent shift. Blank
// lines match blank lines under any shift. Only called after the
// exact match found nothing.
func tolerantMatches(body, oldText string) []tolerantMatch {
	oldHadNL := strings.HasSuffix(oldText, "\n")
	oldLines := strings.Split(strings.TrimSuffix(oldText, "\n"), "\n")

	// Body lines with their byte offsets. SplitAfter keeps each \n so
	// offsets stay exact; the final empty element (body ending in \n)
	// is dropped.
	bodyLines := strings.SplitAfter(body, "\n")
	if n := len(bodyLines); n > 0 && bodyLines[n-1] == "" {
		bodyLines = bodyLines[:n-1]
	}
	offsets := make([]int, len(bodyLines)+1)
	for i, l := range bodyLines {
		offsets[i+1] = offsets[i] + len(l)
	}

	var out []tolerantMatch
	for i := 0; i+len(oldLines) <= len(bodyLines); i++ {
		shift, ok := windowShift(bodyLines[i:i+len(oldLines)], oldLines)
		if !ok {
			continue
		}
		last := i + len(oldLines) - 1
		end := offsets[last] + len(strings.TrimSuffix(bodyLines[last], "\n"))
		if oldHadNL {
			// oldText claimed the newline too; span through it (at
			// EOF without a trailing \n this is simply EOF).
			end = offsets[last+1]
		}
		out = append(out, tolerantMatch{start: offsets[i], end: end, shift: shift})
	}
	return out
}

// windowShift reports whether window matches old line-for-line after
// right-trimming and one uniform indent shift, and what that shift is.
func windowShift(window, old []string) (indentShift, bool) {
	var sh indentShift
	const (
		undecided = iota
		equal
		adding
		removing
	)
	mode := undecided
	for k := range old {
		wl := strings.TrimRight(strings.TrimSuffix(window[k], "\n"), " \t")
		ol := strings.TrimRight(old[k], " \t")
		if wl == "" && ol == "" {
			continue
		}
		if wl == "" || ol == "" {
			return sh, false
		}
		switch mode {
		case undecided:
			switch {
			case wl == ol:
				mode = equal
			case strings.HasSuffix(wl, ol) && isOnlyWS(wl[:len(wl)-len(ol)]):
				mode, sh.add = adding, wl[:len(wl)-len(ol)]
			case strings.HasSuffix(ol, wl) && isOnlyWS(ol[:len(ol)-len(wl)]):
				mode, sh.remove = removing, ol[:len(ol)-len(wl)]
			default:
				return sh, false
			}
		case equal:
			if wl != ol {
				return sh, false
			}
		case adding:
			if wl != sh.add+ol {
				return sh, false
			}
		case removing:
			if ol != sh.remove+wl {
				return sh, false
			}
		}
	}
	// An all-blank oldText never reaches here through Execute (empty
	// oldText is rejected), but a whitespace-only one could: there is
	// no meaningful anchor in that, so refuse.
	if mode == undecided {
		return sh, false
	}
	return sh, true
}

func isOnlyWS(s string) bool {
	return strings.TrimRight(s, " \t") == ""
}

// applyShift re-indents replacement text by the shift discovered
// during tolerant matching, so an oldText captured at the wrong
// indentation still produces a correctly-indented result. Blank lines
// are left alone. A removal prefix missing from a line leaves that
// line unchanged — a slightly-deep indent beats dropped characters.
func applyShift(text string, sh indentShift) string {
	if sh.add == "" && sh.remove == "" {
		return text
	}
	hadNL := strings.HasSuffix(text, "\n")
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if sh.add != "" {
			lines[i] = sh.add + l
		} else if strings.HasPrefix(l, sh.remove) {
			lines[i] = strings.TrimPrefix(l, sh.remove)
		}
	}
	out := strings.Join(lines, "\n")
	if hadNL {
		out += "\n"
	}
	return out
}

// occurrenceLines renders the 1-based line numbers of up to max
// occurrences of needle, for the ambiguity error.
func occurrenceLines(body, needle string, max int) string {
	var nums []string
	for off := 0; len(nums) < max; {
		idx := strings.Index(body[off:], needle)
		if idx < 0 {
			break
		}
		abs := off + idx
		nums = append(nums, fmt.Sprintf("%d", 1+strings.Count(body[:abs], "\n")))
		off = abs + len(needle)
	}
	s := strings.Join(nums, ", ")
	if strings.Count(body, needle) > max {
		s += ", …"
	}
	return s
}

// didYouMeanLines caps the snippet a not-found error embeds. Enough
// to show where a block diverges; small enough not to bloat a
// transcript when oldText was huge.
const didYouMeanLines = 12

// didYouMean anchors on oldText's first non-blank line and, when that
// line exists in the file, returns the file's actual block from there
// so the model sees exactly what to copy. Empty when no anchor hits.
func didYouMean(body, oldText string) string {
	anchor := ""
	for _, l := range strings.Split(oldText, "\n") {
		if strings.TrimSpace(l) != "" {
			anchor = strings.TrimSpace(l)
			break
		}
	}
	if anchor == "" {
		return ""
	}
	bodyLines := strings.Split(body, "\n")
	for i, bl := range bodyLines {
		if strings.TrimSpace(bl) != anchor {
			continue
		}
		n := len(strings.Split(strings.TrimSuffix(oldText, "\n"), "\n"))
		if n > didYouMeanLines {
			n = didYouMeanLines
		}
		end := i + n
		if end > len(bodyLines) {
			end = len(bodyLines)
		}
		return fmt.Sprintf("; its first line matches file line %d but the block diverges — actual content there:\n%s",
			i+1, strings.Join(bodyLines[i:end], "\n"))
	}
	return ""
}
