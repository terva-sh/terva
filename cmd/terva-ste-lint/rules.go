package main

import (
	"fmt"
	"regexp"
	"strings"
)

// Finding is one rule violation, reported at the position of the text that
// carries it.
type Finding struct {
	Text  Text
	Rule  string
	Msg   string
	Quote string
}

func (f Finding) String() string {
	q := f.Quote
	if len(q) > 90 {
		q = q[:87] + "..."
	}
	return fmt.Sprintf("%s:%d: %s: %s\n    %s: %q", f.Text.File, f.Text.Line, f.Rule, f.Msg, f.Text.What, q)
}

var (
	// wordRe deliberately keeps hyphens and apostrophes inside a word, so
	// "case-insensitive" is one word and "don't" is caught by the contraction
	// rule rather than split into two.
	wordRe = regexp.MustCompile(`[A-Za-z][A-Za-z'’-]*`)

	// sentenceSplitRe breaks on terminal punctuation followed by a capital. It
	// does NOT break on ".go", ".jsonl" or "0o777" because the next character is
	// lower case or a digit — which is the whole reason for the lookahead.
	sentenceSplitRe = regexp.MustCompile(`(?:[.!?])[\s]+`)

	contractionRe = regexp.MustCompile(`(?i)\b\w+(n't|'re|'ve|'ll|'d|'s|’t|’re|’ve|’ll)\b`)
	emDashRe      = regexp.MustCompile(`\s—\s|\s--\s`)
	semicolonRe   = regexp.MustCompile(`;`)
	allCapsRe     = regexp.MustCompile(`\b[A-Z]{2,}\b`)
	ingRe         = regexp.MustCompile(`(?i)\b([a-z]{2,}ing)\b`)

	// codeSpanRe strips the spans a reader is meant to type verbatim. A flag,
	// an enum value, or a path is not prose and must not be held to prose rules
	// — `set -e`, `git add -A` and "0755" would otherwise each raise something.
	codeSpanRe = regexp.MustCompile("`[^`]*`|\"[^\"]*\"|'[^']*'|\\$[A-Z_]+|\\b[a-z_]+\\([^)]*\\)|\\b[a-z]+(?:_[a-z]+)+\\b|<[a-z_]+>|\\b[0-9]+(?:o[0-7]+|x[0-9a-fA-F]+)?\\b|\\.[a-z]{2,6}\\b|\\*\\*?/?\\S*")
)

// stripCode blanks the verbatim spans while preserving length-independent word
// boundaries, so positions in the remaining prose stay meaningful.
func stripCode(s string) string {
	return codeSpanRe.ReplaceAllStringFunc(s, func(m string) string {
		return strings.Repeat(" ", len(m))
	})
}

func sentences(paragraph string) []string {
	var out []string
	for _, s := range sentenceSplitRe.Split(paragraph, -1) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// check runs every rule over one piece of tool text.
func check(t Text) []Finding {
	var fs []Finding
	report := func(rule, msg, quote string) {
		fs = append(fs, Finding{Text: t, Rule: rule, Msg: msg, Quote: quote})
	}

	for _, para := range strings.Split(t.Body, "\n\n") {
		para = strings.Join(strings.Fields(para), " ")
		if para == "" {
			continue
		}
		sents := sentences(para)
		if n := len(sents); n > maxParagraphSentences {
			report("paragraph-length",
				fmt.Sprintf("%d sentences in one paragraph, limit %d — split it, one topic each", n, maxParagraphSentences),
				para)
		}
		for _, s := range sents {
			prose := stripCode(s)
			words := wordRe.FindAllString(prose, -1)
			if len(words) > maxSentenceWords {
				report("sentence-length",
					fmt.Sprintf("%d words, limit %d — say one thing per sentence", len(words), maxSentenceWords),
					s)
			}
			checkSentence(s, prose, words, report)
		}
	}
	return fs
}

func checkSentence(raw, prose string, words []string, report func(rule, msg, quote string)) {
	lower := strings.ToLower(prose)

	// Passive voice: a form of "be" followed by a past participle.
	for i := 0; i < len(words)-1; i++ {
		if !beVerbs[strings.ToLower(words[i])] {
			continue
		}
		next := strings.ToLower(words[i+1])
		if strings.HasSuffix(next, "ed") || irregularParticiples[next] {
			report("passive-voice",
				fmt.Sprintf("%q reads as passive — name the actor and use the active form", words[i]+" "+words[i+1]),
				raw)
			break
		}
	}

	// Participles and gerunds.
	for _, m := range ingRe.FindAllStringSubmatch(prose, -1) {
		w := strings.ToLower(m[1])
		if allowedINGForms[w] {
			continue
		}
		report("ing-form",
			fmt.Sprintf("%q is a participle — use a plain verb, or a clause with a subject", m[1]),
			raw)
		break
	}

	if m := contractionRe.FindString(prose); m != "" {
		report("contraction", fmt.Sprintf("%q — write the full form", m), raw)
	}

	// Typographic emphasis. STE has no such device: a capitalised word is
	// either a name or a writer shouting, and the second one is what Phase 1
	// removed from every tool.
	for _, m := range allCapsRe.FindAllString(prose, -1) {
		if allowedCaps[m] {
			continue
		}
		report("caps-emphasis",
			fmt.Sprintf("%q is emphasis, not a name — carry the weight in sentence structure", m),
			raw)
		break
	}

	// Both aside checks run on the stripped sentence: a ` -- ` or `;` inside
	// a verbatim span (`git checkout -- <file>`) is typed text, not prose.
	if emDashRe.MatchString(stripCode(raw)) {
		report("aside", "an em-dash aside hides a second sentence — promote it", raw)
	}
	if semicolonRe.MatchString(stripCode(raw)) {
		report("aside", "a semicolon joins two sentences — write them as two", raw)
	}

	for term, want := range bannedTerms {
		if !containsWord(lower, term) {
			continue
		}
		report("term", fmt.Sprintf("%q — use %s", term, want), raw)
		break
	}
}

// containsWord matches a term on word boundaries so "dir" does not fire inside
// "directory", "just" does not fire inside "adjust", and — the one that
// actually bit — "in the event" does not fire inside "in the events that agree
// with the filters". Multi-word terms get the same treatment: only the outer
// boundaries matter, and an interior "." or space is not a word byte anyway.
func containsWord(haystack, term string) bool {
	for i := 0; ; {
		j := strings.Index(haystack[i:], term)
		if j < 0 {
			return false
		}
		j += i
		beforeOK := j == 0 || !isWordByte(haystack[j-1])
		end := j + len(term)
		afterOK := end == len(haystack) || !isWordByte(haystack[end])
		if beforeOK && afterOK {
			return true
		}
		i = j + 1
		if i >= len(haystack) {
			return false
		}
	}
}

func isWordByte(b byte) bool {
	return b == '_' || b == '-' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}
