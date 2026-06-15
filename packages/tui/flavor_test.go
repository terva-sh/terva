package tui

import (
	"strings"
	"testing"
)

func TestFillFlavor(t *testing.T) {
	verbs := []string{"harness", "wrangle"}
	nouns := []string{"wildcard", "chaos"}

	// always pick index 0
	zero := func(int) int { return 0 }
	cases := []struct {
		name string
		in   string
		pick func(int) int
		want string
	}{
		{"plain string passes through", "thinking", zero, "thinking"},
		{"verb and noun", "{verb} the {noun}.", zero, "harness the wildcard."},
		{"only noun", "wrangling the {noun}", zero, "wrangling the wildcard"},
		{"unknown token left verbatim", "{adj} {noun}", zero, "{adj} wildcard"},
		{"adjacent tokens", "{verb}{noun}", zero, "harnesswildcard"},
	}
	for _, c := range cases {
		if got := fillFlavor(c.in, verbs, nouns, c.pick); got != c.want {
			t.Errorf("%s: fillFlavor(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}

	// Each occurrence is chosen independently (the picker advances).
	seq := []int{0, 1}
	i := 0
	next := func(int) int { v := seq[i%len(seq)]; i++; return v }
	if got := fillFlavor("{noun} and {noun}", verbs, nouns, next); got != "wildcard and chaos" {
		t.Errorf("independent picks: got %q, want %q", got, "wildcard and chaos")
	}
}

func TestFillFlavorEmptyPoolLeavesToken(t *testing.T) {
	// An empty pool must leave the placeholder verbatim, never panic or
	// emit an empty slot.
	if got := fillFlavor("{verb} the {noun}", nil, []string{"chaos"}, func(int) int { return 0 }); got != "{verb} the chaos" {
		t.Errorf("empty verb pool: got %q, want %q", got, "{verb} the chaos")
	}
}

func TestThemeGreetingFillsAndNeverLeaksTokens(t *testing.T) {
	// The default theme is fully configured; Greeting must always return a
	// non-empty, fully-filled tagline with no leftover placeholders.
	for i := 0; i < 50; i++ {
		g := Dark.Greeting()
		if g == "" {
			t.Fatal("Greeting returned empty")
		}
		if strings.ContainsAny(g, "{}") {
			t.Fatalf("Greeting leaked a placeholder: %q", g)
		}
	}
}

func TestThemeGreetingFallsBackWithoutTemplates(t *testing.T) {
	th := Dark
	th.Greetings = nil
	if g := th.Greeting(); g == "" || strings.ContainsAny(g, "{}") {
		t.Errorf("no-template fallback should be a plain non-empty line, got %q", g)
	}
}
