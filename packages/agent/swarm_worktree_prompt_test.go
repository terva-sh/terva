package agent

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/testsupport"
)

// The worktree block tells the coordinator where its sub-agents' files land.
// It is gated on isolation ACTUALLY being on, because with a shared tree every
// sentence in it is false — it would send a coordinator hunting for a
// directory that was never leased.
//
// The gate that matters is the pairing: the block must be present exactly when
// the workspace installs the lease hook, and both now ask
// build.SwarmWorktreesActive.

const worktreeSource = "swarm-worktrees"

// worktreeEnv is the shared fixture: an isolated TERVA_HOME, a credential, and
// a config the sub-tests overwrite.
func worktreeEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	return testsupport.TempDir(t)
}

func hasWorktreeBlock(t *testing.T, args build.Args) bool {
	t.Helper()
	r, err := build.Resolve(args, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range r.SystemSegments {
		if s.Source == worktreeSource {
			return true
		}
	}
	return false
}

func saveSwarmConfig(t *testing.T, autoSwarm, nudge, worktrees *bool) {
	t.Helper()
	if err := config.SaveConfig(config.Config{
		Provider: "openai", Model: "gpt-5",
		AutoSwarmEnabled: autoSwarm, AutoSwarmNudge: nudge, SwarmWorktrees: worktrees,
	}); err != nil {
		t.Fatal(err)
	}
}

// The default is off, and off must stay silent. A coordinator sharing the host
// tree that is told its sub-agents write elsewhere will look for their output
// in a path that does not exist and report the work missing.
func TestResolve_WorktreeBlockAbsentWithoutIsolation(t *testing.T) {
	dir := worktreeEnv(t)
	on := true

	saveSwarmConfig(t, &on, nil, nil) // auto-swarm on, worktrees unset = off
	if hasWorktreeBlock(t, build.Args{CWD: dir}) {
		t.Error("swarm_worktrees is off: the model must not be told its sub-agents get their own worktree")
	}

	off := false
	saveSwarmConfig(t, &on, nil, &off)
	if hasWorktreeBlock(t, build.Args{CWD: dir}) {
		t.Error("swarm_worktrees=false must not carry the worktree block")
	}
}

func TestResolve_WorktreeBlockRidesIsolation(t *testing.T) {
	dir := worktreeEnv(t)
	on := true
	saveSwarmConfig(t, &on, nil, &on)

	if !hasWorktreeBlock(t, build.Args{CWD: dir}) {
		t.Fatal("swarm_worktrees=true with auto-swarm on must carry the worktree block")
	}
	r, err := build.Resolve(build.Args{CWD: dir}, false)
	if err != nil {
		t.Fatal(err)
	}
	// The two things the block exists to say. Pinned as text because a segment
	// that is present but says neither is the same failure with a passing test.
	// Phrased for the STE gate (cmd/terva-ste-lint), which governs model-visible
	// text: short sentences, active voice, no em-dash asides, no CAPS emphasis.
	// Pinned on the two CLAIMS rather than on wording, so a re-phrasing that
	// keeps both still passes.
	for _, want := range []string{
		"not in your working directory", // where a reported file actually is
		"remove the worktree",           // that cleaning up leftovers is allowed
	} {
		if !strings.Contains(r.SystemPrompt, want) {
			t.Errorf("the worktree block does not say %q", want)
		}
	}
}

// The --swarm-worktrees flag beats a config that says otherwise, exactly as it
// does for the lease hook itself. A run that was explicitly told to isolate
// must have a prompt that agrees with the run.
func TestResolve_WorktreeBlockFollowsTheFlagOverride(t *testing.T) {
	dir := worktreeEnv(t)
	on, off := true, false
	saveSwarmConfig(t, &on, nil, &off) // config says no…

	if !hasWorktreeBlock(t, build.Args{CWD: dir, SwarmWorktrees: &on}) {
		t.Error("--swarm-worktrees must add the worktree block over a config that disables it")
	}
	saveSwarmConfig(t, &on, nil, &on) // …and the reverse
	if hasWorktreeBlock(t, build.Args{CWD: dir, SwarmWorktrees: &off}) {
		t.Error("an explicit --swarm-worktrees=false must drop the block over a config that enables it")
	}
}

// Deliberately NOT gated on the proactive-delegation nudge, unlike its
// auto-swarm sibling. The nudge is a disposition a user may find pushy; this is
// a fact about the filesystem the coordinator is about to reason over, and a
// coordinator that spawns with the nudge off needs it exactly as much.
func TestResolve_WorktreeBlockIgnoresTheNudgeToggle(t *testing.T) {
	dir := worktreeEnv(t)
	on, off := true, false
	saveSwarmConfig(t, &on, &off, &on)

	if !hasWorktreeBlock(t, build.Args{CWD: dir}) {
		t.Error("the worktree block must survive nudge=false: it is an environment fact, not a nudge")
	}
	// The sibling really is gone, so this is a divergence and not both blocks
	// riding along regardless.
	r, err := build.Resolve(build.Args{CWD: dir}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range r.SystemSegments {
		if s.Source == "auto-swarm" {
			t.Fatal("fixture is wrong: nudge=false should have dropped the auto-swarm addendum")
		}
	}
}

// No swarm tool, nothing to say. And the same mode gate as every other
// coding-workflow skin: an immersive or tool-suppressed session has no
// sub-agents and no worktrees.
func TestResolve_WorktreeBlockNeedsTheSwarmTool(t *testing.T) {
	dir := worktreeEnv(t)
	on, off := true, false
	saveSwarmConfig(t, &off, nil, &on)
	if hasWorktreeBlock(t, build.Args{CWD: dir}) {
		t.Error("auto-swarm off: worktree isolation is moot, so the block must not ride")
	}

	saveSwarmConfig(t, &on, nil, &on)
	for _, c := range []struct {
		name string
		args build.Args
	}{
		{"chat", build.Args{CWD: dir, Experience: build.ExperienceChat}},
		{"play", build.Args{CWD: dir, Experience: build.ExperiencePlay}},
		{"no-tools", build.Args{CWD: dir, NoTools: true}},
		{"no-workspace-tools", build.Args{CWD: dir, NoWorkspaceTools: true}},
	} {
		if hasWorktreeBlock(t, c.args) {
			t.Errorf("%s must not carry the worktree block", c.name)
		}
	}
}
