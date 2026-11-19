package config

import (
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// 🚨 The key lets a member's turn start with no new post from a person, so
// only the user layer sets it. A project file that names caps must change
// nothing, for a trusted project too.
func TestTalkootWakesIsAUserLayerKey(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	cwd := testsupport.TempDir(t)
	on := true
	if err := SaveConfig(Config{TalkootEnabled: &on}); err != nil {
		t.Fatal(err)
	}
	writeProjectConfig(t, cwd, `{"disable_extensions":["positive-control"],"talkoot_wakes":{"per_member":50,"max_delay":"8760h"}}`)
	for _, trusted := range []bool{false, true} {
		eff := ResolveConfig(cwd, trusted)
		if len(eff.DisableExtensions) != 1 || eff.DisableExtensions[0] != "positive-control" {
			t.Fatal("the project fixture did not reach the resolver")
		}
		if eff.TalkootWakes != nil || TalkootWakes().On {
			t.Fatalf("a project config (trusted=%v) turned wakes on", trusted)
		}
	}

	if err := SaveConfig(Config{TalkootEnabled: &on, TalkootWakes: &TalkootWakesConfig{PerMember: 3, MaxDelay: "90m"}}); err != nil {
		t.Fatal(err)
	}
	if got := TalkootWakes(); !got.On || got.PerMember != 3 || got.MaxDelay != 90*time.Minute || got.Problem != "" {
		t.Fatalf("TalkootWakes() = %+v, want the user layer's caps", got)
	}
}

// Every doubt reads as off, and the policy says why, so the interface can
// show a person a key that does nothing.
func TestTalkootWakesAreOffUnlessTheCapsAreSound(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	if got := TalkootWakes(); got.On || got.Problem != "" {
		t.Fatalf("an empty config has wakes, or a problem to report: %+v", got)
	}
	on, off := true, false
	for _, tc := range []struct {
		name    string
		cfg     Config
		problem string
	}{
		{"talkoot off", Config{TalkootEnabled: &off, TalkootWakes: &TalkootWakesConfig{PerMember: 2}}, "talkoot_enabled is off"},
		{"no count", Config{TalkootEnabled: &on, TalkootWakes: &TalkootWakesConfig{MaxDelay: "1h"}}, "per_member is 0"},
		{"a negative count", Config{TalkootEnabled: &on, TalkootWakes: &TalkootWakesConfig{PerMember: -1}}, "per_member is -1"},
		{"a delay that does not parse", Config{TalkootEnabled: &on, TalkootWakes: &TalkootWakesConfig{PerMember: 2, MaxDelay: "2 days"}}, `max_delay "2 days"`},
		{"a delay below a minute", Config{TalkootEnabled: &on, TalkootWakes: &TalkootWakesConfig{PerMember: 2, MaxDelay: "5s"}}, `max_delay "5s"`},
		{"a negative delay", Config{TalkootEnabled: &on, TalkootWakes: &TalkootWakesConfig{PerMember: 2, MaxDelay: "-1h"}}, `max_delay "-1h"`},
	} {
		if err := SaveConfig(tc.cfg); err != nil {
			t.Fatal(err)
		}
		got := TalkootWakes()
		if got.On || got.PerMember != 0 || got.MaxDelay != 0 {
			t.Errorf("%s: wakes must be off with no cap to read, got %+v", tc.name, got)
		}
		if !strings.Contains(got.Problem, tc.problem) {
			t.Errorf("%s: the problem must name %q, got %q", tc.name, tc.problem, got.Problem)
		}
	}

	if err := SaveConfig(Config{TalkootEnabled: &on, TalkootWakes: &TalkootWakesConfig{PerMember: 1}}); err != nil {
		t.Fatal(err)
	}
	if got := TalkootWakes(); !got.On || got.PerMember != 1 || got.MaxDelay != DefaultTalkootWakeDelay {
		t.Fatalf("a key with no max_delay takes the default of 24h, got %+v", got)
	}
}
