package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSplitFrontmatter(t *testing.T) {
	in := "---\nname: foo\ndescription: bar\n---\nbody text\n"
	front, body := splitFrontmatter(in)
	if front != "name: foo\ndescription: bar" {
		t.Errorf("front = %q", front)
	}
	if body != "body text\n" {
		t.Errorf("body = %q", body)
	}

	front2, body2 := splitFrontmatter("no frontmatter here")
	if front2 != "" || body2 != "no frontmatter here" {
		t.Errorf("expected pass-through, got front=%q body=%q", front2, body2)
	}
}

func TestParseFrontmatter(t *testing.T) {
	front := `name: code-review
description: "Review a recent change."
allowed-tools: [read, bash]
permissions:
  bash: ["git diff*", "git log*"]
`
	s := &Skill{}
	parseFrontmatter(front, s)
	if s.Name != "code-review" {
		t.Errorf("name = %q", s.Name)
	}
	if s.Description != "Review a recent change." {
		t.Errorf("description = %q", s.Description)
	}
	if got := s.AllowedTools; len(got) != 2 || got[0] != "read" || got[1] != "bash" {
		t.Errorf("allowed-tools = %v", got)
	}
	if got := s.Permissions["bash"]; len(got) != 2 || got[0] != "git diff*" || got[1] != "git log*" {
		t.Errorf("permissions[bash] = %v", got)
	}
}

// Real-YAML features the hand-rolled parser couldn't handle: a folded
// multi-line description, a block-style list, the allowed_tools
// underscore spelling, and a multi-key permissions block.
func TestParseFrontmatterRealYAML(t *testing.T) {
	front := `name: deep-skill
description: >
  A longer description that spans
  multiple folded lines.
allowed_tools:
  - read
  - "edit"
  - bash
permissions:
  bash: ["git diff*"]
  read: ["./*.go"]
`
	s := &Skill{}
	parseFrontmatter(front, s)
	if s.Name != "deep-skill" {
		t.Errorf("name = %q", s.Name)
	}
	// Folded scalar joins the lines with spaces and ends with a newline.
	if want := "A longer description that spans multiple folded lines.\n"; s.Description != want {
		t.Errorf("description = %q, want %q", s.Description, want)
	}
	if got := s.AllowedTools; len(got) != 3 || got[0] != "read" || got[1] != "edit" || got[2] != "bash" {
		t.Errorf("allowed_tools (block + underscore spelling) = %v", got)
	}
	if got := s.Permissions["read"]; len(got) != 1 || got[0] != "./*.go" {
		t.Errorf("permissions[read] = %v", got)
	}
}

// Malformed frontmatter must not break discovery: parse leaves fields
// empty (the caller falls back to the directory basename for Name).
func TestParseFrontmatterMalformedDegrades(t *testing.T) {
	s := &Skill{}
	parseFrontmatter("name: [unterminated\n  : : :", s)
	if s.Name != "" || s.Description != "" {
		t.Errorf("malformed frontmatter should yield empty fields, got name=%q desc=%q", s.Name, s.Description)
	}
}

func TestDiscoverProjectAndGlobalPriorityAndDedup(t *testing.T) {
	tmp := t.TempDir()
	tervaHome := filepath.Join(tmp, "home")
	cwd := filepath.Join(tmp, "proj")

	mk := func(dir, name, desc string) {
		full := filepath.Join(dir, name)
		os.MkdirAll(full, 0o755)
		body := "---\nname: " + name + "\ndescription: " + desc + "\n---\n# " + name + "\n"
		os.WriteFile(filepath.Join(full, "SKILL.md"), []byte(body), 0o644)
	}

	// Same skill name in BOTH project and global; project should win.
	mk(filepath.Join(cwd, ".terva", "skills"), "shared", "project version")
	mk(filepath.Join(tervaHome, "skills"), "shared", "global version")
	// Unique skill in global only.
	mk(filepath.Join(tervaHome, "skills"), "global-only", "from global")

	skills, errs := Discover(tervaHome, cwd, "", true /* includeUser */)
	if len(errs) > 0 {
		t.Fatalf("errs: %v", errs)
	}
	// Expect the two user skills + every built-in shipped with the
	// binary (currently the write-terva-extension authoring guide).
	builtins := loadBuiltins()
	want := 2 + len(builtins)
	if len(skills) != want {
		t.Fatalf("expected %d skills (2 user + %d built-in), got %d (%v)", want, len(builtins), len(skills), skills)
	}
	shared := FindByName(skills, "shared")
	if shared == nil || shared.Description != "project version" {
		t.Errorf("expected project to win for 'shared', got %v", shared)
	}
	if FindByName(skills, "global-only") == nil {
		t.Errorf("global-only skill missing")
	}
	// At least one built-in should have made it through.
	for _, b := range builtins {
		if FindByName(skills, b.Name) == nil {
			t.Errorf("built-in skill %q missing from Discover output", b.Name)
		}
	}
}

func TestVisibleSkillsHidesBuiltins(t *testing.T) {
	in := []*Skill{
		{Name: "user-one"},
		{Name: "built-one", Builtin: true},
		{Name: "user-two"},
	}
	out := VisibleSkills(in)
	if len(out) != 2 {
		t.Fatalf("expected 2 visible skills, got %d (%v)", len(out), out)
	}
	for _, s := range out {
		if s.Builtin {
			t.Errorf("built-in %q leaked into visible set", s.Name)
		}
	}
}

func TestSystemPromptAddendum(t *testing.T) {
	skills := []*Skill{
		{Name: "built-a", Description: "Do A.", Builtin: true},
		{Name: "user-b", Description: "Do B.", Path: "/tmp/skills/user-b/SKILL.md"},
	}
	out := SystemPromptAddendum(skills)
	if !contains(out, "- built-a [builtin]: Do A.\n") {
		t.Errorf("builtin entry missing or wrong:\n%s", out)
	}
	if !contains(out, "- user-b [/tmp/skills/user-b/SKILL.md]: Do B.\n") {
		t.Errorf("user entry missing path pointer:\n%s", out)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(sub) > 0 && stringIndex(s, sub) >= 0))
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
