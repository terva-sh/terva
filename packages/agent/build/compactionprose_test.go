package build

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/core/contextpressure"
)

// terva's note leads with the do-not-reply guard and says what to do instead.
// The note is a last-in-turn ephemeral block, the shape that ended 80 of 80
// transcripts with the model answering the NOTE instead of the user, and
// prohibition-first is what measured 20 of 20 answers back on the sibling
// note. ORDER is the assertion.
func TestTervasPressureNoteLeadsWithTheGuard(t *testing.T) {
	note := CompactionPolicy{}.PressureNote(contextpressure.State{Fraction: 0.72, Band: 1, Used: 144_000, Window: 200_000, Compacts: true})
	guard, gauge := strings.Index(note, "Do not reply to this note"), strings.Index(note, "% full")
	if guard < 0 || gauge < 0 || guard > gauge {
		t.Errorf("want the guard before the gauge: %q", note)
	}
	if !strings.Contains(note, "as if the note were not here") {
		t.Errorf("the guard does not tell the model what to do instead: %q", note)
	}
}

// One text for the whole ladder was the tonal defect behind the rewrite:
// entering at 70% read exactly as urgently as arriving at 86%. Each band has
// its own advice, and the note for a band carries that band's.
func TestTervasAdviceGraduatesWithTheBand(t *testing.T) {
	seen := map[string]bool{}
	for band := 1; band <= 4; band++ {
		a := contextPressureAdvice(band)
		if a == "" || seen[a] {
			t.Errorf("band %d has no advice of its own: %q", band, a)
		}
		seen[a] = true
	}
	note := CompactionPolicy{}.PressureNote(contextpressure.State{Fraction: 0.86, Band: 3, Used: 172_000, Window: 200_000, Compacts: true})
	if !strings.Contains(note, contextPressureAdvice(3)) || strings.Contains(note, contextPressureAdvice(1)) {
		t.Errorf("an 86%% note did not carry band 3's advice alone: %q", note)
	}
}

// With automatic compaction off there is no valve, and telling the model one
// exists invites it to defer to an intervention that never comes. The note
// says so, and names /compact at both tiers so the model has a move.
func TestTervasPressureNoteRespectsCompactionOff(t *testing.T) {
	for _, band := range []int{1, 4} {
		note := CompactionPolicy{}.PressureNote(contextpressure.State{Fraction: 0.75, Band: band, Used: 150_000, Window: 200_000})
		if strings.Contains(note, "terva compacts") {
			t.Errorf("band %d: the off-mode note promises compaction: %q", band, note)
		}
		if !strings.Contains(note, "automatic compaction off") || !strings.Contains(note, "/compact") {
			t.Errorf("band %d: the off-mode note should say compaction is off and name /compact: %q", band, note)
		}
	}
}

// A SHELL COMMAND IS NOT ONE ACTION: a failed pipeline may have run its earlier
// stages (measured: twelve failed bash calls in compaction ledgers, all
// composite, six of them `gofmt -w <file> && go test <pkg>`). For a tool that
// either applies or does not, "nothing happened" stays exactly right. The shell
// note is BashTool's own; terva's policy supplies the other.
func TestTervasLedgerFailureNotes(t *testing.T) {
	shell, edit := (&tools.BashTool{}).LedgerFailed(), ledgerFailed("edit")
	if strings.Contains(shell, "does NOT exist") || !strings.Contains(shell, "may still have taken effect") {
		t.Errorf("the shell note claims a composite command had no effect: %q", shell)
	}
	if !strings.Contains(edit, "does NOT exist") {
		t.Errorf("a failed edit writes no bytes and the note must keep saying so: %q", edit)
	}
}

// terva's policy makes the same decisions as the engine's default: only the
// words differ.
func TestTervasPolicyDecidesLikeTheDefault(t *testing.T) {
	for _, s := range []core.CompactionState{
		{Point: core.CompactAfterTurn, Fraction: 0.9, Messages: 10},
		{Point: core.CompactMidTurn, Fraction: 0.5, Messages: 10},
		{Point: core.CompactOversize},
	} {
		if got, want := (CompactionPolicy{}).Decide(s), (core.DefaultCompactionPolicy{}).Decide(s); got.Compact != want.Compact || got.KeepTail != want.KeepTail {
			t.Errorf("%s: terva decided %+v, the default %+v", s.Point, got, want)
		}
	}
}
