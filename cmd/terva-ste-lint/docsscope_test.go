package main

import (
	"os"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// The docs tier exists to carry ONE rule. A tier that quietly grew a second
// one would apply Simplified Technical English to explanation prose, which is
// the thing decision 0015 declined with numbers, so the narrowing is worth a
// test rather than a comment.
func TestDocsTierCarriesTheEmDashRuleAlone(t *testing.T) {
	body := "The tool reads the file — and it reports what it found; " +
		"THIS is emphasis, the result can't be cached, and the pixels are returned to the model " +
		"in a sentence that runs on well past the cap the policy sets for any single sentence."

	full := rulesOf(check(Text{File: "x.go", Line: 1, What: "Description()", Body: body}))
	if len(full) < 4 {
		t.Fatalf("the fixture stopped exercising the other rules: %v", full)
	}

	docs := rulesOf(check(Text{File: "docs/x.md", Line: 1, What: "documentation", Body: body, Rules: rulesDocsEmDash}))
	if !docs[asideEmDashRule] {
		t.Errorf("the em-dash rule must fire on the docs tier, got %v", docs)
	}
	for r := range docs {
		if r != asideEmDashRule {
			t.Errorf("rule %q fired on the docs tier, which carries %q alone", r, asideEmDashRule)
		}
	}
}

// The semicolon half must NOT reach docs/. It is the half decision 0015 left
// with the writer, and the two halves share a name until something splits
// them, which is what makes this the regression worth guarding.
func TestDocsTierIgnoresTheSemicolonAside(t *testing.T) {
	body := "Commands run in the directory; a relative path resolves against it."
	if got := rulesOf(check(Text{File: "docs/x.md", Line: 1, What: "documentation", Body: body, Rules: rulesDocsEmDash})); len(got) != 0 {
		t.Errorf("the docs tier reported %v on a semicolon aside; it must stay silent", got)
	}
	if got := rulesOf(check(Text{File: "x.go", Line: 1, What: "Description()", Body: body})); !got["aside-semicolon"] {
		t.Errorf("the semicolon half must still fire on tool text, got %v", got)
	}
}

// The scope must match the public tier, which is the list the release overlay
// already encodes. Drift here ships a lint that governs a file nobody reads,
// or misses one every user gets.
func TestDocsScopeMatchesTheShippedTier(t *testing.T) {
	texts, err := collectDocs(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]bool{}
	for _, tx := range texts {
		files[tx.File] = true
	}
	if len(files) == 0 {
		t.Fatal("no docs enrolled — the collector has stopped finding the tree")
	}

	src, err := os.ReadFile(repoRoot + "/packages/testsupport/release_overlay_test.go")
	if err != nil {
		t.Skip("release_overlay_test.go absent on this tree")
	}
	body := string(src)
	held := body[strings.Index(body, "docsHeldBack"):strings.Index(body, "docsThatShip")]

	for f := range files {
		if strings.Contains(held, `"`+f+`"`) {
			t.Errorf("%s is held back by the release overlay but the lint enrolls it", f)
		}
	}
	if files["docs/working-agreements.md"] {
		t.Error("docs/working-agreements.md does not ship and must not be enrolled")
	}
	for _, exempt := range []string{"docs/architecture", "docs/decisions", "docs/plans", "docs/proposals", "docs/reviews", "docs/vanity"} {
		for f := range files {
			if strings.HasPrefix(f, exempt+"/") {
				t.Errorf("%s sits under the exempt directory %s", f, exempt)
			}
		}
	}
}

// A tree without docs/ is the public mirror, and it must lint rather than
// fail. Absence is an empty corpus on the same terms as AGENTS.md.
func TestDocsAbsenceIsAnEmptyCorpus(t *testing.T) {
	texts, err := collectDocs(testsupport.TempDir(t))
	if err != nil {
		t.Fatalf("a tree without docs/ must not be an error: %v", err)
	}
	if len(texts) != 0 {
		t.Errorf("expected an empty corpus, got %d texts", len(texts))
	}
}

// Every enrolled text must carry the docs rule set. The set rides on the Text
// rather than on the corpus, so a collector that forgot to stamp one would
// hand a docs page to the full policy in silence.
func TestEveryEnrolledDocCarriesTheDocsRuleSet(t *testing.T) {
	texts, err := collectDocs(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, tx := range texts {
		if tx.Rules != rulesDocsEmDash {
			t.Fatalf("%s:%d carries rule set %d, not the docs tier", tx.File, tx.Line, tx.Rules)
		}
	}
}

// Decision 0016: the docs tier reads headings and table cells. A regression
// here is silent, because the gate would go on reporting zero over a file that
// still carries the character, which is exactly the state 0016 was written to
// end.
func TestDocsTierReadsHeadingsAndTableCells(t *testing.T) {
	src := "# A title — with an aside\n\nOrdinary prose.\n\n" +
		"| col | col |\n|---|---|\n| a cell — with an aside | plain |\n"
	got := map[string]bool{}
	for _, tx := range markdownFurniture("docs/x.md", src, "documentation") {
		got[tx.What] = true
		if tx.Rules != rulesDocsEmDash {
			t.Errorf("%s carries the wrong rule set", tx.What)
		}
	}
	for _, want := range []string{"documentation (heading)", "documentation (table cell)"} {
		if !got[want] {
			t.Errorf("no %s emitted, got %v", want, got)
		}
	}
	if n := len(check(Text{File: "docs/x.md", Line: 1, What: "documentation (heading)",
		Body: "A title — with an aside", Rules: rulesDocsEmDash})); n != 1 {
		t.Errorf("the em-dash rule must fire on a heading, got %d findings", n)
	}
}

// The `|---|:--:|` divider is punctuation. Linting it would report the dashes
// that make it a table, on every table in the tree.
func TestTableSeparatorRowIsNotLinted(t *testing.T) {
	for _, sep := range []string{"|---|---|", "| --- | --- |", "|:--|--:|:-:|", "|---"} {
		src := "| a | b |\n" + sep + "\n| c | d |\n"
		for _, tx := range markdownFurniture("docs/x.md", src, "documentation") {
			if strings.Contains(tx.Body, "--") {
				t.Errorf("separator %q leaked as text: %q", sep, tx.Body)
			}
		}
	}
}

// A fenced code block is typed text, not prose, and a table drawn inside one
// is a picture of a table.
func TestFurnitureSkipsFencedBlocks(t *testing.T) {
	src := "```\n# not a heading — really\n| not a cell — really |\n```\n# a heading\n"
	for _, tx := range markdownFurniture("docs/x.md", src, "documentation") {
		if strings.Contains(tx.Body, "really") {
			t.Errorf("fenced content leaked: %q", tx.Body)
		}
	}
}

// AGENTS.md shares the extractor and is held to the FULL rule set, so the
// widening must not reach it: a heading there would be reported for its
// sentence count on a file the release excludes.
func TestWideningDoesNotReachAgentsMD(t *testing.T) {
	texts, err := collectAgentsMD(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, tx := range texts {
		if strings.Contains(tx.What, "heading") || strings.Contains(tx.What, "table cell") {
			t.Fatalf("AGENTS.md gained %q from the docs widening", tx.What)
		}
	}
}
