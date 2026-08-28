package core

import (
	"strings"
	"testing"
)

// stallGuardCases is every loop-check note that rides the ephemeral tail: both
// rungs, both the plain-repeat and the carried-error form. stallRefusal is
// deliberately absent — it is delivered as a tool ERROR rather than a tail
// block, so it is not competing with the user's message for the reply.
func stallGuardCases() []struct{ name, text string } {
	const tool = "email_move"
	const detail = "pass exactly one of ids, selection, or receipt"
	return []struct{ name, text string }{
		{"rung1 repeat", stallNudge(tool, 3, "")},
		{"rung1 error", stallNudge(tool, 3, detail)},
		{"rung2 repeat", stallHoldOffNudge(tool, 5, "")},
		{"rung2 error", stallHoldOffNudge(tool, 5, detail)},
	}
}

// The note rides the tail as a user-role message, so on the wire it is
// indistinguishable from something the user typed — the same shape that won 20
// of 20 final answers away from the user on the inactive-groups note. It
// therefore leads with the prohibition, and the ORDER is the part that was
// measured (0-of-20 before, 20-of-20 after). A prohibition buried after the
// content it governs does not land.
func TestStallNoteLeadsWithTheProhibition(t *testing.T) {
	for _, tc := range stallGuardCases() {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.HasPrefix(tc.text, "[loop check] Do not reply") {
				t.Fatalf("the note does not LEAD with the prohibition:\n%s", tc.text)
			}
			split := strings.Index(tc.text, "\n\n")
			if split < 0 {
				t.Fatalf("guard and body are not separated by a blank line:\n%s", tc.text)
			}
			// The tool name lives only in the body, so its position proves the
			// content follows the prohibition rather than preceding it.
			if at := strings.Index(tc.text, "email_move"); at < split {
				t.Errorf("the content precedes the prohibition; prohibition-first is the measured ordering:\n%s", tc.text)
			}
		})
	}
}

// escalate_test.go counts "[loop check]" occurrences to prove how many notes a
// turn delivered (1 for the nudge, 2 once the hold-off joins it). The guard
// carries the tag, so the bodies must not: a second tag per note would double
// those counts and quietly turn a passing assertion into a wrong one.
func TestStallNoteCarriesTheTagExactlyOnce(t *testing.T) {
	for _, tc := range stallGuardCases() {
		t.Run(tc.name, func(t *testing.T) {
			if n := strings.Count(tc.text, "[loop check]"); n != 1 {
				t.Errorf("want exactly 1 [loop check] tag, got %d — escalate_test.go counts these:\n%s", n, tc.text)
			}
		})
	}
}

// The one assertion that makes this guard's shape deliberate rather than
// copied. context.pressure.guard tells the model to answer "as if the note were
// not here", which is right for a note the model should ignore and WRONG here:
// the loop check is the one tail block entitled to change what the model does
// next. A full guard would silence the detector it belongs to.
//
// So the shape is partial — prohibit the narration, demand the action — and
// both halves are pinned. Dropping either one is a silent regression: without
// the prohibition the note steals the reply, and without the demand it becomes
// a note the model is told to ignore.
func TestStallGuardIsPartialNotFull(t *testing.T) {
	g := stallGuardText()
	if strings.Contains(g, "as if the note were not here") {
		t.Error("the stall guard borrowed context.pressure.guard's FULL wording; that tells the model to ignore the loop check, which neuters the detector")
	}
	if !strings.Contains(g, "Act on it") {
		t.Error("the partial guard must DEMAND the action, or it is only a silencer: the model would be told not to mention a note it was never told to obey")
	}
	if !strings.Contains(g, "Do not reply") || !strings.Contains(g, "do not mention it") {
		t.Error("the partial guard must still prohibit the narration; that is the failure it exists to prevent")
	}
}
