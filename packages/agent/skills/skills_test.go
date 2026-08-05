package skills

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
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
	tmp := testsupport.TempDir(t)
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

	skills, errs := Discover(tervaHome, cwd, "", true /* includeUser */, true /* includeBuiltin */, true /* trustProject */)
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

// --no-builtin-skills: includeBuiltin=false drops every compiled-in skill
// while user skills — including one squatting on a built-in's name — load
// exactly as before.
func TestDiscoverExcludesBuiltinsOnRequest(t *testing.T) {
	tmp := testsupport.TempDir(t)
	tervaHome := filepath.Join(tmp, "home")
	cwd := filepath.Join(tmp, "proj")

	mk := func(dir, name, desc string) {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + name + "\ndescription: " + desc + "\n---\n# " + name + "\n"
		if err := os.WriteFile(filepath.Join(full, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk(filepath.Join(tervaHome, "skills"), "mine", "a user skill")
	// Shadow a real built-in by name: with built-ins excluded the user's
	// version must still load (it is a user skill, not a built-in).
	shadowed := loadBuiltins()[0].Name
	mk(filepath.Join(tervaHome, "skills"), shadowed, "user shadow")

	got, errs := Discover(tervaHome, cwd, "", true, false /* includeBuiltin */, true)
	if len(errs) > 0 {
		t.Fatalf("errs: %v", errs)
	}
	if len(got) != 2 {
		t.Fatalf("expected exactly the 2 user skills, got %d (%v)", len(got), got)
	}
	for _, s := range got {
		if s.Builtin {
			t.Errorf("built-in %q leaked past includeBuiltin=false", s.Name)
		}
	}
	if s := FindByName(got, shadowed); s == nil || s.Description != "user shadow" {
		t.Errorf("user skill shadowing a built-in name should survive, got %v", s)
	}
}

// Workspace Trust: an UNTRUSTED project's SKILL.md files must NOT be
// discovered (no instruction injection from a cloned repo), while
// user/global skills still load. Trusting the same dir loads them.
func TestDiscoverUntrustedDropsProjectSkills(t *testing.T) {
	tmp := testsupport.TempDir(t)
	tervaHome := filepath.Join(tmp, "home")
	cwd := filepath.Join(tmp, "proj")

	mk := func(dir, name, desc string) {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + name + "\ndescription: " + desc + "\n---\n# " + name + "\n"
		if err := os.WriteFile(filepath.Join(full, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A project-local skill (every project-dir spelling) and the
	// claude/agents compat spellings, plus a user/global skill.
	mk(filepath.Join(cwd, ".terva", "skills"), "evil-project", "from the repo")
	mk(filepath.Join(cwd, ".claude", "skills"), "evil-claude", "from the repo (claude)")
	mk(filepath.Join(cwd, ".agents", "skills"), "evil-agents", "from the repo (agents)")
	mk(filepath.Join(tervaHome, "skills"), "user-global", "from the user home")

	// Untrusted: none of the project skills, but the global one is present.
	restricted, errs := Discover(tervaHome, cwd, "", true, true, false /* trustProject */)
	if len(errs) > 0 {
		t.Fatalf("errs: %v", errs)
	}
	for _, name := range []string{"evil-project", "evil-claude", "evil-agents"} {
		if FindByName(restricted, name) != nil {
			t.Errorf("untrusted workspace leaked project skill %q into discovery", name)
		}
	}
	if FindByName(restricted, "user-global") == nil {
		t.Error("user/global skill must load even when the workspace is untrusted")
	}

	// Trusted: the same project skills now load.
	trusted, errs := Discover(tervaHome, cwd, "", true, true, true /* trustProject */)
	if len(errs) > 0 {
		t.Fatalf("errs: %v", errs)
	}
	for _, name := range []string{"evil-project", "evil-claude", "evil-agents", "user-global"} {
		if FindByName(trusted, name) == nil {
			t.Errorf("trusted workspace should load skill %q", name)
		}
	}
}

// Built-ins are listed, and they sort AFTER the user's own skills so the first
// screen of the picker stays the set the user installed. The ordering is the
// whole reason listing eleven more entries is tolerable, so it is asserted
// rather than left to sort's incidental behaviour.
func TestVisibleSkillsListsBuiltinsAfterUserSkills(t *testing.T) {
	in := []*Skill{
		{Name: "built-one", Builtin: true},
		{Name: "user-two"},
		{Name: "built-two", Builtin: true},
		{Name: "user-one"},
	}
	out := VisibleSkills(in)
	if len(out) != 4 {
		t.Fatalf("expected all 4 skills visible, got %d (%v)", len(out), out)
	}
	var got []string
	for _, s := range out {
		got = append(got, s.Name)
	}
	want := []string{"user-one", "user-two", "built-one", "built-two"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("visible order = %v, want %v", got, want)
		}
	}
	// A built-in reaching the picker is the point; assert one is actually
	// there so a regression to filtering cannot pass on ordering alone.
	var builtins int
	for _, s := range out {
		if s.Builtin {
			builtins++
		}
	}
	if builtins != 2 {
		t.Errorf("expected both built-ins in the visible set, got %d", builtins)
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
