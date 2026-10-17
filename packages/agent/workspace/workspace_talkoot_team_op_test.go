package workspace

import (
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
)

var teamRetitle = []talkoot.Op{{Op: talkoot.OpTeam, Set: map[string]any{"title": "Lake crew", "budget_usd_per_day": float64(9)}}}

// The manager proposes a team op. The card shows each team field before and
// after with its class, and flags the raised budget. Nothing changes until a
// person approves. The roster line records the team change, and an undo puts
// both fields back.
func TestTheManagerProposesATeamChangeAndAPersonApplies(t *testing.T) {
	w, _, _ := managerCrew(t)
	ctx := t.Context()
	info, err := w.ManagerTalkoot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(talkoot.Dir(), "crew", talkoot.FileName)
	before := string(readFile(t, path))
	p, err := talkootManager{w: w, session: info.ID}.Propose("crew", teamRetitle, "", "The team needs a name and more room.")
	if err != nil {
		t.Fatal(err)
	}
	if string(readFile(t, path)) != before {
		t.Fatal("a proposal changed talkoot.md")
	}
	cards := inbox(t, w)
	if len(cards) != 1 || cards[0].Proposal == nil {
		t.Fatalf("inbox: %+v", cards)
	}
	wp := cards[0].Proposal
	if wp.Class != "authority" || len(wp.Changes) != 0 || len(wp.Team) != 2 {
		t.Fatalf("the card shows class %q, changes %+v, team %+v", wp.Class, wp.Changes, wp.Team)
	}
	title, budget := wp.Team[0], wp.Team[1]
	if title.Field != "title" || title.Class != "look" || title.Before != nil || title.After != "Lake crew" || title.Widens {
		t.Errorf("title change: %+v", title)
	}
	if budget.Field != "budget_usd_per_day" || budget.Class != "authority" || budget.Before != float64(5) || budget.After != float64(9) || !budget.Widens {
		t.Errorf("budget change: %+v", budget)
	}
	if !strings.HasPrefix(wp.Title, "the team manager proposes: team (budget_usd_per_day, title)") {
		t.Errorf("card title %q", wp.Title)
	}

	if _, err := w.DecideTalkoot(ctx, ctrlproto.TalkootDecideParams{ID: "crew", By: "sothr", Proposal: p.ID, Decision: ctrlproto.TalkootDecisionApprove}); err != nil {
		t.Fatal(err)
	}
	r, err := talkoot.Parse(readFile(t, path), "crew.md")
	if err != nil {
		t.Fatal(err)
	}
	if r.Title != "Lake crew" || r.BudgetUSDPerDay != 9 {
		t.Fatalf("after the approval: title %q budget %v", r.Title, r.BudgetUSDPerDay)
	}
	lines := rosterLines(t)
	last := lines[len(lines)-1]
	if last.Proposal != p.ID || len(last.Team) != 2 || last.Team[0].After != "Lake crew" {
		t.Fatalf("the roster line is %+v", last)
	}
	page, err := w.TalkootRoom(ctx, ctrlproto.TalkootRoomParams{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	var wired bool
	for _, l := range page.Lines {
		if l.Type == talkoot.LineRoster && l.Proposal == p.ID && len(l.Team) == 2 && l.Team[1].Widens {
			wired = true
		}
	}
	if !wired {
		t.Error("talkoot.room does not carry the team change")
	}

	undo, err := w.ProposeTalkoot(ctx, ctrlproto.TalkootProposeParams{ID: "crew", By: "sothr", Undo: p.ID, Why: "not yet"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.DecideTalkoot(ctx, ctrlproto.TalkootDecideParams{ID: "crew", By: "sothr", Proposal: undo.ID, Decision: ctrlproto.TalkootDecisionApprove}); err != nil {
		t.Fatal(err)
	}
	r, err = talkoot.Parse(readFile(t, path), "crew.md")
	if err != nil {
		t.Fatal(err)
	}
	if r.Title != "" || r.BudgetUSDPerDay != 5 {
		t.Errorf("after the undo: title %q budget %v", r.Title, r.BudgetUSDPerDay)
	}
}

// The team op is the manager's and a person's alone. A member and a recruiter
// are refused when they propose, and an approval refuses a record whose
// proposer was changed on disk.
func TestOnlyTheManagerOrAPersonProposesATeamChange(t *testing.T) {
	w, _, _ := managerCrew(t)
	ctx := t.Context()
	if _, err := w.talkootPropose("crew", "helm", teamRetitle, "", "why", "", nil); err == nil || !strings.Contains(err.Error(), "only the team manager or a person") {
		t.Errorf("a member's team op: %v", err)
	}
	rec, err := w.RecruitTalkoot(ctx, ctrlproto.TalkootRecruitParams{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (talkootRecruit{w: w, session: rec.ID, talkoot: "crew"}).Propose(teamRetitle, "", "why"); err == nil {
		t.Error("a recruiter proposed a team op")
	}
	if _, err := w.ProposeTalkoot(ctx, ctrlproto.TalkootProposeParams{ID: "crew", By: "sothr", Ops: []ctrlproto.TalkootOp{{Op: "team", Set: map[string]any{"title": "Mine"}}}, Why: "a name"}); err != nil {
		t.Errorf("a person's team op: %v", err)
	}
	if _, err := w.ProposeTalkoot(ctx, ctrlproto.TalkootProposeParams{ID: "crew", By: "sothr", Ops: []ctrlproto.TalkootOp{{Op: "team", Set: map[string]any{"team_budget_waived": true}}}, Why: "spend"}); err == nil || !strings.Contains(err.Error(), "0027") {
		t.Errorf("a waiver through a proposal: %v", err)
	}

	info, err := w.ManagerTalkoot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p, err := talkootManager{w: w, session: info.ID}.Propose("crew", teamRetitle, "", "why")
	if err != nil {
		t.Fatal(err)
	}
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	p.Proposer = "helm"
	if err := talkoot.SaveProposal(run.dir, p); err != nil {
		t.Fatal(err)
	}
	if _, err := w.DecideTalkoot(ctx, ctrlproto.TalkootDecideParams{ID: "crew", By: "sothr", Proposal: p.ID, Decision: ctrlproto.TalkootDecisionApprove}); err == nil || !strings.Contains(err.Error(), "only the team manager or a person") {
		t.Errorf("an approval of a tampered team op: %v", err)
	}
	if r := run.roster.Load(); r.Title != "" {
		t.Errorf("the tampered approval applied: title %q", r.Title)
	}
}

// The undo of an approved team change is a team op too, so a member cannot
// propose it.
func TestAMemberCannotUndoATeamChange(t *testing.T) {
	w, _, _ := managerCrew(t)
	ctx := t.Context()
	p, err := w.ProposeTalkoot(ctx, ctrlproto.TalkootProposeParams{ID: "crew", By: "sothr", Ops: []ctrlproto.TalkootOp{{Op: "team", Set: map[string]any{"title": "Mine"}}}, Why: "a name"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.DecideTalkoot(ctx, ctrlproto.TalkootDecideParams{ID: "crew", By: "sothr", Proposal: p.ID, Decision: ctrlproto.TalkootDecisionApprove}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.talkootPropose("crew", "helm", nil, p.ID, "why", "", nil); err == nil || !strings.Contains(err.Error(), "only the team manager or a person") {
		t.Errorf("a member's undo of a team change: %v", err)
	}
	if _, err := w.ProposeTalkoot(ctx, ctrlproto.TalkootProposeParams{ID: "crew", By: "sothr", Undo: p.ID, Why: "back"}); err != nil {
		t.Errorf("a person's undo of a team change: %v", err)
	}
}
