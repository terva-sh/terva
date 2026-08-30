package config

import (
	"path"
	"strings"
	"testing"
)

// Why globMatch exists at all, asserted rather than asserted in a comment.
//
// The obvious "simplification" of this file is to delete globMatch and call
// path.Match. This test is here to fail the moment somebody tries: path.Match
// treats "/" as a separator that "*" may not cross, so on OpenRouter's nested
// ids the natural rule matches NOTHING. That failure is silent — models stay
// visible, no error is raised — which is exactly the kind of bug that survives
// review.
func TestPathMatchWouldSilentlyFailWhichIsWhyGlobMatchExists(t *testing.T) {
	const key = "openrouter/anthropic/claude-sonnet-4.5"

	if ok, err := path.Match("openrouter/*", key); ok || err != nil {
		t.Fatalf("path.Match(\"openrouter/*\", %q) = %v (err %v).\n"+
			"If this now matches, the standard library changed and this file's\n"+
			"whole justification needs re-reading.", key, ok, err)
	}
	if !globMatch("openrouter/*", key) {
		t.Error("globMatch must match where path.Match cannot: that is its entire job")
	}
}

// The case that decides the whole matcher. OpenRouter ids are themselves
// "vendor/model", so the key carries TWO slashes — and "openrouter/*" is the
// most obvious rule anyone will ever write for it. path.Match will not cross a
// "/", so a path-shaped matcher silently matches none of them: the operator
// hides a provider, nothing disappears, and there is no error to explain it.
func TestWildcardCrossesSlashesForNestedOpenRouterIDs(t *testing.T) {
	v := NewModelVisibility([]string{"openrouter/*"})

	for _, id := range []string{
		"anthropic/claude-sonnet-4.5",
		"meta-llama/llama-3.1-70b-instruct",
		"deepseek/deepseek-r1",
	} {
		if !v.Hidden("openrouter", id) {
			t.Errorf("openrouter/* must hide the nested id %q; a path.Match-style\n"+
				"matcher fails exactly here, because * refuses to cross the '/'", id)
		}
	}
	// It stays scoped to the provider it names.
	if v.Hidden("anthropic", "claude-sonnet-4.5") {
		t.Error("openrouter/* must not touch the anthropic provider")
	}
}

// Last match wins is the reason the list is ordered rather than a set: it is
// what lets "hide the lot, keep these" be expressed at all.
func TestLastMatchWinsAllowsBroadHideWithRescues(t *testing.T) {
	v := NewModelVisibility([]string{
		"openrouter/*",
		"!openrouter/anthropic/claude-*",
		"openrouter/anthropic/claude-2*",
	})

	cases := []struct {
		id         string
		wantHidden bool
		why        string
	}{
		{"meta-llama/llama-3.1-70b", true, "swept up by the broad first rule"},
		{"anthropic/claude-sonnet-4.5", false, "rescued by the ! rule after it"},
		{"anthropic/claude-2.1", true, "re-hidden by the narrower rule last"},
	}
	for _, tc := range cases {
		if got := v.Hidden("openrouter", tc.id); got != tc.wantHidden {
			t.Errorf("Hidden(openrouter/%s) = %v, want %v (%s)", tc.id, got, tc.wantHidden, tc.why)
		}
	}
}

// A missing model is a confusing absence unless the picker can say what caused
// it, so the deciding rule comes back with the verdict.
func TestHiddenByReportsTheDecidingRuleAsWritten(t *testing.T) {
	v := NewModelVisibility([]string{"openrouter/*", "!openrouter/anthropic/claude-*"})

	hidden, rule := v.HiddenBy("openrouter", "deepseek/deepseek-r1")
	if !hidden || rule != "openrouter/*" {
		t.Errorf("HiddenBy = (%v, %q), want (true, \"openrouter/*\")", hidden, rule)
	}
	// A rescued model reports the rule that rescued it, not the one that would
	// have hidden it: the answer to "why is this visible" is the ! rule.
	hidden, rule = v.HiddenBy("openrouter", "anthropic/claude-sonnet-4.5")
	if hidden || rule != "!openrouter/anthropic/claude-*" {
		t.Errorf("HiddenBy = (%v, %q), want (false, the ! rule)", hidden, rule)
	}
	// Nothing matched at all: no verdict, no rule.
	if hidden, rule = v.HiddenBy("anthropic", "claude-sonnet-4.5"); hidden || rule != "" {
		t.Errorf("unmatched model = (%v, %q), want (false, \"\")", hidden, rule)
	}
}

func TestVisibilityDefaultsToVisibleAndReportsEmpty(t *testing.T) {
	v := NewModelVisibility(nil)
	if !v.Empty() {
		t.Error("no rules should report Empty, so callers can skip the whole path")
	}
	if v.Hidden("openrouter", "anthropic/claude-sonnet-4.5") {
		t.Error("with no rules every model must be visible")
	}
	// Blank and bare-"!" entries are inert rather than patterns that match
	// nothing confusingly — hand-edited JSON grows these.
	if !NewModelVisibility([]string{"", "  ", "!"}).Empty() {
		t.Error("blank and bare-! entries must not compile into rules")
	}
}

func TestMatchingIgnoresCase(t *testing.T) {
	v := NewModelVisibility([]string{"OpenRouter/Anthropic/Claude-*"})
	if !v.Hidden("openrouter", "anthropic/claude-sonnet-4.5") {
		t.Error("rules should match case-insensitively; ids and providers are\n" +
			"lowercase by convention but a hand-written rule need not be")
	}
}

// Hiding a model writes an exact rule; showing it again leaves no trace, so the
// common change-of-mind does not accumulate cruft in the config.
func TestToggleHideThenShowLeavesNoTrace(t *testing.T) {
	rules := ToggleHiddenModel(nil, "openrouter", "deepseek/deepseek-r1", true)
	if len(rules) != 1 || rules[0] != "openrouter/deepseek/deepseek-r1" {
		t.Fatalf("hide wrote %v, want one exact rule", rules)
	}
	if !NewModelVisibility(rules).Hidden("openrouter", "deepseek/deepseek-r1") {
		t.Fatal("the rule it wrote does not hide the model it names")
	}
	if rules = ToggleHiddenModel(rules, "openrouter", "deepseek/deepseek-r1", false); rules != nil {
		t.Errorf("showing it again should empty the list, got %v", rules)
	}
}

// The case answer 3 of the design turns on: a model swept up by a pattern must
// be rescuable on its own, and deleting a rule cannot do it — the pattern is
// there for the two hundred others.
func TestToggleRescuesAModelCoveredByAPattern(t *testing.T) {
	rules := []string{"openrouter/*"}
	rules = ToggleHiddenModel(rules, "openrouter", "anthropic/claude-sonnet-4.5", false)

	if len(rules) != 2 || rules[0] != "openrouter/*" {
		t.Fatalf("the operator's pattern must survive untouched, got %v", rules)
	}
	if rules[1] != "!openrouter/anthropic/claude-sonnet-4.5" {
		t.Fatalf("rescue should append an explicit ! rule, got %q", rules[1])
	}
	v := NewModelVisibility(rules)
	if v.Hidden("openrouter", "anthropic/claude-sonnet-4.5") {
		t.Error("the rescued model must be visible")
	}
	if !v.Hidden("openrouter", "deepseek/deepseek-r1") {
		t.Error("rescuing one model must not un-hide the rest of the pattern")
	}
}

// Flipping the same model repeatedly must not grow the list — the toggle owns
// its own exact rules and rewrites them rather than stacking them.
func TestToggleIsIdempotentAndDoesNotAccumulate(t *testing.T) {
	rules := []string{"openrouter/*"}
	for range 5 {
		rules = ToggleHiddenModel(rules, "openrouter", "anthropic/claude-sonnet-4.5", false)
		rules = ToggleHiddenModel(rules, "openrouter", "anthropic/claude-sonnet-4.5", true)
	}
	if len(rules) != 1 {
		t.Fatalf("ten flips left %d rules (%v), want just the operator's pattern:\n"+
			"the final state is 'hidden', which the pattern already says", len(rules), rules)
	}

	// Asking for the state it is already in changes nothing at all.
	before := []string{"openrouter/*"}
	after := ToggleHiddenModel(before, "openrouter", "deepseek/deepseek-r1", true)
	if len(after) != 1 || after[0] != "openrouter/*" {
		t.Errorf("hiding an already-hidden model should be a no-op, got %v", after)
	}
}

func TestToggleDoesNotDisturbOtherModelsRules(t *testing.T) {
	rules := []string{"openrouter/a/one", "openrouter/b/two"}
	got := ToggleHiddenModel(rules, "openrouter", "a/one", false)
	if len(got) != 1 || got[0] != "openrouter/b/two" {
		t.Errorf("un-hiding a/one should drop only its own rule, got %v", got)
	}
}

func TestGlobMatchEdges(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"*", "openrouter/anything/at/all", true},
		{"", "", true},
		{"", "x", false},
		{"exact", "exact", true},
		{"exact", "exacts", false},
		{"*claude*", "openrouter/anthropic/claude-sonnet", true},
		{"*claude*", "openrouter/meta/llama", false},
		{"openrouter/*-4.5", "openrouter/anthropic/claude-sonnet-4.5", true},
		{"a**b", "ab", true},
		{"*/*/*", "openrouter/anthropic/claude", true},
		// Trailing wildcards must be free to match nothing.
		{"openrouter/deepseek*", "openrouter/deepseek", true},
	}
	for _, tc := range cases {
		if got := globMatch(tc.pattern, tc.s); got != tc.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tc.pattern, tc.s, got, tc.want)
		}
	}
}

// The two-pointer matcher exists so a pathological pattern cannot hang the
// picker. A naive recursive one goes exponential on this input; if this test
// ever stops returning promptly, the algorithm regressed.
func TestGlobMatchDoesNotBlowUpOnPathologicalPatterns(t *testing.T) {
	pattern := strings.Repeat("*a", 24) + "b"
	s := strings.Repeat("a", 400)
	if globMatch(pattern, s) {
		t.Error("no 'b' in the subject, so this cannot match")
	}
}
