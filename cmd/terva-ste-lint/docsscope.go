package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The public documentation tier is enrolled for ONE rule: the em-dash half of
// the aside rule. Decision 0015 (docs/decisions/0015-docs-prose-lint-scope.md)
// is the ruling, and it declines everything else with the numbers.
//
// Why so little. Detection was never the constraint on this corpus. Every
// pattern worth counting is already countable, and what was missing was a
// verdict saying which counts are defects. Only the em dash has one: AGENTS.md
// adopts unslop rule 13 for docs/, settled on 2026-09-14.
//
// The two rules deliberately NOT here, each for its own reason:
//
//   - The lexical rules would report about twenty findings once, need three
//     exemption lists first (--substrate is a real flag, amazon-bedrock is a
//     provider id, and the curly quotes sit inside quoted terva UI text), and
//     then sit at zero forever.
//   - sentence-length cannot be applied at all. technical-writing holds
//     Simplified Technical English over reference and how-to and AWAY from
//     explanation, because applying it to a reason produces a short assertion
//     with the reason removed. 16 of the 25 prose-only public files are wholly
//     explanation, and the 24 mixed files have no single mode to test against.
//
// Scope stops at the public tier. The record directories are exempt for the
// reason the inventory gives: rewriting a dated artifact changes what it says
// a person thought on a day.
const docsBaselinePath = ".ste/docs-baseline.json"

// docsDir is the root of the documentation tree, relative to the repository.
const docsDir = "docs"

// docsShippedSubdirs are the directories under docs/ that ship whole. They
// mirror docsThatShip in packages/testsupport/release_overlay_test.go and
// shippedSubdirs in docs.go, which is the list this one must not drift from.
// TestDocsScopeMatchesTheShippedTier holds the two together.
var docsShippedSubdirs = []string{"design", "practices"}

// docsExemptFiles are top-level docs/ pages that do NOT ship, and so are not
// part of the public tier this scope governs. The release overlay holds the
// authoritative version of this in docsHeldBack; every other held-back entry
// is a directory, and a directory is excluded by not being listed above.
//
// working-agreements.md is the only held-back file that sits at the top level,
// which is why it needs naming here rather than falling out of the walk.
var docsExemptFiles = map[string]bool{
	"docs/working-agreements.md": true,
}

// collectDocs returns the prose of the public documentation tier: the
// top-level pages that ship, plus docs/design/ and docs/practices/ whole.
//
// A tree without docs/ is an empty corpus rather than an error, on the same
// terms as AGENTS.md: the public mirror is a real tree and it must lint.
func collectDocs(root string) ([]Text, error) {
	var files []string

	top, err := os.ReadDir(filepath.Join(root, docsDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	for _, e := range top {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		rel := docsDir + "/" + e.Name()
		if docsExemptFiles[rel] {
			continue
		}
		files = append(files, rel)
	}

	for _, sub := range docsShippedSubdirs {
		entries, err := os.ReadDir(filepath.Join(root, docsDir, sub))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("docs subdirectory %s/%s: %w", docsDir, sub, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			files = append(files, docsDir+"/"+sub+"/"+e.Name())
		}
	}

	sort.Strings(files)

	var out []Text
	for _, rel := range files {
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		for _, t := range markdownProseAs(rel, string(src), "documentation") {
			// The rule set rides on the Text rather than on the corpus, so
			// check() filters in the one place it already filters, and a rule
			// added later cannot forget to consult it.
			t.Rules = rulesDocsEmDash
			out = append(out, t)
		}
		// Headings and table cells, which markdownProse drops. See
		// markdownFurniture and decision 0016.
		out = append(out, markdownFurniture(rel, string(src), "documentation")...)
	}
	return out, nil
}

// tableSeparatorRe matches the `|---|:--:|` row that divides a table's header
// from its body. It is punctuation rather than text, and linting it would
// report the dashes that make it a table.
var tableSeparatorRe = regexp.MustCompile(`^\|[\s:|-]+\|?$`)

// markdownFurniture returns the text a reader sees that markdownProse does
// not emit: heading text, and each table cell on its own.
//
// markdownProse flushes on both and keeps neither, which is correct for the
// rules it was built for. A heading is not a sentence and a table row is not a
// paragraph, so sentence-length and paragraph-length would report nonsense
// over them. The em-dash rule has no such problem: the character is either
// there or it is not.
//
// Decision 0016 is the ruling. The first sweep reported zero findings on 16
// files that still carried 28 em dashes, because everything here was invisible
// to the gate, and 410 of the public tier's 1,818 sat outside it.
//
// This lives beside the docs collector rather than in markdownProse on
// purpose. AGENTS.md shares that extractor and is held to the FULL rule set,
// so widening it there would report a heading's length and a table row's
// sentence count on a file the release already excludes.
//
// Each cell is its own Text so a finding names one cell rather than a row of
// twelve, and so a fingerprint survives its neighbours being edited.
func markdownFurniture(file, src, kind string) []Text {
	var out []Text
	inFence := false
	for i, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "```"):
			inFence = !inFence
		case inFence:
		case strings.HasPrefix(line, "#"):
			if text := cleanProse(strings.TrimSpace(strings.TrimLeft(line, "# "))); text != "" {
				out = append(out, Text{File: file, Line: i + 1, What: kind + " (heading)", Body: text, Rules: rulesDocsEmDash})
			}
		case strings.HasPrefix(line, "|"):
			if tableSeparatorRe.MatchString(line) {
				continue
			}
			for _, cell := range strings.Split(strings.Trim(line, "|"), "|") {
				if text := cleanProse(strings.TrimSpace(cell)); text != "" {
					out = append(out, Text{File: file, Line: i + 1, What: kind + " (table cell)", Body: text, Rules: rulesDocsEmDash})
				}
			}
		}
	}
	return out
}
