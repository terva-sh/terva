package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
	"terva.sh/terva/packages/privfs"
)

func TestPersonWaivesAndRestoresTeamBudget(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := t.Context()
	text := strings.Replace(string(crewText(cwd)), "budget_usd_per_day: 5", "budget_usd_per_day: 5 # keep the cap", 1)
	if _, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "crew", Text: text}); err != nil {
		t.Fatal(err)
	}
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	if err := run.do(func(rt *talkoot.Router) error { return rt.TurnEnded("helm", 6) }); err != nil {
		t.Fatal(err)
	}
	before, err := w.Talkoot(ctx, ctrlproto.TalkootRef{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(before.Members[0].Status.Pauses, "team") {
		t.Fatal("positive control: team cap did not trip")
	}
	waived := true
	v, err := w.UpdateTalkoot(ctx, ctrlproto.TalkootUpdateParams{ID: "crew", By: "Drew", TeamBudgetWaived: &waived})
	if err != nil {
		t.Fatal(err)
	}
	if !v.TeamBudgetWaived || v.BudgetUSDPerDay != 5 {
		t.Fatalf("view lost waiver or configured budget: %+v", v)
	}
	for _, m := range v.Members {
		if slices.Contains(m.Status.Pauses, "team") {
			t.Fatal("waiver did not lift the existing team pause")
		}
	}
	stored, err := os.ReadFile(filepath.Join(run.dir, talkoot.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), "team_budget_waived: true") || !strings.Contains(string(stored), "# keep the cap") {
		t.Fatalf("waiver did not persist: %s", stored)
	}
	page, err := w.TalkootRoom(ctx, ctrlproto.TalkootRoomParams{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range page.Lines {
		if l.Type == talkoot.LineRoster && l.By == "human:Drew" && strings.Contains(l.Reason, "waived") {
			found = true
		}
	}
	if !found {
		t.Fatal("room did not attribute the waiver to Drew")
	}
	waived = false
	v, err = w.UpdateTalkoot(ctx, ctrlproto.TalkootUpdateParams{ID: "crew", By: "Drew", TeamBudgetWaived: &waived})
	if err != nil {
		t.Fatal(err)
	}
	if v.TeamBudgetWaived || v.BudgetUSDPerDay != 5 {
		t.Fatal("restored cap does not match configured cap")
	}
	for _, m := range v.Members {
		if !slices.Contains(m.Status.Pauses, "team") {
			t.Fatal("restored cap did not count existing spend at once")
		}
	}
}

func TestTeamBudgetWaiverFailureLeavesRosterAndPauses(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("file modes require an ordinary Unix user")
	}
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	if _, err := w.talkootCreate(t.Context(), "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	if err := run.do(func(rt *talkoot.Router) error { return rt.TurnEnded("helm", 6) }); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(run.dir, talkoot.FileName))
	if err != nil {
		t.Fatal(err)
	}
	room := filepath.Join(run.dir, talkoot.RoomFile)
	if err := os.Chmod(room, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(room, 0o600) })
	waived := true
	if _, err := w.UpdateTalkoot(t.Context(), ctrlproto.TalkootUpdateParams{ID: "crew", By: "Drew", TeamBudgetWaived: &waived}); err == nil {
		t.Fatal("waiver applied without a room audit line")
	}
	after, err := os.ReadFile(filepath.Join(run.dir, talkoot.FileName))
	if err != nil || string(before) != string(after) {
		t.Fatalf("failed waiver changed roster: %v", err)
	}
	v, err := w.Talkoot(t.Context(), ctrlproto.TalkootRef{ID: "crew"})
	if err != nil || v.TeamBudgetWaived || !slices.Contains(v.Members[0].Status.Pauses, "team") {
		t.Fatalf("failed waiver changed running state: %+v, %v", v, err)
	}
}

// 🚨 Review finding on PR #1546: restoring the cap wrote its team-spend pause
// line before the roster file. A restoration that then failed left a pause
// line in the room for a cap that was never restored.
//
// ⚠️ Replay under the restored roster usually sets the pause in memory, and
// then EnforceTeamBudget writes nothing. It writes a line when a person's
// resume cleared the cap pause before the waiver, which this fixture does.
func TestFailedCapRestorationWritesNoPauseLine(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := t.Context()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	run, err := w.talkootRunOf("crew")
	if err != nil {
		t.Fatal(err)
	}
	// Over the $5 cap, then a person resumes the team past it.
	if err := run.do(func(rt *talkoot.Router) error { return rt.TurnEnded("helm", 6) }); err != nil {
		t.Fatal(err)
	}
	if err := run.do(func(rt *talkoot.Router) error { return rt.Resume("human:Drew", "", "") }); err != nil {
		t.Fatal(err)
	}
	waived := true
	if _, err := w.UpdateTalkoot(ctx, ctrlproto.TalkootUpdateParams{ID: "crew", By: "Drew", TeamBudgetWaived: &waived}); err != nil {
		t.Fatal(err)
	}
	teamPauses := func() int {
		ls, err := run.room.Read()
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, l := range ls {
			if l.Type == talkoot.LineGuard && l.Guard == talkoot.GuardTeamSpend && l.Action == talkoot.ActionPaused {
				n++
			}
		}
		return n
	}
	base := teamPauses()
	if base != 1 {
		t.Fatalf("fixture: want the one pause line from the first trip, got %d", base)
	}
	// ⚠️ File modes cannot fail the roster write alone: a read-only directory
	// also blocks the room's head file, and a directory in the roster's place
	// fails the waiver's first read. The seam fails only the last step, after
	// the room lines are written.
	errRoster := errors.New("roster write refused by the test")
	writeRosterFile = func(string, []byte) error { return errRoster }
	t.Cleanup(func() { writeRosterFile = privfs.WriteFile })
	waived = false
	if _, err := w.UpdateTalkoot(ctx, ctrlproto.TalkootUpdateParams{ID: "crew", By: "Drew", TeamBudgetWaived: &waived}); !errors.Is(err, errRoster) && (err == nil || !strings.Contains(err.Error(), errRoster.Error())) {
		t.Fatalf("fixture: the restoration did not fail at the roster write: %v", err)
	}
	if n := teamPauses(); n != base {
		t.Fatalf("a failed restoration wrote %d team-spend pause lines", n-base)
	}
	v, err := w.Talkoot(ctx, ctrlproto.TalkootRef{ID: "crew"})
	if err != nil || !v.TeamBudgetWaived || slices.Contains(v.Members[0].Status.Pauses, "team") {
		t.Fatalf("a failed restoration changed running state: %+v, %v", v, err)
	}
	// Positive control: the same restoration succeeds and pauses at once.
	writeRosterFile = privfs.WriteFile
	if _, err := w.UpdateTalkoot(ctx, ctrlproto.TalkootUpdateParams{ID: "crew", By: "Drew", TeamBudgetWaived: &waived}); err != nil {
		t.Fatal(err)
	}
	if n := teamPauses(); n != base+1 {
		t.Fatalf("a committed restoration wrote %d team-spend pause lines, want 1", n-base)
	}
}

// 🚨 Review finding on PR #1546: talkoot.create needs write and steer but
// not spend, and a raw roster text could start a team with its aggregate cap
// already waived and no attributed room record. Create now refuses a decoded
// waiver, whatever YAML form set it.
func TestCreateRefusesAWaivedTeamBudget(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := t.Context()
	base := string(crewText(cwd))
	for name, text := range map[string]string{
		"plain": strings.Replace(base, "budget_usd_per_day: 5\n", "budget_usd_per_day: 5\nteam_budget_waived: true\n", 1),
		"merge": strings.Replace(base, "budget_usd_per_day: 5\n", "budget_usd_per_day: 5\n<<: {team_budget_waived: true}\n", 1),
	} {
		t.Run(name, func(t *testing.T) {
			// Each case has its own id, so one case's team cannot make the
			// next fail as a duplicate.
			id := "crew-" + name
			if _, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: id, Text: text}); err == nil {
				t.Fatal("create started a team with its cap waived")
			} else if strings.Contains(err.Error(), "already exists") {
				t.Fatalf("fixture: refused as a duplicate, not for the waiver: %v", err)
			}
			if _, err := os.Stat(filepath.Join(talkoot.Dir(), id)); !os.IsNotExist(err) {
				t.Fatalf("a refused create left its directory: %v", err)
			}
		})
	}
	// Positive control: the same text without the waiver creates the team.
	if _, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "crew", Text: base}); err != nil {
		t.Fatal(err)
	}
}

func TestTeamBudgetWaiverRefusesMixedAndMemberEdits(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	if _, err := w.talkootCreate(t.Context(), "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	waived := true
	for _, p := range []ctrlproto.TalkootUpdateParams{
		{ID: "crew", By: "Drew", TeamBudgetWaived: &waived, Color: strp("#12A594")},
		{ID: "crew", By: "bad name", TeamBudgetWaived: &waived},
		{ID: "crew", By: "Drew", Ops: []ctrlproto.TalkootOp{{Op: "edit", Member: "helm", Set: map[string]any{"team_budget_waived": true}}}},
	} {
		if _, err := w.UpdateTalkoot(t.Context(), p); err == nil {
			t.Fatalf("accepted invalid update: %+v", p)
		}
	}
	v, err := w.Talkoot(t.Context(), ctrlproto.TalkootRef{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	if v.TeamBudgetWaived {
		t.Fatal("refused update changed the waiver")
	}
}
