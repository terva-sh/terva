package dialogs

import (
	"fmt"
	"testing"

	"terva.sh/terva/packages/tui"
)

func TestLogDialogOpensAtBottomAndScrolls(t *testing.T) {
	d := NewLogDialog()
	lines := make([]string, 50)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i)
	}
	d.Open("log · x", lines)

	if d.top != d.maxTop() || d.top != 50-logViewRows {
		t.Fatalf("should open scrolled to the bottom, top=%d maxTop=%d", d.top, d.maxTop())
	}
	d.HandleKey(kind(tui.KeyHome))
	if d.top != 0 {
		t.Errorf("Home should scroll to top, top=%d", d.top)
	}
	d.HandleKey(kind(tui.KeyPageDown))
	if d.top != logViewRows {
		t.Errorf("PageDown should advance one page, top=%d", d.top)
	}
	d.HandleKey(kind(tui.KeyUp))
	if d.top != logViewRows-1 {
		t.Errorf("Up should move one line, top=%d", d.top)
	}
	d.HandleKey(kind(tui.KeyEnd))
	if d.top != d.maxTop() {
		t.Errorf("End should scroll to bottom, top=%d", d.top)
	}
	if !d.HandleKey(kind(tui.KeyEsc)) || d.Active() {
		t.Error("esc should close")
	}
}

func TestLogDialogTopClampsAtZero(t *testing.T) {
	d := NewLogDialog()
	d.Open("x", []string{"a", "b"}) // fewer than a page
	if d.top != 0 {
		t.Fatalf("short log should sit at top, got %d", d.top)
	}
	d.HandleKey(kind(tui.KeyUp))
	if d.top != 0 {
		t.Errorf("top must not go negative, got %d", d.top)
	}
}

func TestLogDialogEmpty(t *testing.T) {
	d := NewLogDialog()
	d.Open("x", nil)
	if len(d.lines) != 1 || d.lines[0] != "(log is empty)" {
		t.Errorf("empty log should show a placeholder, got %v", d.lines)
	}
}
