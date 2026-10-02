package talkoot

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTemplatesCannotWaiveTeamBudget(t *testing.T) {
	text := strings.Replace(tigerTeam, "home: ~/src/terva\n", "", 1)
	text = strings.Replace(text, "budget_usd_per_day: 40", "budget_usd_per_day: 40\nteam_budget_waived: true", 1)
	for _, source := range []string{"user", "ext:example", "builtin", "repo"} {
		tmpl := parseTemplate("test", []byte(text), source, "test.md")
		if !strings.Contains(tmpl.Problem, "cannot waive") {
			t.Fatalf("%s template accepted a waiver: %+v", source, tmpl)
		}
		// A caller that supplies raw template data cannot bypass the parser.
		if _, err := Instantiate(Template{Name: "test", Raw: []byte(text)}, "/repo", 0, nil); err == nil {
			t.Fatal("raw template enabled the waiver")
		}
	}
	text = strings.Replace(text, "team_budget_waived: true", "team_budget_waived: false", 1)
	if tmpl := parseTemplate("test", []byte(text), "repo", "test.md"); tmpl.Problem != "" {
		t.Fatalf("explicit false waiver refused: %s", tmpl.Problem)
	}
}

func TestTeamBudgetWaiverStillRequiresAValidBudget(t *testing.T) {
	for _, value := range []string{"0", "-1", ".nan", ".inf"} {
		text := strings.Replace(tigerTeam, "budget_usd_per_day: 40", "budget_usd_per_day: "+value+"\nteam_budget_waived: true", 1)
		r, err := Parse([]byte(text), "test.md")
		if err == nil && Validate(r, fakeEnv()) == nil {
			t.Fatalf("waiver accepted invalid budget %s", value)
		}
	}
	text := strings.Replace(tigerTeam, "budget_usd_per_day: 40", "team_budget_waived: true", 1)
	if r, err := Parse([]byte(text), "test.md"); err == nil && Validate(r, fakeEnv()) == nil {
		t.Fatal("waiver accepted a missing team budget")
	}
	for _, key := range []string{"team_budget_waived: [true]", "team_budget_waived: nonsense", "team_budget_waved: true"} {
		text := strings.Replace(tigerTeam, "budget_usd_per_day: 40", "budget_usd_per_day: 40\n"+key, 1)
		if _, err := Parse([]byte(text), "test.md"); err == nil {
			t.Fatalf("accepted invalid waiver %s", key)
		}
	}
}

func TestTeamBudgetWaiverPreservesRoster(t *testing.T) {
	text := []byte(strings.Replace(tigerTeam, "budget_usd_per_day: 40", "budget_usd_per_day: 40 # retain this cap", 1))
	before := mustParse(t, string(text))
	for _, waived := range []bool{true, false} {
		next, err := SetTeamBudgetWaived(text, waived)
		if err != nil {
			t.Fatal(err)
		}
		after := mustParse(t, string(next))
		before.TeamBudgetWaived = waived
		if !reflect.DeepEqual(before, after) || !strings.Contains(string(next), "# retain this cap") {
			t.Fatalf("waiver changed more than its flag: %+v", after)
		}
		text = next
	}
}

// 🚨 Review finding on PR #1546 (round 2): a whole-text update can waive the
// cap through a YAML merge key. Dropping the literal key left the merged
// value in force, so restoring the cap always failed.
func TestRestoringClearsAMergedWaiver(t *testing.T) {
	text := []byte(strings.Replace(tigerTeam, "budget_usd_per_day: 40", "budget_usd_per_day: 40\n<<: {team_budget_waived: true}", 1))
	if !mustParse(t, string(text)).TeamBudgetWaived {
		t.Fatal("fixture: the merge key did not waive the cap")
	}
	next, err := SetTeamBudgetWaived(text, false)
	if err != nil {
		t.Fatalf("restore refused a merged waiver: %v", err)
	}
	if mustParse(t, string(next)).TeamBudgetWaived {
		t.Fatal("restore left the merged waiver in force")
	}
}

func TestTeamBudgetWaiverKeepsMemberLimits(t *testing.T) {
	f := newFixture(t, func(r *Roster, _ *Limits) {
		r.TeamBudgetWaived = true
		r.BudgetUSDPerDay = 1
		r.Members[0].BudgetUSDPerDay = 0.5
		r.Members[1].TurnsPerDay = 1
		// Other member caps cannot exceed the team's configured cap.
		for i := 2; i < len(r.Members); i++ {
			r.Members[i].BudgetUSDPerDay = 0
		}
	})
	if _, err := f.router.Post("Drew", []string{"helm", "atlas"}, "work", nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.router.TurnEnded("helm", 2); err != nil {
		t.Fatal(err)
	}
	if err := f.router.TurnEnded("atlas", 2); err != nil {
		t.Fatal(err)
	}
	for _, st := range f.router.Statuses() {
		if slices.Contains(st.Pauses, pauseTeam) {
			t.Fatalf("waived team limit paused %s", st.Member)
		}
		if st.Member == "helm" && !slices.Contains(st.Pauses, pauseSpend) {
			t.Fatal("member spend limit disappeared")
		}
		if st.Member == "atlas" && !slices.Contains(st.Pauses, pauseTurns) {
			t.Fatal("member turn limit disappeared")
		}
	}
	f.reopen()
	if len(f.guard(GuardTeamSpend)) != 0 {
		t.Fatal("waived cap wrote a team-spend guard")
	}
	f.clock.advance(24 * time.Hour)
	if _, err := f.router.Post("Drew", []string{"gage"}, "next day", nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.router.TurnEnded("gage", 3); err != nil {
		t.Fatal(err)
	}
	if len(f.guard(GuardTeamSpend)) != 0 {
		t.Fatal("waiver expired at the daily reset")
	}
}

func TestTeamBudgetWaiverLiftsOnlyTeamSpendPause(t *testing.T) {
	f := newFixture(t, nil)
	if _, err := f.router.Post("Drew", nil, "work", nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.router.TurnEnded("helm", 50); err != nil {
		t.Fatal(err)
	}
	if len(f.guard(GuardTeamSpend)) != 1 {
		t.Fatal("positive control: cap did not trip")
	}
	if err := f.router.Pause("human:Drew", "atlas", "", "hold this member"); err != nil {
		t.Fatal(err)
	}
	f.roster.TeamBudgetWaived = true
	f.reopen()
	for _, st := range f.router.Statuses() {
		if slices.Contains(st.Pauses, pauseTeam) {
			t.Fatal("historical team cap still blocks under waiver")
		}
		if st.Member == "atlas" && !slices.Contains(st.Pauses, pausePerson) {
			t.Fatal("waiver removed a person's pause")
		}
	}
	f.roster.TeamBudgetWaived = false
	f.reopen()
	if err := f.router.EnforceTeamBudget(); err != nil {
		t.Fatal(err)
	}
	for _, st := range f.router.Statuses() {
		if !slices.Contains(st.Pauses, pauseTeam) {
			t.Fatal("restored cap did not count previous spend")
		}
	}
}
