package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// "skills" used to fall through the subcommand router to the bare-prompt path,
// which launches an interactive TUI with "skills" pre-filled. In a script that
// is a hang rather than an error. The router must claim the word.
func TestSkillsIsARealSubcommand(t *testing.T) {
	isolateSkillHome(t)
	handled, _ := runSkillsCommand([]string{"skills"})
	if !handled {
		t.Error("`terva skills` is not routed, so it falls through to the interactive " +
			"prompt path and hangs a non-TTY caller")
	}
	handled, _ = runSkillsCommand([]string{"skills", "list"})
	if !handled {
		t.Error("`terva skills list` is not routed")
	}
}

// Every other word must still fall through, or this router entry would swallow
// prompts and other subcommands.
func TestSkillsRouterLeavesEverythingElseAlone(t *testing.T) {
	for _, argv := range [][]string{nil, {"doctor"}, {"trust"}, {"skillsets"}, {"tell me about skills"}} {
		if handled, _ := runSkillsCommand(argv); handled {
			t.Errorf("runSkillsCommand claimed %v; it must only claim the exact word \"skills\"", argv)
		}
	}
}

func TestSkillsRejectsAnUnknownArgument(t *testing.T) {
	handled, err := runSkillsCommand([]string{"skills", "wat"})
	if !handled {
		t.Fatal("an unknown argument should still be handled, not fall through to a prompt")
	}
	if err == nil {
		t.Error("`terva skills wat` silently succeeded; a typo should be reported")
	}
}

// isolateSkillHome points both $TERVA_HOME and the OS home at throwaway dirs,
// so a listing test measures its own fixtures rather than whatever skills the
// developer running it happens to have installed.
func isolateSkillHome(t *testing.T) string {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	userHome := testsupport.TempDir(t)
	t.Setenv("HOME", userHome)
	t.Setenv("USERPROFILE", userHome) // os.UserHomeDir on Windows
	return home
}

func writeSkillFixture(t *testing.T, dir, name, description string) {
	t.Helper()
	sd := filepath.Join(dir, name)
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", sd, err)
	}
	doc := "---\nname: " + name + "\ndescription: " + description + "\n---\n\nBody.\n"
	if err := os.WriteFile(filepath.Join(sd, "SKILL.md"), []byte(doc), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

// The listing has to answer the question a person actually has: what is
// available, where did it come from, and which file do I edit.
func TestSkillsListNamesTierAndPath(t *testing.T) {
	home := isolateSkillHome(t)
	writeSkillFixture(t, filepath.Join(home, "skills"), "sys-health", "audit disk, memory and load")

	var buf bytes.Buffer
	if err := listSkills(&buf, testsupport.TempDir(t)); err != nil {
		t.Fatalf("listSkills: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "sys-health") {
		t.Errorf("listing does not name the skill:\n%s", out)
	}
	if !strings.Contains(out, "global") {
		t.Errorf("listing does not report the tier the skill came from:\n%s", out)
	}
	if !strings.Contains(out, filepath.Join(home, "skills", "sys-health")) {
		t.Errorf("listing does not report the file to edit:\n%s", out)
	}
	// A built-in has no file to edit, but it must still say where it came from
	// rather than leaving the column blank, which would read as a bug.
	if !strings.Contains(out, "builtin:") {
		t.Errorf("built-in skills do not report a legible source:\n%s", out)
	}
}

// An untrusted workspace loads no project skills. Printing the shorter list
// without saying why would send the reader hunting for a skill that is sitting
// right there and simply gated.
func TestSkillsListDisclosesAnUntrustedWorkspace(t *testing.T) {
	isolateSkillHome(t)
	cwd := testsupport.TempDir(t)
	writeSkillFixture(t, filepath.Join(cwd, ".terva", "skills"), "project-only", "never loads untrusted")

	var buf bytes.Buffer
	if err := listSkills(&buf, cwd); err != nil {
		t.Fatalf("listSkills: %v", err)
	}
	out := buf.String()

	if strings.Contains(out, "project-only") {
		t.Errorf("an untrusted workspace's project skill was listed as available:\n%s", out)
	}
	if !strings.Contains(out, "untrusted") {
		t.Errorf("the listing omitted project skills without saying why:\n%s", out)
	}
}
