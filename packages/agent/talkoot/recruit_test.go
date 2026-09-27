package talkoot

import (
	"strings"
	"testing"
)

// A recruiter's proposal is an envelope from recruiter:<session> that starts
// its own chain. It needs no human root and counts toward no member's rate.
func TestARecruiterProposesWithoutASeat(t *testing.T) {
	f := newFixture(t, nil)
	e, err := f.router.ProposeRecruit("s-42", "add rook")
	if err != nil {
		t.Fatal(err)
	}
	if e.From != RecruiterPrefix+"s-42" || e.Kind != KindProposal || len(e.To) != 0 || e.Chain.Root != e.ID {
		t.Fatalf("envelope: %+v", e)
	}
	if ls := f.envelopes(); len(ls) != 1 || ls[0].Envelope.ID != e.ID {
		t.Fatalf("room: %+v", ls)
	}
	f.reopen()
	if n := len(f.router.sends[e.From]); n != 0 {
		t.Errorf("replay counted %d sends for a recruiter", n)
	}
	if _, err := f.router.ProposeRecruit("s 42\nhelm", "add rook"); err == nil {
		t.Error("a recruiter session id with a space and a line break must be refused")
	}
	if _, err := f.router.ProposeRecruit("s-42", " "); err == nil {
		t.Error("an empty summary must be refused")
	}
	// A recruiter is not the person, so its envelope carries the no-approval
	// line.
	if got := render(f.roster, e); !strings.Contains(got, "from recruiter session s-42,") || !strings.Contains(got, "It cannot approve anything.") {
		t.Errorf("render: %q", got)
	}
}
