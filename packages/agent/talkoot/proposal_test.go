package talkoot

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/look"
)

// Every member field has exactly one class, and every class names a field. A
// new field without a class fails here (decision 0025, "Costs we accepted").
func TestEveryMemberFieldHasAClass(t *testing.T) {
	var tags []string
	rt := reflect.TypeFor[Member]()
	for i := range rt.NumField() {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("yaml"), ",")
		tags = append(tags, name)
		if _, ok := ClassOf(name); !ok {
			t.Errorf("member field %s has no class in MemberFields", name)
		}
		if json, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ","); json != name {
			t.Errorf("member field %s has JSON name %q, want the YAML name", name, json)
		}
	}
	seen := map[string]bool{}
	for _, f := range MemberFields {
		if !slices.Contains(tags, f.Name) {
			t.Errorf("MemberFields names %s, which is not a member field", f.Name)
		}
		if seen[f.Name] {
			t.Errorf("MemberFields names %s twice", f.Name)
		}
		seen[f.Name] = true
	}
	for field, want := range map[string]FieldClass{"title": ClassLook, "persona": ClassVoice, "posture": ClassAuthority, "id": ClassAuthority, "tools": ClassAuthority} {
		if got, _ := ClassOf(field); got != want {
			t.Errorf("%s is %s, want %s", field, got, want)
		}
	}
}

// commented is tigerTeam's shape with comments a person wrote.
const commented = `---
# The team for the lake schema.
name: tiger
home: ~/src/terva
budget_usd_per_day: 40
members:
  - id: helm # leads
    role: coordinator
    persona: mieli
  - id: atlas
    role: planner
    # Plans only, until the schema settles.
    posture: plan
---

Work from tickets.

Keep this line as it is.
`

func mustApply(t *testing.T, text string, ops ...Op) (string, Roster) {
	t.Helper()
	out, err := ApplyOps([]byte(text), ops)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Parse(out, "out.md")
	if err != nil {
		t.Fatalf("the result does not parse: %v\n%s", err, out)
	}
	return string(out), r
}

func TestApplyOpsKeepsCommentsAndTheCharter(t *testing.T) {
	out, r := mustApply(t, commented, Op{Op: OpEdit, Member: "atlas", Set: map[string]any{"posture": "ask", "tier": "strong"}})
	for _, want := range []string{"# The team for the lake schema.", "# leads", "# Plans only, until the schema settles.", "\nWork from tickets.\n\nKeep this line as it is.\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("the result lost %q:\n%s", want, out)
		}
	}
	atlas, _ := r.member("atlas")
	if atlas.Posture != "ask" || atlas.Tier != "strong" || atlas.Role != RolePlanner {
		t.Errorf("atlas is %+v", atlas)
	}
	if !strings.HasSuffix(out, "---\n\nWork from tickets.\n\nKeep this line as it is.\n") {
		t.Errorf("the charter moved:\n%s", out)
	}
}

func TestEachOperationChangesTheRoster(t *testing.T) {
	_, r := mustApply(t, commented,
		Op{Op: OpAdd, Member: "scout", Set: map[string]any{"role": "specialist", "title": "Scout", "turns_per_day": float64(30)}},
		Op{Op: OpLook, Member: "helm", Set: map[string]any{"title": "Helm", "mark": map[string]any{"shape": "hexagon", "color": "#3B82F6"}}},
		Op{Op: OpEdit, Member: "helm", Set: map[string]any{"persona": ""}},
		Op{Op: OpRemove, Member: "atlas"},
	)
	var ids []string
	for _, m := range r.Members {
		ids = append(ids, m.ID)
	}
	if !slices.Equal(ids, []string{"helm", "scout"}) {
		t.Fatalf("members are %v, want helm then scout", ids)
	}
	helm, _ := r.member("helm")
	if helm.Title != "Helm" || helm.Persona != "" {
		t.Errorf("helm is %+v, want a title and no persona", helm)
	}
	if helm.Mark == nil || *helm.Mark != (look.Mark{Shape: "hexagon", Color: "#3B82F6"}) {
		t.Errorf("helm's mark is %+v, want the look operation's", helm.Mark)
	}
	scout, _ := r.member("scout")
	if scout.Title != "Scout" || scout.TurnsPerDay != 30 || scout.Posture != "plan" || scout.Driver != DriverNative {
		t.Errorf("scout is %+v, want its fields and the defaults", scout)
	}
}

// A null mark returns a member to its default, the way a null resets any
// field, so the card's reset needs no mark of its own.
func TestALookOpClearsAMarkWithNull(t *testing.T) {
	set, _ := mustApply(t, commented,
		Op{Op: OpLook, Member: "helm", Set: map[string]any{"mark": map[string]any{"shape": "tab", "color": "#46A758"}}},
	)
	var ops []Op
	if err := json.Unmarshal([]byte(`[{"op": "look", "member": "helm", "set": {"mark": null}}]`), &ops); err != nil {
		t.Fatal(err)
	}
	out, r := mustApply(t, set, ops...)
	if helm, _ := r.member("helm"); helm.Mark != nil {
		t.Errorf("helm's mark is %+v, want none", helm.Mark)
	}
	if strings.Contains(out, "mark:") {
		t.Errorf("the mark stayed in the text:\n%s", out)
	}
}

// A batch is refused whole, before any text changes, when one operation is
// wrong.
func TestCheckOpsRefusesABadBatch(t *testing.T) {
	r := mustParse(t, commented)
	for _, tc := range []struct {
		name string
		ops  []Op
		want string
	}{
		{"no operation", nil, "at least one"},
		{"an unknown field", []Op{{Op: OpEdit, Member: "helm", Set: map[string]any{"colour": "red"}}}, "not a member field"},
		{"an id change", []Op{{Op: OpEdit, Member: "helm", Set: map[string]any{"id": "pilot"}}}, "id cannot change"},
		{"authority in a look", []Op{{Op: OpLook, Member: "helm", Set: map[string]any{"posture": "yolo"}}}, "look fields only"},
		{"voice in a look", []Op{{Op: OpLook, Member: "helm", Set: map[string]any{"persona": "koestaja"}}}, "look fields only"},
		{"a mark shape outside the set", []Op{{Op: OpLook, Member: "helm", Set: map[string]any{"mark": map[string]any{"shape": "star"}}}}, `mark shape "star"`},
		{"a mark that is not a mark", []Op{{Op: OpLook, Member: "helm", Set: map[string]any{"mark": "hexagon"}}}, "a mark with a shape and a color"},
		{"a mark with another field", []Op{{Op: OpLook, Member: "helm", Set: map[string]any{"mark": map[string]any{"shape": "tab", "eyes": "big"}}}}, `no field "eyes"`},
		{"a missing member", []Op{{Op: OpEdit, Member: "ghost", Set: map[string]any{"title": "x"}}}, "not a member"},
		{"an add of a member", []Op{{Op: OpAdd, Member: "helm", Set: map[string]any{"role": "specialist"}}}, "already a member"},
		{"an edit after its remove", []Op{{Op: OpRemove, Member: "atlas"}, {Op: OpEdit, Member: "atlas", Set: map[string]any{"title": "x"}}}, "not a member"},
		{"a bad id", []Op{{Op: OpAdd, Member: "Scout", Set: map[string]any{"role": "specialist"}}}, "lower case"},
		{"an unknown operation", []Op{{Op: "rename", Member: "helm"}}, "add, edit, remove, or look"},
		{"a nested value", []Op{{Op: OpEdit, Member: "helm", Set: map[string]any{"title": []any{"a"}}}}, "title must be a string"},
		{"a fractional count", []Op{{Op: OpEdit, Member: "helm", Set: map[string]any{"turns_per_day": 2.5}}}, "turns_per_day must be a whole number"},
		{"a string flag", []Op{{Op: OpEdit, Member: "helm", Set: map[string]any{"reviewer": "yes"}}}, "reviewer must be true or false"},
		{"a string budget", []Op{{Op: OpEdit, Member: "helm", Set: map[string]any{"budget_usd_per_day": "5"}}}, "budget_usd_per_day must be a number"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckOps(r, tc.ops)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("CheckOps = %v, want an error naming %q", err, tc.want)
			}
			if _, err := ApplyOps([]byte(commented), tc.ops); err == nil {
				t.Error("ApplyOps took the batch that CheckOps refused")
			}
		})
	}
	if err := CheckOps(r, make([]Op, MaxOps+1)); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("an oversized batch: %v", err)
	}
}

func TestInverseUndoesEachKindOfChange(t *testing.T) {
	before := mustParse(t, commented)
	ops := []Op{
		{Op: OpAdd, Member: "scout", Set: map[string]any{"role": "specialist"}},
		{Op: OpEdit, Member: "helm", Set: map[string]any{"tier": "strong", "persona": ""}},
		{Op: OpRemove, Member: "atlas"},
	}
	text, after := mustApply(t, commented, ops...)
	changes := Diff(before, after)
	if len(changes) != 3 {
		t.Fatalf("Diff = %+v, want three changes", changes)
	}
	undo, err := Inverse(changes)
	if err != nil {
		t.Fatal(err)
	}
	_, back := mustApply(t, text, undo...)
	if d := Diff(before, back); len(d) != 0 {
		t.Fatalf("after the undo these members still differ: %+v", d)
	}
	if _, err := Inverse(nil); err == nil {
		t.Error("an empty change has something to undo")
	}
}

// An undo writes back what the member had, and not the defaults that filled
// in the rest. A field left to its default keeps following it.
func TestAnUndoLeavesDefaultsUnwritten(t *testing.T) {
	before := mustParse(t, commented)
	text, after := mustApply(t, commented,
		Op{Op: OpRemove, Member: "atlas"},
		Op{Op: OpEdit, Member: "helm", Set: map[string]any{"posture": "ask", "driver": "claude"}},
	)
	undo, err := Inverse(Diff(before, after))
	if err != nil {
		t.Fatal(err)
	}
	back, r := mustApply(t, text, undo...)
	if d := Diff(before, r); len(d) != 0 {
		t.Fatalf("after the undo these members still differ: %+v", d)
	}
	for _, unwritten := range []string{"driver:", "workspace:"} {
		if strings.Contains(back, unwritten) {
			t.Errorf("the undo wrote a default, %s:\n%s", unwritten, back)
		}
	}
	// A posture is written back, so the member added back stays pinned to
	// it. A later move to a worktree does not raise atlas to auto-edit.
	_, moved := mustApply(t, back, Op{Op: OpEdit, Member: "atlas", Set: map[string]any{"workspace": "worktree"}})
	if atlas, _ := moved.member("atlas"); atlas.Posture != "plan" {
		t.Errorf("after the undo, a worktree gives atlas posture %s, want its written plan", atlas.Posture)
	}
}

// A default can follow another field: a member's posture follows its
// workspace. A change to the workspace of a member with no posture written
// applies, and the posture follows it.
func TestAChangeKeepsADefaultThatFollowsAnotherField(t *testing.T) {
	_, r := mustApply(t, commented, Op{Op: OpEdit, Member: "helm", Set: map[string]any{"workspace": "worktree"}})
	helm, _ := r.member("helm")
	if helm.Workspace != WorkspaceWorktree || helm.Posture != "auto-edit" {
		t.Fatalf("helm is %+v, want a worktree and the posture that follows it", helm)
	}
	// A posture the person wrote stays, whatever the workspace.
	_, r = mustApply(t, commented, Op{Op: OpEdit, Member: "atlas", Set: map[string]any{"workspace": "worktree"}})
	if atlas, _ := r.member("atlas"); atlas.Posture != "plan" {
		t.Fatalf("atlas is %+v, want its written posture kept", atlas)
	}
}

func TestAValueHasASizeLimit(t *testing.T) {
	r := mustParse(t, commented)
	long := strings.Repeat("x", MaxValueBytes+1)
	if err := CheckOps(r, []Op{{Op: OpLook, Member: "helm", Set: map[string]any{"title": long}}}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("a long title: %v", err)
	}
	if err := CheckOps(r, []Op{{Op: OpLook, Member: "helm", Set: map[string]any{"title": long[:MaxValueBytes]}}}); err != nil {
		t.Fatalf("a title at the limit: %v", err)
	}
}

func TestWidensNamesEachGrant(t *testing.T) {
	base := Member{ID: "jev", Role: RoleSpecialist, Driver: DriverNative, Posture: "plan", Workspace: WorkspaceShared, BudgetUSDPerDay: 5, TurnsPerDay: 20}
	for _, tc := range []struct {
		name string
		edit func(*Member)
		want []string
	}{
		{"a wider posture", func(m *Member) { m.Posture = "auto-edit" }, []string{"posture"}},
		{"a narrower posture", func(m *Member) { m.Posture = "plan" }, nil},
		{"a raised budget", func(m *Member) { m.BudgetUSDPerDay = 10 }, []string{"budget_usd_per_day"}},
		{"a lifted budget", func(m *Member) { m.BudgetUSDPerDay = 0 }, []string{"budget_usd_per_day"}},
		{"a lowered budget", func(m *Member) { m.BudgetUSDPerDay = 2 }, nil},
		{"lifted turns", func(m *Member) { m.TurnsPerDay = 0 }, []string{"turns_per_day"}},
		{"the coordinator role", func(m *Member) { m.Role = RoleCoordinator }, []string{"role"}},
		{"the reviewer flag", func(m *Member) { m.Reviewer = true }, []string{"reviewer"}},
		{"a worker driver", func(m *Member) { m.Driver = "claude" }, []string{"driver"}},
		{"a title", func(m *Member) { m.Title = "Dev" }, nil},
		{"a posture the ranking does not know", func(m *Member) { m.Posture = "sudo" }, []string{"posture"}},
		{"another worker driver", func(m *Member) { m.Driver = "acp:codex" }, []string{"driver"}},
		{"a model", func(m *Member) { m.Model = "opus" }, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			after := base
			tc.edit(&after)
			if got := Widens(MemberChange{Member: "jev", Before: &base, After: &after}); !slices.Equal(got, tc.want) {
				t.Errorf("Widens = %v, want %v", got, tc.want)
			}
		})
	}
	worker := base
	worker.Driver, worker.Workspace, worker.Posture = "claude", WorkspaceWorktree, "auto-edit"
	moved := worker
	moved.Driver, moved.Workspace = "acp:codex", WorkspaceShared
	if got := Widens(MemberChange{Member: "jev", Before: &worker, After: &moved}); !slices.Equal(got, []string{"driver", "workspace"}) {
		t.Errorf("a writer moved into the shared checkout on another driver widens %v", got)
	}
	added := Member{ID: "new", Role: RoleSpecialist, Driver: DriverNative, Posture: "workspace", Workspace: WorkspaceShared}
	if got := Widens(MemberChange{Member: "new", After: &added}); !slices.Equal(got, []string{"posture"}) {
		t.Errorf("an add in workspace posture widens %v, want posture against a default member", got)
	}
}

// A card marks every authority field a change sets, including the ones
// Widens does not rank.
func TestAuthorityChangesNamesEveryAuthorityField(t *testing.T) {
	was := Member{ID: "jev", Role: RoleSpecialist, Driver: DriverNative, Posture: "plan", Workspace: WorkspaceShared, Tier: "weak"}
	is := was
	is.Tier, is.Model, is.Title, is.Persona = "", "opus", "Dev", "mieli"
	if got := AuthorityChanges(MemberChange{Member: "jev", Before: &was, After: &is}); !slices.Equal(got, []string{"model", "tier"}) {
		t.Errorf("AuthorityChanges = %v, want model and tier and no look or voice field", got)
	}
	added := Member{ID: "new", Role: RoleSpecialist, Driver: DriverNative, Posture: "plan", Workspace: WorkspaceShared, BudgetUSDPerDay: 3}
	if got := AuthorityChanges(MemberChange{Member: "new", After: &added}); !slices.Equal(got, []string{"budget_usd_per_day"}) {
		t.Errorf("an add names %v, want its fields that differ from a default member", got)
	}
	if got := AuthorityChanges(MemberChange{Member: "jev", Before: &was}); got != nil {
		t.Errorf("a remove names %v", got)
	}
}

func TestSelfAuthorityAndTheClassOfABatch(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ops   []Op
		self  bool
		class FieldClass
	}{
		{"its own posture", []Op{{Op: OpEdit, Member: "jev", Set: map[string]any{"posture": "workspace"}}}, true, ClassAuthority},
		{"its own title", []Op{{Op: OpLook, Member: "jev", Set: map[string]any{"title": "Dev"}}}, false, ClassLook},
		{"its own persona", []Op{{Op: OpEdit, Member: "jev", Set: map[string]any{"persona": "mieli"}}}, false, ClassVoice},
		{"another's posture", []Op{{Op: OpEdit, Member: "helm", Set: map[string]any{"posture": "ask"}}}, false, ClassAuthority},
		{"its own removal", []Op{{Op: OpRemove, Member: "jev"}}, true, ClassAuthority},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SelfAuthority("jev", tc.ops); got != tc.self {
				t.Errorf("SelfAuthority = %v, want %v", got, tc.self)
			}
			if got := ClassOfOps(tc.ops); got != tc.class {
				t.Errorf("ClassOfOps = %s, want %s", got, tc.class)
			}
		})
	}
}

// A member's proposal is an envelope to no one. It passes the guards a send
// passes, and it delivers nothing.
func TestAProposalIsAGuardedEnvelopeToNoOne(t *testing.T) {
	f := newFixture(t, func(_ *Roster, l *Limits) { l.SendsPerWindow = 2 })
	if _, err := f.router.Propose("helm", "add scout"); !errors.Is(err, ErrNoHumanRoot) {
		t.Fatalf("a proposal with no person at the root of its chain: %v", err)
	}
	post := f.post()
	delivered := len(f.native.got)
	e, err := f.router.Propose("helm", "add scout")
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != KindProposal || e.To == nil || len(e.To) != 0 || e.From != "helm" || e.Chain.Root != post.ID || e.Chain.Hops != 1 {
		t.Fatalf("the proposal envelope is %+v", e)
	}
	if len(f.native.got) != delivered {
		t.Error("a proposal delivered something")
	}
	// The same summary again is not a duplicate: it has no recipient.
	if _, err := f.router.Propose("helm", "add scout"); err != nil {
		t.Fatalf("a second proposal: %v", err)
	}
	if _, err := f.router.Propose("helm", "add scout"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("the third proposal in the window: %v, want the rate limit", err)
	}
	// A restart counts the proposals against the window, as it counts sends.
	f.reopen()
	if _, err := f.send("helm", Outgoing{To: []string{"atlas"}, Body: "hello"}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("after a restart the proposals still count: %v", err)
	}
	if _, err := f.router.Propose("ghost", "x"); err == nil {
		t.Error("a proposal from a non-member was taken")
	}
	if _, err := f.router.Propose("helm", " "); err == nil {
		t.Error("an empty summary was taken")
	}
}

// A proposal reaches no member. A send refuses the kind, and so does the
// router itself, so a later change to the send's checks cannot let one land
// as a note.
func TestAProposalCannotBeSentToAMember(t *testing.T) {
	f := newFixture(t, nil)
	f.post()
	_, err := f.send("helm", Outgoing{To: []string{"atlas"}, Kind: KindProposal, Body: "x"})
	if err == nil || !strings.Contains(err.Error(), "is not message, handoff, note, or answer") {
		t.Fatalf("a send of the proposal kind: %v, want the kind refused", err)
	}
	f.router.mu.Lock()
	_, _, err = f.router.sendLocked("helm", Outgoing{To: []string{"atlas"}, Kind: KindProposal, Body: "x"}, f.clock.now())
	f.router.mu.Unlock()
	if err == nil || !strings.Contains(err.Error(), "addressed to no member") {
		t.Fatalf("the router took a proposal with a recipient: %v", err)
	}
	if got := f.native.to("atlas"); len(got) != 0 {
		t.Errorf("atlas received %q", got)
	}
}

func TestTheHopLimitCountsAProposal(t *testing.T) {
	f := newFixture(t, func(_ *Roster, l *Limits) { l.HopLimit = 1 })
	f.post()
	if _, err := f.router.Propose("helm", "add scout"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.router.Propose("helm", "add scout again"); !errors.Is(err, ErrPaused) {
		t.Fatalf("the second hop: %v, want the chain paused", err)
	}
}

func TestAPersonsProposalStartsItsOwnChain(t *testing.T) {
	f := newFixture(t, nil)
	e, err := f.router.ProposeAs("sothr", "undo the last change")
	if err != nil {
		t.Fatal(err)
	}
	if e.From != "human:sothr" || e.Kind != KindProposal || e.Chain.Root != e.ID || len(e.To) != 0 {
		t.Fatalf("the person's proposal is %+v", e)
	}
	if _, err := f.router.ProposeAs("so thr", "x"); err == nil {
		t.Error("a bad person's name was taken")
	}
}

// A roster line from before the changes field replays, and a new one round-
// trips its changes through the room.
func TestARosterLineCarriesItsChanges(t *testing.T) {
	f := newFixture(t, nil)
	room := OpenRoom(f.dir)
	if err := room.Append(Line{Type: LineRoster, By: "human:sothr", Ref: "abc"}); err != nil {
		t.Fatal(err)
	}
	before := Member{ID: "atlas", Role: RolePlanner, Posture: "plan"}
	after := before
	after.Posture = "ask"
	if err := room.Append(Line{Type: LineRoster, By: "human:sothr", Ref: "def", Proposal: "p1", Proposer: "helm",
		Changes: []MemberChange{{Member: "atlas", Before: &before, After: &after}}}); err != nil {
		t.Fatal(err)
	}
	f.reopen()
	var got []Line
	for _, l := range f.lines() {
		if l.Type == LineRoster {
			got = append(got, l)
		}
	}
	if len(got) != 2 || len(got[0].Changes) != 0 || got[1].Proposer != "helm" || got[1].Changes[0].After.Posture != "ask" {
		t.Fatalf("roster lines read back as %+v", got)
	}
	raw, _ := json.Marshal(got[1].Changes[0])
	if !strings.Contains(string(raw), `"posture":"ask"`) {
		t.Errorf("a change encodes as %s, want the roster's field names", raw)
	}
}
