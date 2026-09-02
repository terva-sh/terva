package skills

import (
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func ptr(s []string) *[]string { return &s }

// The whole opt-out mechanism rests on nil differing from empty. If these two
// ever agree, an operator who deliberately turned the default off gets it back
// with no error and no failing test anywhere else.
func TestAlwaysOnNamesUnsetDiffersFromEmpty(t *testing.T) {
	unset := AlwaysOnNames(nil, nil, false)
	if len(unset) == 0 {
		t.Fatalf("an unset config should fall back to DefaultAlwaysOn %v, got %v", DefaultAlwaysOn, unset)
	}
	if unset[0] != DefaultAlwaysOn[0] {
		t.Errorf("unset gave %v, want DefaultAlwaysOn %v", unset, DefaultAlwaysOn)
	}

	empty := AlwaysOnNames(ptr([]string{}), nil, false)
	if len(empty) != 0 {
		t.Errorf("an explicit empty list must pin nothing, got %v", empty)
	}
}

// The operator's list REPLACES the default rather than adding to it, so
// somebody who names their own standard does not also pay for the shipped one.
func TestAlwaysOnNamesConfiguredReplacesTheDefault(t *testing.T) {
	got := AlwaysOnNames(ptr([]string{"mine"}), nil, false)
	if len(got) != 1 || got[0] != "mine" {
		t.Fatalf("got %v, want [mine]", got)
	}
}

// --pin-skill adds for one run, and a name already in the list does not pin
// twice.
func TestAlwaysOnNamesExtraAddsAndDeduplicates(t *testing.T) {
	got := AlwaysOnNames(ptr([]string{"a"}), []string{"b", "a", ""}, false)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("got %v, want [a b]", got)
	}
}

// --no-always-on-skills beats both the config and --pin-skill.
func TestAlwaysOnNamesOffWinsOverEverything(t *testing.T) {
	if got := AlwaysOnNames(ptr([]string{"a"}), []string{"b"}, true); len(got) != 0 {
		t.Fatalf("off should pin nothing, got %v", got)
	}
}

// discoverIn runs the real ladder over a throwaway home and workspace.
func discoverIn(t *testing.T, tervaHome, cwd string) []*Skill {
	t.Helper()
	got, errs := Discover(tervaHome, cwd, "", true, true, Gate{TrustProject: true})
	for _, e := range errs {
		t.Fatalf("discovery error: %v", e)
	}
	return got
}

// A user skill is pinnable. That is the tier the operator controls.
func TestResolveAlwaysOnPinsAUserSkill(t *testing.T) {
	home := testsupport.TempDir(t)
	mkSkill(t, filepath.Join(home, "skills"), "mine", "my standard")

	got := ResolveAlwaysOn(discoverIn(t, home, ""), []string{"mine"})
	if len(got.Skills) != 1 || got.Skills[0].Name != "mine" {
		t.Fatalf("got %v, refused %v, missing %v", got.Names(), got.Refused, got.Missing)
	}
}

// A project skill enters the prompt with no model decision in front of it, and
// a cloned repository writes the project tier. So a name that exists ONLY there
// is refused rather than pinned.
func TestResolveAlwaysOnRefusesAProjectOnlyName(t *testing.T) {
	home, cwd := testsupport.TempDir(t), testsupport.TempDir(t)
	mkSkill(t, filepath.Join(cwd, ".terva", "skills"), "repo-only", "from the repo")

	active := discoverIn(t, home, cwd)
	if Resolve(active, "repo-only") == nil {
		t.Fatal("fixture never discovered the project skill, so the refusal proves nothing")
	}

	got := ResolveAlwaysOn(active, []string{"repo-only"})
	if len(got.Skills) != 0 {
		t.Errorf("pinned a project-only skill: %v", got.Names())
	}
	if len(got.Refused) != 1 || got.Refused[0] != "repo-only" {
		t.Errorf("refused = %v, want [repo-only]", got.Refused)
	}
}

// The one case a project body may pin: it shadows a name that exists at an
// allowed tier, which the operator listed. The shadowing ladder is then doing
// exactly what it is for, and refusing would break per-project override.
func TestResolveAlwaysOnAllowsAProjectSkillThatShadows(t *testing.T) {
	home, cwd := testsupport.TempDir(t), testsupport.TempDir(t)
	name := anyBuiltinName(t)
	mkSkill(t, filepath.Join(cwd, ".terva", "skills"), name, "the repo's own take")

	active := discoverIn(t, home, cwd)
	winner := Resolve(active, name)
	if winner == nil || !winner.Project {
		t.Fatalf("fixture did not put the project skill on top: %+v", winner)
	}

	got := ResolveAlwaysOn(active, []string{name})
	if len(got.Skills) != 1 || !got.Skills[0].Project {
		t.Fatalf("the shadowing project skill should pin: pinned %v, refused %v", got.Names(), got.Refused)
	}
}

// A name nobody ships is usually a typo, and it reads differently to the
// operator than a refusal, so the two are reported apart.
func TestResolveAlwaysOnReportsAMissingName(t *testing.T) {
	got := ResolveAlwaysOn(discoverIn(t, testsupport.TempDir(t), ""), []string{"no-such-skill"})
	if len(got.Missing) != 1 || got.Missing[0] != "no-such-skill" {
		t.Fatalf("missing = %v, want [no-such-skill]", got.Missing)
	}
	if len(got.Refused) != 0 {
		t.Errorf("a name nobody ships is missing, not refused: %v", got.Refused)
	}
}

// The shipped default has to actually resolve against the built-in set.
// Otherwise every session pins nothing and the feature is silently dead.
func TestDefaultAlwaysOnResolvesAgainstTheBuiltins(t *testing.T) {
	got := ResolveAlwaysOn(discoverIn(t, testsupport.TempDir(t), ""), DefaultAlwaysOn)
	if len(got.Skills) != len(DefaultAlwaysOn) {
		t.Fatalf("DefaultAlwaysOn %v resolved to %v (refused %v, missing %v)",
			DefaultAlwaysOn, got.Names(), got.Refused, got.Missing)
	}
	for _, s := range got.Skills {
		if s.Body == "" {
			t.Errorf("%s pins an empty body", s.Name)
		}
	}
}
