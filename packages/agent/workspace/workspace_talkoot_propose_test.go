package workspace

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/tools"
)

// okProvider answers every request with a short reply, so a member's turn
// ends well and the member takes the chain of what it read.
func okProvider(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
	fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// proposalCrew makes the crew talkoot and runs one turn of helm's, so helm
// holds a seat and a chain with a person at its root. It returns helm's seat
// and the roster file's path.
func proposalCrew(t *testing.T) (*Workspace, talkootSeat, string) {
	t.Helper()
	cwd := talkootHome(t)
	w := openTalkootWorkspaceWith(t, cwd, okProvider)
	ctx := t.Context()
	if _, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "crew", Text: string(crewText(cwd))}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.PostTalkoot(ctx, ctrlproto.TalkootPostParams{ID: "crew", By: "sothr", To: []string{"helm"}, Body: "Plan the schema."}); err != nil {
		t.Fatal(err)
	}
	waitTalkoot(t, "helm's turn", func() bool {
		m := memberView(t, w, "crew", "helm")
		return m.Status.Turns == 1 && !m.Status.Working
	})
	seat, ok := w.talkootSeatOf(memberView(t, w, "crew", "helm").Session)
	if !ok {
		t.Fatal("helm holds no seat")
	}
	return w, seat, filepath.Join(talkoot.Dir(), "crew", talkoot.FileName)
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func rosterLines(t *testing.T) []talkoot.Line {
	return roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineRoster })
}

var jevAsks = []talkoot.Op{{Op: talkoot.OpEdit, Member: "jev", Set: map[string]any{"posture": "ask"}}}

// A member's proposal waits in the inbox and changes nothing. A person's
// approval writes the roster, and the roster line names the proposal, its
// proposer, the person, and jev before and after. An undo takes the same
// path and puts jev back.
func TestAMemberProposesAndAPersonApplies(t *testing.T) {
	w, helm, path := proposalCrew(t)
	ctx := t.Context()
	before := readFile(t, path)
	room, err := w.Subscribe(ctx, ctrlproto.TalkootAddr("crew"))
	if err != nil {
		t.Fatal(err)
	}

	p, err := helm.Propose(jevAsks, "", "jev runs the tests")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFile(t, path), before) {
		t.Fatal("a proposal changed talkoot.md")
	}
	card := nextEvent(t, room, ctrlproto.EventTalkootInbox).Talkoot.Card
	if card.Kind != ctrlproto.TalkootCardProposal || card.ID != p.ID || card.Member != "helm" || card.Proposal == nil {
		t.Fatalf("the inbox event carried %+v", card)
	}
	wp := card.Proposal
	if wp.Why != "jev runs the tests" || wp.Class != "authority" || wp.SelfAuthority || len(wp.Changes) != 1 ||
		wp.Changes[0].After.Posture != "ask" || !reflect.DeepEqual(wp.Changes[0].Widens, []string{"posture"}) {
		t.Fatalf("the card shows %+v", wp)
	}
	if got := inbox(t, w); len(got) != 1 || got[0].ID != p.ID {
		t.Fatalf("talkoot.inbox = %+v", got)
	}
	envs := roomLines(t, "crew", func(l talkoot.Line) bool {
		return l.Type == talkoot.LineEnvelope && l.Envelope.Kind == talkoot.KindProposal
	})
	if len(envs) != 1 || envs[0].Envelope.From != "helm" || envs[0].Envelope.ID != p.Envelope {
		t.Fatalf("the room records the proposal as %+v", envs)
	}

	// An empty ops list is no edit: a client that always sends the field
	// approves the proposal as made.
	out, err := w.DecideTalkoot(ctx, ctrlproto.TalkootDecideParams{ID: "crew", By: "sothr", Proposal: p.ID, Decision: ctrlproto.TalkootDecisionApprove, Ops: []ctrlproto.TalkootOp{}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != ctrlproto.TalkootProposalApproved || out.DecidedBy != "human:sothr" || out.Edited {
		t.Fatalf("the decision returned %+v", out)
	}
	if jev := memberView(t, w, "crew", "jev"); jev.Member.Posture != "ask" {
		t.Fatalf("after the approval jev is %+v", jev.Member)
	}
	if !strings.Contains(string(readFile(t, path)), "posture: ask") {
		t.Fatalf("talkoot.md does not hold the change:\n%s", readFile(t, path))
	}
	lines := rosterLines(t)
	last := lines[len(lines)-1]
	if last.Proposal != p.ID || last.Proposer != "helm" || last.By != "human:sothr" || last.Edited || len(last.Changes) != 1 ||
		last.Changes[0].Before.Posture != "plan" || last.Changes[0].After.Posture != "ask" {
		t.Fatalf("the roster line is %+v", last)
	}
	if done := nextEvent(t, room, ctrlproto.EventTalkootInboxResolved).Talkoot.Card; done.ID != p.ID || done.Proposal != nil {
		t.Fatalf("the resolved event carried %+v", done)
	}
	if got := inbox(t, w); len(got) != 0 {
		t.Fatalf("talkoot.inbox after the approval = %+v", got)
	}

	undo, err := w.ProposeTalkoot(ctx, ctrlproto.TalkootProposeParams{ID: "crew", By: "sothr", Undo: p.ID, Why: "not yet"})
	if err != nil {
		t.Fatal(err)
	}
	if undo.Undoes != p.ID || undo.Proposer != "human:sothr" {
		t.Fatalf("the undo proposal is %+v", undo)
	}
	if _, err := w.DecideTalkoot(ctx, ctrlproto.TalkootDecideParams{ID: "crew", By: "sothr", Proposal: undo.ID, Decision: ctrlproto.TalkootDecisionApprove}); err != nil {
		t.Fatal(err)
	}
	if jev := memberView(t, w, "crew", "jev"); jev.Member.Posture != "plan" {
		t.Fatalf("after the undo jev is %+v", jev.Member)
	}
	list, err := w.TalkootProposals(ctx, ctrlproto.TalkootProposalsParams{ID: "crew", All: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Proposals) != 2 || list.Proposals[0].ID != p.ID || list.Proposals[1].Status != ctrlproto.TalkootProposalApproved {
		t.Fatalf("talkoot.proposals all = %+v", list.Proposals)
	}
	if _, err := w.DecideTalkoot(ctx, ctrlproto.TalkootDecideParams{ID: "crew", By: "sothr", Proposal: p.ID, Decision: ctrlproto.TalkootDecisionApprove}); err == nil {
		t.Error("a decided proposal was decided again")
	}
}

// A decline writes the decision and nothing else: no roster change and no
// roster line.
func TestADeclineWritesNoRoster(t *testing.T) {
	w, helm, path := proposalCrew(t)
	before, lines := readFile(t, path), len(rosterLines(t))
	p, err := helm.Propose(jevAsks, "", "jev runs the tests")
	if err != nil {
		t.Fatal(err)
	}
	out, err := w.DecideTalkoot(t.Context(), ctrlproto.TalkootDecideParams{ID: "crew", By: "sothr", Proposal: p.ID, Decision: ctrlproto.TalkootDecisionDecline, Reason: "not now"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != ctrlproto.TalkootProposalDeclined || out.Reason != "not now" {
		t.Fatalf("the decline returned %+v", out)
	}
	if !bytes.Equal(readFile(t, path), before) || len(rosterLines(t)) != lines {
		t.Fatal("a decline changed the roster or wrote a roster line")
	}
	if got := inbox(t, w); len(got) != 0 {
		t.Fatalf("talkoot.inbox after the decline = %+v", got)
	}
}

// One bad operation refuses the whole batch before anything is written: no
// proposal, no envelope, and no card.
func TestABadBatchIsRefusedWhole(t *testing.T) {
	w, helm, _ := proposalCrew(t)
	for _, ops := range [][]talkoot.Op{
		// yolo in the shared checkout breaks a roster rule.
		{jevAsks[0], {Op: talkoot.OpEdit, Member: "helm", Set: map[string]any{"posture": "yolo"}}},
		// a field that does not exist.
		{jevAsks[0], {Op: talkoot.OpEdit, Member: "helm", Set: map[string]any{"colour": "red"}}},
		// a persona the library does not have.
		{jevAsks[0], {Op: talkoot.OpAdd, Member: "scout", Set: map[string]any{"role": "specialist", "persona": "nobody-has-this"}}},
		// no change at all.
		{{Op: talkoot.OpEdit, Member: "jev", Set: map[string]any{"posture": "plan"}}},
	} {
		if _, err := helm.Propose(ops, "", "try"); err == nil {
			t.Fatalf("the batch %+v was taken", ops)
		}
	}
	if list, _, _ := talkoot.ListProposals(filepath.Join(talkoot.Dir(), "crew")); len(list) != 0 {
		t.Errorf("a refused batch saved %+v", list)
	}
	if envs := roomLines(t, "crew", func(l talkoot.Line) bool {
		return l.Type == talkoot.LineEnvelope && l.Envelope.Kind == talkoot.KindProposal
	}); len(envs) != 0 {
		t.Errorf("a refused batch wrote %d envelopes", len(envs))
	}
	if got := inbox(t, w); len(got) != 0 {
		t.Errorf("a refused batch made cards %+v", got)
	}
}

// A proposal whose roster changed after it was made is refused, and its card
// says why. It still waits, and a person can still decline it.
func TestAStaleProposalIsRefusedAndSaysWhy(t *testing.T) {
	w, helm, path := proposalCrew(t)
	ctx := t.Context()
	p, err := helm.Propose(jevAsks, "", "jev runs the tests")
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(readFile(t, path)), "role: specialist", "role: specialist\n    title: Developer", 1)
	if _, err := w.UpdateTalkoot(ctx, ctrlproto.TalkootUpdateParams{ID: "crew", By: "sothr", Text: edited}); err != nil {
		t.Fatal(err)
	}
	_, err = w.DecideTalkoot(ctx, ctrlproto.TalkootDecideParams{ID: "crew", By: "sothr", Proposal: p.ID, Decision: ctrlproto.TalkootDecisionApprove})
	if talkootCode(err) != ctrlproto.CodeConflict || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("approving a stale proposal: %v (%s)", err, talkootCode(err))
	}
	if jev := memberView(t, w, "crew", "jev"); jev.Member.Posture != "plan" {
		t.Fatal("a stale proposal applied")
	}
	got := inbox(t, w)
	if len(got) != 1 || got[0].Proposal == nil || !strings.Contains(got[0].Proposal.Problem, "changed since") {
		t.Fatalf("the card is %+v, want it waiting with the reason", got)
	}
	// The person's own update wrote its changes on its roster line.
	lines := rosterLines(t)
	if last := lines[len(lines)-1]; last.Proposal != "" || len(last.Changes) != 1 || last.Changes[0].After.Title != "Developer" {
		t.Fatalf("the update's roster line is %+v", last)
	}
	// The way out the refusal names: the same change, edited against the
	// roster as it is now. The person's update survives it.
	out, err := w.DecideTalkoot(ctx, ctrlproto.TalkootDecideParams{ID: "crew", By: "sothr", Proposal: p.ID,
		Decision: ctrlproto.TalkootDecisionApprove, Ops: []ctrlproto.TalkootOp{{Op: "edit", Member: "jev", Set: map[string]any{"posture": "ask"}}}})
	if err != nil {
		t.Fatalf("approving a stale proposal with its operations edited: %v", err)
	}
	if out.Status != talkoot.ProposalApproved || !out.Edited || out.Problem != "" {
		t.Fatalf("the edited approval recorded %+v", out)
	}
	if jev := memberView(t, w, "crew", "jev"); jev.Member.Posture != "ask" || jev.Member.Title != "Developer" {
		t.Fatalf("jev is %+v, want the edit on top of the person's update", jev.Member)
	}
}

// A person can approve an edited version. The edit passes every check the
// proposal did, and the record says it was edited.
func TestAnApprovalCanEditTheProposal(t *testing.T) {
	w, helm, _ := proposalCrew(t)
	ctx := t.Context()
	p, err := helm.Propose(jevAsks, "", "jev runs the tests")
	if err != nil {
		t.Fatal(err)
	}
	bad := []ctrlproto.TalkootOp{{Op: "edit", Member: "jev", Set: map[string]any{"posture": "yolo"}}}
	if _, err := w.DecideTalkoot(ctx, ctrlproto.TalkootDecideParams{ID: "crew", By: "sothr", Proposal: p.ID, Decision: "approve", Ops: bad}); err == nil {
		t.Fatal("an edit that breaks a roster rule applied")
	}
	if got := inbox(t, w); len(got) != 1 || got[0].Proposal.Problem != "" {
		t.Fatalf("a failed edit marked the proposal itself: %+v", got)
	}
	title := []ctrlproto.TalkootOp{{Op: "look", Member: "jev", Set: map[string]any{"title": "Tester"}}}
	out, err := w.DecideTalkoot(ctx, ctrlproto.TalkootDecideParams{ID: "crew", By: "sothr", Proposal: p.ID, Decision: "approve", Ops: title})
	if err != nil {
		t.Fatal(err)
	}
	// The record keeps what helm asked for beside what the person applied.
	if !out.Edited || len(out.Ops) != 1 || out.Ops[0].Op != "edit" || len(out.Applied) != 1 || out.Applied[0].Op != "look" {
		t.Fatalf("the edited approval recorded %+v", out)
	}
	if jev := memberView(t, w, "crew", "jev"); jev.Member.Title != "Tester" || jev.Member.Posture != "plan" {
		t.Fatalf("jev is %+v, want the edit and not the original", jev.Member)
	}
	// The room says the person replaced the operations, so helm is not read
	// as the author of a change it never asked for.
	lines := rosterLines(t)
	if last := lines[len(lines)-1]; last.Proposal != p.ID || last.Proposer != "helm" || !last.Edited {
		t.Fatalf("the edited approval's roster line is %+v", last)
	}
}

// 🚨 An approval whose record cannot be saved changes nothing. The roster
// changes only once the record says approved, so every change from a
// proposal has an approved record behind it, and its undo works.
func TestAnApprovalThatCannotBeRecordedChangesNothing(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("a file mode stops a write only for an ordinary user on unix")
	}
	w, helm, path := proposalCrew(t)
	before := readFile(t, path)
	p, err := helm.Propose(jevAsks, "", "jev runs the tests")
	if err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(filepath.Dir(path), talkoot.ProposalsDir)
	if err := os.Chmod(store, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(store, 0o700) })
	_, err = w.DecideTalkoot(t.Context(), ctrlproto.TalkootDecideParams{ID: "crew", By: "sothr", Proposal: p.ID, Decision: ctrlproto.TalkootDecisionApprove})
	if err == nil || !strings.Contains(err.Error(), "could not be recorded") {
		t.Fatalf("want the failed record reported, got %v", err)
	}
	if !bytes.Equal(readFile(t, path), before) {
		t.Fatal("the roster changed without an approved record")
	}
	if got := inbox(t, w); len(got) != 1 || got[0].ID != p.ID {
		t.Fatalf("the card should still wait, and the inbox is %+v", got)
	}
}

// 🚨 An approval whose roster change fails puts its record back to pending,
// so the store never says approved over a roster that did not change.
func TestAnApprovalTheRoomCannotRecordStaysPending(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("a file mode stops a write only for an ordinary user on unix")
	}
	w, helm, path := proposalCrew(t)
	before := readFile(t, path)
	p, err := helm.Propose(jevAsks, "", "jev runs the tests")
	if err != nil {
		t.Fatal(err)
	}
	room := filepath.Join(filepath.Dir(path), talkoot.RoomFile)
	if err := os.Chmod(room, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(room, 0o600) })
	if _, err := w.DecideTalkoot(t.Context(), ctrlproto.TalkootDecideParams{ID: "crew", By: "sothr", Proposal: p.ID, Decision: ctrlproto.TalkootDecisionApprove}); err == nil {
		t.Fatal("want the approval refused when the room cannot record it")
	}
	if !bytes.Equal(readFile(t, path), before) {
		t.Fatal("the refused approval changed the roster")
	}
	if got, err := talkoot.LoadProposal(filepath.Dir(path), p.ID); err != nil || got.Status != talkoot.ProposalPending || got.DecidedBy != "" {
		t.Fatalf("the record is %+v, %v; want it pending as it was", got, err)
	}
}

// 🚨 A member cannot apply a change to its own authority. Its proposal waits,
// the card names it in the title, and nothing a member holds can decide it.
func TestAMemberCannotApplyItsOwnAuthority(t *testing.T) {
	w, helm, path := proposalCrew(t)
	before := readFile(t, path)
	_, err := helm.Propose([]talkoot.Op{{Op: talkoot.OpEdit, Member: "helm", Set: map[string]any{"posture": "ask"}}}, "", "I need to edit files")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readFile(t, path), before) {
		t.Fatal("helm's proposal for itself changed the roster")
	}
	got := inbox(t, w)
	if len(got) != 1 || !got[0].Proposal.SelfAuthority || !strings.HasPrefix(got[0].Proposal.Title, "helm proposes a change to its own authority") {
		t.Fatalf("the card is %+v", got)
	}
	if helmNow := memberView(t, w, "crew", "helm"); helmNow.Member.Posture != "plan" {
		t.Fatalf("helm is %+v", helmNow.Member)
	}
	// The seat a member's tools hold has no way to decide. Only a ctrlproto
	// client's talkoot.decide does.
	seat := reflect.TypeFor[tools.TalkootSeat]()
	for i := range seat.NumMethod() {
		switch name := seat.Method(i).Name; name {
		case "Send", "Roster", "WriteNote", "ReadNote", "ListNotes", "Propose":
		default:
			t.Errorf("TalkootSeat gained %s. If it decides or writes a roster, a member could apply its own proposal", name)
		}
	}
}

// A seat that an update retired proposes nothing, like a send. An update
// retires the seat inside the router's lock and revokes it after, so each
// check covers its own window.
func TestARetiredSeatCannotPropose(t *testing.T) {
	w, helm, path := proposalCrew(t)
	look := []talkoot.Op{{Op: talkoot.OpLook, Member: "jev", Set: map[string]any{"title": "x"}}}
	// Retired and not yet revoked: the check inside the router call refuses.
	helm.b.retired.Store(true)
	if _, err := helm.Propose(look, "", "x"); !errors.Is(err, errSeatRevoked) {
		t.Fatalf("a retired seat proposed: %v", err)
	}
	helm.b.retired.Store(false)
	// helm leaves the roster, and pilot takes its role.
	renamed := strings.Replace(string(readFile(t, path)), "id: helm", "id: pilot", 1)
	if _, err := w.UpdateTalkoot(t.Context(), ctrlproto.TalkootUpdateParams{ID: "crew", By: "sothr", Text: renamed}); err != nil {
		t.Fatal(err)
	}
	if _, err := helm.Propose(look, "", "x"); !errors.Is(err, errSeatRevoked) {
		t.Fatalf("a revoked seat proposed: %v", err)
	}
}

// 🚨 The pending cap holds under concurrent proposers. Each one counts and
// saves under run.propose, so one slot admits one proposal.
func TestConcurrentProposalsCannotPassTheCap(t *testing.T) {
	w, _, path := proposalCrew(t)
	dir := filepath.Dir(path)
	for range talkoot.MaxPendingProposals - 1 {
		p := talkoot.Proposal{ID: talkoot.NewProposalID(time.Now()), Proposer: "helm", At: time.Now(), Ops: jevAsks, Status: talkoot.ProposalPending}
		if err := talkoot.SaveProposal(dir, p); err != nil {
			t.Fatal(err)
		}
	}
	const proposers = 8
	var (
		wg sync.WaitGroup
		ok atomic.Int32
	)
	for range proposers {
		wg.Go(func() {
			if _, err := w.ProposeTalkoot(t.Context(), ctrlproto.TalkootProposeParams{ID: "crew", By: "sothr", Why: "jev runs the tests",
				Ops: []ctrlproto.TalkootOp{{Op: "edit", Member: "jev", Set: map[string]any{"posture": "ask"}}}}); err == nil {
				ok.Add(1)
			}
		})
	}
	wg.Wait()
	pending, err := w.pendingProposals("crew", dir)
	if err != nil {
		t.Fatal(err)
	}
	if ok.Load() != 1 || len(pending) != talkoot.MaxPendingProposals {
		t.Fatalf("%d of %d proposers passed, and %d proposals wait; want 1 and %d", ok.Load(), proposers, len(pending), talkoot.MaxPendingProposals)
	}
}

// A proposal record that does not parse still shows in the inbox, with its
// problem, and a decline clears it. It cannot be approved.
func TestADamagedProposalShowsAndCanBeDeclined(t *testing.T) {
	w, _, path := proposalCrew(t)
	store := filepath.Join(filepath.Dir(path), talkoot.ProposalsDir)
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	pid := talkoot.NewProposalID(time.Now())
	if err := os.WriteFile(filepath.Join(store, pid+".json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A readable proposal made later still lists after the damaged one, which
	// is older.
	later := talkoot.Proposal{ID: talkoot.NewProposalID(time.Now().Add(time.Second)), Proposer: "helm", At: time.Now(),
		Ops: jevAsks, Status: talkoot.ProposalDeclined}
	if err := talkoot.SaveProposal(filepath.Dir(path), later); err != nil {
		t.Fatal(err)
	}
	got := inbox(t, w)
	if len(got) != 1 || got[0].ID != pid || got[0].Proposal == nil ||
		!strings.Contains(got[0].Proposal.Problem, "does not read") || got[0].Proposal.Title != "proposal "+pid+" does not read" {
		t.Fatalf("want the damaged proposal on a card, got %+v", got)
	}
	ctx := t.Context()
	all, err := w.TalkootProposals(ctx, ctrlproto.TalkootProposalsParams{ID: "crew", All: true})
	if err != nil || len(all.Proposals) != 2 || all.Proposals[0].ID != pid || all.Proposals[1].ID != later.ID {
		t.Fatalf("every proposal should list the damaged one first, as the oldest: %+v, %v", all, err)
	}
	if _, err := w.DecideTalkoot(ctx, ctrlproto.TalkootDecideParams{ID: "crew", By: "sothr", Proposal: pid, Decision: ctrlproto.TalkootDecisionApprove}); err == nil {
		t.Fatal("a damaged proposal was approved")
	}
	if _, err := w.DecideTalkoot(ctx, ctrlproto.TalkootDecideParams{ID: "crew", By: "sothr", Proposal: pid, Decision: ctrlproto.TalkootDecisionDecline}); err != nil {
		t.Fatalf("declining a damaged proposal: %v", err)
	}
	if got := inbox(t, w); len(got) != 0 {
		t.Fatalf("the declined card still waits: %+v", got)
	}
	if p, err := talkoot.LoadProposal(filepath.Dir(path), pid); err != nil || p.Status != talkoot.ProposalDeclined {
		t.Fatalf("the record is %+v, %v", p, err)
	}
	all, err = w.TalkootProposals(ctx, ctrlproto.TalkootProposalsParams{ID: "crew", All: true})
	if err != nil || len(all.Proposals) != 2 || all.Proposals[0].Title != "proposal "+pid+", whose record did not read, was declined" {
		t.Fatalf("the declined record should say what happened to it: %+v, %v", all, err)
	}
}
