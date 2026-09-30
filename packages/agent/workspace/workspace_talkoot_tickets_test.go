package workspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/tools"
)

// ticketCrew runs a talkoot in a workspace that holds a ticket store with one
// in-progress ticket, and returns the workspace and the ticket's id.
func ticketCrew(t *testing.T) (*Workspace, string) {
	t.Helper()
	return ticketCrewWith(t, "")
}

// ticketCrewWith is ticketCrew with more roster members, given as YAML.
func ticketCrewWith(t *testing.T, more string) (*Workspace, string) {
	t.Helper()
	cwd := talkootHome(t)
	ctx := context.Background()
	s, err := ticket.Init(cwd, ticket.InitOptions{Actor: ticket.Actor{ID: "human:sothr", Name: "sothr"}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Create(ctx, ticket.CreateOptions{Title: "Fix the login", Type: "task", Priority: "normal"})
	if err != nil {
		t.Fatal(err)
	}
	id := res.Ticket.ID
	for _, st := range []string{ticket.StatusReady, ticket.StatusInProgress} {
		if _, err := s.Apply(ctx, id, ticket.SetStatus{Status: st}, ticket.ApplyOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	w := openTalkootWorkspace(t, cwd)
	text := "---\nname: crew\nhome: " + cwd + "\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n    posture: plan\n  - id: jev\n    role: specialist\n    posture: workspace\n  - id: vartija\n    role: specialist\n    posture: plan\n    reviewer: true\n" + more + "---\n"
	if _, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "crew", Text: text}); err != nil {
		t.Fatal(err)
	}
	return w, id
}

// A ticket claim moves only between native members. An external member's
// actor would be the one a real session of its harness writes under.
func TestATicketHandoffStaysAmongNativeMembers(t *testing.T) {
	w, id := ticketCrewWith(t, "  - id: tess\n    role: specialist\n    driver: claude\n    workspace: worktree\n")
	jev := readSeatOf(t, w, "jev")
	o := talkoot.Outgoing{To: []string{"tess"}, Kind: talkoot.KindHandoff, Body: "Take it.", Refs: []string{"ticket:" + id}}
	// The check itself, apart from what the router would say of the send.
	if ho, err := w.checkTicketHandoff(t.Context(), jev.b, o); err == nil || !strings.Contains(err.Error(), "only between native members") {
		t.Fatalf("the check of a ticket handoff to an external member: %+v, %v", ho, err)
	}
	before := roomEnvelopes(t, w)
	if _, err := jev.Send(o); err == nil || !strings.Contains(err.Error(), "only between native members") {
		t.Fatalf("a ticket handoff to an external member: %v", err)
	}
	if n := roomEnvelopes(t, w); n != before {
		t.Fatalf("a refused handoff reached the room: %d envelopes, want %d", n, before)
	}
	if c := claimOf(t, w, id); c != nil && c.Actor != "" {
		t.Fatalf("a refused handoff moved the claim: %+v", c)
	}
}

// With tickets turned off in the configuration, a handoff keeps its ticket
// references as text: it goes, and no claim moves.
func TestATicketHandoffWithTicketsOffMovesNoClaim(t *testing.T) {
	w, id := ticketCrew(t)
	jev := readSeatOf(t, w, "jev")
	cfg := filepath.Join(os.Getenv("TERVA_HOME"), "config.json")
	if err := os.WriteFile(cfg, []byte(`{"talkoot_enabled": true, "tickets": false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	before := roomEnvelopes(t, w)
	if _, err := jev.Send(talkoot.Outgoing{To: []string{"helm"}, Kind: talkoot.KindHandoff, Body: "Take it.", Refs: []string{"ticket:" + id}}); err != nil {
		t.Fatalf("the handoff: %v", err)
	}
	if n := roomEnvelopes(t, w); n != before+1 {
		t.Fatalf("the room holds %d envelopes, want %d", n, before+1)
	}
	if c := claimOf(t, w, id); c != nil && c.Actor != "" {
		t.Fatalf("a handoff with tickets off moved the claim: %+v", c)
	}
}

// readSeatOf is seatOf, once the member's turn has read the post that woke
// it. Only then does the member hold that chain, so a send before it races
// the read and the router refuses it for having no human root.
func readSeatOf(t *testing.T, w *Workspace, member string) talkootSeat {
	t.Helper()
	seat := seatOf(t, w, member)
	waitTalkoot(t, member+" reads its post", func() bool {
		return len(roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineRead && l.Member == member })) > 0
	})
	resumeFailed(t, w, "crew", member)
	return seat
}

func ticketOf(t *testing.T, w *Workspace, id string) *ticket.Ticket {
	t.Helper()
	s, err := ticket.Discover(w.cwd)
	if err != nil {
		t.Fatal(err)
	}
	tk, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func claimOf(t *testing.T, w *Workspace, id string) *ticket.Claim {
	t.Helper()
	return ticketOf(t, w, id).Claim
}

func ticketNotes(t *testing.T, w *Workspace, id string) string {
	t.Helper()
	return ticketOf(t, w, id).Body.Notes
}

func roomEnvelopes(t *testing.T, w *Workspace) int {
	t.Helper()
	page, err := w.talkootRoom(t.Context(), "crew", 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, l := range page.Lines {
		if l.Type == talkoot.LineEnvelope {
			n++
		}
	}
	return n
}

// A seated member's ticket tools write as agent:native/<member> in the
// workspace store, and its permission policy refuses the git ticket CLI.
func TestAMemberWritesTicketsAsItself(t *testing.T) {
	w, id := ticketCrew(t)
	seat := seatOf(t, w, "jev")
	var sessID string
	w.talkoot.mu.Lock()
	for id, b := range w.talkoot.seats {
		if b == seat.b {
			sessID = id
		}
	}
	w.talkoot.mu.Unlock()
	s := w.existing(sessID)
	if s == nil {
		t.Fatal("jev's session is not live")
	}
	tl, ok := s.agent.LookupTool("ticket_transition")
	if !ok {
		a := s.argsSnapshot()
		t.Fatalf("jev's session has no ticket tools; store %v, enabled %v, cwd %q, noticket %v", tools.TicketStoreAvailable(a.CWD), config.TicketsEnabled(w.cwd), a.CWD, a.NoTicket)
	}
	tt, ok := tl.(*tools.TicketTransitionTool)
	if !ok || tt.TicketCore == nil || tt.Member == nil {
		t.Fatalf("jev's ticket core has no member lookup: %T", tl)
	}
	m, ok := tt.Member()
	if !ok || m.Actor != "agent:native/jev" || m.Session != sessID || m.Reviewer {
		t.Fatalf("jev's ticket identity: %+v, %v", m, ok)
	}
	// The handoff moves claims in the workspace store, so a member's tools
	// must start there too.
	if cwd := s.argsSnapshot().CWD; cwd != w.cwd {
		t.Fatalf("jev's session works in %q, not the workspace %q", cwd, w.cwd)
	}
	tl, ok = s.agent.LookupTool("ticket_comment")
	if !ok {
		t.Fatal("jev's session has no ticket_comment")
	}
	res, err := tl.Execute(t.Context(), mustJSON(t, map[string]any{"ref": id, "kind": "note", "text": "Started.", "if_revision": ticketOf(t, w, id).Revision}), nil)
	if err != nil || res.IsError {
		t.Fatalf("jev's note: %v, %+v", err, res)
	}
	if by := ticketOf(t, w, id).UpdatedBy.ID; by != "agent:native/jev" {
		t.Fatalf("jev's note was written by %q", by)
	}
	args := mustJSON(t, map[string]string{"command": "git ticket claim " + id})
	if ok, reason, _ := s.gate.Check(t.Context(), "bash", args, "", "probe"); ok || !strings.Contains(reason, "ticket tools") {
		t.Fatalf("jev's bash ran git ticket claim: allowed %v, %q", ok, reason)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A handoff that names a ticket moves its claim to the one recipient. A
// sender that does not hold the claim is refused before the router, so no
// envelope reaches the room.
func TestAHandoffThatNamesATicketMovesItsClaim(t *testing.T) {
	w, id := ticketCrew(t)
	jev := readSeatOf(t, w, "jev")
	helm := readSeatOf(t, w, "helm")
	handoff := func(seat talkootSeat, to ...string) error {
		_, err := seat.Send(talkoot.Outgoing{To: to, Kind: talkoot.KindHandoff, Body: "Take the login fix.", Refs: []string{"ticket:" + id, "path:src/login.go"}})
		return err
	}
	before := roomEnvelopes(t, w)
	if err := handoff(jev, "helm", "vartija"); err == nil || !strings.Contains(err.Error(), "goes to one member") {
		t.Fatalf("a handoff to two members: %v", err)
	}
	if err := handoff(jev, "helm"); err != nil {
		t.Fatalf("jev hands an unclaimed ticket to helm: %v", err)
	}
	if c := claimOf(t, w, id); c == nil || c.Actor != "agent:native/helm" {
		t.Fatalf("the claim after jev's handoff: %+v", c)
	}
	after := roomEnvelopes(t, w)
	if after != before+1 {
		t.Fatalf("the room holds %d envelopes, want %d", after, before+1)
	}
	if err := handoff(jev, "vartija"); err == nil || !strings.Contains(err.Error(), "claimed by agent:native/helm") {
		t.Fatalf("jev hands on a ticket helm holds: %v", err)
	}
	if n := roomEnvelopes(t, w); n != after {
		t.Fatalf("a refused handoff reached the room: %d envelopes, want %d", n, after)
	}
	// jev's handoff woke helm, and that turn fails too.
	resumeFailed(t, w, "crew", "helm")
	e, err := helm.Send(talkoot.Outgoing{To: []string{"vartija"}, Kind: talkoot.KindHandoff, Body: "Review the login fix.", Refs: []string{"ticket:" + id, "ticket:" + id}})
	if err != nil {
		t.Fatalf("helm hands the ticket to the reviewer, named twice: %v", err)
	}
	if c := claimOf(t, w, id); c == nil || c.Actor != "agent:native/vartija" {
		t.Fatalf("the claim after helm's handoff: %+v", c)
	}
	if notes := ticketNotes(t, w, id); !strings.Contains(notes, "agent:native/helm handed this ticket to agent:native/vartija with Talkoot handoff "+e.ID) {
		t.Fatalf("no note names helm's handoff %s: %q", e.ID, notes)
	}
}
