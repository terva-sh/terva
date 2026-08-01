package memory

import (
	"strings"
	"unicode"
)

// dupJaccardThreshold: an add whose word-token set overlaps an existing entry's
// by at least this fraction is treated as a near-duplicate. Deliberately high
// (conservative) so distinct facts that merely share vocabulary aren't blocked —
// when in doubt the add goes through; the model is only stopped on a strong
// match, and told to use replace or rephrase.
const dupJaccardThreshold = 0.85

// findSimilar reports the existing entry that text duplicates — exact after
// normalization (case / surrounding punctuation / collapsed whitespace), or a
// high word-overlap near-duplicate — and whether that match was exact. Returns
// "" when text is sufficiently distinct from every entry.
func findSimilar(entries []string, text string) (match string, exact bool) {
	nt := normalizeForDup(text)
	tt := tokenSet(text)
	for _, e := range entries {
		if normalizeForDup(e) == nt {
			return e, true
		}
		if jaccard(tokenSet(e), tt) >= dupJaccardThreshold {
			return e, false
		}
	}
	return "", false
}

// normalizeForDup lowercases, collapses internal whitespace, and trims leading
// and trailing non-alphanumerics, so "Uses pnpm." and "uses pnpm" compare equal.
func normalizeForDup(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	return strings.TrimFunc(s, func(r rune) bool { return !isAlnum(r) })
}

// tokenSet is the set of lowercased alphanumeric word tokens in s.
func tokenSet(s string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, t := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !isAlnum(r) }) {
		set[t] = struct{}{}
	}
	return set
}

// jaccard is |a∩b| / |a∪b| over two token sets (0 when either is empty).
func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func isAlnum(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
