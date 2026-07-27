package build

import (
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// The security-posture half of the host capability matrix
// (docs/architecture/06-extensibility.md §1.5 carries the full table: tools,
// extensions, MCP, controllers, with anchors).
//
// Every run mode gets a longhand row here, like allModeConstants next door: a
// new mode has to ANSWER what a session may do unattended, not inherit an
// answer from the resolver's else-branch. Both cells are asserted against the
// live resolvers, so this table cannot drift from the code it describes.
//
// The rows worth reading twice:
//
//   - ModeWeb is the one workspace-approval mode that is NOT jailed. That pair
//     is deliberate (permissions.go names the rationale: workspace approval's
//     trust-the-built-ins premise leans on cwd confinement, which the web host
//     provides by serving the workspace it was started in) — but until this
//     row, nothing pinned it, and a refactor could have flipped either half
//     silently.
//   - ModeSwarmAgent runs yolo, and with no --approval on its argv (pinned in
//     swarm/runner_test.go) a child never even builds a gate object. It still
//     runs full extension discovery and the user's MCP set, so this row is a
//     security boundary, not a default: tightening or loosening it is a
//     decision, made here.
//   - ModeAttach and ModeReplay assemble no agent of their own; their rows pin
//     what the resolvers would say if one ever did — fail-closed posture for a
//     future refactor, same idea as SurfaceFailsClosed.
var modePosture = map[Mode]struct {
	approval core.ApprovalMode
	jailed   bool
}{
	ModeInteractive: {core.ApprovalWorkspace, true},
	ModeACP:         {core.ApprovalWorkspace, true},
	ModeBot:         {core.ApprovalWorkspace, true},
	ModeWeb:         {core.ApprovalWorkspace, false},
	ModePrint:       {core.ApprovalYolo, false},
	ModeJSON:        {core.ApprovalYolo, false},
	ModeRPC:         {core.ApprovalYolo, false},
	ModeSwarmAgent:  {core.ApprovalYolo, false},
	ModeAttach:      {core.ApprovalYolo, false},
	ModeReplay:      {core.ApprovalYolo, false},
}

func TestModePostureTableIsExhaustive(t *testing.T) {
	for _, m := range allModeConstants {
		if _, ok := modePosture[m]; !ok {
			t.Errorf("mode %q has no posture row: every run mode must declare its default approval and jail state", m)
		}
	}
	if got, want := len(modePosture), len(allModeConstants); got != want {
		t.Errorf("posture table has %d rows but %d modes are declared; a stale row is as bad as a missing one", got, want)
	}
}

func TestModePostureMatchesTheResolvers(t *testing.T) {
	// A scratch home so a developer's persisted unjail exceptions can't leak
	// into the mode-default question.
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	for m, want := range modePosture {
		if got := ResolveApprovalMode(Args{Mode: m}, config.Config{}); got != want.approval {
			t.Errorf("ResolveApprovalMode(%q) = %s, want %s", m, got, want.approval)
		}
		if got := resolveJail(Args{Mode: m}); got != want.jailed {
			t.Errorf("resolveJail(%q) = %v, want %v", m, got, want.jailed)
		}
	}
}
