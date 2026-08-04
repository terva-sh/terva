package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

const agentsFixture = "# Title\n" +
	"\n" +
	"An opening paragraph that DEFINITELY has emphasis.\n" +
	"It continues on a second line.\n" +
	"\n" +
	"## Commands\n" +
	"\n" +
	"```bash\n" +
	"just build   # NEVER linted, utilize whatever you want\n" +
	"```\n" +
	"\n" +
	"- **Bold rule:** don't do the FORBIDDEN thing.\n" +
	"- A second item with a [link](docs/plan.md) in it.\n" +
	"| a | table |\n" +
	"\n" +
	"A closing *emphasised* paragraph with `release/*` inside a span.\n"

func TestMarkdownProseExtraction(t *testing.T) {
	texts := markdownProse("AGENTS.md", agentsFixture)
	if len(texts) != 4 {
		for _, x := range texts {
			t.Logf("%d %q: %q", x.Line, x.What, x.Body)
		}
		t.Fatalf("got %d blocks, want 4 (two paragraphs, two list items)", len(texts))
	}

	para := texts[0]
	if para.Line != 3 || !strings.Contains(para.Body, "It continues") {
		t.Errorf("paragraph block wrong: line %d body %q", para.Line, para.Body)
	}
	if para.What != "project instructions (Title)" {
		t.Errorf("section label wrong: %q", para.What)
	}

	for _, x := range texts {
		if strings.Contains(x.Body, "just build") || strings.Contains(x.Body, "utilize") {
			t.Errorf("fenced code leaked into the corpus: %q", x.Body)
		}
		if strings.Contains(x.Body, "table") {
			t.Errorf("table row leaked into the corpus: %q", x.Body)
		}
	}

	bullet := texts[1]
	if bullet.What != "project instructions (Commands)" {
		t.Errorf("bullet section wrong: %q", bullet.What)
	}
	if strings.Contains(bullet.Body, "**") || !strings.Contains(bullet.Body, "Bold rule:") {
		t.Errorf("bold not unwrapped to prose: %q", bullet.Body)
	}
	if !strings.Contains(texts[2].Body, "a link in it") {
		t.Errorf("link target not dropped: %q", texts[2].Body)
	}
	last := texts[3]
	if strings.Contains(last.Body, "*emphasised*") || !strings.Contains(last.Body, "emphasised") {
		t.Errorf("single emphasis not unwrapped: %q", last.Body)
	}
	if !strings.Contains(last.Body, "`release/*`") {
		t.Errorf("backtick span was rewritten: %q", last.Body)
	}
}

// TestMarkdownProseFeedsTheRules: the extractor exists so that AGENTS.md prose
// meets the same rules as tool text. Bolded shouting must reach the
// caps-emphasis rule — the unwrap in cleanProse is what makes that possible,
// because the rules' own glob pattern would otherwise blank a **bold** span as
// code.
func TestMarkdownProseFeedsTheRules(t *testing.T) {
	texts := markdownProse("AGENTS.md", agentsFixture)
	rules := map[string]bool{}
	for _, x := range texts {
		for _, f := range check(x) {
			rules[f.Rule] = true
		}
	}
	for _, want := range []string{"caps-emphasis", "contraction"} {
		if !rules[want] {
			t.Errorf("rule %s did not fire on the fixture — bold or bullet prose is escaping the rules", want)
		}
	}
}

func TestCollectAgentsMDAbsenceIsEmpty(t *testing.T) {
	texts, err := collectAgentsMD(testsupport.TempDir(t))
	if err != nil || texts != nil {
		t.Errorf("missing AGENTS.md must be an empty corpus, got %d texts, err %v", len(texts), err)
	}
}

// TestAgentsMDEnrolledWhenPresent: in a tree that carries an AGENTS.md (this
// fork), collect must include it — otherwise the enrollment is decorative. The
// public mirror excludes the file, so absence skips rather than fails.
func TestAgentsMDEnrolledWhenPresent(t *testing.T) {
	if _, err := os.Stat(filepath.Join(repoRoot, agentsMDName)); err != nil {
		t.Skip("no AGENTS.md in this tree (public mirror)")
	}
	texts, err := collect(repoRoot)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	n := 0
	for _, x := range texts {
		if x.File == agentsMDName {
			n++
		}
	}
	if n == 0 {
		t.Error("AGENTS.md exists but contributed no text — the enrollment is broken")
	}
}
