package i18n

import (
	"strings"
	"testing"
)

type upper struct{}

func (upper) T(source string, args ...any) string { return strings.ToUpper(format(source, args)) }
func (upper) P(key, english string, args ...any) string {
	return key + ":" + format(english, args)
}

func TestEnglishWithoutATranslator(t *testing.T) {
	Use(nil)
	if got := T("100% done"); got != "100% done" {
		t.Errorf("T with no args must return the source untouched, got %q", got)
	}
	if got := T("%d files", 3); got != "3 files" {
		t.Errorf("T with args must format, got %q", got)
	}
	if got := P("prompts.x", "keep %s", "this"); got != "keep this" {
		t.Errorf("P must fall back to the English default, got %q", got)
	}
	if got := Errorf("bad %q", "x").Error(); got != `bad "x"` {
		t.Errorf("Errorf = %q", got)
	}
}

func TestUseInstallsAndRemovesATranslator(t *testing.T) {
	t.Cleanup(func() { Use(nil) })
	Use(upper{})
	if got := T("%d files", 2); got != "2 FILES" {
		t.Errorf("T must route through the installed translator, got %q", got)
	}
	if got := P("prompts.x", "keep"); got != "prompts.x:keep" {
		t.Errorf("P must route through the installed translator, got %q", got)
	}
	if got := Errorf("no").Error(); got != "NO" {
		t.Errorf("Errorf must route through T, got %q", got)
	}
	Use(nil)
	if got := T("no"); got != "no" {
		t.Errorf("Use(nil) must restore English, got %q", got)
	}
}

// In renders through the translator it was given, whatever Use installed, and
// through the process-wide one when given nil. The zero Text is In(nil).
func TestInRendersThroughItsOwnTranslator(t *testing.T) {
	t.Cleanup(func() { Use(nil) })
	Use(upper{})
	own := In(prefix("fi:"))
	if got := own.T("%d files", 2); got != "fi:2 files" {
		t.Errorf("In(tr).T = %q, want tr's rendering", got)
	}
	if got := own.P("prompts.x", "keep"); got != "fi:prompts.x=keep" {
		t.Errorf("In(tr).P = %q, want tr's rendering", got)
	}
	if got := own.Errorf("no").Error(); got != "fi:no" {
		t.Errorf("In(tr).Errorf = %q, want tr's rendering", got)
	}
	for name, x := range map[string]Text{"In(nil)": In(nil), "the zero Text": {}} {
		if got := x.T("no"); got != "NO" {
			t.Errorf("%s.T = %q, want the process-wide translator's rendering", name, got)
		}
	}
}

// prefix is a translator that marks its output with a language tag.
type prefix string

func (p prefix) T(source string, args ...any) string { return string(p) + format(source, args) }
func (p prefix) P(key, english string, args ...any) string {
	return string(p) + key + "=" + format(english, args)
}
