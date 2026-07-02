package modes

import (
	"testing"

	"terva.sh/terva/packages/tui"
)

func TestStatusLinePresetName(t *testing.T) {
	if got := statusLinePresetName(nil); got != "default" {
		t.Errorf("nil rows = %q, want default", got)
	}
	if got := statusLinePresetName(statusLinePresets["compact"]); got != "compact" {
		t.Errorf("compact rows = %q", got)
	}
	if got := statusLinePresetName([][]string{{"model"}}); got != "custom" {
		t.Errorf("hand-edited rows = %q, want custom", got)
	}
}

func TestStatusRowsWithSegment(t *testing.T) {
	base := [][]string{{"cwd", "git", "model"}, {"context", "usage"}}

	// Remove strips everywhere and never mutates the input.
	got := statusRowsWithSegment(base, "git", false, 0)
	if statusRowsContain(got, "git") || !statusRowsContain(base, "git") {
		t.Fatalf("remove: got %v (base %v)", got, base)
	}

	// Add lands on the natural row.
	got = statusRowsWithSegment(base, "swarm", true, 1)
	if len(got) != 2 || got[1][len(got[1])-1] != "swarm" {
		t.Fatalf("add to row 1: got %v", got)
	}

	// Adding to a row that doesn't exist grows the layout.
	got = statusRowsWithSegment(base, "clock", true, 2)
	if len(got) != 3 || got[2][0] != "clock" {
		t.Fatalf("add to new row: got %v", got)
	}

	// Adding an already-present segment is a no-op.
	got = statusRowsWithSegment(base, "git", true, 0)
	if !statusRowsEqual(got, base) {
		t.Fatalf("re-add changed the layout: %v", got)
	}

	// Removing the last segment of a row drops the row.
	got = statusRowsWithSegment([][]string{{"model"}, {"clock"}}, "clock", false, 2)
	if len(got) != 1 {
		t.Fatalf("empty row should drop: %v", got)
	}
}

// The toggles operate on the effective layout: explicit config when
// set, the built-in defaults otherwise.
func TestApplyStatusSegmentToggleFromDefaults(t *testing.T) {
	i := NewInteractive(InteractiveConfig{Theme: tui.Dark, SettingsStore: &fakeSettingsStore{}})

	// Defaults include git; toggling it off materializes an explicit
	// rows config without it.
	i.applyStatusSegmentToggle("git", false)
	if statusRowsContain(i.cfg.StatusLineRows, "git") {
		t.Fatalf("git still present: %v", i.cfg.StatusLineRows)
	}
	if len(i.cfg.StatusLineRows) == 0 {
		t.Fatal("toggle should materialize an explicit rows config")
	}

	// Clock is config-only; toggling it on adds it to the ambient row.
	i.applyStatusSegmentToggle("clock", true)
	if !statusRowsContain(i.cfg.StatusLineRows, "clock") {
		t.Fatalf("clock missing: %v", i.cfg.StatusLineRows)
	}
}

func TestApplyStatusLinePreset(t *testing.T) {
	i := NewInteractive(InteractiveConfig{Theme: tui.Dark, SettingsStore: &fakeSettingsStore{}})
	i.applyStatusLinePreset("compact")
	if statusLinePresetName(i.cfg.StatusLineRows) != "compact" {
		t.Fatalf("preset not applied: %v", i.cfg.StatusLineRows)
	}
	i.applyStatusLinePreset("default")
	if i.cfg.StatusLineRows != nil {
		t.Fatalf("default should clear the rows config: %v", i.cfg.StatusLineRows)
	}
	// "custom" is informational: it must not clobber a hand layout.
	i.cfg.StatusLineRows = [][]string{{"model"}}
	i.applyStatusLinePreset("custom")
	if !statusRowsEqual(i.cfg.StatusLineRows, [][]string{{"model"}}) {
		t.Fatalf("custom clobbered the layout: %v", i.cfg.StatusLineRows)
	}
}

// Preset names in the picker resolve against tui segment IDs so the
// canned layouts can't drift from the registry.
func TestStatusLinePresetsUseKnownSegments(t *testing.T) {
	known := map[string]bool{}
	for _, row := range tui.DefaultStatusRows(false) {
		for _, s := range row {
			known[s] = true
		}
	}
	for _, extra := range []string{"session", "clock", "usage", "tags", "bridge", "ext"} {
		known[extra] = true
	}
	for name, preset := range statusLinePresets {
		for _, row := range preset {
			for _, s := range row {
				if !known[s] {
					t.Errorf("preset %s references unknown segment %q", name, s)
				}
			}
		}
	}
}
