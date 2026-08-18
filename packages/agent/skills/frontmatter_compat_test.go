package skills

import (
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// disable-model-invocation scopes model DISCOVERY, not the load path: /skill
// primes the editor and the model then loads the skill through the same tool,
// so refusing the tool call would break the human-invoked flow entirely.
func TestDisableModelInvocationHidesFromManifestOnly(t *testing.T) {
	tmp := testsupport.TempDir(t)
	tervaHome := filepath.Join(tmp, "home")
	dir := filepath.Join(tervaHome, "skills")
	mkSkill(t, dir, "human-only", "Only by hand.", "disable-model-invocation: true")
	mkSkill(t, dir, "ordinary", "Anyone may pick this.")

	got, _ := Discover(tervaHome, "", "", true, false, Gate{TrustProject: true})
	addendum := SystemPromptAddendum(got)
	if strings.Contains(addendum, "human-only") {
		t.Errorf("a disable-model-invocation skill must not be advertised to the model:\n%s", addendum)
	}
	if !strings.Contains(addendum, "ordinary") {
		t.Errorf("the ordinary skill should still be advertised:\n%s", addendum)
	}
	if s := Resolve(got, "human-only"); s == nil {
		t.Error("the skill must still resolve — /skill is how a human invokes it")
	}
	// It also stays in the picker: the user opted the MODEL out, not themselves.
	var seen bool
	for _, s := range VisibleSkills(got) {
		if s.Name == "human-only" {
			seen = true
		}
	}
	if !seen {
		t.Error("a human-only skill must still appear in /skills")
	}
}

// The underscore spelling reaches the same field. A SKILL.md travels between
// ecosystems that disagree about which is canonical, and a key terva silently
// ignores is a behaviour its author thought they had configured.
func TestDisableModelInvocationUnderscoreSpelling(t *testing.T) {
	tmp := testsupport.TempDir(t)
	tervaHome := filepath.Join(tmp, "home")
	mkSkill(t, filepath.Join(tervaHome, "skills"), "human-only-alt", "Only by hand.", "disable_model_invocation: true")

	got, _ := Discover(tervaHome, "", "", true, false, Gate{TrustProject: true})
	if strings.Contains(SystemPromptAddendum(got), "human-only-alt") {
		t.Error("the underscore spelling of disable-model-invocation was ignored")
	}
}

// An all-hidden catalog must produce no manifest at all, rather than a header
// advertising an empty list.
func TestSystemPromptAddendumEmptyWhenAllHidden(t *testing.T) {
	tmp := testsupport.TempDir(t)
	tervaHome := filepath.Join(tmp, "home")
	mkSkill(t, filepath.Join(tervaHome, "skills"), "hidden", "Nope.", "disable-model-invocation: true")

	got, _ := Discover(tervaHome, "", "", true, false, Gate{TrustProject: true})
	if len(got) != 1 {
		t.Fatalf("expected the skill to load, got %d", len(got))
	}
	if a := SystemPromptAddendum(got); a != "" {
		t.Errorf("addendum should be empty when nothing is model-visible, got:\n%s", a)
	}
}

func TestArgumentHintBothSpellings(t *testing.T) {
	dir := filepath.Join(testsupport.TempDir(t), "skills")
	mkSkill(t, dir, "hyphen", "d", `argument-hint: "what to review"`)
	mkSkill(t, dir, "underscore", "d", `argument_hint: "what to ship"`)
	mkSkill(t, dir, "neither", "d")

	got, _ := scanTier(tier{dir: dir, label: "global", namespace: NamespaceTerva})
	by := map[string]*Skill{}
	for _, s := range got {
		by[s.Name] = s
	}
	if by["hyphen"].ArgumentHint != "what to review" {
		t.Errorf("argument-hint = %q", by["hyphen"].ArgumentHint)
	}
	if by["underscore"].ArgumentHint != "what to ship" {
		t.Errorf("argument_hint = %q", by["underscore"].ArgumentHint)
	}
	if by["neither"].ArgumentHint != "" {
		t.Errorf("a skill with no hint should have none, got %q", by["neither"].ArgumentHint)
	}
}

// Neither field may leak into the OTHER: a skill that only sets an argument
// hint must stay model-invocable, or the compat read would silently mute it.
func TestArgumentHintDoesNotDisableModelInvocation(t *testing.T) {
	tmp := testsupport.TempDir(t)
	tervaHome := filepath.Join(tmp, "home")
	mkSkill(t, filepath.Join(tervaHome, "skills"), "hinted", "Still pickable.", `argument-hint: "a target"`)

	got, _ := Discover(tervaHome, "", "", true, false, Gate{TrustProject: true})
	if !strings.Contains(SystemPromptAddendum(got), "hinted") {
		t.Error("an argument-hint must not remove a skill from the model's manifest")
	}
}
