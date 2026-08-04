package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// AGENTS.md is the heaviest single source in the system prompt terva builds
// for a session in this repository: build.go loads the file byte for byte
// into every turn, after the config-layer context files. That makes it
// model-facing decision text of exactly the kind this lint governs.
//
// It is also a fork-local file behind the release EXCLUDES, so a tree
// without one — the public mirror — has nothing to lint here. Absence is an
// empty corpus, not an error; TestAgentsMDEnrolledWhenPresent keeps the
// enrollment honest in trees that do carry the file.
//
// Only prose is enrolled: paragraphs and list items. Fenced code, headings
// and table rows are not sentences, and prose rules can only misfire on them
// (the i18n.H lesson, recorded in extract.go).
const agentsMDName = "AGENTS.md"

func collectAgentsMD(root string) ([]Text, error) {
	src, err := os.ReadFile(filepath.Join(root, agentsMDName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return markdownProse(agentsMDName, string(src)), nil
}

var (
	listMarkerRe = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+`)
	linkRe       = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	strongRe     = regexp.MustCompile(`\*\*([^*]+)\*\*|__([^_]+)__`)
	emphasisRe   = regexp.MustCompile(`\*([^*\s][^*]*)\*`)
)

// markdownProse splits a markdown document into prose blocks — one Text per
// paragraph or list item, at the line the block starts on. A list item is its
// own block even without a blank line before it, so a long bullet list is not
// misread as one over-long paragraph.
func markdownProse(file, src string) []Text {
	var (
		out     []Text
		block   []string
		start   int
		section = "preamble"
		inFence bool
	)
	flush := func() {
		if len(block) == 0 {
			return
		}
		out = append(out, Text{
			File: file,
			Line: start,
			What: "project instructions (" + section + ")",
			Body: strings.Join(block, " "),
		})
		block = nil
	}
	for i, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			flush()
			continue
		}
		if inFence {
			continue
		}
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "#"):
			flush()
			section = strings.TrimSpace(strings.TrimLeft(line, "# "))
		case strings.HasPrefix(line, "|"):
			flush()
		case listMarkerRe.MatchString(raw):
			flush()
			start = i + 1
			block = append(block, cleanProse(listMarkerRe.ReplaceAllString(raw, "")))
		default:
			if len(block) == 0 {
				start = i + 1
			}
			block = append(block, cleanProse(line))
		}
	}
	flush()
	return out
}

// cleanProse reduces markdown notation to the prose the reader sees: link
// text without its target, emphasis without its markers. Backtick spans stay
// verbatim — the rules already blank them as code, and an asterisk inside one
// (a glob) must not read as an emphasis marker. Without the unwrap, the
// rules' glob pattern would blank a **bold** span as code and bolded prose
// would silently escape every rule.
func cleanProse(s string) string {
	parts := strings.Split(s, "`")
	for i := 0; i < len(parts); i += 2 { // even indexes sit outside backtick spans
		p := linkRe.ReplaceAllString(parts[i], "$1")
		p = strongRe.ReplaceAllString(p, "$1$2")
		p = emphasisRe.ReplaceAllString(p, "$1")
		// A bold span that contains a backtick span opens in one part and
		// closes in another, so strongRe cannot see the pair. The stray
		// markers would then glue two sentences together at ".**". Outside a
		// code span a double asterisk is only ever a bold marker: drop it.
		p = strings.ReplaceAll(p, "**", "")
		parts[i] = p
	}
	return strings.Join(parts, "`")
}
