package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/testsupport"
)

// The Talkoot row sits in the agents group, starts off, says that a change
// waits for a restart, and writes talkoot_enabled to the user layer.
func TestSettingsTalkootRow(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	s := newTestSession()
	s.ws = &Workspace{}

	it := findSetting(s.settingsView(), "talkoot")
	if it == nil || it.Type != "bool" || it.Group != "agents" {
		t.Fatalf("no talkoot bool row in the agents group: %+v", it)
	}
	if it.Value != "false" {
		t.Errorf("talkoot row starts %q, want false", it.Value)
	}
	if it.Note != "applies after the daemon restarts" {
		t.Errorf("talkoot row note = %q; it must say the change waits for a restart", it.Note)
	}

	if err := s.settingsAction("set", map[string]string{"key": "talkoot", "value": "true"}); err != nil {
		t.Fatalf("set talkoot: %v", err)
	}
	if !config.TalkootEnabled() {
		t.Fatal("the row did not write talkoot_enabled to the user layer")
	}
	if it := findSetting(s.settingsView(), "talkoot"); it == nil || it.Value != "true" {
		t.Errorf("the view does not reflect the change: %+v", it)
	}

	if err := s.settingsAction("set", map[string]string{"key": "talkoot", "value": "false"}); err != nil {
		t.Fatalf("clear talkoot: %v", err)
	}
	if config.TalkootEnabled() {
		t.Fatal("turning the row off left talkoot_enabled on")
	}
}

// 🔑 A cloned repository controls its project configuration, so a
// talkoot_enabled key there must not turn Talkoot on. The row writes the user
// layer, and the reader reads only that layer.
func TestProjectConfigStillCannotTurnTalkootOn(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	repo := testsupport.TempDir(t)
	path := config.ProjectConfigPath(repo)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"talkoot_enabled": true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := config.LoadProjectConfig(repo); err != nil {
		t.Fatalf("load project config: %v", err)
	}
	t.Chdir(repo)
	if config.TalkootEnabled() {
		t.Fatal("a project configuration turned Talkoot on")
	}
	s := newTestSession()
	s.ws = &Workspace{}
	if it := findSetting(s.settingsView(), "talkoot"); it == nil || it.Value != "false" {
		t.Errorf("the row reads the project key: %+v", it)
	}
}
