package modes

import (
	"testing"

	"terva.sh/terva/packages/tui"
)

// 'g' requests the OPPOSITE of the current user-disabled state.
func TestMCPDialogGlobalToggle(t *testing.T) {
	d := newMCPDialog()
	d.Open([]MCPInfo{
		{Name: "github", Scope: "global", UserDisabled: false},
		{Name: "repo", Scope: "global", UserDisabled: true},
	})

	act := d.HandleKey(rn('g'))
	if !act.ToggleGlobal || act.Name != "github" || act.On {
		t.Errorf("g on an enabled server should request disable, got %+v", act)
	}
	d.HandleKey(kind(tui.KeyDown))
	act = d.HandleKey(rn('g'))
	if !act.ToggleGlobal || act.Name != "repo" || !act.On {
		t.Errorf("g on a disabled server should request enable, got %+v", act)
	}
}

// 'p' toggles this project's disable: On = desired enabled-for-project.
func TestMCPDialogProjectToggle(t *testing.T) {
	d := newMCPDialog()
	d.Open([]MCPInfo{
		{Name: "github", Scope: "global", ProjectDisabled: false},
		{Name: "repo", Scope: "project", ProjectDisabled: true},
	})

	act := d.HandleKey(rn('p'))
	if !act.ToggleProject || act.Name != "github" || act.On {
		t.Errorf("p on a project-enabled server should request project-disable, got %+v", act)
	}
	d.HandleKey(kind(tui.KeyDown))
	act = d.HandleKey(rn('p'))
	if !act.ToggleProject || act.Name != "repo" || !act.On {
		t.Errorf("p on a project-disabled server should request project-enable, got %+v", act)
	}
}

func TestMCPDialogEscCloses(t *testing.T) {
	d := newMCPDialog()
	d.Open([]MCPInfo{{Name: "x"}})
	if act := d.HandleKey(kind(tui.KeyEsc)); !act.Close || d.Active() {
		t.Errorf("esc should close, got %+v active=%v", act, d.Active())
	}
}

// mcpStateLabel explains on/off in precedence order; "failed" vs "not
// running" distinguishes a crashed spawn from a not-yet-up server.
func TestMCPStateLabel(t *testing.T) {
	cases := []struct {
		it   MCPInfo
		want string
	}{
		{MCPInfo{UserDisabled: true}, "off (user cfg)"},
		{MCPInfo{ProjectDisabled: true}, "off (project)"},
		{MCPInfo{ProjectGated: true}, "off (untrusted)"},
		{MCPInfo{Effective: true, StartupError: "spawn: no such file"}, "failed (see log)"},
		{MCPInfo{Effective: true, Connected: false}, "off (not running)"},
		{MCPInfo{Effective: true, Connected: true, Tools: 3}, "on"},
	}
	for _, c := range cases {
		if got := mcpStateLabel(c.it); got != c.want {
			t.Errorf("mcpStateLabel(%+v) = %q, want %q", c.it, got, c.want)
		}
	}
}

func TestMCPDialogSetItemsClampsCursor(t *testing.T) {
	d := newMCPDialog()
	d.Open([]MCPInfo{{Name: "a"}, {Name: "b"}, {Name: "c"}})
	d.cursor = 2
	d.SetItems([]MCPInfo{{Name: "a"}})
	if d.cursor != 0 {
		t.Errorf("cursor should clamp into range, got %d", d.cursor)
	}
}

// 'l' opens the server log for a row that has one.
func TestMCPDialogLogKey(t *testing.T) {
	d := newMCPDialog()
	d.Open([]MCPInfo{{Name: "github", HasLog: true}, {Name: "repo", HasLog: false}})
	if act := d.HandleKey(rn('l')); !act.OpenLog || act.Name != "github" {
		t.Errorf("l on a server with a log should open it, got %+v", act)
	}
	d.HandleKey(kind(tui.KeyDown))
	if act := d.HandleKey(rn('l')); act.OpenLog {
		t.Error("l on a server without a log should not open the viewer")
	}
}
