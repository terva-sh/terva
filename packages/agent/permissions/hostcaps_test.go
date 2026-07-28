package permissions

import (
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/mode"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// The security-posture half of the host capability matrix
// (docs/architecture/06-extensibility.md §1.5 carries the full table: tools,
// extensions, MCP, controllers, with anchors).
//
// The table itself is permissions.go's modePosture — it used to be duplicated
// here and asserted against the resolvers, back when the resolvers answered
// with `||` chains and a mirror was the only way to pin them. The resolvers
// read the table now, so the two guards below check what a mirror never could:
// that the table covers every declared mode, and that nothing upstream in the
// precedence chain swallows the row before it is consulted.

// The asymmetry that decides which way an unknown mode tilts, and the sibling
// of TestSurfaceFailsClosed.
//
// A mode with no row wrongly asked to confirm costs a prompt somebody can
// answer. A mode with no row wrongly handed yolo runs every tool unconfirmed,
// anywhere on the filesystem, and is silent about it. So an unclassified mode
// must be granted nothing, which is what ApprovalAsk plus a locked sandbox
// means. This branch is unreachable from the shipping binary today; the test
// exists so that stays true by decision rather than by luck.
func TestPostureFailsClosed(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	for _, m := range []mode.Mode{"", "some-future-mode"} {
		approval, jailed := postureOf(m)
		if approval != core.ApprovalAsk {
			t.Errorf("postureOf(%q) approval = %q, want %q — an unrecognized mode must not be granted trust", m, approval, core.ApprovalAsk)
		}
		if !jailed {
			t.Errorf("postureOf(%q) jailed = false — an unrecognized mode must not be handed the filesystem", m)
		}
		// And the resolvers must actually carry it, not just the helper.
		if got := ResolveApprovalMode(Inputs{Mode: m}, config.Config{}); got != core.ApprovalAsk {
			t.Errorf("ResolveApprovalMode(%q) = %q, want %q", m, got, core.ApprovalAsk)
		}
		if !ResolveJail(Inputs{Mode: m}) {
			t.Errorf("ResolveJail(%q) = false, want true", m)
		}
	}
}

func TestModePostureTableIsExhaustive(t *testing.T) {
	for _, m := range mode.All() {
		if _, ok := modePosture[m]; !ok {
			t.Errorf("mode %q has no posture row: every run mode must declare its default approval and jail state", m)
		}
	}
	if got, want := len(modePosture), len(mode.All()); got != want {
		t.Errorf("posture table has %d rows but %d modes are declared; a stale row is as bad as a missing one", got, want)
	}
}

// TestModePostureMatchesTheResolvers pins that a bare `Inputs{Mode: m}` against
// an empty config actually reaches the posture row. The resolvers consult it
// last, behind --approval, --no-yolo, the config default, --jail/--no-jail and
// the persisted unjail store; a branch above that returned early for some mode
// would leave the table describing a default nobody gets.
func TestModePostureMatchesTheResolvers(t *testing.T) {
	// A scratch home so a developer's persisted unjail exceptions can't leak
	// into the mode-default question.
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	for m, want := range modePosture {
		if got := ResolveApprovalMode(Inputs{Mode: m}, config.Config{}); got != want.approval {
			t.Errorf("ResolveApprovalMode(%q) = %s, want %s", m, got, want.approval)
		}
		if got := ResolveJail(Inputs{Mode: m}); got != want.jailed {
			t.Errorf("ResolveJail(%q) = %v, want %v", m, got, want.jailed)
		}
	}
}
