package dialogs

import (
	"testing"

	"terva.sh/terva/packages/tui"
)

// 'g' requests the OPPOSITE of the current manifest-enabled state.
func TestExtensionsDialogGlobalToggle(t *testing.T) {
	d := NewExtensionsDialog()
	d.Open([]ExtInfo{
		{Name: "memory", Scope: "global", GlobalEnabled: true, Running: true},
		{Name: "index", Scope: "global", GlobalEnabled: false},
	})

	act := d.HandleKey(rn('g'))
	if !act.ToggleGlobal || act.Name != "memory" || act.On {
		t.Errorf("g on an enabled ext should request disable, got %+v", act)
	}
	d.HandleKey(kind(tui.KeyDown))
	act = d.HandleKey(rn('g'))
	if !act.ToggleGlobal || act.Name != "index" || !act.On {
		t.Errorf("g on a disabled ext should request enable, got %+v", act)
	}
}

// 'p' toggles this project's disable: On = desired enabled-for-project.
func TestExtensionsDialogProjectToggle(t *testing.T) {
	d := NewExtensionsDialog()
	d.Open([]ExtInfo{
		{Name: "memory", Scope: "global", GlobalEnabled: true, ProjectDisabled: false},
		{Name: "index", Scope: "global", GlobalEnabled: true, ProjectDisabled: true},
	})

	act := d.HandleKey(rn('p'))
	if !act.ToggleProject || act.Name != "memory" || act.On {
		t.Errorf("p on a project-enabled ext should request project-disable, got %+v", act)
	}
	d.HandleKey(kind(tui.KeyDown))
	act = d.HandleKey(rn('p'))
	if !act.ToggleProject || act.Name != "index" || !act.On {
		t.Errorf("p on a project-disabled ext should request project-enable, got %+v", act)
	}
}

func TestExtensionsDialogEscCloses(t *testing.T) {
	d := NewExtensionsDialog()
	d.Open([]ExtInfo{{Name: "x"}})
	if act := d.HandleKey(kind(tui.KeyEsc)); !act.Close || d.Active() {
		t.Errorf("esc should close, got %+v active=%v", act, d.Active())
	}
}

// stateLabel explains on/off in precedence order; "not running" flags an
// enabled-but-crashed extension.
func TestExtensionsStateLabel(t *testing.T) {
	cases := []struct {
		it   ExtInfo
		want string
	}{
		{ExtInfo{GlobalEnabled: false}, "off (disabled)"},
		{ExtInfo{GlobalEnabled: true, UserConfigDisabled: true}, "off (user cfg)"},
		{ExtInfo{GlobalEnabled: true, ProjectDisabled: true}, "off (project)"},
		{ExtInfo{GlobalEnabled: true, ProjectGated: true}, "off (untrusted)"},
		{ExtInfo{GlobalEnabled: true, Running: false}, "off (not running)"},
		{ExtInfo{GlobalEnabled: true, Running: true}, "on"},
	}
	for _, c := range cases {
		if got := stateLabel(c.it); got != c.want {
			t.Errorf("stateLabel(%+v) = %q, want %q", c.it, got, c.want)
		}
	}
}

func TestExtensionsDialogSetItemsClampsCursor(t *testing.T) {
	d := NewExtensionsDialog()
	d.Open([]ExtInfo{{Name: "a"}, {Name: "b"}, {Name: "c"}})
	d.cursor = 2
	d.SetItems([]ExtInfo{{Name: "a"}})
	if d.cursor != 0 {
		t.Errorf("cursor should clamp into range, got %d", d.cursor)
	}
}

// 'l' opens the log for a row that has one; otherwise it's a no-op note.
func TestExtensionsDialogLogKey(t *testing.T) {
	d := NewExtensionsDialog()
	d.Open([]ExtInfo{{Name: "a", HasLog: true}, {Name: "b", HasLog: false}})
	if act := d.HandleKey(rn('l')); !act.OpenLog || act.Name != "a" {
		t.Errorf("l on a row with a log should open it, got %+v", act)
	}
	d.HandleKey(kind(tui.KeyDown))
	if act := d.HandleKey(rn('l')); act.OpenLog {
		t.Error("l on a row without a log should not open the viewer")
	}
}
