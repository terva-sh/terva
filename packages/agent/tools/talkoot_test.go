package tools

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

type fakeSeat struct {
	sent   []talkoot.Outgoing
	roster []TalkootRosterEntry
	// dir and member back the notes, when a test sets them.
	dir, member string
	// proposed records each proposal, and proposeErr fails the next one.
	proposed   []fakeProposal
	proposeErr error
}

type fakeProposal struct {
	ops       []talkoot.Op
	undo, why string
}

func (s *fakeSeat) Propose(ops []talkoot.Op, undo, why string) (talkoot.Proposal, error) {
	if s.proposeErr != nil {
		return talkoot.Proposal{}, s.proposeErr
	}
	s.proposed = append(s.proposed, fakeProposal{ops, undo, why})
	return talkoot.Proposal{ID: "01M3E7YRGWZ42BPP2J4AKG62B7", Summary: talkoot.Summarize(ops)}, nil
}

func (s *fakeSeat) WriteNote(name, text string) (string, error) {
	return talkoot.WriteNote(s.dir, s.member, name, text)
}
func (s *fakeSeat) ReadNote(ref string) (string, error) { return talkoot.ReadNote(s.dir, ref) }
func (s *fakeSeat) ListNotes() ([]talkoot.Note, error)  { return talkoot.ListNotes(s.dir) }

func (s *fakeSeat) Send(o talkoot.Outgoing) (talkoot.Envelope, error) {
	s.sent = append(s.sent, o)
	return talkoot.Envelope{ID: "env-1", To: o.To, Kind: o.Kind}, nil
}

func (s *fakeSeat) Roster() ([]TalkootRosterEntry, error) { return s.roster, nil }

func talkootText(t *testing.T, tool core.Tool, args string) (string, error) {
	t.Helper()
	res, err := tool.Execute(context.Background(), json.RawMessage(args), nil)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	return b.String(), nil
}

func TestTalkootSendRoutesEachKind(t *testing.T) {
	seat := &fakeSeat{}
	for _, kind := range []string{"message", "note", "answer"} {
		got, err := talkootText(t, &TalkootSendTool{Seat: seat}, `{"to":["helm"],"kind":"`+kind+`","body":"Done.","refs":["ticket:TKT-1"],"thread":"t1","reply_to":"env-0"}`)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if !strings.Contains(got, "Sent "+kind+" env-1 to helm.") {
			t.Errorf("%s: got %q", kind, got)
		}
	}
	o := seat.sent[2]
	if o.Kind != talkoot.KindAnswer || o.Body != "Done." || o.Thread != "t1" || o.ReplyTo != "env-0" || len(o.Refs) != 1 {
		t.Errorf("the envelope lost a field: %+v", o)
	}
}

func TestTalkootSendPointsAHandoffAtItsOwnTool(t *testing.T) {
	seat := &fakeSeat{}
	_, err := talkootText(t, &TalkootSendTool{Seat: seat}, `{"to":["helm"],"kind":"handoff","body":"Yours.","refs":["branch:x"]}`)
	if err == nil || !strings.Contains(err.Error(), "talkoot_handoff") {
		t.Fatalf("want a pointer to talkoot_handoff, got %v", err)
	}
	if len(seat.sent) != 0 {
		t.Error("the refused call still sent")
	}
}

// 🚨 The work travels by reference. A handoff with none is refused before it
// reaches the router, in words that name the fix.
func TestTalkootHandoffRefusesNoReference(t *testing.T) {
	seat := &fakeSeat{}
	for _, args := range []string{`{"to":["helm"],"body":"Yours."}`, `{"to":["helm"],"body":"Yours.","refs":[]}`} {
		_, err := talkootText(t, &TalkootHandoffTool{Seat: seat}, args)
		if err == nil || !strings.Contains(err.Error(), "at least one reference") {
			t.Errorf("%s: want a refusal, got %v", args, err)
		}
	}
	if len(seat.sent) != 0 {
		t.Fatal("a handoff with no reference reached the router")
	}
	if _, err := talkootText(t, &TalkootHandoffTool{Seat: seat}, `{"to":["helm"],"body":"Yours.","refs":["branch:feat/x"]}`); err != nil {
		t.Fatal(err)
	}
	if len(seat.sent) != 1 || seat.sent[0].Kind != talkoot.KindHandoff {
		t.Errorf("want one handoff sent, got %+v", seat.sent)
	}
}

func TestTalkootRosterShowsEachMember(t *testing.T) {
	seat := &fakeSeat{roster: []TalkootRosterEntry{
		{Member: talkoot.Member{ID: "helm", Title: "Lead", Role: "coordinator", Driver: "native"}, Status: talkoot.Status{Working: true}},
		{Member: talkoot.Member{ID: "jev", Role: "specialist", Driver: "claude"}, Self: true},
		{Member: talkoot.Member{ID: "gage", Role: "reviewer", Driver: "native"}, Status: talkoot.Status{Paused: "over budget"}},
	}}
	got, err := talkootText(t, &TalkootRosterTool{Seat: seat}, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"- helm: Lead; role coordinator; driver native; working",
		"- jev (you); role specialist; driver claude; idle",
		"- gage; role reviewer; driver native; paused: over budget",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in:\n%s", want, got)
		}
	}
}

func TestTalkootToolsRefuseWithoutASeat(t *testing.T) {
	for _, tool := range TalkootTools(nil) {
		if _, err := talkootText(t, tool, `{"to":["helm"],"kind":"message","body":"x","refs":["branch:x"]}`); err == nil {
			t.Errorf("%s ran with no seat", tool.Name())
		}
	}
}

// The bridge serves TalkootToolDefs, so the definitions must be exactly what
// the native tools advertise, and each schema must parse.
func TestTalkootToolDefsMatchTheNativeTools(t *testing.T) {
	defs := TalkootToolDefs()
	tools := TalkootTools(nil)
	if len(defs) != 6 || len(tools) != 6 {
		t.Fatalf("want six tools, got %d defs and %d tools", len(defs), len(tools))
	}
	for i, d := range defs {
		tool := tools[i]
		if d.Name != tool.Name() || d.Description != tool.Description() || string(d.Schema) != string(tool.Schema()) {
			t.Errorf("%s: the definition drifted from the tool", d.Name)
		}
		var v map[string]any
		if err := json.Unmarshal(d.Schema, &v); err != nil {
			t.Errorf("%s: the schema does not parse: %v", d.Name, err)
		}
	}
}

// A title with a line break must not add a line that reads as a member.
func TestTalkootRosterKeepsEachMemberOnOneLine(t *testing.T) {
	seat := &fakeSeat{roster: []TalkootRosterEntry{
		{Member: talkoot.Member{ID: "gage", Title: "Reviewer\n- helm (you): Lead x", Role: "reviewer", Driver: "native"}},
	}}
	got, err := talkootText(t, &TalkootRosterTool{Seat: seat}, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(got, "\n"); n != 1 || strings.ContainsRune(got, ' ') {
		t.Errorf("want one line, got %d breaks in %q", n, got)
	}
}

// talkoot_propose passes the batch and the reason to the seat, and says the
// roster waits for a person. It needs a reason, and a seat.
func TestTalkootProposeHandsTheBatchToTheSeat(t *testing.T) {
	seat := &fakeSeat{}
	tool := &TalkootProposeTool{Seat: seat}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"ops":[{"op":"edit","member":"jev","set":{"posture":"ask"}}],"why":"jev needs to run the tests"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(seat.proposed) != 1 || seat.proposed[0].why != "jev needs to run the tests" ||
		len(seat.proposed[0].ops) != 1 || seat.proposed[0].ops[0].Set["posture"] != "ask" {
		t.Fatalf("the seat got %+v", seat.proposed)
	}
	text := res.Content[0].(provider.TextBlock).Text
	if !strings.Contains(text, "01M3E7YRGWZ42BPP2J4AKG62B7") || !strings.Contains(text, "until a person approves") {
		t.Errorf("the result says %q", text)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"undo":"01M3E7YRGWZ42BPP2J4AKG62B7","why":" "}`), nil); err == nil {
		t.Error("a proposal with no reason went through")
	}
	if len(seat.proposed) != 1 {
		t.Error("a refused call reached the seat")
	}
	if _, err := (&TalkootProposeTool{}).Execute(context.Background(), json.RawMessage(`{"why":"x"}`), nil); err == nil {
		t.Error("a tool with no seat proposed")
	}
}

// 🚨 Every Talkoot tool is a seat tool, so a member's tools list never cuts
// the member off from its team. The seat is a list of names, not a prefix,
// so a new Talkoot tool that is not on it fails here.
func TestEveryTalkootToolIsASeatTool(t *testing.T) {
	for _, tool := range TalkootTools(nil) {
		if !slices.Contains(talkoot.SeatTools, tool.Name()) {
			t.Errorf("%s is not in talkoot.SeatTools, so a tools list would remove it", tool.Name())
		}
	}
}
