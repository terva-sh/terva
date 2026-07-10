package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/tui"
)

func TestListPickerSelectAndCancel(t *testing.T) {
	p := &listPicker{}
	if p.open("t", "hint", nil) {
		t.Fatal("open with no items must refuse")
	}
	items := []pickerItem{
		{label: "alpha", value: "a"},
		{label: "beta", value: "b", hint: "extra"},
		{label: "gamma", value: "c"},
	}
	if !p.open("t", "hint", items) || !p.isActive() {
		t.Fatal("open failed")
	}

	// Bounds: Up at the top stays put; Down walks; Down at the
	// bottom stays put.
	p.handleKey(tui.Key{Kind: tui.KeyUp})
	if p.cursor != 0 {
		t.Fatalf("cursor = %d after Up at top", p.cursor)
	}
	p.handleKey(tui.Key{Kind: tui.KeyDown})
	p.handleKey(tui.Key{Kind: tui.KeyDown})
	p.handleKey(tui.Key{Kind: tui.KeyDown})
	if p.cursor != 2 {
		t.Fatalf("cursor = %d after Down x3", p.cursor)
	}

	act := p.handleKey(tui.Key{Kind: tui.KeyEnter})
	if !act.Select || act.Value != "c" {
		t.Fatalf("enter action = %+v", act)
	}
	if p.isActive() {
		t.Fatal("picker should close on select")
	}

	p.open("t", "hint", items)
	act = p.handleKey(tui.Key{Kind: tui.KeyEsc})
	if !act.Close || p.isActive() {
		t.Fatalf("esc action = %+v active=%v", act, p.isActive())
	}
}

func TestListPickerRenderShape(t *testing.T) {
	p := &listPicker{}
	p.open("title", "the hint", []pickerItem{
		{label: "alpha", value: "a", hint: "h"},
		{label: "beta", value: "b"},
	})
	lines := p.render(tui.Dark, 60)
	// header + hint + 2 rows + rule
	if len(lines) != 5 {
		t.Fatalf("render returned %d lines:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[0], "title") || !strings.Contains(lines[1], "the hint") {
		t.Fatalf("header/hint missing:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[2], "alpha") || !strings.Contains(lines[2], "(h)") {
		t.Fatalf("row 0 = %q", lines[2])
	}
}

func TestCursorWindow(t *testing.T) {
	cases := []struct {
		cursor, total, maxRows int
		wantStart, wantEnd     int
	}{
		{0, 5, 10, 0, 5},   // fits: whole list
		{0, 30, 10, 0, 10}, // top
		{15, 30, 10, 10, 20},
		{29, 30, 10, 20, 30}, // bottom clamp
		{4, 30, 10, 0, 10},   // near top clamp
		{0, 0, 10, 0, 0},     // empty
		{3, 7, 0, 0, 7},      // no cap
	}
	for _, c := range cases {
		s, e := CursorWindow(c.cursor, c.total, c.maxRows)
		if s != c.wantStart || e != c.wantEnd {
			t.Errorf("cursorWindow(%d,%d,%d) = (%d,%d), want (%d,%d)",
				c.cursor, c.total, c.maxRows, s, e, c.wantStart, c.wantEnd)
		}
		if c.maxRows > 0 && c.total > 0 && (c.cursor < s || c.cursor >= e) {
			t.Errorf("cursorWindow(%d,%d,%d): cursor outside window [%d,%d)",
				c.cursor, c.total, c.maxRows, s, e)
		}
	}
}
