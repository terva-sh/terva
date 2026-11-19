package workspace

import (
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// 🚨 A recap of sub-agents is a user message that no person wrote. A member
// in a chain that no person began could start a sub-agent with a small task
// and then merge in the turn that the recap begins. That turn is unattended
// too. The control types the same words as a person does, and that turn
// merges.
func TestATurnThatARecapBeganIsNoPersonsTurn(t *testing.T) {
	p := &runScript{reply: "Pull request 12 is green."}
	w, seat, clock, f := mergeCrew(t, p, "workspace")
	approved(t, w, seat, ciTask)
	fire(t, w, clock, 16*time.Minute)
	waitRunEnds(t, w, "the end of the run", 1)
	// helm reads the run's report, so it works in the routine's chain.
	waitTalkoot(t, "helm to read the report", func() bool {
		rest := p.rest()
		m := memberView(t, w, "crew", "helm")
		return len(rest) > 0 && strings.Contains(rest[len(rest)-1], reportMark) && !m.Status.Working
	})
	s, err := w.resolve(memberView(t, w, "crew", "helm").Session)
	if err != nil {
		t.Fatal(err)
	}
	idle := func(what string, turns int) {
		t.Helper()
		waitTalkoot(t, what, func() bool {
			return len(p.rest()) > turns && !s.busyNow()
		})
	}

	const recap = "[auto-swarm update] 1 sub-agent(s) finished: the small task is done."
	p.set(func() { p.memberBash, p.memberBashOn = `{"command":"just pr-merge 12"}`, "the small task is done." })
	before, turns := f.trunk(t), len(p.rest())
	s.queueFrom(ctrlproto.QueueSourceSwarmRecap, recap)
	// Two requests: the one that calls bash, and the one after its result.
	idle("the end of the turn that the recap began", turns+1)
	if after := f.trunk(t); after != before {
		t.Fatalf("trunk moved from %s to %s in a turn that a recap began", before, after)
	}
	if rest := p.rest(); !strings.Contains(rest[len(rest)-1], mergeDenied) {
		t.Fatal("helm did not read the refusal of the merge rule")
	}

	// The control: a person types into the session.
	p.set(func() { p.memberBashOn = "Merge it now, please." })
	turns = len(p.rest())
	s.queue("Merge it now, please.")
	idle("the end of the turn that a person began", turns+1)
	if after := f.trunk(t); after == before {
		t.Fatal("a turn that a person typed did not merge, so this test cannot see a merge")
	}
}
