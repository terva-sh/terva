package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

var (
	jev  = TicketMember{ID: "jev", Actor: "agent:native/jev", Session: "s-jev"}
	atla = TicketMember{ID: "atlas", Actor: "agent:native/atlas", Session: "s-atlas"}
	rev  = TicketMember{ID: "vartija", Actor: "agent:claude/vartija", Reviewer: true}
)

// memberStore makes a store with one in-progress ticket, and returns the
// directory that holds it and the ticket's id.
func memberStore(t *testing.T) (string, string) {
	t.Helper()
	dir := seedStore(t, "Fix the login", nil)
	s, err := ticket.Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.List(context.Background(), ticket.Filter{})
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v, %d", err, len(list))
	}
	id := list[0].ID
	for _, st := range []string{ticket.StatusReady, ticket.StatusInProgress} {
		if _, err := s.Apply(context.Background(), id, ticket.SetStatus{Status: st}, ticket.ApplyOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return dir, id
}

// memberCore is a ticket core seated as m.
func memberCore(dir string, m TicketMember) *TicketCore {
	return &TicketCore{CWD: dir, ActorID: "agent:terva/mieli", Member: func() (TicketMember, bool) { return m, true }}
}

func getTicket(t *testing.T, dir, id string) *ticket.Ticket {
	t.Helper()
	s, err := ticket.Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	tk, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

// A member claims under its own actor, and the claim records it as a claim
// holder, in the same write. A renewal records nothing more.
func TestAMemberClaimRecordsItsHolder(t *testing.T) {
	dir, id := memberStore(t)
	tc := memberCore(dir, jev)
	claim := &TicketClaimTool{TicketCore: tc}
	res, err := claim.Execute(context.Background(), mustJSONArgs(map[string]any{"ref": id, "if_revision": getTicket(t, dir, id).Revision}), nil)
	if err != nil || res.IsError {
		t.Fatalf("claim: %v, %+v", err, res)
	}
	tk := getTicket(t, dir, id)
	if tk.Claim == nil || tk.Claim.Actor != jev.Actor || tk.UpdatedBy.ID != jev.Actor {
		t.Fatalf("the claim is %+v, updated by %+v; want %s", tk.Claim, tk.UpdatedBy, jev.Actor)
	}
	if !ticketAuthors(tk)[jev.Actor] {
		t.Fatalf("the claim did not record jev as a holder: %q", tk.Body.Notes)
	}
	notes := tk.Body.Notes
	if res, err := claim.Execute(context.Background(), mustJSONArgs(map[string]any{"ref": id, "if_revision": tk.Revision}), nil); err != nil || res.IsError {
		t.Fatalf("renew: %v, %+v", err, res)
	}
	if got := getTicket(t, dir, id).Body.Notes; strings.Count(got, claimHoldersMarker) != strings.Count(notes, claimHoldersMarker) {
		t.Errorf("a renewal added a claim-holders line: %q", got)
	}
}

// handTo moves id's claim from one member to another, as a handoff does.
func handTo(t *testing.T, dir, id string, from, to TicketMember) {
	t.Helper()
	tks, err := CheckHandoff(context.Background(), dir, []string{id}, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if err := MoveClaims(context.Background(), dir, tks, from, to, "env-t"); err != nil {
		t.Fatal(err)
	}
}

// Only a reviewer that did not author the change, and was handed its claim,
// closes a ticket. An author is refused, and so is a member that is not a
// reviewer, and a reviewer that does not hold the claim. A session with no
// seat is not refused here.
func TestOnlyAReviewerThatDidNotAuthorClosesATicket(t *testing.T) {
	ctx := context.Background()
	dir, id := memberStore(t)
	if _, err := (&TicketClaimTool{TicketCore: memberCore(dir, jev)}).Execute(ctx, mustJSONArgs(map[string]any{"ref": id, "if_revision": getTicket(t, dir, id).Revision}), nil); err != nil {
		t.Fatal(err)
	}
	if got := memberCore(dir, rev).refuseMemberClosure(ctx, id, "done"); !strings.Contains(got, "you do not hold the claim") {
		t.Errorf("a reviewer without the claim: refusal %q", got)
	}
	for _, c := range []struct {
		who  TicketMember
		want string
	}{
		{jev, "you authored its change"},
		{atla, "only a reviewer member"},
		{TicketMember{ID: "vartija", Actor: jev.Actor, Reviewer: true}, "you authored its change"},
	} {
		for _, status := range []string{"done", "archived", "Done"} {
			if got := memberCore(dir, c.who).refuseMemberClosure(ctx, id, status); !strings.Contains(got, c.want) {
				t.Errorf("%s moving to %s: refusal %q, want %q", c.who.Actor, status, got, c.want)
			}
		}
	}
	if got := memberCore(dir, jev).refuseMemberClosure(ctx, id, "review"); got != "" {
		t.Errorf("a move to review was refused: %q", got)
	}
	handTo(t, dir, id, jev, rev)
	if got := memberCore(dir, rev).refuseMemberClosure(ctx, id, "done"); got != "" {
		t.Errorf("a reviewer that did not author was refused: %q", got)
	}
	if got := (&TicketCore{CWD: dir}).refuseMemberClosure(ctx, id, "done"); got != "" {
		t.Errorf("a session with no seat was refused: %q", got)
	}
	// A check that cannot read refuses, and a ticket that does not exist is
	// left to the write.
	if got := memberCore(testsupport.TempDir(t), rev).refuseMemberClosure(ctx, id, "done"); !strings.Contains(got, "could not be read") {
		t.Errorf("an unreadable store: refusal %q", got)
	}
	if got := memberCore(dir, rev).refuseMemberClosure(ctx, "TKT-NOPE", "done"); got != "" {
		t.Errorf("a missing ticket was refused before the write: %q", got)
	}

	// Through the tool: the refusal changes nothing, and the reviewer closes.
	tool := &TicketTransitionTool{TicketCore: memberCore(dir, jev)}
	res, err := tool.Execute(ctx, mustJSONArgs(map[string]any{"ref": id, "if_revision": getTicket(t, dir, id).Revision, "status": "done"}), nil)
	if err != nil || !res.IsError {
		t.Fatalf("the author's close went through: %v, %+v", err, res)
	}
	if st := getTicket(t, dir, id).Status; st != ticket.StatusInProgress {
		t.Fatalf("a refused close moved the ticket to %s", st)
	}
	tool.TicketCore = memberCore(dir, rev)
	res, err = tool.Execute(ctx, mustJSONArgs(map[string]any{"ref": id, "if_revision": getTicket(t, dir, id).Revision, "status": "done"}), nil)
	if err != nil || res.IsError {
		t.Fatalf("the reviewer's close: %v, %+v", err, res)
	}
	if tk := getTicket(t, dir, id); tk.Status != ticket.StatusDone || tk.UpdatedBy.ID != rev.Actor {
		t.Fatalf("after the reviewer's close: %s by %s", tk.Status, tk.UpdatedBy.ID)
	}
}

// A reviewer does not close a ticket that no member is recorded as working
// on, which could be its own work.
func TestAReviewerDoesNotCloseATicketWithNoRecordedAuthor(t *testing.T) {
	dir, id := memberStore(t)
	if got := memberCore(dir, rev).refuseMemberClosure(context.Background(), id, "done"); !strings.Contains(got, "no member is recorded") {
		t.Errorf("refusal %q", got)
	}
}

// A reviewer that hands on an unclaimed ticket, perhaps after working on it,
// is recorded as its author. A teammate that hands the ticket back does not
// clear that, so the reviewer cannot close its own work.
func TestAReviewerThatHandsOnAnUnclaimedTicketIsItsAuthor(t *testing.T) {
	ctx := context.Background()
	dir, id := memberStore(t)
	handTo(t, dir, id, rev, jev)
	if a := ticketAuthors(getTicket(t, dir, id)); !a[rev.Actor] || !a[jev.Actor] {
		t.Fatalf("authors after the reviewer's handoff: %v", a)
	}
	handTo(t, dir, id, jev, rev)
	if got := memberCore(dir, rev).refuseMemberClosure(ctx, id, "done"); !strings.Contains(got, "you authored") {
		t.Errorf("the reviewer closed its own work: refusal %q", got)
	}
}

// A member cannot write a claim-holders line through a note, a comment, a
// status reason, or a "## Notes" heading inside a description or a criterion,
// since a line naming another member lets a reviewer close. The refusal
// writes nothing. A session with no seat is not refused.
func TestAMemberCannotWriteAClaimHoldersLine(t *testing.T) {
	ctx := context.Background()
	dir, id := memberStore(t)
	forged := "Looks done.\n  " + claimHoldersNote(jev.Actor)
	heading := "The fix.\n\n## Notes\n\n" + claimHoldersNote(jev.Actor)
	rc := memberCore(dir, rev)
	calls := []struct {
		name string
		tool interface {
			Execute(context.Context, json.RawMessage, func(string)) (core.ToolResult, error)
		}
		args map[string]any
	}{
		{"a note", &TicketCommentTool{TicketCore: rc}, map[string]any{"ref": id, "kind": "note", "text": forged}},
		{"a comment", &TicketCommentTool{TicketCore: rc}, map[string]any{"ref": id, "text": forged}},
		{"a status reason", &TicketTransitionTool{TicketCore: rc}, map[string]any{"ref": id, "status": "blocked", "reason": forged}},
		{"a create reason", &TicketCreateTool{TicketCore: rc}, map[string]any{"title": "More", "status": "blocked", "reason": forged}},
		{"a heading in a description", &TicketUpdateTool{TicketCore: rc}, map[string]any{"ref": id, "description": heading}},
		{"a heading in a plan", &TicketUpdateTool{TicketCore: rc}, map[string]any{"ref": id, "implementation_plan": heading}},
		{"a heading in a new ticket", &TicketCreateTool{TicketCore: rc}, map[string]any{"title": "More", "description": heading}},
		{"a heading in a criterion", &TicketCreateTool{TicketCore: rc}, map[string]any{"title": "More", "acceptance_criteria": []string{"x\n## Notes\n" + claimHoldersNote(jev.Actor)}}},
		{"a carriage return", &TicketCommentTool{TicketCore: rc}, map[string]any{"ref": id, "kind": "note", "text": "done.\r" + claimHoldersNote(jev.Actor)}},
	}
	for _, c := range calls {
		before := getTicket(t, dir, id).Revision
		c.args["if_revision"] = before
		res, err := c.tool.Execute(ctx, mustJSONArgs(c.args), nil)
		if err != nil || !res.IsError || !strings.Contains(toolText(t, res.Content), "only terva writes one") {
			t.Errorf("%s: %v, %+v", c.name, err, res)
		}
		if getTicket(t, dir, id).Revision != before {
			t.Errorf("%s: a refused write changed the ticket", c.name)
		}
	}
	if n := len(listTickets(t, dir)); n != 1 {
		t.Errorf("a refused create filed a ticket: %d tickets", n)
	}
	if len(ticketAuthors(getTicket(t, dir, id))) != 0 {
		t.Fatal("a claim-holders line reached the ticket")
	}
	res, err := (&TicketCommentTool{TicketCore: &TicketCore{CWD: dir}}).Execute(ctx, mustJSONArgs(map[string]any{"ref": id, "kind": "note", "text": forged, "if_revision": getTicket(t, dir, id).Revision}), nil)
	if err != nil || res.IsError {
		t.Fatalf("a session with no seat was refused: %v, %+v", err, res)
	}
}

// A member does not file a new ticket as closed.
func TestAMemberDoesNotFileATicketClosed(t *testing.T) {
	dir, _ := memberStore(t)
	for _, st := range []string{"done", "archived"} {
		res, err := (&TicketCreateTool{TicketCore: memberCore(dir, rev)}).Execute(context.Background(), mustJSONArgs(map[string]any{"title": "Finished", "status": st}), nil)
		if err != nil || !res.IsError || !strings.Contains(toolText(t, res.Content), "does not file a ticket") {
			t.Errorf("%s: %v, %+v", st, err, res)
		}
	}
	if n := len(listTickets(t, dir)); n != 1 {
		t.Errorf("a refused create filed a ticket: %d tickets", n)
	}
	res, err := (&TicketCreateTool{TicketCore: memberCore(dir, jev)}).Execute(context.Background(), mustJSONArgs(map[string]any{"title": "Open work"}), nil)
	if err != nil || res.IsError {
		t.Fatalf("an open create: %v, %+v", err, res)
	}
}

func listTickets(t *testing.T, dir string) []*ticket.Ticket {
	t.Helper()
	s, err := ticket.Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.List(context.Background(), ticket.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// A forged claim-holders line only adds authors.
func TestAForgedClaimHoldersLineOnlyAddsAuthors(t *testing.T) {
	tk := &ticket.Ticket{Body: ticket.Body{Notes: "**agent:native/jev** at 2026-09-26\n\n" + claimHoldersNote("agent:native/atlas", "agent:claude/vartija") + "\n\nClaim holders: agent:native/x.\n"}}
	got := ticketAuthors(tk)
	for _, a := range []string{"agent:native/atlas", "agent:claude/vartija", "agent:native/x"} {
		if !got[a] {
			t.Errorf("%s is not an author: %v", a, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("authors: %v", got)
	}
}

// A handoff checks the claim before the send: the sender must hold it, or the
// ticket must be unclaimed, and the ticket must be in the store.
func TestCheckHandoff(t *testing.T) {
	ctx := context.Background()
	dir, id := memberStore(t)
	if tks, err := CheckHandoff(ctx, dir, []string{id}, jev, atla); err != nil || len(tks) != 1 || tks[0].ID != id {
		t.Fatalf("an unclaimed ticket: %v, %+v", err, tks)
	}
	if _, err := CheckHandoff(ctx, dir, []string{id}, jev, rev); err == nil || !strings.Contains(err.Error(), "has no claim") {
		t.Errorf("an unclaimed ticket to a reviewer: %v", err)
	}
	if _, err := (&TicketClaimTool{TicketCore: memberCore(dir, jev)}).Execute(ctx, mustJSONArgs(map[string]any{"ref": id, "if_revision": getTicket(t, dir, id).Revision}), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckHandoff(ctx, dir, []string{id}, jev, rev); err != nil {
		t.Fatalf("the holder's handoff: %v", err)
	}
	if tks, err := CheckHandoff(ctx, dir, []string{id, id, id[:len(id)-4]}, jev, atla); err != nil || len(tks) != 1 {
		t.Errorf("one ticket named three ways: %v, %+v", err, tks)
	}
	if _, err := CheckHandoff(ctx, dir, []string{id}, atla, jev); err == nil || !strings.Contains(err.Error(), "claimed by agent:native/jev") {
		t.Errorf("another member's handoff: %v", err)
	}
	if _, err := CheckHandoff(ctx, dir, []string{"TKT-NOPE"}, jev, atla); err == nil || !strings.Contains(err.Error(), "names no ticket") {
		t.Errorf("an unknown ticket: %v", err)
	}
	s, err := ticket.Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := s.Create(ctx, ticket.CreateOptions{Title: "Old work", Type: "task", Priority: "normal", Status: ticket.StatusDone})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CheckHandoff(ctx, dir, []string{closed.Ticket.ID}, jev, atla); err == nil || !strings.Contains(err.Error(), "is done") {
		t.Errorf("a done ticket: %v", err)
	}
	if _, err := CheckHandoff(ctx, testsupport.TempDir(t), []string{id}, jev, atla); !errors.Is(err, ErrNoTicketStore) {
		t.Errorf("no store: %v", err)
	}
}

// A handoff moves the claim to the recipient, and records the sender and a
// working recipient as holders. A reviewer recipient holds the claim to review
// it, so it is not recorded, and it can close the ticket.
func TestMoveClaims(t *testing.T) {
	ctx := context.Background()
	dir, id := memberStore(t)
	if _, err := (&TicketClaimTool{TicketCore: memberCore(dir, jev)}).Execute(ctx, mustJSONArgs(map[string]any{"ref": id, "if_revision": getTicket(t, dir, id).Revision}), nil); err != nil {
		t.Fatal(err)
	}
	s, err := ticket.Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx, id, ticket.ClaimTicket{Branch: "feat/login"}, ticket.ApplyOptions{Actor: ticket.Actor{ID: jev.Actor}}); err != nil {
		t.Fatal(err)
	}
	tks, err := CheckHandoff(ctx, dir, []string{id}, jev, atla)
	if err != nil {
		t.Fatal(err)
	}
	if err := MoveClaims(ctx, dir, tks, jev, atla, "env-1"); err != nil {
		t.Fatal(err)
	}
	tk := getTicket(t, dir, id)
	if tk.Claim == nil || tk.Claim.Actor != atla.Actor || tk.Claim.Session == nil || *tk.Claim.Session != atla.Session {
		t.Fatalf("the claim after the handoff: %+v", tk.Claim)
	}
	if tk.Claim.Branch == nil || *tk.Claim.Branch != "feat/login" {
		t.Errorf("the handoff did not keep the claim's branch: %v", tk.Claim.Branch)
	}
	if !strings.Contains(tk.Body.Notes, jev.Actor+" handed this ticket to "+atla.Actor+" with Talkoot handoff env-1") {
		t.Errorf("no note names the handoff: %q", tk.Body.Notes)
	}
	if a := ticketAuthors(tk); !a[jev.Actor] || !a[atla.Actor] {
		t.Fatalf("authors after the handoff: %v", a)
	}

	tks, err = CheckHandoff(ctx, dir, []string{id}, atla, rev)
	if err != nil {
		t.Fatal(err)
	}
	if err := MoveClaims(ctx, dir, tks, atla, rev, "env-2"); err != nil {
		t.Fatal(err)
	}
	tk = getTicket(t, dir, id)
	if tk.Claim == nil || tk.Claim.Actor != rev.Actor || ticketAuthors(tk)[rev.Actor] {
		t.Fatalf("a handoff to a reviewer: claim %+v, authors %v", tk.Claim, ticketAuthors(tk))
	}
	if got := memberCore(dir, rev).refuseMemberClosure(ctx, id, "done"); got != "" {
		t.Fatalf("the reviewer that received the claim cannot close: %q", got)
	}

	// A note written between the check and the move, as a recipient's turn
	// might, does not stop it. A claim that changed hands does.
	tks, err = CheckHandoff(ctx, dir, []string{id}, rev, jev)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx, id, ticket.AppendNote{Text: "a change in between"}, ticket.ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := MoveClaims(ctx, dir, tks, rev, jev, "env-3"); err != nil {
		t.Fatalf("a note in between stopped the move: %v", err)
	}
	if c := getTicket(t, dir, id).Claim; c == nil || c.Actor != jev.Actor {
		t.Fatalf("the claim after the move: %+v", c)
	}
	stale, err := CheckHandoff(ctx, dir, []string{id}, jev, atla)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx, id, ticket.ClaimTicket{Force: true}, ticket.ApplyOptions{Actor: ticket.Actor{ID: "human:sothr"}}); err != nil {
		t.Fatal(err)
	}
	if err := MoveClaims(ctx, dir, stale, jev, atla, "env-4"); err == nil || !strings.Contains(err.Error(), "the claim moved") {
		t.Fatalf("a move after the claim changed hands: %v", err)
	}
	if c := getTicket(t, dir, id).Claim; c == nil || c.Actor != "human:sothr" {
		t.Fatalf("the claim after a refused move: %+v", c)
	}
}

// The seam the handoff tool reads: a claim that did not move after the send
// is a sent handoff, and the tool says not to send it again.
func TestAClaimThatDidNotMoveIsStillASentHandoff(t *testing.T) {
	seat := &claimSeat{err: &ClaimNotMovedError{Envelope: talkoot.Envelope{ID: "e1", Kind: talkoot.KindHandoff, To: []string{"atlas"}}, Err: errors.New("revision changed")}}
	res, err := talkootSend(seat, talkoot.Outgoing{Kind: talkoot.KindHandoff})
	if err != nil || res.IsError {
		t.Fatalf("the tool failed a sent handoff: %v, %+v", err, res)
	}
	if text := toolText(t, res.Content); !strings.Contains(text, "Do not send the handoff again") || !strings.Contains(text, "e1") {
		t.Errorf("the tool result: %q", text)
	}
}

type claimSeat struct {
	TalkootSeat
	err error
}

func (s *claimSeat) Send(talkoot.Outgoing) (talkoot.Envelope, error) {
	var e *ClaimNotMovedError
	errors.As(s.err, &e)
	return e.Envelope, s.err
}
