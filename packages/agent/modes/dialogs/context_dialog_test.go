package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/tui"
)

func TestContextDialogTabSwitchAndClose(t *testing.T) {
	d := NewContextDialog()
	d.Open("sid", "/path/sid.session", []string{"over1", "over2"}, []string{"ext1"})
	if !d.Active() {
		t.Fatal("should be active after Open")
	}
	if got := d.body(); len(got) != 2 || got[0] != "over1" {
		t.Fatalf("overview body = %v; want the overview slice", got)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyTab})
	if got := d.body(); len(got) != 1 || got[0] != "ext1" {
		t.Fatalf("after Tab, body = %v; want the extensions slice", got)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyTab}) // 2 tabs -> wraps back to overview
	if got := d.body(); got[0] != "over1" {
		t.Fatalf("Tab should wrap back to overview, got %v", got)
	}
	if closed := d.HandleKey(tui.Key{Kind: tui.KeyEsc}); !closed || d.Active() {
		t.Fatal("esc should close the dialog")
	}
}

func TestContextDialogWrapsLongLines(t *testing.T) {
	long := strings.Repeat("alpha beta ", 30) // ~330 chars on one logical line
	d := NewContextDialog()
	d.Open("", "", []string{long, "", "short"}, nil)

	wrapped := d.wrappedBody(40) // limit = width-2 = 38
	if len(wrapped) < 2 {
		t.Fatalf("a %d-char line should wrap into multiple rows at width 40, got %d rows", len(long), len(wrapped))
	}
	for _, l := range wrapped {
		if len(l) > 38 { // ASCII body: byte len == display width
			t.Fatalf("wrapped row exceeds the 38-col limit: %d in %q", len(l), l)
		}
	}
	// The blank separator line survives wrapping as a blank line.
	blanks := 0
	for _, l := range wrapped {
		if l == "" {
			blanks++
		}
	}
	if blanks == 0 {
		t.Error("blank separator line was dropped by wrapping")
	}
}

// A long colored line must keep its color on EVERY wrapped row, not just the
// first — each rendered row resets SGR independently, so the continuation
// pieces need the leading escape re-applied.
func TestContextDialogWrapKeepsColor(t *testing.T) {
	var th tui.Theme
	colorOpen := strings.SplitN(th.FG256(244, "X"), "X", 2)[0] // the FG escape, no text
	colored := th.FG256(244, strings.Repeat("word ", 40))      // one long grey logical line

	d := NewContextDialog()
	d.Open("", "", []string{colored}, nil)
	wrapped := d.wrappedBody(40)
	if len(wrapped) < 2 {
		t.Fatalf("expected the long line to wrap into multiple rows, got %d", len(wrapped))
	}
	// Every wrapped row must carry the colour, not just the first (keep-style).
	for i, l := range wrapped {
		if !strings.Contains(l, colorOpen) {
			t.Errorf("wrapped row %d lost its colour (%q): %q", i, colorOpen, l)
		}
	}
}

func TestContextDialogScrollClamp(t *testing.T) {
	body := make([]string, 50)
	d := NewContextDialog()
	d.Open("", "", body, nil)

	// Scroll up at the top stays at 0.
	d.HandleKey(tui.Key{Kind: tui.KeyUp})
	if d.scroll != 0 {
		t.Fatalf("scroll up at top = %d; want 0", d.scroll)
	}
	// Render clamps an over-scroll to the last full page.
	d.scroll = 1000
	_ = d.Render(tui.Theme{}, 80)
	if want := len(body) - contextBodyRows; d.scroll != want {
		t.Fatalf("scroll clamped to %d; want %d", d.scroll, want)
	}
}
