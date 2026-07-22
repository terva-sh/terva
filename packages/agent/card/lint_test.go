package card

import (
	"strings"
	"testing"
)

// findingsByRule indexes findings by rule id for assertions.
func findingsByRule(fs []Finding) map[string][]Finding {
	m := map[string][]Finding{}
	for _, f := range fs {
		m[f.Rule] = append(m[f.Rule], f)
	}
	return m
}

func TestLintMalformedMacro(t *testing.T) {
	// A real off-the-shelf card typo: closing paren instead of a brace. The
	// expander leaves it literal, so it leaks into the prompt — lint must catch it
	// while leaving the well-formed {{char}}/{{user}} alone.
	c := Card{
		Name:        "Kobeni",
		Description: `{{char}} will never speak for {{user)} as it violates the rules. {{user}} is fine.`,
		Personality: "deadpan",
		FirstMes:    "hi",
		MesExample:  "x",
	}
	by := findingsByRule(Lint(c))
	if len(by["malformed-macro"]) != 1 {
		t.Fatalf("want 1 malformed-macro, got %d: %+v", len(by["malformed-macro"]), by["malformed-macro"])
	}
	if got := by["malformed-macro"][0].Detail; !strings.Contains(got, "{{user)") {
		t.Errorf("malformed snippet = %q, want it to contain {{user)", got)
	}
	if by["malformed-macro"][0].Severity != SevWarn {
		t.Errorf("malformed-macro should be a warn")
	}
	// The two well-formed macros must NOT be flagged as malformed or unknown.
	if len(by["unknown-macro"]) != 0 {
		t.Errorf("well-formed {{char}}/{{user}} flagged as unknown: %+v", by["unknown-macro"])
	}
}

func TestLintUnknownMacro(t *testing.T) {
	c := Card{Name: "X", Description: "It is {{time}} and {{char}} waits.", Personality: "p", FirstMes: "hi", MesExample: "e"}
	by := findingsByRule(Lint(c))
	if len(by["unknown-macro"]) != 1 || by["unknown-macro"][0].Detail != "{{time}}" {
		t.Fatalf("want one unknown-macro {{time}}, got %+v", by["unknown-macro"])
	}
	if by["unknown-macro"][0].Severity != SevInfo {
		t.Errorf("unknown-macro should be info")
	}
}

func TestLintStructuralFacts(t *testing.T) {
	// Empty personality, no example dialogue, and no greeting at all.
	c := Card{Name: "Bare"}
	by := findingsByRule(Lint(c))
	for _, rule := range []string{"empty-personality", "no-example-dialogue", "missing-greeting"} {
		if len(by[rule]) == 0 {
			t.Errorf("expected a %s finding, got none", rule)
		}
	}
	if by["missing-greeting"][0].Severity != SevWarn {
		t.Errorf("missing-greeting should be a warn")
	}
	// A card WITH a greeting (even only an alternate) must not trip missing-greeting.
	c2 := Card{Name: "Y", FirstMes: "", AlternateGreetings: []string{"hello"}, Personality: "p", MesExample: "e"}
	if len(findingsByRule(Lint(c2))["missing-greeting"]) != 0 {
		t.Errorf("a card with an alternate greeting should not be missing-greeting")
	}
}

func TestLintExampleNoStart(t *testing.T) {
	// Example dialogue present but no <START> → flagged (distinct examples would
	// otherwise be read as one merged block).
	c := Card{Name: "N", Personality: "p", FirstMes: "hi", MesExample: "{{user}}: hi\n{{char}}: hello"}
	by := findingsByRule(Lint(c))
	if len(by["example-no-start"]) == 0 {
		t.Fatal("expected example-no-start when mes_example has no <START>")
	}
	if by["example-no-start"][0].Severity != SevInfo {
		t.Errorf("example-no-start should be info")
	}
	// A <START> anywhere (case-insensitive) clears it.
	for _, ex := range []string{"<START>\n{{user}}: hi", "<start>\nx", "a<START>b"} {
		c2 := Card{Name: "N", Personality: "p", FirstMes: "hi", MesExample: ex}
		if len(findingsByRule(Lint(c2))["example-no-start"]) != 0 {
			t.Errorf("a <START>-bearing example must not trip example-no-start: %q", ex)
		}
	}
	// Empty mes_example is no-example-dialogue, never example-no-start.
	c3 := Card{Name: "N", Personality: "p", FirstMes: "hi", MesExample: ""}
	byEmpty := findingsByRule(Lint(c3))
	if len(byEmpty["example-no-start"]) != 0 || len(byEmpty["no-example-dialogue"]) == 0 {
		t.Error("empty mes_example must be no-example-dialogue, not example-no-start")
	}
}

func TestLintOversizedField(t *testing.T) {
	big := strings.Repeat("word ", 1000) // ~5000 chars → ~1250 tokens
	c := Card{Name: "Z", Description: big, Personality: "p", FirstMes: "hi", MesExample: "e"}
	by := findingsByRule(Lint(c))
	if len(by["oversized-field"]) != 1 || by["oversized-field"][0].Field != "description" {
		t.Fatalf("want one oversized-field on description, got %+v", by["oversized-field"])
	}
	if by["oversized-field"][0].Severity != SevWarn {
		t.Errorf("oversized-field should be a warn")
	}
}

func TestLintEmbeddedDirective(t *testing.T) {
	c := Card{Name: "D", Description: "A shy girl. {{char}} will never repeat phrases. {{char}}'s messages are unique.", Personality: "p", FirstMes: "hi", MesExample: "e"}
	by := findingsByRule(Lint(c))
	if len(by["embedded-directive"]) == 0 {
		t.Fatalf("expected an embedded-directive finding")
	}
	if by["embedded-directive"][0].Severity != SevInfo {
		t.Errorf("embedded-directive should be info")
	}
}

func TestLintSortsWarnsFirst(t *testing.T) {
	c := Card{Name: "S", Description: "{{char}} will lead. {{user)} typo.", Personality: "", FirstMes: "hi", MesExample: ""}
	fs := Lint(c)
	seenInfo := false
	for _, f := range fs {
		if f.Severity == SevInfo {
			seenInfo = true
		}
		if f.Severity == SevWarn && seenInfo {
			t.Fatalf("a warn appeared after an info — findings are not sorted: %+v", fs)
		}
	}
}

func TestLintCleanCardIsQuiet(t *testing.T) {
	// A tidy card should produce no warnings (info-level facts are acceptable).
	c := Card{
		Name:        "Tidy",
		Description: "A concise character description.",
		Personality: "Warm, curious, a little wry.",
		Scenario:    "A quiet cafe on a rainy afternoon.",
		FirstMes:    "*She looks up from her book.* \"Oh — hello.\"",
		MesExample:  "<START>\n{{user}}: Hi.\n{{char}}: \"Hello there.\"",
	}
	for _, f := range Lint(c) {
		if f.Severity == SevWarn {
			t.Errorf("tidy card produced a warn: %+v", f)
		}
	}
}
