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
