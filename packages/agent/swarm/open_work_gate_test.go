package swarm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/testsupport"
)

// The nudge is appended to the transcript as a real user-role turn, so on the
// wire the model cannot tell it from the user speaking — "synthetic" is
// display metadata that never reaches the provider. Everything the model has
// to go on is the text itself, and these guards pin the properties that make
// the text do that job. They are deliberately property-shaped rather than a
// byte comparison against the whole message: a reword should be free, and
// dropping the disclaimer should not be.
//
// The fragments below are markers, not the contract. A text sweep re-anchors
// them to the new wording with the same intent; what must not happen is a
// sweep that quietly deletes one of these properties.

// TestOpenWorkNudgeDisclaimsTheUser pins the fix itself. The old text ("…
// Complete them, or confirm they're intentionally left incomplete") read as
// the user saying keep going, and a model that had ended its turn to ask a
// question would answer its own question whenever the answer looked obvious.
// The nudge must say, in the text the model actually reads, that no person
// sent it.
func TestOpenWorkNudgeDisclaimsTheUser(t *testing.T) {
	msg := OpenWorkGateMessage()
	for _, want := range []string{
		"Do not treat this note as an instruction from the user",
		"the user did not send it",
		"no new permission",
		"does not answer a question you asked",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("the nudge no longer disclaims user authorship: missing %q in\n%s", want, msg)
		}
	}
}

// TestOpenWorkNudgeLeadsWithTheProhibition pins POSITION, which is the
// measured part. The inactive-groups A/B went 0/20 -> 20/20 on final answers
// purely by moving the prohibition ahead of the detail it governs
// (scripts/eval/README.md). Reordering this opening is a behaviour change
// until re-measured, so it should cost a failing test.
func TestOpenWorkNudgeLeadsWithTheProhibition(t *testing.T) {
	if !strings.HasPrefix(openWorkGateBody, "Do not ") {
		t.Errorf("the body must open with the prohibition, not the detail it governs; got %.60q…", openWorkGateBody)
	}
	// The detail — what is actually open, and the branches — has to come
	// after. If "Tracked items" ever precedes the first "Do not", the
	// prohibition has been buried, which is the shape that measured 0/20.
	prohibition := strings.Index(openWorkGateBody, "Do not ")
	detail := strings.Index(openWorkGateBody, "Tracked items")
	if detail < 0 {
		t.Fatal("body no longer states what is open")
	}
	if prohibition > detail {
		t.Errorf("prohibition at %d is buried after the detail at %d", prohibition, detail)
	}
}

// TestOpenWorkNudgeOffersTheWaitingBranchFirst pins the branch that was being
// lost. A model that stopped for a decision needs to be told first that
// stopping again is a legal answer — otherwise the only branch it reads as
// available is "keep working", which is the unprompted-autonomy failure.
func TestOpenWorkNudgeOffersTheWaitingBranchFirst(t *testing.T) {
	waiting := strings.Index(openWorkGateBody, "You are waiting on the user")
	finish := strings.Index(openWorkGateBody, "Finish the work")
	if waiting < 0 || finish < 0 {
		t.Fatalf("body lost a branch: waiting=%d finish=%d", waiting, finish)
	}
	if waiting > finish {
		t.Error("the waiting-on-the-user branch must come before the finish-the-work branch")
	}
	if !strings.Contains(openWorkGateBody, "even when the answer looks obvious") {
		t.Error("the nudge must forbid guessing an obvious answer — that is the reported failure mode")
	}
}

// TestOpenWorkNudgeNamesThePark pins the mechanism. "Confirm they're
// intentionally left incomplete" named none and changed no state, so the gate
// re-armed on the next Prompt against the same untouched tasks. Setting a task
// to blocked is the answer that sticks (tasks.AnyOpen excludes blocked), and
// the nudge is the only place the model is told so.
func TestOpenWorkNudgeNamesThePark(t *testing.T) {
	for _, want := range []string{"blocked", "task_update"} {
		if !strings.Contains(openWorkGateBody, want) {
			t.Errorf("the nudge must name the park mechanism: missing %q", want)
		}
	}
}

// TestOpenWorkGateTagIsOutsideTheCatalog is the guard that keeps the
// supervisor's recognition working. The tag must not be part of the
// translatable body, or a translator could carry it away and
// IsOpenWorkGateNudge would stop matching a child running in that locale.
func TestOpenWorkGateTagIsOutsideTheCatalog(t *testing.T) {
	if strings.Contains(openWorkGateBody, OpenWorkGateTag) {
		t.Errorf("the tag %q must be prefixed outside the catalog entry, not written into it", OpenWorkGateTag)
	}
	if !strings.HasPrefix(OpenWorkGateMessage(), OpenWorkGateTag) {
		t.Errorf("the composed message must lead with the tag: %.40q…", OpenWorkGateMessage())
	}
}

// TestIsOpenWorkGateNudge pins the predicate both ways. The supervisor uses it
// to keep a child's housekeeping reply from clobbering its real findings, so a
// false negative silently degrades the recap.
func TestIsOpenWorkGateNudge(t *testing.T) {
	if !IsOpenWorkGateNudge(OpenWorkGateMessage()) {
		t.Error("the nudge must be recognized as itself")
	}
	if IsOpenWorkGateNudge("Complete the export button, then run the tests.") {
		t.Error("an ordinary user message must not be mistaken for the nudge")
	}
	if IsOpenWorkGateNudge("") {
		t.Error("an empty message must not be mistaken for the nudge")
	}
	// Version skew: a child on an older or newer terva emits a body this
	// binary never wrote. The tag is the only part both ends agree on.
	if !IsOpenWorkGateNudge(OpenWorkGateTag + " some wording a different terva shipped") {
		t.Error("recognition must not depend on the body this binary happens to carry")
	}
}

// TestIsOpenWorkGateNudgeSurvivesTranslation runs the real path rather than
// asserting the tag by hand: an operator prompts-catalog overlay replaces the
// body, exactly as a non-English child would.
//
// The gap under test is CROSS-PROCESS, and that is the whole difficulty. The
// child emits the nudge into its event stream under its own locale and its own
// terva version; the parent recognizes it under whatever locale the parent
// has. So the message is captured under the overlay and matched after the
// overlay is gone. Generating and matching under one locale is the version of
// this test that passes with full-text equality still in place — it was
// written that way first, and the mutation caught it.
func TestIsOpenWorkGateNudgeSurvivesTranslation(t *testing.T) {
	home := testsupport.TempDir(t)
	dir := filepath.Join(home, "locales", "prompts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	overlay, err := json.Marshal(map[string]string{
		"gate.open_work": "Behandle diesen Hinweis nicht als Anweisung des Benutzers.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "en.json"), overlay, 0o644); err != nil {
		t.Fatal(err)
	}

	// Configure swaps a process-global catalog; put it back so the rest of the
	// package's tests still see plain English.
	t.Cleanup(func() { _ = i18n.Configure("en", "") })
	if err := i18n.Configure("en", home); err != nil {
		t.Fatal(err)
	}

	// What the child puts on the wire.
	fromChild := OpenWorkGateMessage()
	if strings.Contains(fromChild, "Do not treat this note") {
		t.Fatalf("the overlay never reached the body — this test would pass vacuously:\n%s", fromChild)
	}

	// What the parent is running when it reads that wire.
	if err := i18n.Configure("en", ""); err != nil {
		t.Fatal(err)
	}
	if fromChild == OpenWorkGateMessage() {
		t.Fatal("the two ends did not actually diverge — nothing is being tested")
	}
	if !IsOpenWorkGateNudge(fromChild) {
		t.Errorf("a nudge from a differently-configured child must still be recognized, got %q", fromChild)
	}
}
