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

	// contractionRe holds the suffixes that are ALWAYS a contraction. "'s" is
	// deliberately absent: it is a possessive far more often than it is "is",
	// and apostropheSRe below decides that case on the stem.
	contractionRe = regexp.MustCompile(`(?i)\b\w+(n't|'re|'ve|'ll|'d|’t|’re|’ve|’ll|’d)\b`)

	// apostropheSRe captures the stem before an "'s" so the check can ask
	// whether it is "it's" or "the product's".
	apostropheSRe = regexp.MustCompile(`(?i)\b([a-z]+)['’]s\b`)
	emDashRe      = regexp.MustCompile(`\s—\s|\s--\s`)
	semicolonRe   = regexp.MustCompile(`;`)
	allCapsRe     = regexp.MustCompile(`\b[A-Z]{2,}\b`)
	ingRe         = regexp.MustCompile(`(?i)\b([a-z]{2,}ing)\b`)

	// codeSpanRe strips the spans a reader is meant to type verbatim. A flag,
	// an enum value, or a path is not prose and must not be held to prose rules
	// — `set -e`, `git add -A` and "0755" would otherwise each raise something.
	//
	// The single-quoted span is not an alternative here. It needs a test on the
	// characters either side of the quote, which is blankSingleQuoted below.
	codeSpanRe = regexp.MustCompile("`[^`]*`|\"[^\"]*\"|\\$[A-Z_]+|\\b[a-z_]+\\([^)]*\\)|\\b[a-z]+(?:_[a-z]+)+\\b|<[a-z_]+>|\\b[0-9]+(?:o[0-7]+|x[0-9a-fA-F]+)?\\b|\\.[a-z]{2,6}\\b|\\*\\*?/?\\S*")
)

// stripCode blanks the verbatim spans while preserving length-independent word
// boundaries, so positions in the remaining prose stay meaningful.
func stripCode(s string) string {
	s = blankSingleQuoted(s)
	return codeSpanRe.ReplaceAllStringFunc(s, func(m string) string {
		return strings.Repeat(" ", len(m))
	})
}

// blankSingleQuoted blanks 'a quoted span' and leaves an English possessive
// alone, which the same character also spells.
//
// What separates them is the neighbours rather than the quote: a possessive
// carries a letter on both sides of its apostrophe, and an opening quote never
// does. So both ends are tested, and "the agent's tools" and "the agents'
// tools" stay prose while "give 'confirm' and 'revise'" does not.
//
// This replaced a bare '[^']*' alternative in codeSpanRe that read the gap
// between two possessives as one quoted span. The failure was silent and it
// cost the documentation gate real findings: "terva's posture — what is
// encrypted ... the store's grant model" blanked both em dashes before
// emDashRe saw them, and docs/controllers.md passed with four of them still in
// it. TKT-01M2NZAP8 carries the measurement.
//
// A scan rather than another alternative, for two reasons. RE2 has no
// lookbehind, so a pattern must consume the character before the quote and put
// it back afterwards. Worse, consuming the character after the closing quote
// hides the next span, which 'a' 'b' trips on at once.
func blankSingleQuoted(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		if s[i] != '\'' || (i > 0 && quoteNeighbour(s[i-1])) {
			continue
		}
		end := closingQuote(s, i)
		if end < 0 {
			continue
		}
		if b == nil {
			b = []byte(s)
		}
		for j := i; j <= end; j++ {
			b[j] = ' '
		}
		i = end
	}
	if b == nil {
		return s
	}
	return string(b)
}

// closingQuote returns the index of the quote that closes the span opened at
// open, or -1 when the span never closes.
//
// A candidate with a letter after it is a possessive inside the quoted text,
// so the search steps over it rather than stopping: 'don't do this' closes at
// the end and not in the middle. An unclosed quote returns -1 and blanks
// nothing, because half a span is more likely a stray apostrophe than a span.
func closingQuote(s string, open int) int {
	for j := open + 1; j < len(s); j++ {
		if s[j] == '\n' {
			return -1
		}
		if s[j] != '\'' {
			continue
		}
		if j+1 == len(s) || !quoteNeighbour(s[j+1]) {
			return j
		}
	}
	return -1
}

// quoteNeighbour reports whether a byte beside an apostrophe binds it into a
// word. isWordByte is not reused: it counts "-" for the sake of the term
// check, and a hyphen beside a quote is punctuation rather than a possessive.
func quoteNeighbour(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
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
	// Every rule reports through here, so the rule set filters in one place
	// and a rule added later cannot forget to consult it.
	report := func(rule, msg, quote string) {
		if !t.Rules.covers(rule) {
			return
		}
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
	} else if m := contractionOfIs(prose); m != "" {
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
	//
	// They report under separate names because the docs/ tier takes the
	// em-dash half alone. Splitting them is what lets one rule set carry one
	// half; see rulesDocsEmDash in policy.go.
	if emDashRe.MatchString(stripCode(raw)) {
		report("aside-em-dash", "an em-dash aside hides a second sentence — promote it", raw)
	}
	if semicolonRe.MatchString(stripCode(raw)) {
		report("aside-semicolon", "a semicolon joins two sentences — write them as two", raw)
	}

	for term, want := range bannedTerms {
		if !containsWord(lower, term) {
			continue
		}
		report("term", fmt.Sprintf("%q — use %s", term, want), raw)
		break
	}
}

// contractionOfIs returns the first "'s" that shortens is or has, and the
// empty string when every "'s" in the sentence is a possessive. Only a stem in
// contractionStems counts, so "a persona's icon field" passes and "it's" does
// not.
func contractionOfIs(prose string) string {
	for _, m := range apostropheSRe.FindAllStringSubmatch(prose, -1) {
		if contractionStems[strings.ToLower(m[1])] {
			return m[0]
		}
	}
	return ""
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
