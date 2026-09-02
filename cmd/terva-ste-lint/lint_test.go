package main

import (
	"go/token"
	"os"
	"regexp"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/testsupport"
)

const repoRoot = "../.."

// TestEnrolledDirsExist: an enrolled directory that has been renamed or moved
// must fail here rather than silently linting nothing. A scope registry that
// points at a path which no longer exists is the quiet way a gate stops
// gating.
func TestEnrolledDirsExist(t *testing.T) {
	for _, d := range enrolledDirs {
		if fi, err := os.Stat(repoRoot + "/" + d); err != nil || !fi.IsDir() {
			t.Errorf("enrolled directory %q does not exist — fix scope.go or restore the path", d)
		}
	}
}

// TestExtractionFindsTheCoreTools: the extractor walks Go syntax, so a change
// to how a tool declares its text (a new helper, a different literal shape)
// can drop it from the corpus without any test noticing. These six are the
// tools every session uses; if the extractor stops seeing them it is broken,
// whatever the finding count says.
func TestExtractionFindsTheCoreTools(t *testing.T) {
	texts, err := collect(repoRoot)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(texts) < 100 {
		t.Errorf("only %d pieces of tool text extracted — the extractor is probably broken", len(texts))
	}
	byFile := map[string]int{}
	for _, x := range texts {
		byFile[x.File]++
	}
	for _, want := range []string{
		"packages/agent/tools/read.go",
		"packages/agent/tools/write.go",
		"packages/agent/tools/edit.go",
		"packages/agent/tools/bash.go",
		"packages/agent/tools/grep.go",
		"packages/agent/tools/glob.go",
	} {
		if byFile[want] == 0 {
			t.Errorf("no tool text extracted from %s", want)
		}
	}
}

// TestExtractorSeesThroughConstants: slugDesc, optionsDesc and the tasktool
// desc* constants are declared apart from the method that returns them,
// precisely so two tool shapes cannot describe one field differently. If the
// extractor stops resolving them, that shared text silently leaves the corpus
// and the lint passes by looking at less.
func TestExtractorSeesThroughConstants(t *testing.T) {
	texts, err := collect(repoRoot)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	var joined strings.Builder
	for _, x := range texts {
		joined.WriteString(x.Body)
		joined.WriteString("\n")
	}
	all := joined.String()
	for _, want := range []string{
		"A name is never a replacement for a clear question", // slugDesc
		"The answers for the user to select",                 // optionsDesc
		"Archive the current task list",                      // descArchive, via a const
	} {
		if !strings.Contains(all, want) {
			t.Errorf("extractor missed constant-declared tool text: %q", want)
		}
	}
}

// TestRulesFireOnKnownBadText is the teeth test. Each case is prose that the
// Phase 1 conversion removed; if a rule stops firing on its own motivating
// example, the rule is decorative.
func TestRulesFireOnKnownBadText(t *testing.T) {
	cases := []struct {
		rule string
		body string
	}{
		{"caps-emphasis", "This is THE way to inspect an image."},
		{"contraction", "The activated group can't change in this reply."},
		{"passive-voice", "The image pixels are returned to the model."},
		{"ing-form", "Write a file, creating parent directories."},
		{"aside", "Commands run in the directory; a relative path resolves against it."},
		{"term", "Paths are returned relative to cwd in lexical order."},
		{"sentence-length", "This one sentence deliberately runs on and on and on past the " +
			"cap that the policy sets for a single sentence, so that the length rule has " +
			"something it must object to."},
	}
	for _, c := range cases {
		fs := check(Text{File: "x.go", Line: 1, What: "Description()", Body: c.body})
		var got []string
		for _, f := range fs {
			got = append(got, f.Rule)
		}
		found := false
		for _, r := range got {
			if r == c.rule {
				found = true
			}
		}
		if !found {
			t.Errorf("rule %q did not fire on %q (fired: %v)", c.rule, c.body, got)
		}
	}
}

// TestRulesStaySilentOnGoodText: the other half of the teeth test. A lint that
// fires on its own house style gets switched off, so the converted text of the
// six core tools must be clean of everything except the two length rules the
// baseline still holds.
func TestRulesStaySilentOnGoodText(t *testing.T) {
	good := []string{
		"Read a file from disk. Use this tool also to examine a local image.",
		"The tool obeys .gitignore and does not search binary files.",
		"Give the path to a png, jpg, gif, or webp file.",
		"Set this to false only when the options are the complete set.",
		"The maximum number of matches to return. The default is 200.",
	}
	for _, g := range good {
		for _, f := range check(Text{File: "x.go", Line: 1, What: "Description()", Body: g}) {
			t.Errorf("false positive %s on clean text %q: %s", f.Rule, g, f.Msg)
		}
	}
}

// The contraction rule used to read every apostrophe-s as a contraction, so
// it reported "the product's own noun" and wanted the full form of a word that
// has none. STE bans the contraction, not the possessive.
//
// Note for whoever extends these cases: keep ONE apostrophe per sentence.
// stripCode treats a single-quoted span as verbatim text, so two possessives
// blank the prose between them and the second one is never seen.
func TestPossessiveIsNotAContraction(t *testing.T) {
	possessive := []string{
		"The group's tools stay hidden.",
		"A persona's icon field identifies the row.",
		"The product's own noun stays.",
		"Read the sub-agent's report.",
	}
	for _, s := range possessive {
		for _, f := range check(Text{File: "x.go", Line: 1, What: "Description()", Body: s}) {
			if f.Rule == "contraction" {
				t.Errorf("possessive read as a contraction in %q: %s", s, f.Msg)
			}
		}
	}
}

// The other half: dropping "'s" from the always-a-contraction list must not
// blind the rule to "it's". A stem in contractionStems still fires.
func TestContractionOfIsStillFires(t *testing.T) {
	cases := []string{
		"It's the same file.",
		"That's the trap.",
		"There's one entry left.",
		"The tool reports what's missing.",
	}
	for _, s := range cases {
		var found bool
		for _, f := range check(Text{File: "x.go", Line: 1, What: "Description()", Body: s}) {
			if f.Rule == "contraction" {
				found = true
			}
		}
		if !found {
			t.Errorf("contraction rule missed %q", s)
		}
	}
}

// The suffixes that have no possessive reading must keep firing untouched.
func TestUnambiguousContractionsStillFire(t *testing.T) {
	cases := []string{
		"The group can't change in this reply.",
		"They're held by the baseline.",
		"They've run the gate already.",
		"It'll stop at the first error.",
		"The agent said it'd stop.",
	}
	for _, s := range cases {
		var found bool
		for _, f := range check(Text{File: "x.go", Line: 1, What: "Description()", Body: s}) {
			if f.Rule == "contraction" {
				found = true
			}
		}
		if !found {
			t.Errorf("contraction rule missed %q", s)
		}
	}
}

// The structure-only rule set reports the rules that keep an instruction
// unambiguous and drops the ones that police register. The same text must
// yield the same structural findings under both sets, so the tier narrows the
// report and never changes it.
func TestStructuralTierDropsRegisterRules(t *testing.T) {
	body := "This one sentence deliberately runs on and on and on past the cap that " +
		"the policy sets for a single sentence, and it can't stop."

	full := rulesOf(check(Text{File: "x.go", Line: 1, What: "Description()", Body: body}))
	if !full["sentence-length"] || !full["contraction"] {
		t.Fatalf("fixture no longer fires both classes under rulesFull: %v", full)
	}

	narrow := rulesOf(check(Text{File: "x.go", Line: 1, What: "Description()", Body: body, Rules: rulesStructure}))
	if !narrow["sentence-length"] {
		t.Errorf("structure tier dropped a structural rule: %v", narrow)
	}
	for r := range narrow {
		if ruleClasses[r] != classStructural {
			t.Errorf("structure tier reported %q, which is not structural", r)
		}
	}
}

// The vocabulary rule lives outside check(), so it needs its own proof that it
// consults the rule set.
func TestStructuralTierDropsVocabulary(t *testing.T) {
	dict := map[string]bool{"the": true}
	body := "The quixotic filibuster."
	if len(checkVocabulary(Text{File: "x.go", Body: body}, dict)) == 0 {
		t.Fatal("fixture no longer fires the vocabulary rule under rulesFull")
	}
	if fs := checkVocabulary(Text{File: "x.go", Body: body, Rules: rulesStructure}, dict); len(fs) != 0 {
		t.Errorf("structure tier reported %d vocabulary finding(s)", len(fs))
	}
}

// A rule absent from ruleClasses is invisible to the structure tier, which
// makes a forgotten classification a silently missed finding rather than a
// failure. So read the rule names out of the source and demand each one.
func TestEveryRuleIsClassified(t *testing.T) {
	src, err := os.ReadFile("rules.go")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{vocabularyRule: true} // reported by dictionary.go
	for _, m := range regexp.MustCompile(`report\("([a-z-]+)"`).FindAllStringSubmatch(string(src), -1) {
		names[m[1]] = true
	}
	if len(names) < 5 {
		t.Fatalf("only found %d rule names in rules.go — the scan has stopped working", len(names))
	}
	for n := range names {
		if ruleClasses[n] == 0 {
			t.Errorf("rule %q is reported but not classified in ruleClasses", n)
		}
	}
	for n := range ruleClasses {
		if !names[n] {
			t.Errorf("ruleClasses names %q, which no rule reports — typo, or a dead entry", n)
		}
	}
}

func rulesOf(fs []Finding) map[string]bool {
	out := map[string]bool{}
	for _, f := range fs {
		out[f.Rule] = true
	}
	return out
}

// A pinned body sits in the frozen prefix of every request, so it is enrolled.
// Absence here is the failure this test exists for: the enrollment is a
// silently-empty corpus if the collector stops finding the file.
func TestPinnedSkillBodyIsEnrolled(t *testing.T) {
	texts, err := collect(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	var seen int
	for _, tx := range texts {
		if !strings.HasPrefix(tx.What, "pinned skill body (") {
			continue
		}
		seen++
		if tx.Rules != rulesStructure {
			t.Errorf("%s:%d is enrolled under the full rule set, not the structure tier", tx.File, tx.Line)
		}
		if strings.Contains(tx.Body, "description:") {
			t.Errorf("%s:%d carries frontmatter, which the description collector already covers", tx.File, tx.Line)
		}
	}
	if seen == 0 {
		t.Fatalf("no pinned skill body enrolled, but skills.DefaultAlwaysOn names %v", skills.DefaultAlwaysOn)
	}
}

// The whole point of the structure tier is that the pinned body lands with no
// baseline. If this fails, either the body drifted or the tier did.
func TestPinnedSkillBodyIsClean(t *testing.T) {
	texts, err := collect(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, tx := range texts {
		if !strings.HasPrefix(tx.What, "pinned skill body (") {
			continue
		}
		for _, f := range check(tx) {
			t.Errorf("pinned body is not clean: %s", f)
		}
	}
}

// A name in DefaultAlwaysOn with no SKILL.md means the list and the binary have
// drifted. Skipping it would leave the lint covering nothing and reporting
// success, so the collector fails instead.
func TestPinnedSkillBodyMissingIsAnError(t *testing.T) {
	if _, err := collectPinnedSkillBodies(testsupport.TempDir(t)); err == nil {
		t.Fatal("a root with no builtin skills should be an error, not an empty corpus")
	}
}

// A finding has to point at the line a person can open, so the frontmatter the
// collector drops has to be added back as an offset.
func TestBodyAfterFrontmatterKeepsLineNumbers(t *testing.T) {
	src := "---\nname: x\ndescription: y\n---\n\nFirst prose line.\n"
	body, offset := bodyAfterFrontmatter(src)
	if offset != 4 {
		t.Fatalf("offset = %d, want 4", offset)
	}
	if strings.Contains(body, "description:") {
		t.Fatal("frontmatter survived the split")
	}
	texts := markdownProse("x/SKILL.md", body)
	if len(texts) != 1 {
		t.Fatalf("got %d prose blocks, want 1", len(texts))
	}
	if got := texts[0].Line + offset; got != 6 {
		t.Errorf("prose starts at line %d, want 6", got)
	}
}

// A file with no frontmatter comes back whole, so the collector cannot silently
// eat a body that opens with prose.
func TestBodyAfterFrontmatterPassesThroughPlainMarkdown(t *testing.T) {
	src := "# Title\n\nSome prose.\n"
	body, offset := bodyAfterFrontmatter(src)
	if body != src || offset != 0 {
		t.Errorf("plain markdown was altered: offset=%d", offset)
	}
}

// TestPlaceholderIsNotLinted: a description built by concatenation carries a
// runtime value (bash's shell name, world_note's reserved entry name). The
// placeholder that stands in for it must not read as a shouted word or pad a
// sentence's word count — it did both on the first run.
func TestPlaceholderIsNotLinted(t *testing.T) {
	fset := token.NewFileSet()
	src := `package p
type T struct{}
func name() string { return "bash" }
func (T) Description() string { return "Run a shell command with " + name() + ". The tool merges stdout and stderr." }
`
	texts, err := extractFile(fset, "p/x.go", src)
	if err != nil {
		t.Fatalf("extractFile: %v", err)
	}
	if len(texts) != 1 {
		t.Fatalf("want 1 text, got %d", len(texts))
	}
	for _, f := range check(texts[0]) {
		t.Errorf("placeholder produced a finding: %s: %s\n  %s", f.Rule, f.Msg, f.Quote)
	}
}

// TestBaselineOnlyShrinks: a fingerprint the corpus no longer produces must be
// reported, so the accepted set cannot quietly become a permanent amnesty.
func TestBaselineOnlyShrinks(t *testing.T) {
	f := Finding{Text: Text{File: "a.go"}, Rule: "sentence-length", Quote: "one two three"}
	base := map[string]bool{
		fingerprint(f): true,
		"a.go:sentence-length:deadbeefdeadbeefff": true,
	}
	fresh, stale, unjudged := applyBaseline([]Finding{f}, base, judgedRules("", true))
	if len(fresh) != 0 {
		t.Errorf("a baselined finding was reported as new: %v", fresh)
	}
	if len(stale) != 1 || stale[0] != "a.go:sentence-length:deadbeefdeadbeefff" {
		t.Errorf("stale baseline entry not reported, got %v", stale)
	}
	if len(unjudged) != 0 {
		t.Errorf("every rule ran, so nothing may be unjudged: %v", unjudged)
	}
}

// TestFingerprintIgnoresLineNumbers: the baseline must survive an edit above
// the text it holds. A fingerprint that moved with the line would churn the
// file on every unrelated change, and a baseline that churns gets regenerated
// blindly — which is how a violation sneaks in under cover of noise.
func TestFingerprintIgnoresLineNumbers(t *testing.T) {
	a := Finding{Text: Text{File: "a.go", Line: 10}, Rule: "term", Quote: "relative to cwd"}
	b := Finding{Text: Text{File: "a.go", Line: 900}, Rule: "term", Quote: "relative  to   cwd"}
	if fingerprint(a) != fingerprint(b) {
		t.Errorf("fingerprint moved with the line number or whitespace:\n  %s\n  %s", fingerprint(a), fingerprint(b))
	}
}

// TestMissingDictionaryIsNotAnError: every fresh clone and every CI runner
// lacks the word list, because we cannot ship it. If its absence ever becomes
// a failure, the gate breaks everywhere at once.
func TestMissingDictionaryIsNotAnError(t *testing.T) {
	if _, ok := loadDictionary(testsupport.TempDir(t)); ok {
		t.Error("loadDictionary reported a dictionary in an empty directory")
	}
}

// A baseline written on the one machine that HAS the word list carries
// vocabulary fingerprints. No CI runner and no fresh clone has that file, so
// the rule does not run there and those entries never fire — which the
// staleness check read as "the text improved", failed the gate on, and told the
// reader to fix by shrinking a baseline that describes text nobody looked at.
// Permanently red, on every runner, for a file that is correct.
//
// TestMissingDictionaryIsNotAnError already asserted the absence is not an
// error. It is scoped to loadDictionary, so it stayed green while the baseline
// turned that same absence into a failure one layer up.
func TestVocabularyEntriesAreNotStaleWhereTheRuleCannotRun(t *testing.T) {
	const vocab = "a.go:vocabulary:aaaaaaaaaaaa"
	base := map[string]bool{vocab: true}

	// The CI runner: no dictionary, so the rule never ran.
	_, stale, unjudged := applyBaseline(nil, base, judgedRules("", false))
	if len(stale) != 0 {
		t.Errorf("a vocabulary entry went stale on a run with no dictionary: %v\n"+
			"every CI runner is in this state, so the gate reds permanently", stale)
	}
	if len(unjudged) != 1 || unjudged[0] != vocab {
		t.Errorf("the entry should be reported unjudged, got %v", unjudged)
	}
}

// The complement, and the half that keeps the fix from being "never report
// anything": with the dictionary present the rule DID run, so the same entry
// not firing is real staleness and must still fail.
func TestVocabularyEntriesStillGoStaleWhereTheRuleDidRun(t *testing.T) {
	const vocab = "a.go:vocabulary:aaaaaaaaaaaa"
	base := map[string]bool{vocab: true}

	_, stale, unjudged := applyBaseline(nil, base, judgedRules("", true))
	if len(stale) != 1 || stale[0] != vocab {
		t.Errorf("a vocabulary entry that no longer fires on a run that CHECKED it was not "+
			"reported stale, got %v — suspension has swallowed the shrink rule", stale)
	}
	if len(unjudged) != 0 {
		t.Errorf("the rule ran, so nothing may be unjudged: %v", unjudged)
	}
}

// The second way a rule goes quiet, which the dictionary case shares a root
// with: -rule narrows the run to one rule, and every OTHER rule's entries used
// to go stale at once. `terva-ste-lint -rule term -check` could not pass.
func TestNarrowingToOneRuleDoesNotStaleTheRest(t *testing.T) {
	base := map[string]bool{
		"a.go:term:aaaaaaaaaaaa":            true,
		"a.go:sentence-length:bbbbbbbbbbbb": true,
		"a.go:passive-voice:cccccccccccc":   true,
	}

	_, stale, unjudged := applyBaseline(nil, base, judgedRules("term", true))
	if len(stale) != 1 || stale[0] != "a.go:term:aaaaaaaaaaaa" {
		t.Errorf("stale should hold only the rule that ran, got %v", stale)
	}
	if len(unjudged) != 2 {
		t.Errorf("the two rules that did not run should be unjudged, got %v", unjudged)
	}
}

// ruleOf reads the rule out of a fingerprint, and it reads from the RIGHT
// because the leftmost field is a path. A left-to-right split would name the
// directory as the rule on any path containing a colon, and would then judge
// every such entry against a rule that does not exist.
func TestRuleOfReadsPastAColonInThePath(t *testing.T) {
	for _, tc := range []struct{ fp, want string }{
		{"a.go:term:aaaaaaaaaaaa", "term"},
		{"pkg/od:d/a.go:sentence-length:bbbbbbbbbbbb", "sentence-length"},
		{fingerprint(Finding{Text: Text{File: "x/y.go"}, Rule: "vocabulary", Quote: "w"}), "vocabulary"},
		{"malformed", ""},
	} {
		if got := ruleOf(tc.fp); got != tc.want {
			t.Errorf("ruleOf(%q) = %q, want %q", tc.fp, got, tc.want)
		}
	}
}

// TestDictionaryIsNotCommitted: the whole point of the quarantine. If this
// file ever appears in the repository, the cut would carry ASD's copyrighted
// word list into a public mirror.
func TestDictionaryIsNotCommitted(t *testing.T) {
	if _, err := os.Stat(repoRoot + "/" + dictionaryPath); err == nil {
		t.Errorf("%s exists in the repository — it is copyright ASD and must stay untracked", dictionaryPath)
	}
}
