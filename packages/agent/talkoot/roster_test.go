package talkoot

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/look"
	"terva.sh/terva/packages/testsupport"
)

// tigerTeam is the example roster from docs/proposals/talkoot.md, with every
// field in the proposal's field table set on at least one member.
const tigerTeam = `---
name: tiger
title: Tiger Team
home: ~/src/terva
budget_usd_per_day: 40
members:
  - id: helm
    role: coordinator
    persona: mieli
    driver: native
    tier: strong
  - id: atlas
    role: planner
    persona: arkkitehti
    driver: native
    tier: strong
    posture: plan
  - id: jev
    role: specialist
    title: Developer
    driver: claude
    model: opus
    workspace: worktree
    posture: auto-edit
    budget_usd_per_day: 20
  - id: yelp
    role: specialist
    title: Reviewer
    reviewer: true
    driver: acp:codex
    posture: plan
    turns_per_day: 60
  - id: gage
    role: specialist
    persona: koestaja
    driver: native
    tier: medium
---

Work from tickets. Hand work off with a ticket or a branch, never a summary.
`

// fakeEnv knows the personas and drivers the tests name, and nothing else.
func fakeEnv() Env {
	personas := map[string]bool{"mieli": true, "arkkitehti": true, "koestaja": true}
	drivers := map[string]bool{"claude": true, "acp:codex": false}
	return Env{
		PersonaExists: func(ref string) bool { return personas[ref] },
		Driver: func(name string) (bool, error) {
			cost, ok := drivers[name]
			if !ok {
				return false, errors.New("unknown")
			}
			return cost, nil
		},
		Tiers: []string{"weak", "medium", "strong", "cheap"},
		// claude can narrow the core tools, and acp:codex cannot narrow.
		DriverTools: func(driver string, tools []string) error {
			if driver != "claude" {
				return errors.New("the backend has no allowlist")
			}
			for _, t := range tools {
				if !slices.Contains([]string{"read", "write", "edit", "bash", "grep", "glob"}, t) {
					return fmt.Errorf("claude has no tool %q", t)
				}
			}
			return nil
		},
	}
}

func mustParse(t *testing.T, raw string) Roster {
	t.Helper()
	r, err := Parse([]byte(raw), "test.md")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestParseReadsEveryFieldInTheProposalTable(t *testing.T) {
	r := mustParse(t, tigerTeam)
	if r.Name != "tiger" || r.Title != "Tiger Team" || r.Home != "~/src/terva" || r.BudgetUSDPerDay != 40 {
		t.Errorf("team fields: %+v", r)
	}
	if !strings.HasPrefix(r.Charter, "Work from tickets.") {
		t.Errorf("charter: %q", r.Charter)
	}
	want := map[string]Member{
		"helm":  {ID: "helm", Role: RoleCoordinator, Persona: "mieli", Driver: DriverNative, Tier: "strong", Posture: "plan", Workspace: WorkspaceShared},
		"atlas": {ID: "atlas", Role: RolePlanner, Persona: "arkkitehti", Driver: DriverNative, Tier: "strong", Posture: "plan", Workspace: WorkspaceShared},
		"jev":   {ID: "jev", Role: RoleSpecialist, Title: "Developer", Driver: "claude", Model: "opus", Posture: "auto-edit", Workspace: WorkspaceWorktree, BudgetUSDPerDay: 20},
		"yelp":  {ID: "yelp", Role: RoleSpecialist, Title: "Reviewer", Reviewer: true, Driver: "acp:codex", Posture: "plan", Workspace: WorkspaceShared, TurnsPerDay: 60},
		"gage":  {ID: "gage", Role: RoleSpecialist, Persona: "koestaja", Driver: DriverNative, Tier: "medium", Posture: "plan", Workspace: WorkspaceShared},
	}
	if len(r.Members) != len(want) {
		t.Fatalf("members: got %d, want %d", len(r.Members), len(want))
	}
	for _, m := range r.Members {
		if !reflect.DeepEqual(m, want[m.ID]) {
			t.Errorf("member %s:\n got %+v\nwant %+v", m.ID, m, want[m.ID])
		}
	}
	if err := Validate(r, fakeEnv()); err != nil {
		t.Errorf("the proposal's own example must validate: %v", err)
	}
}

func TestDefaultsFollowTheWorkspace(t *testing.T) {
	r := mustParse(t, "---\nname: t\nhome: /x\nbudget_usd_per_day: 1\nmembers:\n  - id: a\n    role: coordinator\n  - id: b\n    role: specialist\n    workspace: worktree\n---\n")
	a, b := r.Members[0], r.Members[1]
	if a.Driver != DriverNative || a.Workspace != WorkspaceShared || a.Posture != "plan" {
		t.Errorf("a shared member defaults to native and plan, got %+v", a)
	}
	if b.Posture != "auto-edit" {
		t.Errorf("a worktree member defaults to auto-edit, got %q", b.Posture)
	}
}

// A misspelt cap must not parse as no cap.
func TestParseRefusesAnUnknownKey(t *testing.T) {
	raw := strings.Replace(tigerTeam, "    budget_usd_per_day: 20", "    budget_usd_per_dy: 20", 1)
	_, err := Parse([]byte(raw), "test.md")
	if err == nil || !strings.Contains(err.Error(), "budget_usd_per_dy") {
		t.Fatalf("want an error naming the unknown key, got %v", err)
	}
}

func TestParseNeedsFrontmatter(t *testing.T) {
	for _, raw := range []string{"", "name: tiger\n", "---\nname: tiger\n"} {
		if _, err := Parse([]byte(raw), "test.md"); err == nil {
			t.Errorf("want an error for %q", raw)
		}
	}
	if _, err := Parse([]byte("---\r\nname: t\r\n---\r\nbody\r\n"), "test.md"); err != nil {
		t.Errorf("CRLF frontmatter should parse: %v", err)
	}
}

// Each case breaks one rule in the Tiger team and names the text the problem
// must carry. The problem text is what a person reads to fix the file.
func TestValidateRefusals(t *testing.T) {
	cases := []struct {
		name string
		edit func(r *Roster)
		want string
	}{
		{"no budget", func(r *Roster) { r.BudgetUSDPerDay = 0 }, "budget_usd_per_day is missing"},
		{"NaN budget", func(r *Roster) { r.BudgetUSDPerDay = math.NaN() }, "budget_usd_per_day is missing or not a positive number"},
		{"infinite budget", func(r *Roster) { r.BudgetUSDPerDay = math.Inf(1) }, "budget_usd_per_day is missing or not a positive number"},
		{"NaN member budget", func(r *Roster) { r.Members[2].BudgetUSDPerDay = math.NaN() }, "jev: budget_usd_per_day is not a positive number"},
		{"posture in another case", func(r *Roster) { r.Members[0].Posture = "YOLO" }, `posture "YOLO" is not an approval mode`},
		{"no home", func(r *Roster) { r.Home = "" }, "home is missing"},
		{"bad team colour", func(r *Roster) { r.Color = "orange" }, `color "orange" is not a #RRGGBB value`},
		{"two coordinators", func(r *Roster) { r.Members[1].Role = RoleCoordinator }, "exactly one coordinator, and this one has 2"},
		{"no coordinator", func(r *Roster) { r.Members[0].Role = RoleSpecialist }, "exactly one coordinator, and this one has 0"},
		{"bad role", func(r *Roster) { r.Members[4].Role = "boss" }, `gage: role "boss"`},
		{"duplicate id", func(r *Roster) { r.Members[1].ID = "helm" }, `"helm" appears twice`},
		{"bad id", func(r *Roster) { r.Members[1].ID = "At/las" }, `"At/las"`},
		{"two shared writers", func(r *Roster) {
			r.Members[1].Posture = "ask"
			r.Members[4].Posture = "workspace"
		}, "members atlas, gage can all write to the shared checkout"},
		{"yolo in the shared checkout", func(r *Roster) { r.Members[0].Posture = "yolo" }, "helm: posture yolo needs workspace: worktree"},
		{"bad posture", func(r *Roster) { r.Members[0].Posture = "godmode" }, `posture "godmode"`},
		{"bad workspace", func(r *Roster) { r.Members[0].Workspace = "home" }, `workspace "home"`},
		{"mark shape outside the set", func(r *Roster) { r.Members[0].Mark = &look.Mark{Shape: "star"} }, `helm: mark shape "star"`},
		{"mark color that is not hex", func(r *Roster) { r.Members[0].Mark = &look.Mark{Color: "blue"} }, `helm: mark color "blue"`},
		{"empty mark", func(r *Roster) { r.Members[0].Mark = &look.Mark{} }, "helm: mark sets neither shape nor color"},
		{"model and tier", func(r *Roster) { r.Members[0].Model = "opus" }, "helm: set model or tier, not both"},
		{"bad tier", func(r *Roster) { r.Members[0].Tier = "huge" }, `tier "huge"`},
		{"member over the team budget", func(r *Roster) { r.Members[2].BudgetUSDPerDay = 41 }, "jev: budget_usd_per_day 41.00 is above the talkoot's 40.00"},
		{"reviewer that is not a specialist", func(r *Roster) { r.Members[1].Reviewer = true }, "atlas: reviewer is for a specialist"},
		{"no-cost driver without turns", func(r *Roster) { r.Members[3].TurnsPerDay = 0 }, `yelp: driver "acp:codex" reports no cost, so the member needs turns_per_day`},
		{"missing persona", func(r *Roster) { r.Members[4].Persona = "nobody" }, `gage: persona "nobody" not found`},
		{"unregistered driver", func(r *Roster) { r.Members[2].Driver = "gemini" }, `jev: driver "gemini" is not registered`},
		{"an empty tools list", func(r *Roster) { r.Members[0].Tools = []string{} }, "helm: tools is empty"},
		{"a bad tool name", func(r *Roster) { r.Members[0].Tools = []string{"read", "rm -rf"} }, `helm: tool "rm -rf" must be`},
		{"a star inside a name", func(r *Roster) { r.Members[0].Tools = []string{"mcp_*_x"} }, `helm: tool "mcp_*_x" must be`},
		{"a tool named twice", func(r *Roster) { r.Members[0].Tools = []string{"read", "read"} }, `helm: tool "read" appears twice`},
		{"too many tools", func(r *Roster) {
			for i := range MaxTools + 1 {
				r.Members[0].Tools = append(r.Members[0].Tools, fmt.Sprintf("t%d", i))
			}
		}, "helm: tools names 65 tools, above the 64 limit"},
		{"an unregistered driver with tools", func(r *Roster) {
			r.Members[2].Driver = "gemini"
			r.Members[2].Tools = []string{"read"}
		}, `jev: driver "gemini" is not registered`},
		{"tools on a backend with no allowlist", func(r *Roster) { r.Members[3].Tools = []string{"read"} }, `yelp: driver "acp:codex" cannot narrow its tools`},
		{"yolo on a worker, even in a worktree", func(r *Roster) { r.Members[2].Posture = "yolo" }, "jev: posture yolo is not open to a worker member"},
		{"idle_stop that is no duration", func(r *Roster) { r.Members[2].IdleStop = "soon" }, `jev: idle_stop "soon" is not a duration`},
		{"idle_stop under a minute", func(r *Roster) { r.Members[2].IdleStop = "30s" }, "jev: idle_stop 30s is shorter than 1m0s"},
		{"idle_stop below zero", func(r *Roster) { r.Members[2].IdleStop = "-5m" }, "jev: idle_stop -5m0s is shorter than"},
		{"idle_stop on a native member", func(r *Roster) { r.Members[0].IdleStop = "30m" }, "helm: idle_stop is for a worker member"},
		{"a tool the backend cannot name", func(r *Roster) { r.Members[2].Tools = []string{"read", "mcp_github_*"} }, `jev: driver "claude" cannot narrow its tools: claude has no tool "mcp_github_*"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := mustParse(t, tigerTeam)
			tc.edit(&r)
			err := Validate(r, fakeEnv())
			var p *Problems
			if !errors.As(err, &p) {
				t.Fatalf("want *Problems, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want a problem containing %q, got %v", tc.want, p.List)
			}
			if len(p.List) != 1 {
				t.Errorf("one broken rule should report one problem, got %v", p.List)
			}
		})
	}
}

// A person fixes a roster in one pass only if every problem is reported.
func TestValidateReportsEveryProblem(t *testing.T) {
	r := mustParse(t, tigerTeam)
	r.BudgetUSDPerDay = 0
	r.Members[4].Persona = "nobody"
	r.Members[2].Driver = "gemini"
	err := Validate(r, fakeEnv())
	var p *Problems
	if !errors.As(err, &p) || len(p.List) != 3 {
		t.Fatalf("want three problems, got %v", err)
	}
}

func enable(t *testing.T, home string, on bool) {
	t.Helper()
	body := `{"talkoot_enabled": false}`
	if on {
		body = `{"talkoot_enabled": true}`
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeRoster(t *testing.T, home, id, raw string) {
	t.Helper()
	dir := filepath.Join(home, "talkoot", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The feature ships off. With the flag unset or false, nothing reads a roster,
// even one that is on disk and valid.
func TestLoadersStayOffUntilTheUserLayerEnablesThem(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	writeRoster(t, home, "tiger", tigerTeam)

	if _, err := Load("tiger", fakeEnv()); !errors.Is(err, ErrDisabled) {
		t.Errorf("unset flag: want ErrDisabled, got %v", err)
	}
	enable(t, home, false)
	if _, err := List(); !errors.Is(err, ErrDisabled) {
		t.Errorf("false flag: want ErrDisabled, got %v", err)
	}

	enable(t, home, true)
	ids, err := List()
	if err != nil || !reflect.DeepEqual(ids, []string{"tiger"}) {
		t.Fatalf("List: got %v, %v", ids, err)
	}
	r, err := Load("tiger", fakeEnv())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r.ID != "tiger" || len(r.Members) != 5 {
		t.Errorf("loaded %+v", r)
	}
}

// 🔑 The user layer is the only layer. ProjectConfig is what a cloned
// repository controls, so a talkoot_enabled key there would let a repository
// turn on a team with postures and spend.
func TestProjectConfigCannotEnableTalkoot(t *testing.T) {
	typ := reflect.TypeOf(config.ProjectConfig{})
	for i := 0; i < typ.NumField(); i++ {
		if tag := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]; tag == "talkoot_enabled" {
			t.Fatal("ProjectConfig has talkoot_enabled; decision 0022 rule 3 keeps it on the user layer")
		}
	}
}

// 🚨 An Env with a missing field would skip the check that field feeds, and
// the roster would pass. Validate refuses instead.
func TestValidateFailsClosedOnAnIncompleteEnv(t *testing.T) {
	r := mustParse(t, tigerTeam)
	for _, strip := range []func(*Env){
		func(e *Env) { e.PersonaExists = nil },
		func(e *Env) { e.Driver = nil },
		func(e *Env) { e.Tiers = nil },
		func(e *Env) { e.DriverTools = nil },
	} {
		env := fakeEnv()
		strip(&env)
		if err := Validate(r, env); err == nil || !strings.Contains(err.Error(), "cannot check the roster") {
			t.Errorf("want a refusal, got %v", err)
		}
	}
}

// The proposal keeps this package clear of the swarm and the workspace, so the
// router and the drivers can depend on it without a cycle.
func TestRosterDependencyBoundary(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "terva.sh/terva/packages/agent/talkoot").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	forbidden := []string{
		"terva.sh/terva/packages/agent/swarm",
		"terva.sh/terva/packages/agent/workspace",
		"terva.sh/terva/packages/agent/worker",
		"terva.sh/terva/packages/agent/tools",
	}
	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if slices.Contains(forbidden, dep) {
			t.Errorf("talkoot must not depend on %s", dep)
		}
	}
}

func TestLoadRefusesAnIDThatCouldEscapeTheDirectory(t *testing.T) {
	for _, id := range []string{"../x", "a/b", "Tiger", ""} {
		if _, err := loadFrom(testsupport.TempDir(t), id, fakeEnv()); err == nil {
			t.Errorf("id %q should be refused", id)
		}
	}
}

// YAML spells NaN and infinity as .nan and .inf, so the refusal has to hold
// for a roster read from disk, not only for one built in a test.
func TestParsedNaNBudgetIsRefused(t *testing.T) {
	raw := strings.Replace(tigerTeam, "budget_usd_per_day: 40", "budget_usd_per_day: .nan", 1)
	r := mustParse(t, raw)
	if !math.IsNaN(r.BudgetUSDPerDay) {
		t.Fatalf("the probe needs YAML to read .nan as NaN, got %v", r.BudgetUSDPerDay)
	}
	if err := Validate(r, fakeEnv()); err == nil || !strings.Contains(err.Error(), "budget_usd_per_day") {
		t.Errorf("want the NaN budget refused, got %v", err)
	}
}

func TestListSkipsDirectoriesWithoutARoster(t *testing.T) {
	dir := testsupport.TempDir(t)
	// gamma holds a directory named talkoot.md, which is not a roster.
	if err := os.MkdirAll(filepath.Join(dir, "gamma", FileName), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"alpha", "beta", "Not-an-id"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{"beta", "Not-an-id"} {
		if err := os.WriteFile(filepath.Join(dir, d, FileName), []byte("---\n---\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := listIn(dir)
	if err != nil || !reflect.DeepEqual(ids, []string{"beta"}) {
		t.Errorf("got %v, %v", ids, err)
	}
	if ids, err := listIn(filepath.Join(dir, "missing")); err != nil || ids != nil {
		t.Errorf("a missing directory is no talkoots, got %v, %v", ids, err)
	}
}

// A worker member's idle stop reads its roster value, and the default without
// one. idle_stop off keeps the process up, and each accepted value passes
// Validate.
func TestIdleStopAfter(t *testing.T) {
	for v, want := range map[string]time.Duration{"": DefaultIdleStop, "2h": 2 * time.Hour, "1m": time.Minute} {
		if d, on := (Member{IdleStop: v}).IdleStopAfter(); !on || d != want {
			t.Errorf("IdleStopAfter(%q) = %v, %v; want %v", v, d, on, want)
		}
	}
	if _, on := (Member{IdleStop: IdleStopOff}).IdleStopAfter(); on {
		t.Error("idle_stop off still stops")
	}
	for _, v := range []string{"30m", "2h", "1m", "off"} {
		r := mustParse(t, tigerTeam)
		r.Members[2].IdleStop = v
		if err := Validate(r, fakeEnv()); err != nil {
			t.Errorf("idle_stop %q refused: %v", v, err)
		}
	}
}
