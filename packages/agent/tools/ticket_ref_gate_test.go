package tools

// Criterion 5 of TKT-01M26CWP. The store-qualified ref grammar rests on a
// property of git-ticket rather than of terva: the library splits a reference
// at the first colon, so `<store>/<id>` lands whole in the identifier and
// `check --strict` passes over it. Nothing in terva enforces that, and the
// version that provides it is a pin in go.mod.
//
// The ticket's first pass recorded this as a standing obligation, "re-verified
// whenever the git-ticket pin moves". That is not a state a ticket can reach,
// and it relies on somebody remembering at the moment they bump a version.
// This test is the obligation instead. A pin that stops taking a qualified ref
// fails here, in both CI lanes, on the commit that moves it.

import (
	"context"
	"runtime/debug"
	"strings"
	"testing"

	ticket "github.com/terva-sh/git-ticket/ticket"
)

// The qualified form survives the pinned check. A failure here means the pin
// moved to a version that reads `ticket:<store>/<id>` differently, and the
// grammar in .tickets/CONVENTIONS.md has to be re-decided rather than patched.
func TestQualifiedRefPassesThePinnedCheck(t *testing.T) {
	const ref = "ticket:work/TKT-01M26CWPN8T2YG3AFMRJPEGW3Y"
	dir := seedStore(t, "The store the ref is written in", nil)
	c := &TicketCore{CWD: dir, ActorID: "agent:test", Card: &TicketCard{CWD: dir}}
	id := seedTicketWithRefs(t, c, ref)

	// A check that finds nothing is the pass condition here, so a store with
	// no qualified ref in it would pass for the wrong reason. Read the ref
	// back off the disk before trusting a clean report.
	if body := ticketToolText(t, runTicketTool(t, &TicketGetTool{TicketCore: c}, map[string]any{"ref": id})); !strings.Contains(body, ref) {
		t.Fatalf("the store holds no qualified ref, so a clean check would prove nothing: %s", body)
	}

	rep := checkStore(t, dir)

	if len(rep.Errors) > 0 || len(rep.Warnings) > 0 {
		t.Errorf("git-ticket %s refuses a qualified ref that the grammar depends on.\n"+
			"errors: %s\nwarnings: %s\n"+
			"Re-read \"Naming a ticket in another store\" in docs/proposals/ticket-agent-workflow.md before changing either side.",
			gitTicketPin(), findingText(rep.Errors), findingText(rep.Warnings))
	}
}

// The control for the test above. `check` must actually read the references
// field, or a clean pass over the qualified ref proves only that nobody looked.
// An untyped reference in the same position earns reference_untyped.
//
// It lives in its own store, because a finding here would otherwise be a
// finding there.
func TestUntypedRefStillFailsThePinnedCheck(t *testing.T) {
	dir := seedStore(t, "The control store", nil)
	c := &TicketCore{CWD: dir, ActorID: "agent:test", Card: &TicketCard{CWD: dir}}
	seedTicketWithRefs(t, c, "PROJ-1234")

	rep := checkStore(t, dir)

	found := false
	for _, f := range append(append([]ticket.Finding{}, rep.Errors...), rep.Warnings...) {
		if f.Code == "reference_untyped" {
			found = true
		}
	}
	if !found {
		t.Errorf("git-ticket %s reports no reference_untyped for an untyped ref, so the check no longer reads the references field.\n"+
			"A clean result in TestQualifiedRefPassesThePinnedCheck then proves nothing.\nerrors: %s\nwarnings: %s",
			gitTicketPin(), findingText(rep.Errors), findingText(rep.Warnings))
	}
}

func checkStore(t *testing.T, dir string) *ticket.Report {
	t.Helper()
	s, err := ticket.Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := s.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func findingText(fs []ticket.Finding) string {
	if len(fs) == 0 {
		return "none"
	}
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Code+" "+f.Field+" "+f.Message)
	}
	return strings.Join(out, "; ")
}

// gitTicketPin names the version under test, so a failure says which bump
// broke the grammar rather than leaving the reader to find it.
func gitTicketPin() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "(version unknown)"
	}
	for _, d := range info.Deps {
		if d.Path == "github.com/terva-sh/git-ticket" {
			return d.Version
		}
	}
	return "(version unknown)"
}
