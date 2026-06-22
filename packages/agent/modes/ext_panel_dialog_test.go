package modes

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"

	"terva.sh/terva/packages/tui"
)

// A panel row wider than the panel wraps across rows instead of running off
// the right edge (the /memory overflow), the selection highlight is kept on
// every wrapped segment of a selected entry, and blank separators survive.
func TestExtPanelWrapsLongLines(t *testing.T) {
	th := tui.Theme{Muted: 8, Tool: 2, Accent: 4, SelectionFG: 7, SelectionBG: 4}
	sel := th.SelectionStyle()

	d := newExtPanelDialog()
	// One logical entry far wider than the panel, marked selected via the
	// zero-width-space sentinel the renderer recognizes.
	longEntry := "​   1. " + strings.Repeat("alpha beta ", 12)
	d.Open("memory", "p", "Memory", []string{
		"  USER — cross-project",
		longEntry,
		"", // blank separator between sections must survive
		"  PROJECT — this repo",
		"    (none)",
	}, "↑/↓ select · esc close")

	const width = 40
	out := d.Render(th, width)

	// No emitted row overflows the panel width.
	for _, row := range out {
		if w := runewidth.StringWidth(stripANSIBytes(row)); w > width {
			t.Errorf("row exceeds width %d (visible=%d): %q", width, w, stripANSIBytes(row))
		}
	}

	// The long entry wrapped into more than one row, and each carries the
	// selection highlight (not just the first).
	selRows := 0
	for _, row := range out {
		if strings.Contains(stripANSIBytes(row), "alpha") {
			if !strings.Contains(row, sel) {
				t.Errorf("wrapped selected row missing highlight: %q", stripANSIBytes(row))
			}
			selRows++
		}
	}
	if selRows < 2 {
		t.Fatalf("expected the long selected entry to wrap into >=2 rows, got %d", selRows)
	}

	// The blank separator survived wrapping.
	blank := false
	for _, row := range out {
		if strings.TrimSpace(stripANSIBytes(row)) == "" {
			blank = true
		}
	}
	if !blank {
		t.Error("blank separator row was dropped")
	}
}
