package modes

import (
	"errors"
	"strings"
	"testing"

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

// The picker reflects the mode the daemon reports on the settings surface.
func TestApprovalSettingItemReflectsSurfaceMode(t *testing.T) {
	i := newApprovalTestInteractive(newFakeCarrier())

	item, ok := i.approvalSettingItem()
	if !ok {
		t.Fatal("want an approval item when the settings surface carries one")
	}
	if item.Key != "approval_mode" {
		t.Fatalf("item key = %q", item.Key)
	}
	if got := item.Options[item.Choice].Value; got != "workspace" {
		t.Errorf("picker choice = %q, want workspace (the surface's mode)", got)
	}
}

// An unreadable settings surface means no picker — the TUI has no local gate to
// fall back on.
func TestApprovalSettingItemAbsentWhenSurfaceUnavailable(t *testing.T) {
	c := newFakeCarrier()
	c.surfErr = errors.New("daemon gone")
	i := newApprovalTestInteractive(c)

	if _, ok := i.approvalSettingItem(); ok {
		t.Error("unreadable settings surface → no approval picker")
	}
}

// Switching the mode pushes a settings surface action, which is where the
// daemon swaps enforcement on the gate and rebuilds the session's tool set.
// Session-only: the picker must never write the persistent default (the store
// has no approval field precisely because nothing should call it).
func TestApplyApprovalModeSendsSurfaceAction(t *testing.T) {
	c := newFakeCarrier()
	i := newApprovalTestInteractive(c)

	i.applyApprovalModeSetting("plan")

	select {
	case act := <-c.surfActs:
		if act.id != "settings" || act.action != "set" ||
			act.args["key"] != "approval" || act.args["value"] != "plan" {
			t.Fatalf("surface action = %+v, want settings/set approval=plan", act)
		}
	default:
		t.Fatal("applyApprovalModeSetting sent no surface action")
	}
}

func TestApplyApprovalModeRejectsBadValue(t *testing.T) {
	c := newFakeCarrier()
	i := newApprovalTestInteractive(c)

	i.applyApprovalModeSetting("bogus")

	select {
	case act := <-c.surfActs:
		t.Fatalf("an invalid mode must not switch anything; got %+v", act)
	default:
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
