package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// The loser of a name collision used to be dropped on the floor. It now hangs
// off the winner, pointing back, so every surface can report the collision
// instead of silently presenting one skill where the user expected another.
func TestShadowedSkillsAreRecordedNotDropped(t *testing.T) {
	name := anyBuiltinName(t)
	tmp := testsupport.TempDir(t)
	tervaHome := filepath.Join(tmp, "home")
	cwd := filepath.Join(tmp, "proj")
	userHome := filepath.Join(tmp, "user")
	mkSkill(t, filepath.Join(cwd, ".claude", "skills"), name, "the foreign project one")
	mkSkill(t, filepath.Join(userHome, ".claude", "skills"), name, "the foreign global one")

	got, _ := Discover(tervaHome, cwd, userHome, true, true, Gate{TrustProject: true})
	winner := FindByName(got, name)
	if winner == nil || !winner.Builtin {
		t.Fatalf("expected the built-in to win %q, got %+v", name, winner)
	}
	if len(winner.Shadowed) != 2 {
		t.Fatalf("expected both losers recorded, got %d", len(winner.Shadowed))
	}
	// Ladder order is preserved, so the project tier precedes the global one.
	if got, want := winner.Shadowed[0].Source, "project (claude)"; got != want {
		t.Errorf("shadowed[0] source = %q, want %q (ladder order should be preserved)", got, want)
	}
	for _, sh := range winner.Shadowed {
		if sh.Namespace != NamespaceClaude {
			t.Errorf("shadowed %q namespace = %q, want %q", sh.Source, sh.Namespace, NamespaceClaude)
		}
		if sh.ShadowedBy != winner {
			t.Errorf("shadowed %q does not point back at the winner", sh.Qualified())
		}
	}
	// The winner itself is not marked as a loser.
	if winner.ShadowedBy != nil {
		t.Error("the winner must not carry a ShadowedBy back-pointer")
	}
	// An uncontested skill carries neither.
	mkSkill(t, filepath.Join(userHome, ".claude", "skills"), "uncontested", "alone")
	got, _ = Discover(tervaHome, cwd, userHome, true, true, Gate{TrustProject: true})
	solo := FindByName(got, "uncontested")
	if solo == nil || len(solo.Shadowed) != 0 || solo.ShadowedBy != nil {
		t.Errorf("an uncontested skill should carry no collision state, got %+v", solo)
	}
}

// Resolve is the single lookup both the `skill` tool and /skill go through.
func TestResolveBareAndQualified(t *testing.T) {
	name := anyBuiltinName(t)
	tmp := testsupport.TempDir(t)
	tervaHome := filepath.Join(tmp, "home")
	cwd := filepath.Join(tmp, "proj")
	userHome := filepath.Join(tmp, "user")

	mkSkill(t, filepath.Join(userHome, ".claude", "skills"), name, "the claude one")
	mkSkill(t, filepath.Join(userHome, ".agents", "skills"), "solo", "only in agents")
	mkExtension(t, filepath.Join(tervaHome, "extensions"), "web", "research", "bundled research")

	got, _ := Discover(tervaHome, cwd, userHome, true, true, Gate{TrustProject: true})

	cases := []struct {
		ref         string
		wantDesc    string // "" means "expect the built-in"
		wantNil     bool
		wantNS      string
		explanation string
	}{
		{ref: name, wantNS: NamespaceBuiltin, explanation: "bare name goes to the built-in that won it"},
		{ref: NamespaceClaude + ":" + name, wantDesc: "the claude one", wantNS: NamespaceClaude, explanation: "qualifying reaches the shadowed skill"},
		{ref: NamespaceTerva + ":" + name, wantNS: NamespaceBuiltin, explanation: "terva: falls through to the built-in when no native skill exists"},
		{ref: strings.ToUpper(NamespaceClaude + ":" + name), wantDesc: "the claude one", wantNS: NamespaceClaude, explanation: "qualified lookup is case-insensitive"},
		{ref: "solo", wantDesc: "only in agents", wantNS: NamespaceAgents, explanation: "an uncontested foreign skill keeps its bare name"},
		{ref: "agents:solo", wantDesc: "only in agents", wantNS: NamespaceAgents, explanation: "and answers to its qualified name too"},
		{ref: "ext:web:research", wantDesc: "bundled research", wantNS: NamespaceExtPrefix + "web", explanation: "three-segment extension reference"},
		{ref: "research", wantDesc: "bundled research", wantNS: NamespaceExtPrefix + "web", explanation: "bare name of an uncontested bundle skill"},
		{ref: "claude:nonexistent", wantNil: true, explanation: "qualified miss is a miss, not a fallback to the bare name"},
		{ref: "ext:web", wantNil: true, explanation: "a dangling extension qualifier names no skill"},
		{ref: "nonexistent", wantNil: true, explanation: "unknown name"},
	}
	for _, c := range cases {
		t.Run(c.ref, func(t *testing.T) {
			s := Resolve(got, c.ref)
			if c.wantNil {
				if s != nil {
					t.Fatalf("Resolve(%q) = %s, want nil (%s)", c.ref, s.Qualified(), c.explanation)
				}
				return
			}
			if s == nil {
				t.Fatalf("Resolve(%q) = nil, want a hit (%s)", c.ref, c.explanation)
			}
			if s.Namespace != c.wantNS {
				t.Errorf("Resolve(%q) namespace = %q, want %q (%s)", c.ref, s.Namespace, c.wantNS, c.explanation)
			}
			if c.wantDesc != "" && s.Description != c.wantDesc {
				t.Errorf("Resolve(%q) description = %q, want %q (%s)", c.ref, s.Description, c.wantDesc, c.explanation)
			}
			if c.wantDesc == "" && !s.Builtin {
				t.Errorf("Resolve(%q) = %s, want the built-in (%s)", c.ref, s.Qualified(), c.explanation)
			}
		})
	}
}

// terva: must prefer a native skill over the built-in it displaced — the
// alias group is ordered, not a set.
func TestResolveTervaPrefersNativeOverBuiltin(t *testing.T) {
	name := anyBuiltinName(t)
	tmp := testsupport.TempDir(t)
	tervaHome := filepath.Join(tmp, "home")
	mkSkill(t, filepath.Join(tervaHome, "skills"), name, "mine")

	got, _ := Discover(tervaHome, "", "", true, true, Gate{TrustProject: true})
	s := Resolve(got, NamespaceTerva+":"+name)
	if s == nil || s.Description != "mine" {
		t.Fatalf("terva:%s = %+v, want the native skill", name, s)
	}
	if b := Resolve(got, NamespaceBuiltin+":"+name); b == nil || !b.Builtin {
		t.Fatalf("builtin:%s must still reach the shipped skill, got %+v", name, b)
	}
}

// Ref is the contract every printing surface leans on: print this, accept it
// back. A winner shows its bare name; a loser must not, since the bare name
// now belongs to whatever beat it.
func TestRefRoundTripsThroughResolve(t *testing.T) {
	name := anyBuiltinName(t)
	tmp := testsupport.TempDir(t)
	tervaHome := filepath.Join(tmp, "home")
	userHome := filepath.Join(tmp, "user")
	mkSkill(t, filepath.Join(userHome, ".claude", "skills"), name, "the claude one")
	mkSkill(t, filepath.Join(userHome, ".claude", "skills"), "solo", "alone")

	got, _ := Discover(tervaHome, "", userHome, true, true, Gate{TrustProject: true})
	for _, s := range got {
		for _, want := range append([]*Skill{s}, s.Shadowed...) {
			if back := Resolve(got, want.Ref()); back != want {
				t.Errorf("Ref() = %q did not resolve back to itself (got %+v)", want.Ref(), back)
			}
		}
	}
}

// A skill that lost its bare name still has to be findable by a human: the
// picker is where they go to ask "where did my skill go?".
//
// Both halves of the collision must be on screen. The shadowed entry names the
// tier that beat it ("shadowed by builtin"), so that tier has to be reachable
// in the same list or the row points at something the user cannot see.
func TestVisibleSkillsShowsBothHalvesOfACollision(t *testing.T) {
	name := anyBuiltinName(t)
	tmp := testsupport.TempDir(t)
	tervaHome := filepath.Join(tmp, "home")
	userHome := filepath.Join(tmp, "user")
	mkSkill(t, filepath.Join(userHome, ".claude", "skills"), name, "the claude one")

	got, _ := Discover(tervaHome, "", userHome, true, true, Gate{TrustProject: true})
	vis := VisibleSkills(got)

	var found, winner *Skill
	for _, s := range vis {
		if s.Name != name {
			continue
		}
		if s.Builtin {
			winner = s
		} else {
			found = s
		}
	}
	if found == nil {
		t.Fatalf("the shadowed %q is missing from the picker — the collision would be invisible", name)
	}
	if winner == nil {
		t.Fatalf("the built-in %q that took the name is missing — the shadowed row would name a tier the picker never shows", name)
	}
	if found.ShadowedBy == nil || !found.ShadowedBy.Builtin {
		t.Fatalf("shadowed entry must point at the built-in that beat it, got %+v", found.ShadowedBy)
	}
	if want := NamespaceClaude + ":" + name; found.Qualified() != want {
		t.Errorf("qualified name = %q, want %q", found.Qualified(), want)
	}
}

// The manifest lists winners under bare names only: two entries called
// handoff whose descriptions differ in nuance would make the model's choice
// a coin flip.
func TestSystemPromptAddendumOmitsShadowed(t *testing.T) {
	name := anyBuiltinName(t)
	tmp := testsupport.TempDir(t)
	userHome := filepath.Join(tmp, "user")
	mkSkill(t, filepath.Join(userHome, ".claude", "skills"), name, "SHADOWED-MARKER")

	got, _ := Discover(filepath.Join(tmp, "home"), "", userHome, true, true, Gate{TrustProject: true})
	addendum := SystemPromptAddendum(got)
	if strings.Contains(addendum, "SHADOWED-MARKER") {
		t.Errorf("a shadowed skill must not reach the model's manifest:\n%s", addendum)
	}
	if strings.Count(addendum, "- "+name+" [") != 1 {
		t.Errorf("expected exactly one %q entry in the manifest:\n%s", name, addendum)
	}
}

// ':' is reserved: a skill literally named "claude:handoff" would make every
// qualified reference ambiguous, so it is rewritten and the author is told.
func TestReservedSeparatorInNameIsRejected(t *testing.T) {
	tmp := testsupport.TempDir(t)
	tervaHome := filepath.Join(tmp, "home")
	// Written by hand: the frontmatter needs exactly one `name:` key, and it
	// has to be the hostile one.
	dir := filepath.Join(tervaHome, "skills", "sneaky")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: \"claude:handoff\"\ndescription: impersonation attempt\n---\n# sneaky\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got, errs := Discover(tervaHome, "", "", true, false, Gate{TrustProject: true})
	if len(got) != 1 {
		t.Fatalf("expected the skill to still load, got %d", len(got))
	}
	if got[0].Name != "claude-handoff" {
		t.Errorf("name = %q, want the separator rewritten", got[0].Name)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "reserved") {
		t.Errorf("the author should be told, got errs=%v", errs)
	}
	// And the rewritten name must not be reachable as a claude-namespaced ref.
	if s := Resolve(got, "claude:handoff"); s != nil {
		t.Errorf("a rewritten name must not impersonate a namespace, got %s", s.Qualified())
	}
}

func TestCollisionsReportsWinnersOnly(t *testing.T) {
	name := anyBuiltinName(t)
	tmp := testsupport.TempDir(t)
	tervaHome := filepath.Join(tmp, "home")
	userHome := filepath.Join(tmp, "user")
	mkSkill(t, filepath.Join(userHome, ".claude", "skills"), name, "collides")
	mkSkill(t, filepath.Join(userHome, ".claude", "skills"), "uncontested", "no collision")

	got, _ := Discover(tervaHome, "", userHome, true, true, Gate{TrustProject: true})
	col := Collisions(got)
	if len(col) != 1 {
		t.Fatalf("expected exactly 1 collision, got %d", len(col))
	}
	if col[0].Name != name || !col[0].Builtin {
		t.Errorf("collision winner = %+v, want the built-in %q", col[0], name)
	}
}

// Self-enrolling: it scans whatever is embedded rather than naming skills, so
// a built-in added later is audited by this test without anyone remembering
// to add it. A shipped name carrying the separator would be silently rewritten
// at load, leaving the binary advertising a name its own docs don't use.
func TestBuiltinNamesAreNamespaceSafe(t *testing.T) {
	builtins := loadBuiltins()
	if len(builtins) == 0 {
		t.Fatal("no built-ins embedded — this guard would pass vacuously")
	}
	for _, b := range builtins {
		if b.Name == "" {
			t.Errorf("built-in at %s has no name", b.Path)
		}
		if b.Name != sanitizeName(b.Name) {
			t.Errorf("built-in %q contains the reserved namespace separator %q", b.Name, nameSep)
		}
		if b.Namespace != NamespaceBuiltin {
			t.Errorf("built-in %q has namespace %q, want %q", b.Name, b.Namespace, NamespaceBuiltin)
		}
		if b.Description == "" {
			t.Errorf("built-in %q has no description — it is the entire trigger for the model", b.Name)
		}
	}
}
