package modes

import (
	"strings"
	"testing"
)

// skillDirective is what /skill writes into the editor; the model reads it to
// load the named skill via the `skill` tool, so the wording is a contract.
func TestSkillDirective(t *testing.T) {
	if got, want := skillDirective("release", ""), `Use the "release" skill for: `; got != want {
		t.Errorf("no-task directive = %q, want %q", got, want)
	}
	if got, want := skillDirective("release", "ship v1.2"), `Use the "release" skill for: ship v1.2`; got != want {
		t.Errorf("task directive = %q, want %q", got, want)
	}
}

// /skill must be registered as its own command, distinct from /skills, so
// exact-name dispatch routes them to different handlers.
func TestSkillCommandRegistered(t *testing.T) {
	var hasSkill, hasSkills bool
	for _, c := range BuiltinSlashCommands() {
		switch c.Name {
		case "/skill":
			hasSkill = true
		case "/skills":
			hasSkills = true
		}
	}
	if !hasSkill {
		t.Error("/skill not in the slash catalog")
	}
	if !hasSkills {
		t.Error("/skills missing — regression")
	}
}

// /skill <partial> completes skill NAMES (not command names), with the bare
// name shown but the full "/skill <name>" used as the Tab/Enter replacement.
func TestSkillArgCompletion(t *testing.T) {
	s := newSlashSuggester()
	s.SetSkills([]SkillCompletion{
		{Name: "release", Desc: "cut a release"},
		{Name: "review-rules", Desc: "house review"},
		{Name: "deploy", Desc: "ship it"},
	})

	// "/skill " (trailing space) → every skill.
	if all := s.matches("/skill "); len(all) != 3 {
		t.Fatalf("`/skill ` matches = %d, want 3", len(all))
	}

	// "/skill re" → release + review-rules (prefix), not deploy. Display is the
	// bare name; Name is the full replacement.
	got := map[string]string{}
	for _, c := range s.matches("/skill re") {
		got[c.Name] = c.Display
	}
	if got["/skill release"] != "release" || got["/skill review-rules"] != "review-rules" {
		t.Errorf("prefix match/display wrong: %v", got)
	}
	if _, ok := got["/skill deploy"]; ok {
		t.Errorf("deploy should not match prefix 're': %v", got)
	}

	// Selection (what Tab inserts / Enter runs) is the full "/skill <name>".
	s.Reset()
	if sel := s.Selection("/skill re"); !strings.HasPrefix(sel, "/skill ") || sel == "/skill " {
		t.Errorf("selection = %q, want a /skill <name> replacement", sel)
	}

	// Once a space follows the name, completion stops (typing the request).
	if m := s.matches("/skill release "); len(m) != 0 {
		t.Errorf("completion should stop after the name; got %d entries", len(m))
	}

	// "/skill" with no space still completes COMMAND names (/skill, /skills),
	// never argument entries.
	var sawSkillsCmd bool
	for _, c := range s.matches("/skill") {
		if c.Name == "/skills" {
			sawSkillsCmd = true
		}
		if strings.HasPrefix(c.Name, "/skill ") {
			t.Errorf("command-name mode leaked an arg entry: %q", c.Name)
		}
	}
	if !sawSkillsCmd {
		t.Error("/skills should appear while completing the command name")
	}
}
