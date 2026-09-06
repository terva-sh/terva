package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/tui"
)

// groupedFixture is the shape the daemon emits: declared groups in display
// order, items ordered group by group, and one long description that wraps.
func groupedFixture() ([]SettingsGroup, []SettingsItem) {
	groups := []SettingsGroup{
		{ID: "security", Label: "Security & trust", Desc: "who may run what, and whether this workspace is trusted"},
		{ID: "context", Label: "Context & prompt", Desc: "what rides the prompt each turn"},
	}
	items := []SettingsItem{
		{Key: "approval", Label: "Approval mode", Group: "security",
			Options: []SettingsOption{{Value: "ask", Label: "ask"}, {Value: "yolo", Label: "yolo"}},
			Desc:    "How tool calls are gated for this session."},
		{Key: "trust", Label: "Trust this workspace", Group: "security",
			Desc: "Load project extensions, skills, and context files from this directory."},
		{Key: "lazy_tools", Label: "Lazy tool loading", Group: "context",
			Desc: "Trim the tool schemas that fill context every turn, and let the model activate a group when it needs one. The saving is largest on a long session."},
	}
	return groups, items
}

// Level 1 is the categories, and only the categories. The whole point of the
// two-level pane is that ~45 item rows are not on screen at once.
func TestSettingsOpensOnTheCategoryPicker(t *testing.T) {
	d := NewSettingsDialog()
	d.Open(groupedFixture())
	out := settingsPlain(d, 80)

	for _, want := range []string{"Security & trust", "Context & prompt", "(2)", "(1)"} {
		if !strings.Contains(out, want) {
			t.Errorf("category picker missing %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"Approval mode", "Lazy tool loading", "[ ]", "[→]"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("category picker leaked the item row %q:\n%s", unwanted, out)
		}
	}
}

// Enter descends into the category under the cursor; esc comes back to it,
// with the level-1 cursor where it was rather than reset to the top.
func TestSettingsEscUnwindsOneLevelKeepingTheCategoryCursor(t *testing.T) {
	d := NewSettingsDialog()
	d.Open(groupedFixture())

	d.HandleKey(tui.Key{Kind: tui.KeyDown}) // onto "Context & prompt"
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if !d.inGroup {
		t.Fatal("enter did not descend into the category")
	}
	if out := settingsPlain(d, 80); !strings.Contains(out, "Lazy tool loading") {
		t.Errorf("descended into the wrong category:\n%s", out)
	}

	act := d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if act.Close {
		t.Fatal("esc closed the dialog instead of going back a level")
	}
	if d.inGroup {
		t.Fatal("esc did not return to the category picker")
	}
	if d.groupCursor != 1 {
		t.Errorf("category cursor = %d, want 1 (where it was left)", d.groupCursor)
	}
	// A second esc closes.
	if act := d.HandleKey(tui.Key{Kind: tui.KeyEsc}); !act.Close {
		t.Error("a second esc should close the dialog")
	}
}

// One category is not a choice. The picker would cost a keypress and show a
// single row, so the dialog opens on the items and esc closes from there. This
// is also what a daemon that predates groups produces.
func TestSettingsSingleCategorySkipsThePicker(t *testing.T) {
	d := NewSettingsDialog()
	d.Open(nil, []SettingsItem{
		{Key: "a", Label: "first", Desc: "what a does"},
		{Key: "b", Label: "second", Desc: "what b does"},
	})
	if !d.inGroup {
		t.Fatal("a single category should open straight onto its items")
	}
	if out := settingsPlain(d, 80); !strings.Contains(out, "first") {
		t.Errorf("items not rendered:\n%s", out)
	}
	if act := d.HandleKey(tui.Key{Kind: tui.KeyEsc}); !act.Close {
		t.Error("esc should close when there is no level to go back to")
	}
}

// An item naming no group, or a group the view never declared, has to end up
// somewhere a person can see. A silently dropped row is the failure mode this
// bucket exists to prevent.
func TestSettingsUndeclaredGroupFallsIntoTheTrailingBucket(t *testing.T) {
	groups, items := groupedFixture()
	items = append(items,
		SettingsItem{Key: "orphan", Label: "Orphaned row", Group: "nonexistent"},
		SettingsItem{Key: "nameless", Label: "Ungrouped row"},
	)
	d := NewSettingsDialog()
	d.Open(groups, items)

	if got := len(d.groups); got != 3 {
		t.Fatalf("groups = %d, want 3 (two declared + the trailing bucket)", got)
	}
	bucket := d.groups[len(d.groups)-1]
	if bucket.ID != "" {
		t.Errorf("the trailing bucket should carry no id, got %q", bucket.ID)
	}
	if got := len(d.member[len(d.member)-1]); got != 2 {
		t.Errorf("bucket holds %d items, want both strays", got)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEnd})
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	out := settingsPlain(d, 80)
	for _, want := range []string{"Orphaned row", "Ungrouped row"} {
		if !strings.Contains(out, want) {
			t.Errorf("stray item %q is not reachable:\n%s", want, out)
		}
	}
}

// The focus rule: the row you are on shows its whole description, every other
// row shows one line. Without it the long descriptions cost three rows each on
// every frame, which is what pushed the pane past the terminal.
func TestSettingsOnlyTheFocusedItemShowsItsWholeDescription(t *testing.T) {
	groups, items := groupedFixture()
	d := NewSettingsDialog()
	d.Open(groups, items)
	d.HandleKey(tui.Key{Kind: tui.KeyDown}) // "Context & prompt"
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})

	// lazy_tools is the only item in that group, so it is focused: the tail of
	// its description must be on screen.
	if out := settingsPlain(d, 60); !strings.Contains(out, "long session") {
		t.Errorf("the focused item lost the end of its description:\n%s", out)
	}

	// In the security group the cursor is on the first row, so trust's
	// description is truncated to one line with an ellipsis.
	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	d.HandleKey(tui.Key{Kind: tui.KeyHome})
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	out := settingsPlain(d, 60)
	if !strings.Contains(out, "…") {
		t.Errorf("an unfocused description should be elided:\n%s", out)
	}
	// "from this directory" opens the second wrapped line, so it is on screen
	// only when the whole description is. Matching a phrase that spans the
	// wrap point would pass whatever the renderer did.
	if strings.Contains(out, "from this directory") {
		t.Errorf("an unfocused item printed its whole description:\n%s", out)
	}
	// Moving onto it reveals the rest.
	d.HandleKey(tui.Key{Kind: tui.KeyDown})
	if out := settingsPlain(d, 60); !strings.Contains(out, "from this directory") {
		t.Errorf("the description did not open when the row took focus:\n%s", out)
	}
}

// The frame rule is drawn to exactly `width`, so a row wider than it spills
// past the border. That holds on the category picker too, at every width.
func TestSettingsCategoryPickerFitsWidth(t *testing.T) {
	for _, width := range []int{120, 100, 80, 60, 40} {
		d := NewSettingsDialog()
		d.Open(groupedFixture())
		got, worst := widestLine(d.Render(tui.Dark, width))
		if got > width {
			t.Errorf("width %d: a category line painted %d cells: %q", width, got, ansiRE.ReplaceAllString(worst, ""))
		}
	}
}

// Same guard one level down, where the elided descriptions are built by hand
// out of a wrapped line plus an ellipsis.
func TestSettingsGroupedItemsFitWidth(t *testing.T) {
	for _, width := range []int{120, 100, 80, 60, 40} {
		d := NewSettingsDialog()
		d.Open(groupedFixture())
		d.HandleKey(tui.Key{Kind: tui.KeyEnter})
		got, worst := widestLine(d.Render(tui.Dark, width))
		if got > width {
			t.Errorf("width %d: an item line painted %d cells: %q", width, got, ansiRE.ReplaceAllString(worst, ""))
		}
	}
}

// A parent toggle adds its conditional children, so the index the cursor held
// now means a different row. Reopen restores by (group, key) instead.
func TestSettingsReopenKeepsThePositionWhenRowsAppear(t *testing.T) {
	groups := []SettingsGroup{
		{ID: "security", Label: "Security & trust"},
		{ID: "agents", Label: "Delegation & panels"},
	}
	before := []SettingsItem{
		{Key: "approval", Label: "Approval mode", Group: "security"},
		{Key: "auto_swarm", Label: "Background sub-agents", Group: "agents"},
		{Key: "swarm_worktrees", Label: "Sub-agent worktrees", Group: "agents"},
	}
	d := NewSettingsDialog()
	d.Open(groups, before)
	d.HandleKey(tui.Key{Kind: tui.KeyDown})  // the agents category
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // into it
	d.HandleKey(tui.Key{Kind: tui.KeyDown})  // onto swarm_worktrees

	// auto_swarm went on: two children appear directly after their parent,
	// which is exactly where the old cursor index pointed.
	after := []SettingsItem{
		{Key: "approval", Label: "Approval mode", Group: "security"},
		{Key: "auto_swarm", Label: "Background sub-agents", Group: "agents"},
		{Key: "auto_swarm_nudge", Label: "Nudge on idle", Group: "agents"},
		{Key: "external_workers", Label: "External workers", Group: "agents"},
		{Key: "swarm_worktrees", Label: "Sub-agent worktrees", Group: "agents"},
	}
	d.Reopen(groups, after)

	if !d.inGroup {
		t.Error("reopen dropped back to the category picker")
	}
	if d.groups[d.groupCursor].ID != "agents" {
		t.Errorf("category = %q, want agents", d.groups[d.groupCursor].ID)
	}
	it, ok := d.currentItem()
	if !ok || it.Key != "swarm_worktrees" {
		t.Errorf("cursor landed on %+v, want swarm_worktrees", it)
	}
}

// The other half: the row under the cursor is the one that went away. The
// cursor has to land somewhere valid in the same group rather than off the end.
func TestSettingsReopenClampsWhenTheCursorRowDisappears(t *testing.T) {
	groups := []SettingsGroup{{ID: "agents", Label: "Delegation & panels"}}
	d := NewSettingsDialog()
	d.Open(groups, []SettingsItem{
		{Key: "auto_swarm", Label: "Background sub-agents", Group: "agents"},
		{Key: "auto_swarm_nudge", Label: "Nudge on idle", Group: "agents"},
	})
	d.HandleKey(tui.Key{Kind: tui.KeyDown}) // onto the conditional child

	d.Reopen(groups, []SettingsItem{
		{Key: "auto_swarm", Label: "Background sub-agents", Group: "agents"},
	})

	it, ok := d.currentItem()
	if !ok {
		t.Fatal("the cursor is not on any item after the row under it vanished")
	}
	if it.Key != "auto_swarm" {
		t.Errorf("cursor on %q, want the surviving row", it.Key)
	}
}

// A toggle writes back to the right item even though the cursor is an index
// into one group rather than into the flat list.
func TestSettingsToggleInASecondGroupHitsTheRightItem(t *testing.T) {
	groups, items := groupedFixture()
	d := NewSettingsDialog()
	d.Open(groups, items)
	d.HandleKey(tui.Key{Kind: tui.KeyDown})  // "Context & prompt"
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // into it

	act := d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if !act.Toggle || act.Key != "lazy_tools" || !act.Value {
		t.Fatalf("toggle = %+v, want lazy_tools set true", act)
	}
}

// The option list had no window at all, and the theme picker already lists
// more themes than a short terminal shows.
func TestSettingsOptionListWindowsAndFollowsItsCursor(t *testing.T) {
	options := make([]SettingsOption, 0, 20)
	for _, name := range []string{"auto", "dark", "light", "solarized", "gruvbox", "nord", "dracula",
		"monokai", "ayu", "rose-pine", "catppuccin", "tokyo-night", "everforest", "kanagawa"} {
		options = append(options, SettingsOption{Value: name, Label: name})
	}
	d := NewSettingsDialog()
	d.Open(nil, []SettingsItem{{
		Key: "theme", Label: "color theme", Options: options,
		Desc: "choose a theme from $TERVA_HOME/themes or a loaded extension",
	}})
	d.MaxRows = 8
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // open the option view

	out := settingsPlain(d, 80)
	if !strings.Contains(out, "more below") {
		t.Errorf("a 14-option list in an 8-row pane must advertise the remainder:\n%s", out)
	}
	if strings.Contains(out, "kanagawa") {
		t.Errorf("the whole option list was emitted unwindowed:\n%s", out)
	}

	d.HandleKey(tui.Key{Kind: tui.KeyEnd})
	out = settingsPlain(d, 80)
	if !strings.Contains(out, "kanagawa") {
		t.Errorf("the option window did not follow its cursor to the end:\n%s", out)
	}
	if strings.Contains(out, "more below") {
		t.Errorf("the cursor is on the last option, nothing is below it:\n%s", out)
	}
}

// Esc in the option view goes back to the items, not out of the dialog: it is
// the third level of the same stack.
func TestSettingsEscFromOptionsReturnsToTheItems(t *testing.T) {
	groups, items := groupedFixture()
	d := NewSettingsDialog()
	d.Open(groups, items)
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // into security
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // approval is an enum: option view
	if !d.selecting {
		t.Fatal("enter on an enum should open the option view")
	}

	if act := d.HandleKey(tui.Key{Kind: tui.KeyEsc}); act.Close {
		t.Fatal("esc from the option view closed the whole dialog")
	}
	if d.selecting || !d.inGroup {
		t.Errorf("esc should land back on the item list (selecting=%v inGroup=%v)", d.selecting, d.inGroup)
	}
}
