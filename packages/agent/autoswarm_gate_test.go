package agent

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/mode"
	"terva.sh/terva/packages/testsupport"
)

func TestHasBaseWorkspaceTools(t *testing.T) {
	cases := []struct {
		name string
		args build.Args
		want bool
	}{
		{"coding", build.Args{}, true},
		{"chat", build.Args{Experience: build.ExperienceChat}, false},
		{"play", build.Args{Experience: build.ExperiencePlay}, false},
		{"no-tools", build.Args{NoTools: true}, false},
		{"no-workspace-tools", build.Args{NoWorkspaceTools: true}, false},
	}
	for _, c := range cases {
		if got := build.HasBaseWorkspaceTools(c.args); got != c.want {
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
	if err := config.SaveConfig(config.Config{Provider: "openai", Model: "gpt-5", AutoSwarmEnabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	dir := testsupport.TempDir(t)

	hasAutoSwarm := func(t *testing.T, exp string, noTools, noWS bool) bool {
		t.Helper()
		r, err := build.Resolve(build.Args{CWD: dir, Experience: exp, NoTools: noTools, NoWorkspaceTools: noWS}, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range r.SystemSegments {
			if s.Source == "auto-swarm" {
				return true
			}
		}
		return false
	}

	if !hasAutoSwarm(t, "", false, false) {
		t.Error("coding session with auto-swarm enabled should carry the addendum")
	}
	if hasAutoSwarm(t, build.ExperienceChat, false, false) {
		t.Error("--chat must not carry the auto-swarm addendum")
	}
	if hasAutoSwarm(t, build.ExperiencePlay, false, false) {
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
		r, err := build.Resolve(build.Args{CWD: dir}, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range r.SystemSegments {
			if s.Source == "auto-swarm" {
				return true
			}
		}
		return false
	}
	save := func(enabled, nudge *bool) {
		if err := config.SaveConfig(config.Config{Provider: "openai", Model: "gpt-5", AutoSwarmEnabled: enabled, AutoSwarmNudge: nudge}); err != nil {
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

// Every swarm child (--swarm-agent) carries the deliverable contract: the
// coordinator's recap surfaces only the child's final assistant message, so
// the addendum pins "end with your findings, restate them after wrap-up
// nudges" — for persona-less children too (the review-crew charters state
// the same contract in their own voice). Normal sessions must not carry it.
func TestResolve_SwarmChildDeliverableAddendum(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	if err := config.SaveConfig(config.Config{Provider: "openai", Model: "gpt-5"}); err != nil {
		t.Fatal(err)
	}
	dir := testsupport.TempDir(t)

	resolve := func(mode mode.Mode) *build.Resolved {
		t.Helper()
		r, err := build.Resolve(build.Args{CWD: dir, Mode: mode}, false)
		if err != nil {
			t.Fatal(err)
		}
		return &r
	}
	hasSeg := func(r *build.Resolved) bool {
		for _, s := range r.SystemSegments {
			if s.Source == "swarm-child" {
				return true
			}
		}
		return false
	}

	child := resolve(mode.SwarmAgent)
	if !hasSeg(child) {
		t.Fatal("swarm child must carry the deliverable-contract segment")
	}
	if !strings.Contains(child.SystemPrompt, "ONLY your final assistant message") {
		t.Error("child system prompt missing the deliverable contract text")
	}
	if hasSeg(resolve(mode.Interactive)) {
		t.Error("an interactive session must not carry the swarm-child contract")
	}
}
