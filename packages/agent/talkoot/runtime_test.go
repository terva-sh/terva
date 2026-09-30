package talkoot

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func TestObserveSeesEverySealedLine(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := CreateRoom(dir); err != nil {
		t.Fatal(err)
	}
	r := OpenRoom(dir)
	var got []Line
	r.Observe(func(l Line) { got = append(got, l) })
	for _, m := range []string{"jev", "gage"} {
		if err := r.Append(Line{Type: LineTurn, Member: m, CostUSD: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 2 || got[0].Member != "jev" || got[1].Member != "gage" {
		t.Fatalf("want both lines in order, got %+v", got)
	}
	if got[0].Kid != "" || got[0].MAC != "" {
		t.Errorf("an observer sees the line, not its seal: %+v", got[0])
	}
}

// A line that never reached the room must not reach an observer either.
func TestObserveSkipsAFailedAppend(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := CreateRoom(dir); err != nil {
		t.Fatal(err)
	}
	r := OpenRoom(dir)
	var got []Line
	r.Observe(func(l Line) { got = append(got, l) })
	if err := os.MkdirAll(filepath.Join(dir, HeadTempFile, "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := r.Append(Line{Type: LineTurn, Member: "jev"}); err == nil {
		t.Fatal("want the append to fail while its head cannot be written")
	}
	if len(got) != 0 {
		t.Errorf("an observer saw a line the room did not keep: %+v", got)
	}
}

// 🚨 A router stopped for shutdown writes no delivery line, so the next one
// still owes the envelope. A turn that ends after the stop is still recorded,
// or its spend would vanish with the restart.
func TestAStoppedRouterRecordsTurnsAndOwesDeliveries(t *testing.T) {
	f := newFixture(t, nil)
	f.router.Stop()
	f.post()
	if got := f.native.to("helm"); len(got) != 0 {
		t.Fatalf("a stopped router delivered: %q", got)
	}
	if err := f.router.TurnEnded("jev", 1.5); err != nil {
		t.Fatal(err)
	}
	turns := 0
	for _, l := range f.lines() {
		if l.Type == LineTurn && l.Member == "jev" && l.CostUSD == 1.5 {
			turns++
		}
	}
	if turns != 1 {
		t.Fatalf("the turn after the stop was not recorded: %+v", f.lines())
	}
	f.reopen()
	f.router.Release()
	if got := f.native.to("helm"); len(got) != 1 || !strings.Contains(got[0], "Plan the lake schema.") {
		t.Errorf("the next router must deliver the owed envelope once, got %q", got)
	}
}

// The workspace writes seat and roster lines into the room. The router must
// read past them: no damage, no pause, no spend.
func TestReplayIgnoresSeatAndRosterLines(t *testing.T) {
	f := newFixture(t, nil)
	for _, l := range []Line{
		{Type: LineSeat, Member: "helm", Ref: "20260925-abc"},
		{Type: LineRoster, By: "human:sothr", Ref: "0123abcd"},
		{Type: LineSeat, Member: "helm"},
	} {
		if err := f.router.room.Append(l); err != nil {
			t.Fatal(err)
		}
	}
	f.reopen()
	for _, s := range f.router.Statuses() {
		if s.Paused != "" || s.SpendUSD != 0 || s.Working {
			t.Errorf("a seat or roster line changed %s: %+v", s.Member, s)
		}
	}
}

// 🚨 A turn still running when the daemon stops never reports its cost. It
// counts as free, and the member pauses until a person looks, after a
// restart too. Otherwise a spend cap would forget that turn.
func TestAnUnreportedTurnPausesTheMember(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.router.TurnUnreported("jev", "the turn had not reported its cost when the daemon stopped"); err != nil {
		t.Fatal(err)
	}
	paused := func(when string) {
		t.Helper()
		for _, s := range f.router.Statuses() {
			if s.Member == "jev" {
				if !strings.Contains(s.Paused, "had not reported its cost") {
					t.Errorf("%s: want jev paused for the unreported turn, got %q", when, s.Paused)
				}
				if s.Turns != 1 || s.SpendUSD != 0 {
					t.Errorf("%s: want one free turn, got %d turns and $%v", when, s.Turns, s.SpendUSD)
				}
			}
		}
	}
	paused("live")
	f.reopen()
	paused("after a restart")
}

// A turn whose process stopped is charged what it spent, and the member
// pauses with the reason, live and after a restart. A bad cost is charged as
// nothing, and the reason says so.
func TestAStoppedTurnIsChargedAndPauses(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.router.TurnStopped("jev", 0.25, "the worker stopped before its turn ended"); err != nil {
		t.Fatal(err)
	}
	check := func(when string) {
		t.Helper()
		for _, s := range f.router.Statuses() {
			if s.Member != "jev" {
				continue
			}
			if !strings.Contains(s.Paused, "the worker stopped before its turn ended") {
				t.Errorf("%s: paused %q, want the stop", when, s.Paused)
			}
			if s.Turns != 1 || s.SpendUSD != 0.25 {
				t.Errorf("%s: %d turns and $%v, want one turn charged $0.25", when, s.Turns, s.SpendUSD)
			}
		}
	}
	check("live")
	f.reopen()
	check("after a restart")

	g := newFixture(t, nil)
	if err := g.router.TurnStopped("jev", math.NaN(), "stopped"); err != nil {
		t.Fatal(err)
	}
	for _, s := range g.router.Statuses() {
		if s.Member == "jev" && (s.SpendUSD != 0 || !strings.Contains(s.Paused, "turn cost of NaN")) {
			t.Errorf("a NaN cost: spend $%v, paused %q", s.SpendUSD, s.Paused)
		}
	}
}

// 🚨 A member an update removed can still be mid-turn, and its turn spent the
// team's money. The new router counts it toward the talkoot's budget, live
// and in replay, or every removal would lower the team's spend for the day.
func TestAFormerMembersTurnCountsTowardTheTeam(t *testing.T) {
	f := newFixture(t, func(r *Roster, _ *Limits) {
		r.BudgetUSDPerDay = 5
		for i := range r.Members {
			r.Members[i].BudgetUSDPerDay = 0
		}
	})
	if err := f.router.TurnEnded("jev", 2); err != nil {
		t.Fatal(err)
	}
	// The update: jev leaves, and the router is rebuilt over the same room.
	var kept []Member
	for _, m := range f.roster.Members {
		if m.ID != "jev" {
			kept = append(kept, m)
		}
	}
	f.roster.Members = kept
	f.reopen()
	// jev's turn from before the update ends now, through the new router.
	if err := f.router.TurnEnded("jev", 2); err != nil {
		t.Fatalf("the new router refused the turn of a member that left: %v", err)
	}
	if err := f.router.TurnEnded("helm", 1.5); err != nil {
		t.Fatal(err)
	}
	teamPaused := func(when string) {
		t.Helper()
		for _, s := range f.router.Statuses() {
			if s.Member == "helm" && !strings.Contains(s.Paused, "of the talkoot's") {
				t.Errorf("%s: want the team budget tripped at $5.50 of $5, got %q", when, s.Paused)
			}
		}
	}
	teamPaused("live")
	f.reopen()
	teamPaused("after a restart")
	if err := f.router.TurnEnded("../x", 1); err == nil {
		t.Error("want a turn for a name that is no member id refused")
	}
}

// A queued turn is charged and counted, and the member's working slot stays
// as it is.
func TestAQueuedTurnIsChargedAndHoldsNoSlot(t *testing.T) {
	f := newFixture(t, nil)
	f.post()
	status := func() Status {
		for _, s := range f.router.Statuses() {
			if s.Member == "helm" {
				return s
			}
		}
		t.Fatal("no status for helm")
		return Status{}
	}
	if !status().Working {
		t.Fatal("the post left helm idle")
	}
	if err := f.router.QueuedTurnEnded("helm", 0.2); err != nil {
		t.Fatal(err)
	}
	s := status()
	if !s.Working {
		t.Error("a queued turn freed helm's working slot")
	}
	if s.Turns != 1 || s.SpendUSD != 0.2 {
		t.Errorf("%d turns and $%v, want one turn charged $0.2", s.Turns, s.SpendUSD)
	}
	f.reopen()
	if s := status(); s.SpendUSD != 0.2 {
		t.Errorf("after a restart, $%v, want $0.2", s.SpendUSD)
	}
}

// FreeSlot frees a working member's slot and records no turn. On a member
// that holds no slot it does nothing.
func TestFreeSlotFreesTheSlotWithoutATurn(t *testing.T) {
	f := newFixture(t, nil)
	f.post()
	status := func() Status {
		for _, s := range f.router.Statuses() {
			if s.Member == "helm" {
				return s
			}
		}
		t.Fatal("no status for helm")
		return Status{}
	}
	if err := f.router.FreeSlot("helm"); err != nil {
		t.Fatal(err)
	}
	if s := status(); s.Working || s.Turns != 0 {
		t.Errorf("working %v with %d turns, want the slot free and no turn", s.Working, s.Turns)
	}
	if err := f.router.FreeSlot("helm"); err != nil {
		t.Fatal(err)
	}
	if err := f.router.FreeSlot("Not An Id"); err == nil {
		t.Error("FreeSlot took a name that is not a member id")
	}
}

// A queued turn whose process stopped is charged, pauses the member, and
// leaves its working slot as it is.
func TestAStoppedQueuedTurnIsChargedAndPauses(t *testing.T) {
	f := newFixture(t, nil)
	f.post()
	if err := f.router.QueuedTurnStopped("helm", 0.2, "the worker stopped before its turn ended"); err != nil {
		t.Fatal(err)
	}
	for _, s := range f.router.Statuses() {
		if s.Member != "helm" {
			continue
		}
		if !s.Working || s.SpendUSD != 0.2 || !strings.Contains(s.Paused, "stopped before its turn ended") {
			t.Errorf("working %v, $%v, paused %q; want still working, $0.2, paused", s.Working, s.SpendUSD, s.Paused)
		}
	}
}
