package talkoot

import (
	"strings"
	"testing"
)

// A team op sets the title, the colour, the budget, and the charter. The
// frontmatter keeps its comments, and the members do not change.
func TestATeamOpChangesTheTeamFields(t *testing.T) {
	before, err := Parse([]byte(commented), "in.md")
	if err != nil {
		t.Fatal(err)
	}
	out, r := mustApply(t, commented, Op{Op: OpTeam, Set: map[string]any{
		"title": "Lake crew", "color": "#3B82F6", "budget_usd_per_day": float64(55), "charter": "Work from tickets only.\n",
	}})
	if r.Title != "Lake crew" || r.Color != "#3B82F6" || r.BudgetUSDPerDay != 55 || r.Charter != "Work from tickets only." {
		t.Fatalf("roster: title %q color %q budget %v charter %q", r.Title, r.Color, r.BudgetUSDPerDay, r.Charter)
	}
	if !strings.Contains(out, "# The team for the lake schema.") || !strings.Contains(out, "# Plans only, until the schema settles.") {
		t.Errorf("the result lost a comment:\n%s", out)
	}
	if len(r.Members) != len(before.Members) || Diff(before, r) != nil {
		t.Errorf("a team op changed a member: %+v", Diff(before, r))
	}
	got := DiffTeam(before, r)
	if len(got) != 4 || got[0].Field != "title" || got[3].Field != "budget_usd_per_day" {
		t.Fatalf("team diff: %+v", got)
	}
	if !TeamWidens(got[3]) {
		t.Error("a raised budget does not widen")
	}
	if ClassOfOps([]Op{{Op: OpTeam, Set: map[string]any{"title": "x"}}}) != ClassLook ||
		ClassOfOps([]Op{{Op: OpTeam, Set: map[string]any{"charter": "x"}}}) != ClassVoice ||
		ClassOfOps([]Op{{Op: OpTeam, Set: map[string]any{"budget_usd_per_day": 3}}}) != ClassAuthority {
		t.Error("a team field has the wrong class")
	}
	if s := Summarize([]Op{{Op: OpTeam, Set: map[string]any{"title": "x", "color": "#000000"}}}); s != "team (color, title)" {
		t.Errorf("summary %q", s)
	}
}

// The undo of a team op puts each field back, an unset field included, and a
// batch can mix a team op with a member op.
func TestATeamOpUndoesAndMixesWithAMemberOp(t *testing.T) {
	before, err := Parse([]byte(commented), "in.md")
	if err != nil {
		t.Fatal(err)
	}
	out, after := mustApply(t, commented,
		Op{Op: OpTeam, Set: map[string]any{"title": "Lake crew", "budget_usd_per_day": 20, "charter": ""}},
		Op{Op: OpEdit, Member: "atlas", Set: map[string]any{"posture": "ask"}},
	)
	if after.Charter != "" || after.BudgetUSDPerDay != 20 {
		t.Fatalf("charter %q budget %v", after.Charter, after.BudgetUSDPerDay)
	}
	if TeamWidens(DiffTeam(before, after)[2]) {
		t.Error("a lowered budget widens")
	}
	undo, err := Inverse(Diff(before, after), DiffTeam(before, after))
	if err != nil {
		t.Fatal(err)
	}
	_, back := mustApply(t, out, undo...)
	if back.Title != "" || back.BudgetUSDPerDay != 40 || back.Charter != before.Charter {
		t.Errorf("after the undo: title %q budget %v charter %q", back.Title, back.BudgetUSDPerDay, back.Charter)
	}
	if d := DiffTeam(before, back); d != nil {
		t.Errorf("the undo left %+v", d)
	}
	if d := Diff(before, back); d != nil {
		t.Errorf("the undo left member changes %+v", d)
	}
}

func TestCheckOpsRefusesABadTeamOp(t *testing.T) {
	r, err := Parse([]byte(commented), "in.md")
	if err != nil {
		t.Fatal(err)
	}
	for name, op := range map[string]Op{
		"a member":       {Op: OpTeam, Member: "helm", Set: map[string]any{"title": "x"}},
		"no field":       {Op: OpTeam},
		"the waiver":     {Op: OpTeam, Set: map[string]any{"team_budget_waived": true}},
		"the home":       {Op: OpTeam, Set: map[string]any{"home": "/tmp"}},
		"the members":    {Op: OpTeam, Set: map[string]any{"members": []any{}}},
		"a bad colour":   {Op: OpTeam, Set: map[string]any{"color": "blue"}},
		"no budget":      {Op: OpTeam, Set: map[string]any{"budget_usd_per_day": nil}},
		"a zero budget":  {Op: OpTeam, Set: map[string]any{"budget_usd_per_day": 0}},
		"a text budget":  {Op: OpTeam, Set: map[string]any{"budget_usd_per_day": "50"}},
		"two-line title": {Op: OpTeam, Set: map[string]any{"title": "a\nb"}},
		"a long charter": {Op: OpTeam, Set: map[string]any{"charter": strings.Repeat("x", MaxCharterBytes+1)}},
	} {
		if err := CheckOps(r, []Op{op}); err == nil {
			t.Errorf("%s: the team op passed", name)
		}
	}
	if err := CheckOps(r, []Op{{Op: OpTeam, Set: map[string]any{"team_budget_waived": true}}}); err == nil || !strings.Contains(err.Error(), "0027") {
		t.Errorf("the waiver refusal does not name 0027: %v", err)
	}
	if !HasTeamOp([]Op{{Op: OpEdit, Member: "helm"}, {Op: OpTeam}}) || HasTeamOp([]Op{{Op: OpEdit, Member: "helm"}}) {
		t.Error("HasTeamOp is wrong")
	}
}

// A field whose current value does not fit a proposal cannot change through
// one, because the undo could not restore it.
func TestATeamOpRefusesAFieldWhoseUndoCouldNotRestoreIt(t *testing.T) {
	long := strings.Repeat("x", MaxCharterBytes+1)
	text := strings.Replace(commented, "budget_usd_per_day: 40\n", "budget_usd_per_day: 40\ntitle: |\n  two\n  lines\n", 1) + long + "\n"
	r, err := Parse([]byte(text), "in.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"charter", "title"} {
		err := CheckOps(r, []Op{{Op: OpTeam, Set: map[string]any{field: "short"}}})
		if err == nil || !strings.Contains(err.Error(), "talkoot.update") {
			t.Errorf("%s: %v", field, err)
		}
	}
	if err := CheckOps(r, []Op{{Op: OpTeam, Set: map[string]any{"color": "#3B82F6"}}}); err != nil {
		t.Errorf("a field that fits still changes: %v", err)
	}
}
