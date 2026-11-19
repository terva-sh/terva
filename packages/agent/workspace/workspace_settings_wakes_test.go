package workspace

import (
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/testsupport"
)

func wakeCapValues(it *ctrlproto.SettingItem) string {
	var vs []string
	for _, o := range it.Options {
		vs = append(vs, o.Value)
	}
	return strings.Join(vs, " ")
}

// 🚨 TKT-01M7PGWMJZ. A member that was at work before a person turned wakes
// on has no talkoot_wake, because a session gets the tool when its tools are
// built. The row builds them again, so the member has the tool at its next
// turn with no restart.
func TestTheWakesRowGivesARunningMemberTheToolWithNoRestart(t *testing.T) {
	w, seat, _ := wakeCrew(t, "", okProvider)
	s, err := w.resolve(memberView(t, w, "crew", "helm").Session)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.agent.LookupTool("talkoot_send"); !ok {
		t.Fatal("the positive control: helm has no talkoot_send")
	}
	if _, ok := s.agent.LookupTool("talkoot_wake"); ok {
		t.Fatal("helm has talkoot_wake before a person turned wakes on")
	}
	if it := findSetting(s.settingsView(), "talkoot_wakes"); it == nil || it.Value != "off" || it.Type != "enum" || it.Group != "agents" {
		t.Fatalf("the wakes row must start off, in the agents group: %+v", it)
	}

	if err := s.settingsAction("set", map[string]string{"key": "talkoot_wakes", "value": "2"}); err != nil {
		t.Fatal(err)
	}
	if pol := config.TalkootWakes(); !pol.On || pol.PerMember != 2 {
		t.Fatalf("the row did not write the cap to the user layer: %+v", pol)
	}
	if _, ok := s.agent.LookupTool("talkoot_wake"); !ok {
		t.Fatal("helm has no talkoot_wake after the person turned wakes on")
	}
	if _, err := seat.Wake(time.Minute, "Check it."); err != nil {
		t.Fatalf("a wake after the row turned wakes on was refused: %v", err)
	}
	if it := findSetting(s.settingsView(), "talkoot_wakes"); it == nil || it.Value != "2" {
		t.Errorf("the row does not show the cap: %+v", it)
	}

	if err := s.settingsAction("set", map[string]string{"key": "talkoot_wakes", "value": "off"}); err != nil {
		t.Fatal(err)
	}
	if cfg, err := config.LoadConfig(); err != nil || cfg.TalkootWakes != nil {
		t.Fatalf("off must remove the key: %+v %v", cfg.TalkootWakes, err)
	}
	if _, ok := s.agent.LookupTool("talkoot_wake"); ok {
		t.Error("helm kept talkoot_wake after the person turned wakes off")
	}
	if _, err := seat.Wake(time.Minute, "Check it."); err == nil || !strings.Contains(err.Error(), "wakes are off") {
		t.Errorf("the seat must refuse a wake after the row turned wakes off, got %v", err)
	}
}

// The row offers a small set of caps. A cap that a person wrote by hand joins
// them while the key holds it, and a change of the cap keeps max_delay.
func TestTheWakesRowKeepsWhatAPersonWroteByHand(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	wakesConfig(t, `{"per_member": 4, "max_delay": "90m"}`)
	s := newTestSession()
	s.ws = &Workspace{}

	it := findSetting(s.settingsView(), "talkoot_wakes")
	if it == nil || it.Value != "4" || wakeCapValues(it) != "off 1 2 3 4 5" {
		t.Fatalf("the row must show the cap of 4 among its choices: %+v", it)
	}
	// 🚨 A number that the row does not offer is refused, so a client cannot
	// set a cap of thousands.
	for _, bad := range []string{"7", "5000", "0", "-1", "many"} {
		if err := s.settingsAction("set", map[string]string{"key": "talkoot_wakes", "value": bad}); err == nil {
			t.Errorf("the row took the cap %q", bad)
		}
	}
	if pol := config.TalkootWakes(); pol.PerMember != 4 {
		t.Fatalf("a refused value changed the cap: %+v", pol)
	}
	if err := s.settingsAction("set", map[string]string{"key": "talkoot_wakes", "value": "2"}); err != nil {
		t.Fatal(err)
	}
	if pol := config.TalkootWakes(); !pol.On || pol.PerMember != 2 || pol.MaxDelay != 90*time.Minute {
		t.Fatalf("a new cap must keep max_delay: %+v", pol)
	}
	if it := findSetting(s.settingsView(), "talkoot_wakes"); it == nil || wakeCapValues(it) != "off 1 2 3 5" {
		t.Errorf("a cap that the key no longer holds must leave the choices: %+v", it)
	}
}

// Wakes mean nothing while Talkoot is off, so the row is not there.
func TestTheWakesRowIsHiddenWhileTalkootIsOff(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	s := newTestSession()
	s.ws = &Workspace{}
	if findSetting(s.settingsView(), "talkoot") == nil {
		t.Fatal("the positive control: no talkoot row")
	}
	if it := findSetting(s.settingsView(), "talkoot_wakes"); it != nil {
		t.Errorf("the wakes row shows while Talkoot is off: %+v", it)
	}
}
