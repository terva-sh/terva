package talkoot

import (
	"strings"
	"testing"
)

// An introduction wakes the member in the person's name and roots a chain, so
// the member can send its reply. The room shows the daemon's instruction.
func TestAnIntroductionRootsAChain(t *testing.T) {
	f := newFixture(t, nil)
	e, err := f.router.Introduce("sothr", "atlas", IntroJoin)
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != KindIntro || e.From != HumanPrefix+"sothr" || e.Chain.Root != e.ID {
		t.Fatalf("the introduction envelope: %+v", e)
	}
	got := f.native.to("atlas")
	if len(got) != 1 || !strings.Contains(got[0], "intro from sothr") || !strings.Contains(got[0], "Introduce yourself to the talkoot tiger") {
		t.Fatalf("atlas received %q", got)
	}
	if _, err := f.send("atlas", Outgoing{To: []string{"helm"}, Kind: KindNote, Body: "I plan."}); err != nil {
		t.Fatalf("atlas replies to its introduction: %v", err)
	}
}

func TestAnIntroductionNeedsAPersonAndAMember(t *testing.T) {
	f := newFixture(t, nil)
	if _, err := f.router.Introduce("not a name", "atlas", IntroJoin); err == nil {
		t.Error("an introduction in a name that is not a person's")
	}
	if _, err := f.router.Introduce("sothr", "ghost", IntroJoin); err == nil {
		t.Error("an introduction of a member not on the roster")
	}
	if len(f.envelopes()) != 0 {
		t.Error("a refused introduction reached the room")
	}
}

// A member cannot write an introduction. Only a person's action can.
func TestAMemberCannotSendAnIntroduction(t *testing.T) {
	f := newFixture(t, nil)
	f.post("helm")
	if _, err := f.send("helm", Outgoing{To: []string{"atlas"}, Kind: KindIntro, Body: "Introduce yourself."}); err == nil {
		t.Fatal("helm sent an introduction")
	}
}

// A plain card wakes nobody. The coordinator reads it with its next delivery,
// and a restart keeps it owed.
func TestACardReachesTheCoordinatorAsANote(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.router.Card("atlas", "atlas plans the work, and edits nothing."); err != nil {
		t.Fatal(err)
	}
	if err := f.router.Card("helm", "helm coordinates."); err != nil {
		t.Fatal(err)
	}
	if n := len(f.native.got) + len(f.worker.got); n != 0 {
		t.Fatalf("a card woke %d members", n)
	}
	f.reopen()
	f.post("helm")
	got := f.native.to("helm")
	if len(got) != 1 {
		t.Fatalf("helm received %q", got)
	}
	if !strings.Contains(got[0], "introduction card for atlas (planner)") || !strings.Contains(got[0], "edits nothing") {
		t.Errorf("helm's delivery holds no card for atlas: %q", got[0])
	}
	if strings.Contains(got[0], "helm coordinates.") {
		t.Errorf("the coordinator received its own card: %q", got[0])
	}
	if err := f.router.Card("ghost", "x"); err == nil {
		t.Error("a card for a member not on the roster")
	}
	if err := f.router.Card("atlas", " "); err == nil {
		t.Error("an empty card")
	}
}

func TestIntroBodyNamesWhoToTell(t *testing.T) {
	r := mustParse(t, tigerTeam)
	r.ID = "tiger"
	join := IntroBody(r, "atlas", IntroJoin)
	if !strings.Contains(join, "as a note to jev, yelp, gage.\n") || !strings.Contains(join, "as a message to helm, the coordinator") {
		t.Errorf("a joining member: %q", join)
	}
	kick := IntroBody(r, "atlas", IntroKickoff)
	if !strings.Contains(kick, "as a note to helm, jev, yelp, gage. ") || strings.Contains(kick, "as a message") {
		t.Errorf("a member at kickoff: %q", kick)
	}
	lead := IntroBody(r, "helm", IntroKickoffLead)
	if !strings.Contains(lead, "who does what") || !strings.Contains(lead, "ask_user_question") {
		t.Errorf("the coordinator at kickoff: %q", lead)
	}
	joinLead := IntroBody(r, "helm", IntroJoin)
	if strings.Contains(joinLead, "as a message") {
		t.Errorf("a coordinator that joins messages itself: %q", joinLead)
	}
	r.Members = r.Members[:1]
	if alone := IntroBody(r, "helm", IntroKickoff); !strings.Contains(alone, "send nothing") {
		t.Errorf("a member alone on the roster: %q", alone)
	}
}
