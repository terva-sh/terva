package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// writeExtMemory lays down the memory EXTENSION's files the way a user who has
// been running it has them: <home>/ext-data/memory/{user.md,projects/<key>/memory.md},
// 0644, bucketed by the same ProjectKey core computes.
func writeExtMemory(t *testing.T, home, cwd, userBody, projectBody string) {
	t.Helper()
	root := filepath.Join(home, extDataDirName, extName)
	if userBody != "" {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, userFileName), []byte(userBody), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if projectBody != "" {
		dir := filepath.Join(root, projectsDirName, core.ProjectKey(cwd))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, projectFileName), []byte(projectBody), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The point of the whole migration: a user with months of accumulated memories
// keeps them when the extension is retired, in both scopes.
func TestAdoptCopiesExtensionMemory(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := "/Users/someone/Workspace/a-repo"
	writeExtMemory(t, home, cwd,
		"# User memory\n\n- prefers worktrees over branch switching\n",
		"# Project memory\n\n- uses pnpm, not npm\n- tests in crates/*/tests\n")

	if err := Adopt(home, cwd); err != nil {
		t.Fatalf("Adopt: %v", err)
	}

	user, project := NewUserStore(), NewStore()
	if err := user.Rebind(UserDir(home)); err != nil {
		t.Fatal(err)
	}
	if err := project.Rebind(ProjectDir(home, cwd)); err != nil {
		t.Fatal(err)
	}
	if got := user.List(); len(got) != 1 || got[0] != "prefers worktrees over branch switching" {
		t.Errorf("user memory not adopted: %v", got)
	}
	if got := project.List(); len(got) != 2 || got[0] != "uses pnpm, not npm" {
		t.Errorf("project memory not adopted: %v", got)
	}
}

// COPY, never move: an install that rolls back to the extension must find its
// data exactly where it left it. This is the property that makes a bug in Adopt
// survivable.
func TestAdoptLeavesTheOriginals(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := "/Users/someone/Workspace/a-repo"
	writeExtMemory(t, home, cwd, "- a user fact\n", "- a project fact\n")

	if err := Adopt(home, cwd); err != nil {
		t.Fatal(err)
	}

	extRoot := filepath.Join(home, extDataDirName, extName)
	for _, p := range []string{
		filepath.Join(extRoot, userFileName),
		filepath.Join(extRoot, projectsDirName, core.ProjectKey(cwd), projectFileName),
	} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("original destroyed: %s: %v", p, err)
			continue
		}
		if !strings.Contains(string(b), "fact") {
			t.Errorf("original emptied: %s = %q", p, b)
		}
	}
}

// After the first adoption the core copy is authoritative. Re-running must not
// revert what has been written since — which is what a second copy would do, and
// Adopt runs on every session.
func TestAdoptIsIdempotentAndNeverReverts(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := "/Users/someone/Workspace/a-repo"
	writeExtMemory(t, home, cwd, "", "- the old fact\n")

	if err := Adopt(home, cwd); err != nil {
		t.Fatal(err)
	}
	s := NewStore()
	if err := s.Rebind(ProjectDir(home, cwd)); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("the old fact"); err != nil {
		t.Fatal(err)
	}
	if err := s.Add("a fact written since"); err != nil {
		t.Fatal(err)
	}

	// Second session: Adopt runs again, and must do nothing.
	if err := Adopt(home, cwd); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	got := s.List()
	if len(got) != 1 || got[0] != "a fact written since" {
		t.Fatalf("re-adoption reverted core's memory: %v", got)
	}
}

// A home the extension never ran in is the common case for a new install, and
// must be silent rather than an error.
func TestAdoptNoopWithoutExtensionData(t *testing.T) {
	home := testsupport.TempDir(t)
	if err := Adopt(home, "/Users/someone/Workspace/a-repo"); err != nil {
		t.Fatalf("Adopt with no ext data: %v", err)
	}
	if _, err := os.Stat(Root(home)); err == nil {
		t.Error("Adopt created a memory root with nothing to put in it")
	}
}

// Project memory is keyed by the same ProjectKey the sessions dir buckets by,
// which is what makes the adoption a straight copy rather than a re-keying.
func TestProjectDirUsesProjectKey(t *testing.T) {
	home := "/home"
	cwd := "/Users/someone/Workspace/a-repo"
	want := filepath.Join(home, dirName, projectsDirName, core.ProjectKey(cwd))
	if got := ProjectDir(home, cwd); got != want {
		t.Fatalf("ProjectDir = %q, want %q", got, want)
	}
	// No cwd means no project scope — NOT a shared bucket, which would surface
	// one repo's facts in an unrelated one.
	if got := ProjectDir(home, ""); got != "" {
		t.Errorf("ProjectDir with no cwd = %q, want empty", got)
	}
	if got := ProjectDir("", cwd); got != "" {
		t.Errorf("ProjectDir with no home = %q, want empty", got)
	}
}

// Adopting only one scope must not block the other: a user may have user memory
// and no project memory for this repo, or the reverse.
func TestAdoptHandlesOneScopeOnly(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := "/Users/someone/Workspace/a-repo"
	writeExtMemory(t, home, cwd, "- only a user fact\n", "")

	if err := Adopt(home, cwd); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	user := NewUserStore()
	if err := user.Rebind(UserDir(home)); err != nil {
		t.Fatal(err)
	}
	if got := user.List(); len(got) != 1 {
		t.Fatalf("user scope not adopted when project scope was absent: %v", got)
	}
}
