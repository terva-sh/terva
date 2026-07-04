package agent

import (
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func TestHasBaseWorkspaceTools(t *testing.T) {
	cases := []struct {
		name string
		args Args
		want bool
	}{
		{"coding", Args{}, true},
		{"chat", Args{Experience: ExperienceChat}, false},
		{"play", Args{Experience: ExperiencePlay}, false},
		{"no-tools", Args{NoTools: true}, false},
		{"no-workspace-tools", Args{NoWorkspaceTools: true}, false},
	}
	for _, c := range cases {
		if got := hasBaseWorkspaceTools(c.args); got != c.want {
			t.Errorf("%s: hasBaseWorkspaceTools = %v, want %v", c.name, got, c.want)
		}
	}
}

// The auto-swarm addendum is a coding-workflow skin: present in a normal coding
// session when auto-swarm is enabled, and suppressed in every immersive /
// tool-suppressed mode even with the global flag ON (the leak found in review —
// it was telling an immersive persona it could fork background sub-agents in a
// working directory it has no tools to touch).
func TestResolve_AutoSwarmAddendumGatedByMode(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	enabled := true
	if err := SaveConfig(Config{Provider: "openai", Model: "gpt-5", AutoSwarmEnabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	dir := testsupport.TempDir(t)

	hasAutoSwarm := func(t *testing.T, exp string, noTools, noWS bool) bool {
		t.Helper()
		r, err := Resolve(Args{CWD: dir, Experience: exp, NoTools: noTools, NoWorkspaceTools: noWS}, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range r.systemSegments {
			if s.Source == "auto-swarm" {
				return true
			}
		}
		return false
	}

	if !hasAutoSwarm(t, "", false, false) {
		t.Error("coding session with auto-swarm enabled should carry the addendum")
	}
	if hasAutoSwarm(t, ExperienceChat, false, false) {
		t.Error("--chat must not carry the auto-swarm addendum")
	}
	if hasAutoSwarm(t, ExperiencePlay, false, false) {
		t.Error("--play must not carry the auto-swarm addendum")
	}
	if hasAutoSwarm(t, "", true, false) {
		t.Error("--no-tools must not carry the auto-swarm addendum")
	}
	if hasAutoSwarm(t, "", false, true) {
		t.Error("--no-workspace-tools must not carry the auto-swarm addendum")
	}
}

// The two toggles are independent: the swarm_spawn tool (AutoSwarmEnabled) and
// the proactive-delegation nudge (AutoSwarmNudge). The addendum rides only when
// both are on; nudge defaults ON so enabling auto-swarm keeps prior behavior.
func TestResolve_AutoSwarmNudgeGate(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	dir := testsupport.TempDir(t)

	hasAddendum := func() bool {
		r, err := Resolve(Args{CWD: dir}, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range r.systemSegments {
			if s.Source == "auto-swarm" {
				return true
			}
		}
		return false
	}
	save := func(enabled, nudge *bool) {
		if err := SaveConfig(Config{Provider: "openai", Model: "gpt-5", AutoSwarmEnabled: enabled, AutoSwarmNudge: nudge}); err != nil {
			t.Fatal(err)
		}
	}
	tp, fp := true, false

	save(&tp, nil) // enabled, nudge default
	if !hasAddendum() {
		t.Error("enabled + default nudge should carry the addendum")
	}
	save(&tp, &fp) // enabled tool, nudge off → tool stays, addendum gone
	if hasAddendum() {
		t.Error("nudge=false should drop the addendum (the tool stays, gated separately)")
	}
	save(&fp, &tp) // tool off → no addendum regardless of nudge
	if hasAddendum() {
		t.Error("disabled auto-swarm should carry no addendum")
	}
}
