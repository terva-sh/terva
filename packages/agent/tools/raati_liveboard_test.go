package tools

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/raati"
	"terva.sh/terva/packages/vote"
)

func newTestBoard() (*raatiLiveBoard, func() string) {
	last := ""
	b := newRaatiLiveBoard("käräjät", "gate", "convene", raati.DefaultPanel(), func(s string) { last = s })
	return b, func() string { return last }
}

func ballot(unit, verdict string, conf float64) *vote.Ballot {
	return &vote.Ballot{Unit: unit, Verdict: vote.Verdict(verdict), Confidence: conf}
}

// The board is informative from the FIRST frame. Before this, a convening
// showed nothing at all until the first ballot landed — minutes of wall clock
// against an empty box.
func TestLiveBoardSeedsEverySeatBeforeAnyEvent(t *testing.T) {
	_, last := newTestBoard()
	// Nothing has happened yet, so nothing has been emitted; the first
	// narration is what paints it.
	if last() != "" {
		t.Fatalf("emitted before any event: %q", last())
	}
	b, last := newTestBoard()
	b.Narrate("YATA-1 takes the seat…")
	got := last()
	for _, u := range raati.DefaultPanel() {
		if !strings.Contains(got, u.Name) {
			t.Errorf("seat %s missing from the first frame:\n%s", u.Name, got)
		}
	}
	if !strings.Contains(got, "käräjät") || !strings.Contains(got, "gate") || !strings.Contains(got, "convene") {
		t.Errorf("the convening's shape is not on the board:\n%s", got)
	}
}

// The whole complaint: one line, replaced. Narration is now the board's status
// line, so the seats survive it.
func TestNarrationDoesNotReplaceTheBoard(t *testing.T) {
	b, last := newTestBoard()
	b.Event(raati.Event{Kind: raati.EventSeated, Unit: "YATA-1", Binding: "anthropic/claude-opus-4-1"})
	b.Narrate("YATA-1 has cast its ballot: reject (confidence 0.88)")

	got := last()
	if !strings.Contains(got, "YATA-1 has cast its ballot") {
		t.Errorf("narration lost:\n%s", got)
	}
	if !strings.Contains(got, "anthropic/claude-opus-4-1") {
		t.Errorf("narration replaced the seats instead of joining them:\n%s", got)
	}
	if !strings.Contains(got, "KUSANAGI-2") {
		t.Errorf("the other seats vanished:\n%s", got)
	}
}

func TestLiveBoardFoldsTheDeliberation(t *testing.T) {
	b, last := newTestBoard()
	b.Event(raati.Event{Kind: raati.EventRound, Round: 1})
	b.Event(raati.Event{Kind: raati.EventSeated, Unit: "YATA-1", Binding: "anthropic/claude-opus-4-1"})
	b.Event(raati.Event{Kind: raati.EventVoted, Unit: "YATA-1", Round: 1, Ballot: ballot("YATA-1", "reject", 0.88)})
	b.Event(raati.Event{Kind: raati.EventAbsent, Unit: "MAGATAMA-3", Why: "round deadline"})
	b.Event(raati.Event{Kind: raati.EventInquiry, Unit: "YATA-1", Source: raati.SourceRecord})
	b.Event(raati.Event{Kind: raati.EventInquiry, Unit: "KUSANAGI-2", Source: raati.SourceUnanswered})

	got := last()
	for _, want := range []string{
		"round 1",
		"reject", "0.88", // a ballot without its confidence is half a ballot
		"absent — round deadline",
		"the panel asked 2",
		"(1 open)", // open questions are unmet evidence, not a footnote
	} {
		if !strings.Contains(got, want) {
			t.Errorf("board missing %q:\n%s", want, got)
		}
	}
}

// seat_order "turn" re-seats a unit on a NEW binding for the final round. Its
// previous round's verdict is not this seat's answer any more, and a board
// still showing it would report a ballot that no longer exists.
func TestReseatClearsThePreviousVerdict(t *testing.T) {
	b, last := newTestBoard()
	b.Event(raati.Event{Kind: raati.EventSeated, Unit: "YATA-1", Binding: "anthropic/claude-opus-4-1"})
	b.Event(raati.Event{Kind: raati.EventVoted, Unit: "YATA-1", Round: 1, Ballot: ballot("YATA-1", "reject", 0.88)})
	if !strings.Contains(last(), "0.88") {
		t.Fatalf("ballot not shown:\n%s", last())
	}

	b.Event(raati.Event{Kind: raati.EventSeated, Unit: "YATA-1", Binding: "kimi/k3"})
	got := last()
	if strings.Contains(got, "0.88") {
		t.Errorf("a reseated unit still shows its old round's ballot:\n%s", got)
	}
	if !strings.Contains(got, "kimi/k3") {
		t.Errorf("reseat did not move the binding:\n%s", got)
	}
}

// A live view folded behind "… 4 more lines, ctrl+o to expand" is not a live
// view. The TUI collapses a tool body past tui.ToolCollapseLines (12) — the
// bound is asserted here rather than imported, because a tool package has no
// business importing the UI; it is the shape of the box this has to fit.
func TestLiveBoardFitsTheToolBoxUncollapsed(t *testing.T) {
	const toolCollapseLines = 12
	b, last := newTestBoard()
	for i, u := range raati.DefaultPanel() {
		b.Event(raati.Event{Kind: raati.EventSeated, Unit: u.Name, Binding: "some-provider/some-model-4-8"})
		b.Event(raati.Event{Kind: raati.EventVoted, Unit: u.Name, Round: 2, Ballot: ballot(u.Name, "abstain", float64(i)/10)})
		b.Event(raati.Event{Kind: raati.EventInquiry, Unit: u.Name, Source: raati.SourceUnanswered})
	}
	b.Event(raati.Event{Kind: raati.EventRound, Round: 3})
	b.Narrate("cross-examination changed positions — the panel converges")

	if n := len(strings.Split(last(), "\n")); n > toolCollapseLines {
		t.Errorf("board is %d lines, over the %d the tool box shows uncollapsed:\n%s", n, toolCollapseLines, last())
	}
}

func TestLiveBoardNilIsInert(t *testing.T) {
	var b *raatiLiveBoard
	b.Narrate("x")
	b.Event(raati.Event{Kind: raati.EventRound, Round: 1})
}
