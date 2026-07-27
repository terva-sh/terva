package workspace

import (
	"testing"

	"terva.sh/terva/packages/agent/config"
)

// Until now the only way to see or change Workspace Trust from a terminal was
// to remember that `/trust` exists. The web panel has had a labelled toggle in
// its workspace drawer since trust shipped; the TUI's own settings and
// permissions panes said nothing at all, so a restricted workspace looked
// identical to a trusted one from inside the panes that exist to answer
// "what may this session do".
//
// Both panes are generic views of a wire surface, so putting trust on the
// surface is what puts it in front of every client at once.

func TestSettingsPaneOffersTheTrustToggle(t *testing.T) {
	w, s := trustSession(t)

	it := findSetting(s.settingsView(), "trust")
	if it == nil {
		t.Fatalf("no trust row in the settings view: %+v", s.settingsView().Items)
	}
	if it.Type != "bool" {
		t.Errorf("trust row type = %q, want bool", it.Type)
	}
	if it.Value != "false" {
		t.Errorf("trust row starts at %q, want false for an untrusted temp dir", it.Value)
	}

	// Toggling it is the same act as /trust: persisted, and live.
	if err := s.settingsAction("set", map[string]string{"key": "trust", "value": "true"}); err != nil {
		t.Fatalf("set trust=true: %v", err)
	}
	if it := findSetting(s.settingsView(), "trust"); it == nil || it.Value != "true" {
		t.Errorf("the pane does not read back the change: %+v", it)
	}
	if !w.Trusted() {
		t.Error("the toggle did not move the workspace verdict")
	}
	if !spawnGate(t, s) {
		t.Error("the toggle did not reach swarm_spawn's gate")
	}
	store, err := config.LoadTrustStore()
	if err != nil {
		t.Fatalf("load trust store: %v", err)
	}
	if ok, _ := store.IsTrusted(w.cwd); !ok {
		t.Error("the toggle did not persist — the directory is untrusted again on the next launch")
	}

	// And back off, so the pane is a real toggle rather than a one-way door.
	if err := s.settingsAction("set", map[string]string{"key": "trust", "value": "false"}); err != nil {
		t.Fatalf("set trust=false: %v", err)
	}
	if w.Trusted() {
		t.Error("toggling trust off left the workspace trusted")
	}
	if store, err := config.LoadTrustStore(); err == nil {
		if ok, _ := store.IsTrusted(w.cwd); ok {
			t.Error("toggling trust off left the directory in the trust store")
		}
	}
}

func TestPermissionsPaneStatesTheTrustPosture(t *testing.T) {
	w, s := trustSession(t)

	v := s.permissionsView()
	if v.CWD != w.cwd {
		t.Errorf("permissions view cwd = %q, want %q", v.CWD, w.cwd)
	}
	if v.Trusted {
		t.Error("a fresh temp dir should read as untrusted")
	}

	if err := s.settingsAction("set", map[string]string{"key": "trust", "value": "true"}); err != nil {
		t.Fatalf("set trust=true: %v", err)
	}
	if v := s.permissionsView(); !v.Trusted {
		t.Error("the permissions pane still reports the workspace untrusted after it was trusted")
	}
}
