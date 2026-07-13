package modes

import (
	"errors"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/tui"
)

type fakeSettingsStore struct{}

func (f *fakeSettingsStore) SetInlineImages(bool) error         { return nil }
func (f *fakeSettingsStore) SetRecursiveFileSuggest(bool) error { return nil }
func (f *fakeSettingsStore) SetRespectGitignore(bool) error     { return nil }
func (f *fakeSettingsStore) SetTheme(string) error              { return nil }
func (f *fakeSettingsStore) SetStatusLineRows([][]string) error { return nil }

// newApprovalTestInteractive wires an Interactive onto the fake carrier — the
// shipping seam. The gate lives daemon-side: the TUI reads the settings and
// permissions surfaces and pushes changes back as surface actions, never
// touching a *core.Agent or a *core.ConfirmGate.
func newApprovalTestInteractive(c *fakeCarrier) *Interactive {
	return &Interactive{
		turns: newTurnEngine(),
		dirty: make(chan struct{}, 1),
		cfg: InteractiveConfig{
			Carrier:        c,
			CarrierSession: "s1",
			SettingsStore:  &fakeSettingsStore{},
			Theme:          tui.Dark,
		},
	}
}

// findSettingsItem returns the dialog row for key — a test helper for asserting
// on the generic settings rows.
func findSettingsItem(items []dialogs.SettingsItem, key string) (dialogs.SettingsItem, bool) {
	for _, it := range items {
		if it.Key == key {
			return it, true
		}
	}
	return dialogs.SettingsItem{}, false
}

// The generic settings rows reflect the daemon surface: the approval picker's
// selected option is the mode the surface reports, and a bool item round-trips.
func TestDaemonSettingsReflectSurface(t *testing.T) {
	i := newApprovalTestInteractive(newFakeCarrier())

	items := i.daemonSettingsItems()
	approval, ok := findSettingsItem(items, "approval")
	if !ok {
		t.Fatal("want an approval row when the settings surface carries one")
	}
	if got := approval.Options[approval.Choice].Value; got != "workspace" {
		t.Errorf("picker choice = %q, want workspace (the surface's mode)", got)
	}
	lazy, ok := findSettingsItem(items, "lazy_tools")
	if !ok {
		t.Fatal("want the lazy_tools bool row from the settings surface")
	}
	if !lazy.Value {
		t.Error("lazy_tools row should be checked (surface value true)")
	}
}

// An unreadable settings surface yields no rows — the TUI has no local settings
// to fall back on when the daemon is gone.
func TestDaemonSettingsAbsentWhenSurfaceUnavailable(t *testing.T) {
	c := newFakeCarrier()
	c.surfErr = errors.New("daemon gone")
	i := newApprovalTestInteractive(c)

	if items := i.daemonSettingsItems(); len(items) != 0 {
		t.Errorf("unreadable settings surface → no rows, got %d", len(items))
	}
}

// Switching the mode pushes a settings surface action, which is where the
// daemon swaps enforcement on the gate and rebuilds the session's tool set.
// Session-only: the picker must never write the persistent default (the store
// has no approval field precisely because nothing should call it). Invalid
// values can't reach here — the picker only offers the daemon's options, and
// the daemon validates — so no local validation is duplicated.
func TestApplyApprovalModeSendsSurfaceAction(t *testing.T) {
	c := newFakeCarrier()
	i := newApprovalTestInteractive(c)

	i.applyApprovalMode("plan")

	select {
	case act := <-c.surfActs:
		if act.id != "settings" || act.action != "set" ||
			act.args["key"] != "approval" || act.args["value"] != "plan" {
			t.Fatalf("surface action = %+v, want settings/set approval=plan", act)
		}
	default:
		t.Fatal("applyApprovalMode sent no surface action")
	}
}

// The permissions inspector paints the daemon's wire view: mode, rules grouped
// by source, and this session's revocable grants.
func TestBuildPermissionsViewRendersWireView(t *testing.T) {
	i := newApprovalTestInteractive(newFakeCarrier())

	info, grants := i.buildPermissionsView()
	text := strings.Join(info, "\n")

	for _, want := range []string{"approval mode", "safe", "[user]", "bash", "deny", "no deletes", "this session"} {
		if !strings.Contains(text, want) {
			t.Errorf("permissions view missing %q\n---\n%s", want, text)
		}
	}
	// The fixture grants allow-all plus one tool; both surface as revocable rows.
	if len(grants) != 2 {
		t.Errorf("grants = %+v, want allow-all + write", grants)
	}
}

func TestBuildPermissionsViewSurfaceUnavailable(t *testing.T) {
	c := newFakeCarrier()
	c.surfErr = errors.New("daemon gone")
	i := newApprovalTestInteractive(c)

	info, _ := i.buildPermissionsView()
	if !strings.Contains(strings.Join(info, "\n"), "daemon gone") {
		t.Errorf("an unreadable permissions surface should surface the error, got %v", info)
	}
}
