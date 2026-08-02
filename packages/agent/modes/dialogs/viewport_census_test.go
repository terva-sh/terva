package dialogs

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A dialog that stores its own scroll offset is a dialog that will clamp it its
// own way, page by its own number, and forget Home/End — which is exactly the
// nine-way drift the Viewport was written to end. Three of those nine clamped
// at len(body)-1 and could scroll a body until one line sat alone in an empty
// pane; that bug existed three times because the code did.
//
// This guard is self-enrolling: it discovers dialogs by scanning the package
// rather than listing them, so a NEW dialog with a hand-rolled offset trips it
// on the commit that adds it. A guard naming its subjects cannot fail when one
// is added (see the host-census guard for the same shape).
//
// LogDialog is the documented exception. Its `top` predates the Viewport and is
// the implementation the Viewport was modelled on — correct clamping, Home/End,
// and vim g/G on top. It is exempt by name, and shrinking this list is the only
// direction it should ever move.
var scrollFieldExempt = map[string]bool{
	"log_dialog.go": true, // `top`, the reference implementation — see above
}

// bareScrollField matches a struct field storing a scroll offset as a plain int.
var bareScrollField = regexp.MustCompile(`(?m)^\s*(scroll|offset|viewTop|top)\s+int\b`)

func TestNoDialogRollsItsOwnScrollOffset(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if name == "viewport.go" || scrollFieldExempt[name] {
			continue
		}
		checked++
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatal(err)
		}
		if m := bareScrollField.FindString(string(src)); m != "" {
			t.Errorf("%s declares its own scroll offset (%q).\n"+
				"Use the shared Viewport: it clamps once, pages by the pane height, "+
				"and answers Home/End. If this dialog genuinely cannot, add it to "+
				"scrollFieldExempt with the reason.", name, strings.TrimSpace(m))
		}
	}
	// A regexp that matched nothing because it scanned nothing would pass this
	// test forever while guarding not one line.
	if checked < 20 {
		t.Fatalf("only scanned %d dialog files; the census is not seeing the package", checked)
	}
}

// The companion: point the same regexp at a shape it MUST catch, so a regexp
// that silently stopped matching cannot leave the guard above green.
func TestTheScrollFieldRegexpHasTeeth(t *testing.T) {
	for _, bad := range []string{
		"type D struct {\n\tactive bool\n\tscroll int\n}",
		"type D struct {\n\toffset  int\n}",
		"type D struct {\n\ttop int // first visible line\n}",
	} {
		if !bareScrollField.MatchString(bad) {
			t.Errorf("regexp missed a hand-rolled offset:\n%s", bad)
		}
	}
	for _, ok := range []string{
		"type D struct {\n\tvp Viewport\n}",
		"type D struct {\n\tcursor int\n}",   // a selection is not a scroll offset
		"type D struct {\n\tselected int\n}", // ditto
	} {
		if bareScrollField.MatchString(ok) {
			t.Errorf("regexp claimed a legitimate field:\n%s", ok)
		}
	}
}

// bespokeWindow matches a dialog computing its own visible-slice bounds.
//
// CursorWindow and clampViewTop were two answers to one question — "how does the
// offset follow a cursor" — and which one a dialog got depended on which helper
// its author happened to find. The model picker re-centred on every keypress;
// the session list scrolled only near an edge; nothing recorded either as a
// decision. Both now live on the Viewport as Center and RevealPadded, named at
// the call site, and this keeps a third from growing back.
var bespokeWindow = regexp.MustCompile(`func\s+(CursorWindow|clampViewTop|cursorWindow|windowFor)\s*\(`)

func TestNoDialogGrowsItsOwnWindowHelper(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "viewport.go" {
			continue
		}
		checked++
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatal(err)
		}
		if m := bespokeWindow.FindString(string(src)); m != "" {
			t.Errorf("%s defines its own windowing helper (%q).\n"+
				"Viewport already has one: Center for a list that changes under the "+
				"cursor, RevealPadded for one you navigate. Name the policy at the "+
				"call site instead of encoding it in a helper's name.", name, strings.TrimSpace(m))
		}
	}
	if checked < 20 {
		t.Fatalf("only scanned %d dialog files; the census is not seeing the package", checked)
	}
}

func TestTheWindowHelperRegexpHasTeeth(t *testing.T) {
	for _, bad := range []string{
		"func CursorWindow(cursor, total, maxRows int) (int, int) {",
		"func clampViewTop(viewTop, cursor, window, total int) int {",
	} {
		if !bespokeWindow.MatchString(bad) {
			t.Errorf("regexp missed a bespoke windowing helper:\n%s", bad)
		}
	}
	for _, ok := range []string{
		"func (v *Viewport) Window() (start, end int) {",
		"func WindowMoreAbove(th tui.Theme, start int) string {",
	} {
		if bespokeWindow.MatchString(ok) {
			t.Errorf("regexp claimed a legitimate function:\n%s", ok)
		}
	}
}
