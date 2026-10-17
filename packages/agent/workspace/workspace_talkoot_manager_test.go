package workspace

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// managerCrew makes the crew talkoot in a fresh home, and returns the
// workspace, its directory, and a count of the requests that reach the
// provider.
func managerCrew(t *testing.T) (*Workspace, string, *atomic.Int64) {
	t.Helper()
	cwd := talkootHome(t)
	var calls atomic.Int64
	w := openTalkootWorkspaceWith(t, cwd, func(rw http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		okProvider(rw, r)
	})
	if _, err := w.CreateTalkoot(t.Context(), ctrlproto.TalkootCreateParams{ID: "crew", Text: string(crewText(cwd))}); err != nil {
		t.Fatal(err)
	}
	return w, cwd, &calls
}

// The manager opens on a static greeting with no model call. A second open
// returns the same session, and the session holds the three manager tools and
// no tool of a seat.
func TestTheTeamManagerOpensOnceOnAStaticGreeting(t *testing.T) {
	w, _, calls := managerCrew(t)
	ctx := t.Context()
	info, err := w.ManagerTalkoot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Manager || !strings.EqualFold(info.Persona, "hautoja") || info.Recruit != "" {
		t.Fatalf("info: manager %v, persona %q, recruit %q", info.Manager, info.Persona, info.Recruit)
	}
	again, err := w.ManagerTalkoot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != info.ID {
		t.Fatalf("a second open made session %s, want %s", again.ID, info.ID)
	}
	s := w.existing(info.ID)
	if s == nil {
		t.Fatal("the manager session is not live")
	}
	msgs := s.agent.Messages()
	if len(msgs) != 1 || msgs[0].Meta[core.MetaSource] != managerGreetingSource || textOf(msgs[0]) != managerGreeting {
		t.Fatalf("transcript: %+v", msgs)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("the provider saw %d requests before the person wrote", n)
	}
	if tl, ok := s.agent.LookupTool("talkoot_propose"); !ok {
		t.Error("the manager has no talkoot_propose")
	} else if _, ok := tl.(*tools.TalkootManagerProposeTool); !ok {
		t.Errorf("talkoot_propose is %T, want the manager's", tl)
	}
	for _, name := range []string{"talkoot_teams", "talkoot_roster"} {
		if _, ok := s.agent.LookupTool(name); !ok {
			t.Errorf("the manager has no %s", name)
		}
	}
	for _, name := range []string{"talkoot_send", "talkoot_handoff", "talkoot_note_write", "talkoot_note_read"} {
		if _, ok := s.agent.LookupTool(name); ok {
			t.Errorf("the manager holds %s", name)
		}
	}
	if !w.SteersTalkoot(info.ID) {
		t.Error("the manager session does not steer, so a client without steer could prompt it")
	}
}

// The record survives a restart, so the next daemon opens the same
// conversation. A record that names a session which is not a manager is
// replaced, and not trusted.
func TestTheTeamManagerSurvivesARestartAndRefusesAForeignRecord(t *testing.T) {
	w, cwd, _ := managerCrew(t)
	ctx := t.Context()
	info, err := w.ManagerTalkoot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	w2 := openTalkootWorkspace(t, cwd)
	w2.LoadTalkoots()
	again, err := w2.ManagerTalkoot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != info.ID || !again.Manager {
		t.Fatalf("after a restart the manager is %s (manager %v), want %s", again.ID, again.Manager, info.ID)
	}

	plain, err := w2.CreateSession(ctx, ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(talkoot.Dir(), managerFile)
	raw, _ := json.Marshal(managerRecord{Sessions: map[string]string{w2.cwd: plain.ID}})
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	fresh, err := w2.ManagerTalkoot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ID == plain.ID || !fresh.Manager {
		t.Fatalf("the record named a plain session, and the manager is %s (manager %v)", fresh.ID, fresh.Manager)
	}
	if got := readManagerRecord(path).Sessions[w2.cwd]; got != fresh.ID {
		t.Errorf("the record names %q, want the new manager %q", got, fresh.ID)
	}
}

// The manager may propose a remove, which the recruiter may not. The proposal
// names the manager as its proposer and leaves the roster alone until a person
// decides it.
func TestTheTeamManagerProposesARemoveAndTheRosterWaits(t *testing.T) {
	w, _, _ := managerCrew(t)
	info, err := w.ManagerTalkoot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	m := talkootManager{w: w, session: info.ID}
	p, err := m.Propose("crew", []talkoot.Op{{Op: talkoot.OpRemove, Member: "jev"}}, "", "Nobody hands jev work.")
	if err != nil {
		t.Fatal(err)
	}
	if p.Proposer != talkoot.ManagerPrefix+info.ID {
		t.Fatalf("proposer %q, want %q", p.Proposer, talkoot.ManagerPrefix+info.ID)
	}
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	if !hasMember(run.roster.Load().Members, "jev") {
		t.Error("the proposal removed jev before a person decided it")
	}
	if _, err := m.Propose("nope", []talkoot.Op{{Op: talkoot.OpRemove, Member: "jev"}}, "", "why"); err == nil {
		t.Error("the manager proposed to a talkoot that does not exist")
	}
}

// talkoot_teams lists each team with its members, and talkoot_roster reads
// one team by id.
func TestTheTeamManagerReadsEveryTeam(t *testing.T) {
	w, _, _ := managerCrew(t)
	info, err := w.ManagerTalkoot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	m := talkootManager{w: w, session: info.ID}
	teams, err := m.Teams()
	if err != nil {
		t.Fatal(err)
	}
	if len(teams) != 1 || teams[0].ID != "crew" || !teams[0].Running || len(teams[0].Members) != 2 {
		t.Fatalf("teams: %+v", teams)
	}
	res, err := (&tools.TalkootManagerRosterTool{Manager: m}).Execute(t.Context(), []byte(`{"talkoot":"crew"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if text := res.Content[0].(provider.TextBlock).Text; !strings.Contains(text, "Talkoot crew.") || !strings.Contains(text, "- jev;") {
		t.Errorf("the roster text:\n%s", text)
	}
	// The id comes from the model. A path in it reaches no file: the lookup
	// checks the running teams, then talkoot.ValidID, before it stats one.
	for _, id := range []string{"nope", "../crew", "crew/../crew", "/etc"} {
		if _, err := m.Roster(id); err == nil || !strings.Contains(err.Error(), "talkoot_teams") {
			t.Errorf("roster of %q: %v", id, err)
		}
		if _, err := m.Propose(id, []talkoot.Op{{Op: talkoot.OpRemove, Member: "jev"}}, "", "why"); err == nil {
			t.Errorf("a proposal to %q passed", id)
		}
	}
}

func hasMember(members []talkoot.Member, id string) bool {
	for _, m := range members {
		if m.ID == id {
			return true
		}
	}
	return false
}

// A record that cannot be written takes its new session with it, so no marked
// session outlives the failure with the manager's tools.
func TestAFailedManagerRecordLeavesNoMarkedSession(t *testing.T) {
	w, _, _ := managerCrew(t)
	path := filepath.Join(talkoot.Dir(), managerFile)
	// A directory where the record goes makes the write fail.
	if err := os.MkdirAll(filepath.Join(path, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ManagerTalkoot(t.Context()); err == nil {
		t.Fatal("the open passed with no record written")
	}
	list, err := w.Sessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if w.isManagerSession(s.ID) {
			t.Errorf("session %s keeps the manager mark after the failed write", s.ID)
		}
	}
}

// Each workspace keeps its own manager, because a session store belongs to one
// workspace. A second workspace in the same home neither replaces the first
// one's manager nor resumes it.
func TestEachWorkspaceKeepsItsOwnTeamManager(t *testing.T) {
	w, _, _ := managerCrew(t)
	ctx := t.Context()
	first, err := w.ManagerTalkoot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	other := openTalkootWorkspace(t, testsupport.TempDir(t))
	second, err := other.ManagerTalkoot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Fatal("the second workspace resumed the first one's manager")
	}
	for i := 0; i < 2; i++ {
		again, err := w.ManagerTalkoot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if again.ID != first.ID {
			t.Fatalf("open %d in the first workspace made %s, want %s", i, again.ID, first.ID)
		}
		back, err := other.ManagerTalkoot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if back.ID != second.ID {
			t.Fatalf("open %d in the second workspace made %s, want %s", i, back.ID, second.ID)
		}
	}
}

// lakeDraft is a new team as the manager writes it: no home, which the
// workspace fills in.
const lakeDraft = "---\nname: lake\nbudget_usd_per_day: 3\nmembers:\n  - id: helm\n    role: coordinator\n---\n"

// A draft writes no roster. It belongs to the workspace that made it, a second
// draft of the same id replaces it, and the person's Create spends it.
func TestTheTeamManagerDraftsATeamThePersonCreates(t *testing.T) {
	w, _, _ := managerCrew(t)
	info, err := w.ManagerTalkoot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	m := talkootManager{w: w, session: info.ID}
	d, err := m.Draft("lake", lakeDraft, "A crew for the lake house.")
	if err != nil {
		t.Fatal(err)
	}
	if d.Replaced || d.Budget != 3 || len(d.Members) != 1 || d.Members[0].ID != "helm" {
		t.Fatalf("draft: %+v", d)
	}
	if talkootExists("lake") {
		t.Fatal("a draft wrote a roster")
	}
	list, err := talkoot.Drafts(w.cwd)
	if err != nil || len(list) != 1 || list[0].Why != "A crew for the lake house." || list[0].Session != info.ID {
		t.Fatalf("drafts: %+v, %v", list, err)
	}
	if other, _ := talkoot.Drafts(testsupport.TempDir(t)); len(other) != 0 {
		t.Errorf("another workspace sees %d drafts", len(other))
	}
	if d, err = m.Draft("lake", lakeDraft, "A smaller crew."); err != nil || !d.Replaced {
		t.Fatalf("a second draft: %+v, %v", d, err)
	}

	pv, _, err := w.talkootPreview(ctrlproto.TalkootPreviewParams{ID: "lake", Text: list[0].Text})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.CreateTalkoot(t.Context(), ctrlproto.TalkootCreateParams{ID: "lake", Text: pv.Text}); err != nil {
		t.Fatal(err)
	}
	if left, _ := talkoot.Drafts(w.cwd); len(left) != 0 {
		t.Errorf("create left %d drafts", len(left))
	}
}

// The manager cannot draft over a team, set a home, or name a member that
// cannot run here. Each refusal saves nothing.
func TestTheTeamManagerDraftRefusesWhatCreateWouldRefuse(t *testing.T) {
	w, _, _ := managerCrew(t)
	m := talkootManager{w: w, session: "s"}
	for name, c := range map[string]struct{ id, text string }{
		"an existing team": {"crew", strings.Replace(lakeDraft, "lake", "crew", 1)},
		"a home":           {"lake", strings.Replace(lakeDraft, "budget_usd", "home: /etc\nbudget_usd", 1)},
		"a missing driver": {"lake", strings.Replace(lakeDraft, "role: coordinator", "role: coordinator\n    driver: nope-agent", 1)},
		"a bad id":         {"Lake", lakeDraft},
		"no members":       {"lake", "---\nname: lake\nbudget_usd_per_day: 3\nmembers: []\n---\n"},
	} {
		if _, err := m.Draft(c.id, c.text, "why"); err == nil {
			t.Errorf("%s: the draft was saved", name)
		}
	}
	if _, err := m.Draft("lake", lakeDraft, strings.Repeat("x", talkoot.MaxDraftWhyBytes+1)); err == nil {
		t.Error("an oversized reason was saved")
	}
	if list, _ := talkoot.Drafts(w.cwd); len(list) != 0 {
		t.Errorf("refusals saved %d drafts", len(list))
	}
}
