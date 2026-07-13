package modes

// /settings support for the status bar: a layout preset picker plus
// per-segment show/hide toggles. Both persist through one seam — the
// full rows value via SettingsStore.SetStatusLineRows — so the config
// file stays the single source of truth and hand-edited layouts
// coexist with the dialog (they surface as the "custom" preset).

import (
	"slices"
	"strings"

	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// statusLinePresets are the picker's canned layouts. "default" is
// deliberately absent — it persists as nil so the built-in per-mode
// defaults (and future changes to them) keep applying.
var statusLinePresets = map[string][][]string{
	"compact": {
		{"cwd", "git", "model", "context", "usage"},
	},
	"detailed": {
		{"cwd", "git", "edits", "model", "thinking", "tokens", "cost"},
		{"context", "usage", "swarm"},
		{"session", "clock", "tags", "tasks", "bridge", "ext"},
	},
}

// statusToggleSegments is the curated set exposed as /settings
// toggles, with each segment's natural row in a three-row layout so
// toggling one on lands it somewhere sensible.
var statusToggleSegments = []struct {
	seg  string
	row  int
	desc string
}{
	{"git", 0, "branch, dirty marker, and +/- line counts vs HEAD"},
	{"edits", 0, "lines the agent's edit/write tools changed this session"},
	{"thinking", 0, "the model's reasoning level"},
	{"swarm", 1, "live background agent count"},
	{"tasks", 2, "the built-in task board's current task"},
	{"session", 2, "the session file's short name"},
	{"clock", 2, "a 24h wall clock"},
}

// statusLinePresetName classifies the current rows config for the
// picker: nil = default, a preset match by value, anything else custom.
func statusLinePresetName(rows [][]string) string {
	if len(rows) == 0 {
		return "default"
	}
	for name, preset := range statusLinePresets {
		if statusRowsEqual(rows, preset) {
			return name
		}
	}
	return "custom"
}

func statusRowsEqual(a, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !slices.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

// effectiveStatusRows is what the bar currently renders from: the
// explicit config, or the built-in defaults for this mode.
func (i *Interactive) effectiveStatusRows() [][]string {
	if len(i.cfg.StatusLineRows) > 0 {
		return i.cfg.StatusLineRows
	}
	return tui.DefaultStatusRows(i.cfg.Experience != "")
}

func statusRowsContain(rows [][]string, seg string) bool {
	for _, row := range rows {
		if slices.Contains(row, seg) {
			return true
		}
	}
	return false
}

// statusRowsWithSegment returns a deep copy of rows with seg present
// or absent. Adding places the segment at the end of its natural row
// (growing the layout when the row doesn't exist yet); removing
// strips it everywhere and drops rows that empty out.
func statusRowsWithSegment(rows [][]string, seg string, on bool, naturalRow int) [][]string {
	out := make([][]string, 0, len(rows)+1)
	for _, row := range rows {
		kept := make([]string, 0, len(row))
		for _, s := range row {
			if !on && s == seg {
				continue
			}
			kept = append(kept, s)
		}
		if len(kept) > 0 {
			out = append(out, kept)
		}
	}
	if on && !statusRowsContain(out, seg) {
		for len(out) <= naturalRow {
			out = append(out, nil)
		}
		out[naturalRow] = append(out[naturalRow], seg)
	}
	return out
}

// applyStatusLinePreset handles the /settings picker: persist the
// preset's rows (nil for default) and apply live.
func (i *Interactive) applyStatusLinePreset(value string) {
	defer func() {
		if i.rend != nil {
			i.rend.Clear()
		}
		i.invalidate()
	}()
	if value == "custom" {
		return // informational choice: keep the hand-edited layout
	}
	rows := statusLinePresets[value] // nil for "default"
	i.persistStatusRows(rows, "status line: "+value)
}

// applyStatusSegmentToggle handles one segment's show/hide toggle on
// top of whatever layout is in effect.
func (i *Interactive) applyStatusSegmentToggle(seg string, on bool) {
	naturalRow := 0
	for _, t := range statusToggleSegments {
		if t.seg == seg {
			naturalRow = t.row
			break
		}
	}
	rows := statusRowsWithSegment(i.effectiveStatusRows(), seg, on, naturalRow)
	i.persistStatusRows(rows, "status "+seg+" "+onOff(on))
}

func (i *Interactive) persistStatusRows(rows [][]string, note string) {
	if i.cfg.SettingsStore != nil {
		if err := i.cfg.SettingsStore.SetStatusLineRows(rows); err != nil {
			i.mu.Lock()
			i.statusErr = i18n.T("settings: %s", err.Error())
			i.mu.Unlock()
			return
		}
	}
	i.mu.Lock()
	i.cfg.StatusLineRows = rows
	i.statusOK = note
	i.statusErr = ""
	i.mu.Unlock()
}

// statusLineSettingsItems builds the /settings entries: the preset
// picker plus the curated segment toggles reflecting the effective
// layout.
func (i *Interactive) statusLineSettingsItems() []dialogs.SettingsItem {
	preset := statusLinePresetName(i.cfg.StatusLineRows)
	options := []dialogs.SettingsOption{
		{Value: "default", Label: "default", Desc: "built-in three-row layout (adapts to chat/play)"},
		{Value: "compact", Label: "compact", Desc: "one row: cwd, git, model, context, usage"},
		{Value: "detailed", Label: "detailed", Desc: "three rows including edits, swarm, session, clock"},
	}
	if preset == "custom" {
		options = append(options, dialogs.SettingsOption{Value: "custom", Label: "custom", Desc: "hand-edited status_line.rows in config.json"})
	}
	choice := 0
	for idx, opt := range options {
		if opt.Value == preset {
			choice = idx
		}
	}
	items := []dialogs.SettingsItem{{
		Key:     "status_line",
		Label:   "status line",
		Desc:    "segment layout preset; fine-tune rows in config.json (status_line.rows)",
		Options: options,
		Choice:  choice,
	}}
	effective := i.effectiveStatusRows()
	for _, t := range statusToggleSegments {
		items = append(items, dialogs.SettingsItem{
			Key:   "statusseg_" + t.seg,
			Label: "status: " + t.seg,
			Desc:  t.desc,
			Value: statusRowsContain(effective, t.seg),
		})
	}
	return items
}

// dispatchStatusSegmentToggle routes "statusseg_*" toggle keys; returns
// false for keys it doesn't own.
func (i *Interactive) dispatchStatusSegmentToggle(key string, value bool) bool {
	seg, ok := strings.CutPrefix(key, "statusseg_")
	if !ok {
		return false
	}
	i.applyStatusSegmentToggle(seg, value)
	return true
}
