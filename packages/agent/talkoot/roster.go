// Package talkoot reads and validates Talkoot rosters: the persistent teams of
// docs/proposals/talkoot.md. A roster names each member's role, driver, model,
// posture, workspace, and budget, so it is an authority document. Decision 0022
// keeps it in the user layer, and this package reads it from $TERVA_HOME only.
package talkoot

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/core/permission"
)

// FileName is the roster file inside a talkoot's directory.
const FileName = "talkoot.md"

// Roles a member may hold. A talkoot has exactly one coordinator.
const (
	RoleCoordinator = "coordinator"
	RolePlanner     = "planner"
	RoleSpecialist  = "specialist"
)

// Workspaces a member may run in.
const (
	WorkspaceShared   = "shared"   // the talkoot's home checkout
	WorkspaceWorktree = "worktree" // a worktree leased for this member
)

// DriverNative runs a member as a daemon session. Any other driver names a
// registered worker backend.
const DriverNative = "native"

// ErrDisabled is returned by every loader while talkoot_enabled is off.
var ErrDisabled = errors.New("talkoot: disabled; set talkoot_enabled in $TERVA_HOME/config.json")

// Roster is one talkoot: the frontmatter of talkoot.md, plus its body as the
// charter every member receives.
type Roster struct {
	// ID is the directory name under Dir. It is the talkoot's address in
	// #talkoot:<id>, so it never changes.
	ID              string   `yaml:"-"`
	Name            string   `yaml:"name"`
	Title           string   `yaml:"title,omitempty"`
	Home            string   `yaml:"home"`
	BudgetUSDPerDay float64  `yaml:"budget_usd_per_day"`
	Members         []Member `yaml:"members"`
	Charter         string   `yaml:"-"`
	// Source is the path the roster was read from, for error messages.
	Source string `yaml:"-"`
}

// Member is one agent in a talkoot. Identity is ID and Persona. Authority is
// everything from Driver down, and it sits here rather than on the persona
// because a persona grants nothing (decision 0022 rule 2).
//
// A new field needs a class in MemberFields. The JSON names match the YAML
// names, because a roster line in the room carries members.
type Member struct {
	ID              string  `yaml:"id" json:"id"`
	Role            string  `yaml:"role" json:"role"`
	Title           string  `yaml:"title,omitempty" json:"title,omitempty"`
	Persona         string  `yaml:"persona,omitempty" json:"persona,omitempty"`
	Driver          string  `yaml:"driver,omitempty" json:"driver,omitempty"`
	Model           string  `yaml:"model,omitempty" json:"model,omitempty"`
	Tier            string  `yaml:"tier,omitempty" json:"tier,omitempty"`
	Posture         string  `yaml:"posture,omitempty" json:"posture,omitempty"`
	Workspace       string  `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Reviewer        bool    `yaml:"reviewer,omitempty" json:"reviewer,omitempty"`
	BudgetUSDPerDay float64 `yaml:"budget_usd_per_day,omitempty" json:"budget_usd_per_day,omitempty"`
	TurnsPerDay     int     `yaml:"turns_per_day,omitempty" json:"turns_per_day,omitempty"`
	// Tools narrows the member to the named tools, and absent means the
	// posture's full set. It never widens: a tool the posture refuses stays
	// refused. The seat tools stay without a listing (SeatTools).
	Tools []string `yaml:"tools,omitempty" json:"tools,omitempty"`
}

// FieldClass says who may change a member field, and how (decision 0025
// rule 2). Look changes what a person sees, voice changes what the member
// says and does, and authority changes what the member may do.
type FieldClass string

const (
	ClassLook      FieldClass = "look"
	ClassVoice     FieldClass = "voice"
	ClassAuthority FieldClass = "authority"
)

// MemberFields gives every member field its class, by its YAML name, in the
// order a roster lists them.
//
// 🔑 An id is authority, not identity to rename. It is the member's seat and
// its address in the room, so a new id is a remove and an add.
var MemberFields = []struct {
	Name  string
	Class FieldClass
}{
	{"id", ClassAuthority},
	{"role", ClassAuthority},
	{"title", ClassLook},
	{"persona", ClassVoice},
	{"driver", ClassAuthority},
	{"model", ClassAuthority},
	{"tier", ClassAuthority},
	{"posture", ClassAuthority},
	{"workspace", ClassAuthority},
	{"reviewer", ClassAuthority},
	{"budget_usd_per_day", ClassAuthority},
	{"turns_per_day", ClassAuthority},
	{"tools", ClassAuthority},
}

// ClassOf returns the class of a member field, and false for a name that is
// not a member field.
func ClassOf(field string) (FieldClass, bool) {
	for _, f := range MemberFields {
		if f.Name == field {
			return f.Class, true
		}
	}
	return "", false
}

// Writes reports whether the member's posture lets it change files. Every
// posture except plan can, "ask" included: an approval gates the write, and
// the write still lands in the checkout.
func (m Member) Writes() bool { return m.Posture != string(permission.ApprovalPlan) }

// idPattern bounds talkoot and member ids. No colon or slash, because the
// room address is #talkoot:<id> and a federated id uses "/".
var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// ValidID reports whether id can name a talkoot. A caller that joins an id
// onto Dir checks it first, so the path stays under Dir.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Parse reads a roster from the text of talkoot.md and applies the defaults.
// It checks the shape only. Validate checks the rules.
//
// 🔑 An unknown key is an error. A typo such as budget_usd_per_dy would
// otherwise parse as no cap at all, and a roster is the one file where a
// silently dropped field widens authority.
func Parse(raw []byte, source string) (Roster, error) {
	front, body, ok := splitFrontmatter(raw)
	if !ok {
		return Roster{}, fmt.Errorf("talkoot: %s: no YAML frontmatter between --- lines", source)
	}
	var r Roster
	dec := yaml.NewDecoder(bytes.NewReader(front))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil {
		return Roster{}, fmt.Errorf("talkoot: %s: %w", source, err)
	}
	r.Charter = strings.TrimSpace(string(body))
	r.Source = source
	for i := range r.Members {
		applyDefaults(&r.Members[i])
	}
	return r, nil
}

// applyDefaults fills what the proposal's field table leaves optional. A
// member in its own worktree defaults to auto-edit, and every other member
// defaults to plan.
func applyDefaults(m *Member) {
	if m.Driver == "" {
		m.Driver = DriverNative
	}
	if m.Workspace == "" {
		m.Workspace = WorkspaceShared
	}
	if m.Posture == "" {
		if m.Workspace == WorkspaceWorktree {
			m.Posture = string(permission.ApprovalAutoEdit)
		} else {
			m.Posture = string(permission.ApprovalPlan)
		}
	}
}

func splitFrontmatter(raw []byte) (front, body []byte, ok bool) {
	s := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return nil, nil, false
	}
	rest := s[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, nil, false
	}
	after := rest[end+len("\n---"):]
	if after != "" && after[0] != '\n' {
		return nil, nil, false
	}
	return []byte(rest[:end]), []byte(strings.TrimPrefix(after, "\n")), true
}

// Env is what Validate checks names against: the persona library, the worker
// registry, and the swarm tiers.
//
// 🔑 The caller supplies it. The worker registry and the tier table both
// import the swarm, and this package imports neither the swarm nor the
// workspace (docs/proposals/talkoot.md, "Where the code lives"). The layer
// that wires Talkoot into the daemon builds an Env from persona.Lookup,
// worker.Lookup with Backend.ReportsCost, and tools.SwarmTierNames.
type Env struct {
	// PersonaExists reports whether a persona reference resolves.
	PersonaExists func(ref string) bool
	// Driver reports whether a worker backend is registered and whether it
	// reports cost. Validate never asks it about DriverNative, which always
	// reports cost.
	Driver func(name string) (reportsCost bool, err error)
	// Tiers is every tier name a member may set.
	Tiers []string
	// DriverTools refuses a tools list that a worker backend cannot pass to
	// its harness: a backend with no allowlist, or a name it cannot express.
	// Validate never asks it about DriverNative, which narrows every tool.
	DriverTools func(driver string, tools []string) error
}

// incomplete names the Env fields that are unset. Validate fails closed on
// them: a missing driver registry would otherwise pass every driver, and the
// no-cost rule with it.
func (e Env) incomplete() []string {
	var out []string
	if e.PersonaExists == nil {
		out = append(out, "PersonaExists")
	}
	if e.Driver == nil {
		out = append(out, "Driver")
	}
	if len(e.Tiers) == 0 {
		out = append(out, "Tiers")
	}
	if e.DriverTools == nil {
		out = append(out, "DriverTools")
	}
	return out
}

// Problems is every rule a roster breaks. Validate reports them all at once,
// so a person fixes the file in one pass.
type Problems struct {
	Source string
	List   []string
}

func (p *Problems) Error() string {
	return fmt.Sprintf("talkoot: %s: %s", p.Source, strings.Join(p.List, "; "))
}

// Validate checks a parsed roster against the rules in docs/proposals/talkoot.md
// and returns *Problems, or nil when the roster is sound.
func Validate(r Roster, env Env) error {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }

	if missing := env.incomplete(); len(missing) > 0 {
		add("cannot check the roster: the environment has no %s", strings.Join(missing, ", "))
		return &Problems{Source: r.Source, List: out}
	}

	if r.ID != "" && !idPattern.MatchString(r.ID) {
		add("talkoot id %q must be lower case letters, digits, and dashes, starting with a letter", r.ID)
	}
	if strings.TrimSpace(r.Name) == "" {
		add("name is missing")
	}
	if strings.TrimSpace(r.Home) == "" {
		add("home is missing")
	}
	// 🔑 A cap nobody set is a cap nobody has, so the talkoot budget is
	// required rather than defaulted.
	// YAML reads .nan and .inf as floats, and NaN fails every comparison,
	// so a NaN budget would pass both this rule and the member cap below.
	if !finite(r.BudgetUSDPerDay) || r.BudgetUSDPerDay <= 0 {
		add("budget_usd_per_day is missing or not a positive number")
	}
	if len(r.Members) == 0 {
		add("members is empty")
	}

	seen := map[string]bool{}
	coordinators := 0
	var sharedWriters []string
	for i, m := range r.Members {
		who := m.ID
		if who == "" {
			who = fmt.Sprintf("members[%d]", i)
		}
		switch {
		case m.ID == "":
			add("%s has no id", who)
		case !idPattern.MatchString(m.ID):
			add("member id %q must be lower case letters, digits, and dashes, starting with a letter", m.ID)
		case seen[m.ID]:
			add("member id %q appears twice", m.ID)
		}
		seen[m.ID] = true

		switch m.Role {
		case RoleCoordinator:
			coordinators++
		case RolePlanner, RoleSpecialist:
		default:
			add("%s: role %q is not coordinator, planner, or specialist", who, m.Role)
		}
		if m.Reviewer && m.Role != RoleSpecialist {
			add("%s: reviewer is for a specialist, not a %s", who, m.Role)
		}

		if m.Persona != "" && !env.PersonaExists(m.Persona) {
			add("%s: persona %q not found", who, m.Persona)
		}
		if m.Model != "" && m.Tier != "" {
			add("%s: set model or tier, not both", who)
		}
		if m.Tier != "" && !slices.Contains(env.Tiers, m.Tier) {
			add("%s: tier %q is not one of %s", who, m.Tier, strings.Join(env.Tiers, ", "))
		}

		if _, err := permission.ParseApprovalMode(m.Posture); err != nil {
			add("%s: posture %q is not an approval mode", who, m.Posture)
		}
		switch m.Workspace {
		case WorkspaceShared:
			// 🚨 yolo in the person's own checkout is the failure the
			// revived-leased-worker fix (TKT-01M396QWR) closed for workers.
			// A roster must not reopen it by hand.
			if m.Posture == string(permission.ApprovalYolo) {
				add("%s: posture yolo needs workspace: worktree", who)
			}
			if m.Writes() {
				sharedWriters = append(sharedWriters, who)
			}
		case WorkspaceWorktree:
		default:
			add("%s: workspace %q is not shared or worktree", who, m.Workspace)
		}

		if !finite(m.BudgetUSDPerDay) || m.BudgetUSDPerDay < 0 {
			add("%s: budget_usd_per_day is not a positive number", who)
		}
		if r.BudgetUSDPerDay > 0 && m.BudgetUSDPerDay > r.BudgetUSDPerDay {
			add("%s: budget_usd_per_day %.2f is above the talkoot's %.2f", who, m.BudgetUSDPerDay, r.BudgetUSDPerDay)
		}
		if m.TurnsPerDay < 0 {
			add("%s: turns_per_day is negative", who)
		}
		toolsOK := true
		if m.Tools != nil {
			if err := checkTools(m.Tools); err != nil {
				add("%s: %v", who, err)
				toolsOK = false
			}
		}

		if m.Driver != DriverNative {
			reportsCost, err := env.Driver(m.Driver)
			switch {
			case err != nil:
				add("%s: driver %q is not registered", who, m.Driver)
			case !reportsCost && m.TurnsPerDay <= 0:
				// A spend cap on a driver that reports no spend never trips.
				add("%s: driver %q reports no cost, so the member needs turns_per_day", who, m.Driver)
			}
			// An unregistered driver is one problem, not two.
			if err == nil && m.Tools != nil && toolsOK {
				if err := env.DriverTools(m.Driver, m.Tools); err != nil {
					add("%s: driver %q cannot narrow its tools: %v", who, m.Driver, err)
				}
			}
		}
	}
	if len(r.Members) > 0 && coordinators != 1 {
		add("a talkoot needs exactly one coordinator, and this one has %d", coordinators)
	}
	// Two writers in one checkout overwrite each other's edits. A second
	// writer takes a worktree.
	if len(sharedWriters) > 1 {
		add("members %s can all write to the shared checkout; give all but one workspace: worktree or posture: plan",
			strings.Join(sharedWriters, ", "))
	}

	if len(out) == 0 {
		return nil
	}
	return &Problems{Source: r.Source, List: out}
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// Dir is where rosters live: $TERVA_HOME/talkoot. There is no project-layer
// counterpart, by decision 0022 rule 3.
func Dir() string { return filepath.Join(config.TervaHome(), "talkoot") }

// Load reads and validates the roster for one talkoot id.
func Load(id string, env Env) (Roster, error) {
	if !config.TalkootEnabled() {
		return Roster{}, ErrDisabled
	}
	return loadFrom(Dir(), id, env)
}

func loadFrom(dir, id string, env Env) (Roster, error) {
	if !idPattern.MatchString(id) {
		return Roster{}, fmt.Errorf("talkoot: id %q must be lower case letters, digits, and dashes, starting with a letter", id)
	}
	path := filepath.Join(dir, id, FileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		return Roster{}, fmt.Errorf("talkoot: %w", err)
	}
	r, err := Parse(raw, path)
	if err != nil {
		return Roster{}, err
	}
	r.ID = id
	if err := Validate(r, env); err != nil {
		return Roster{}, err
	}
	return r, nil
}

// List returns the ids of every talkoot directory that holds a roster, sorted.
// It does not validate them. Load does that one at a time, so one broken
// roster does not hide the others.
func List() ([]string, error) {
	if !config.TalkootEnabled() {
		return nil, ErrDisabled
	}
	return listIn(Dir())
}

func listIn(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("talkoot: %w", err)
	}
	var ids []string
	for _, e := range entries {
		if !e.IsDir() || !idPattern.MatchString(e.Name()) {
			continue
		}
		if fi, err := os.Stat(filepath.Join(dir, e.Name(), FileName)); err == nil && fi.Mode().IsRegular() {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}
