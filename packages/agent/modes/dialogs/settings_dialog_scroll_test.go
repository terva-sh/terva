package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/tui"
)

// scrollFixture is a list long enough to overflow a capped pane, whose LAST
// item carries a wrapped description — the shape that trapped the real
// dialog: "status: clock" was reachable but its description line was not.
func scrollFixture() []SettingsItem {
	items := make([]SettingsItem, 0, 8)
	for _, k := range []string{"one", "two", "three", "four", "five", "six", "seven"} {
		items = append(items, SettingsItem{
			Key: k, Label: "setting " + k,
			Desc: "what setting " + k + " does",
		})
	}
	items = append(items, SettingsItem{
		Key: "clock", Label: "status: clock",
		Desc: "the wall clock in the status line",
	})
	return items
}

func settingsPlain(d *SettingsDialog, width int) string {
	return ansiRE.ReplaceAllString(strings.Join(d.Render(tui.Dark, width), "\n"), "")
}

// The cursor item's whole block — row plus description — must be revealed,
// not just its row. With the cursor on the last item there is nothing below
// the block, so nothing may remain hidden: revealing only the row left the
// final description as a permanent, unreachable "1 more below".
func TestSettingsCursorOnLastItemRevealsItsDescription(t *testing.T) {
	d := NewSettingsDialog()
	d.Open(nil, scrollFixture())
	d.MaxRows = 8
	for range d.items {
		d.HandleKey(tui.Key{Kind: tui.KeyDown})
	}
	out := settingsPlain(d, 80)
	if !strings.Contains(out, "the wall clock in the status line") {
		t.Errorf("last item's description not revealed:\n%s", out)
	}
	if strings.Contains(out, "more below") {
		t.Errorf("cursor at the last item still claims more below:\n%s", out)
	}
}

// End jumps the cursor to the last item and Home back to the first — the
// escape hatch that also cures a stuck bottom in one keypress.
func TestSettingsHomeEndJumpCursor(t *testing.T) {
	d := NewSettingsDialog()
	d.Open(nil, scrollFixture())
	d.MaxRows = 8
	d.HandleKey(tui.Key{Kind: tui.KeyEnd})
	if d.cursor != len(d.items)-1 {
		t.Errorf("End: cursor = %d, want %d", d.cursor, len(d.items)-1)
	}
	if out := settingsPlain(d, 80); !strings.Contains(out, "the wall clock in the status line") {
		t.Errorf("End did not reveal the last description:\n%s", out)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyHome})
	if d.cursor != 0 {
		t.Errorf("Home: cursor = %d, want 0", d.cursor)
	}
}

// The wheel carries the same intent as ↑/↓ (viewport.go binds it there for
// body-scrolling dialogs); in a cursor-driven list it moves the cursor.
func TestSettingsMouseWheelMovesCursor(t *testing.T) {
	d := NewSettingsDialog()
	d.Open(nil, scrollFixture())
	d.HandleKey(tui.Key{Kind: tui.KeyMouseWheelDown})
	d.HandleKey(tui.Key{Kind: tui.KeyMouseWheelDown})
	if d.cursor != 2 {
		t.Errorf("two wheel-down events: cursor = %d, want 2", d.cursor)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyMouseWheelUp})
	if d.cursor != 1 {
		t.Errorf("wheel-up: cursor = %d, want 1", d.cursor)
	}
}

// A block taller than the pane cannot show whole; the item row (the
// selectable line) must win the tie-break and stay visible.
func TestSettingsOversizedBlockKeepsItemRowVisible(t *testing.T) {
	long := strings.Repeat("a very long description that wraps and wraps ", 12)
	d := NewSettingsDialog()
	d.Open(nil, []SettingsItem{
		{Key: "a", Label: "first", Desc: "short"},
		{Key: "b", Label: "the oversized one", Desc: long},
		{Key: "c", Label: "last", Desc: "short"},
	})
	d.MaxRows = 5
	d.HandleKey(tui.Key{Kind: tui.KeyDown})
	if out := settingsPlain(d, 60); !strings.Contains(out, "the oversized one") {
		t.Errorf("item row scrolled out under its own description:\n%s", out)
	}
}
