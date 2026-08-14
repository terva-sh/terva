package workspace

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/swarm"
)

// The swarm-hold nudge is injected as a user-role turn, and "synthetic" is
// display metadata that never reaches the provider — so the text is the only
// thing that can tell the model no person wrote it. These guards pin the
// properties that do that job, in the same shape as the open-work gate's
// (packages/agent/swarm/open_work_gate_test.go).
//
// The fragments are markers, not the contract: a reword re-anchors them with
// the same intent. What must not happen is a sweep that quietly drops one of
// the properties.

// TestSwarmHoldNudgeDisclaimsTheUser pins the fix. The old text opened "You
// indicated you're finishing, but …" and never said who was speaking, so a
// model that took it for the user answered the user rather than noting the
// hold.
func TestSwarmHoldNudgeDisclaimsTheUser(t *testing.T) {
	msg := swarmWaitGateMessage()
	for _, want := range []string{
		"automatic check from terva",
		"the user did not send it",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the nudge no longer disclaims user authorship: missing %q in\n%s", want, msg)
		}
	}
}

// TestSwarmHoldNudgeLeadsWithTheProhibition pins POSITION, which is the
// measured part of the house pattern: the prohibition goes before the detail
// it governs (scripts/eval/README.md, the inactive-groups A/B). Reordering is
// a behaviour change until re-measured.
func TestSwarmHoldNudgeLeadsWithTheProhibition(t *testing.T) {
	if !strings.HasPrefix(swarmHoldBody, "Do not ") {
		t.Errorf("the body must open with the prohibition; got %.60q…", swarmHoldBody)
	}
	prohibition := strings.Index(swarmHoldBody, "Do not ")
	detail := strings.Index(swarmHoldBody, "still active")
	if detail < 0 {
		t.Fatal("body no longer states what is still running")
	}
	if prohibition > detail {
		t.Errorf("prohibition at %d is buried after the detail at %d", prohibition, detail)
	}
}

// TestSwarmHoldNudgeNamesThePush pins the part that keeps a held coordinator
// from polling its children. The recap is pushed, so the nudge has to say so —
// otherwise "wait" reads as an instruction to go and look.
func TestSwarmHoldNudgeNamesThePush(t *testing.T) {
	for _, want := range []string{"[auto-swarm update]", "do not need to watch"} {
		if !strings.Contains(swarmHoldBody, want) {
			t.Errorf("the nudge must name the push, not just the wait: missing %q", want)
		}
	}
}

// TestSwarmHoldTagIsOutsideTheCatalog mirrors the open-work guard: a tag
// written into the translatable body could be carried away by a translation,
// and the tag is what marks the turn as harness-authored at a glance.
func TestSwarmHoldTagIsOutsideTheCatalog(t *testing.T) {
	if strings.Contains(swarmHoldBody, swarmHoldTag) {
		t.Errorf("the tag %q must be prefixed outside the catalog entry, not written into it", swarmHoldTag)
	}
	if !strings.HasPrefix(swarmWaitGateMessage(), swarmHoldTag) {
		t.Errorf("the composed message must lead with the tag: %.40q…", swarmWaitGateMessage())
	}
}

// TestHarnessNudgeTagsAreDistinct keeps the two at-close gates
// distinguishable. They fire from the same boundary in registration order, and
// a shared or overlapping tag would make a transcript — and
// swarm.IsOpenWorkGateNudge, which matches on its tag alone — unable to tell
// which gate spoke.
func TestHarnessNudgeTagsAreDistinct(t *testing.T) {
	if swarmHoldTag == swarm.OpenWorkGateTag {
		t.Fatalf("the two gates share a tag: %q", swarmHoldTag)
	}
	if swarm.IsOpenWorkGateNudge(swarmWaitGateMessage()) {
		t.Error("the swarm-hold nudge must not be recognized as the open-work nudge")
	}
}
