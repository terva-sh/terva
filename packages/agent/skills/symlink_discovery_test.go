package skills

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func makeLinkedSkill(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("---\nname: linked\ndescription: example\n---\nRead resource.txt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "resource.txt"), []byte("resource"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
}

func TestLinkedSkillDiscoveryResolvesResourcesAndBrokenLinks(t *testing.T) {
	root := testsupport.TempDir(t)
	dir := filepath.Join(root, "skills")
	target := filepath.Join(root, "source")
	makeLinkedSkill(t, target, filepath.Join(dir, "alias"))
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "cycle-b"), filepath.Join(dir, "cycle-a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "cycle-a"), filepath.Join(dir, "cycle-b")); err != nil {
		t.Fatal(err)
	}
	got, errs := scanTier(tier{dir: dir, label: "project", namespace: NamespaceClaude, project: true})
	if len(got) != 1 {
		t.Fatalf("got %d skills, errors: %v", len(got), errs)
	}
	s := got[0]
	wantPath, err := filepath.EvalSymlinks(filepath.Join(target, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Path != wantPath || s.Namespace != NamespaceClaude || !s.Project {
		t.Fatalf("unexpected skill: %+v", s)
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(s.Path), "resource.txt"))
	if err != nil || string(b) != "resource" {
		t.Fatalf("relative resource: %q, %v", b, err)
	}
	if len(errs) != 2 {
		t.Fatalf("expected cycle diagnostics, got %v", errs)
	}

	linkedRoot := filepath.Join(root, "linked-root")
	if err := os.Symlink(dir, linkedRoot); err != nil {
		t.Fatal(err)
	}
	got, _ = scanTier(tier{dir: linkedRoot, label: "global", namespace: NamespaceTerva})
	if len(got) != 1 || got[0].Path != wantPath {
		t.Fatalf("linked root discovery: %+v", got)
	}
}

func TestLinkedSkillsRetainTrustAndTierPrecedence(t *testing.T) {
	root := testsupport.TempDir(t)
	cwd, home := filepath.Join(root, "workspace"), filepath.Join(root, "terva-home")
	makeLinkedSkill(t, filepath.Join(root, "project-source"), filepath.Join(cwd, ".terva", "skills", "alias"))
	makeLinkedSkill(t, filepath.Join(root, "global-source"), filepath.Join(home, "skills", "alias"))
	for _, trusted := range []bool{false, true} {
		got, errs := Discover(home, cwd, "", true, false, Gate{TrustProject: trusted})
		if len(errs) != 0 || len(got) != 1 {
			t.Fatalf("trusted=%v: skills=%v errors=%v", trusted, got, errs)
		}
		if got[0].Project != trusted || got[0].Namespace != NamespaceTerva {
			t.Fatalf("trusted=%v: %+v", trusted, got[0])
		}
		if trusted && (len(got[0].Shadowed) != 1 || got[0].Shadowed[0].Project) {
			t.Fatal("global skill did not retain its shadowed identity")
		}
	}
}

func TestLinkedSkillFileKeepsDirectoryResources(t *testing.T) {
	root := testsupport.TempDir(t)
	dir := filepath.Join(root, "skills")
	target := filepath.Join(root, "source")
	makeLinkedSkill(t, target, filepath.Join(dir, "alias"))
	shared := filepath.Join(root, "shared.md")
	path := filepath.Join(target, "SKILL.md")
	if err := os.Rename(path, shared); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, path); err != nil {
		t.Skipf("file symlinks unavailable: %v", err)
	}
	got, errs := scanTier(tier{dir: dir, label: "project", namespace: NamespaceTerva, project: true})
	if len(errs) != 0 || len(got) != 1 {
		t.Fatalf("skills=%v errors=%v", got, errs)
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(got[0].Path), "resource.txt"))
	if err != nil || string(b) != "resource" {
		t.Fatalf("relative resource: %q, %v", b, err)
	}
}

func TestLinkedSkillsPreserveOrdinaryPathsAndDeduplicateAliases(t *testing.T) {
	root := testsupport.TempDir(t)
	dir := filepath.Join(root, "skills")
	target := filepath.Join(dir, "original")
	makeLinkedSkill(t, target, filepath.Join(dir, "a-alias"))
	if err := os.Symlink(target, filepath.Join(dir, "zz-alias")); err != nil {
		t.Fatal(err)
	}
	linkedRoot := filepath.Join(root, "linked-root")
	if err := os.Symlink(dir, linkedRoot); err != nil {
		t.Fatal(err)
	}
	got, errs := scanTier(tier{dir: linkedRoot, label: "global", namespace: NamespaceTerva})
	if len(errs) != 0 || len(got) != 1 {
		t.Fatalf("skills=%v errors=%v", got, errs)
	}
	wantPath := filepath.Join(linkedRoot, "original", "SKILL.md")
	if got[0].Path != wantPath {
		t.Fatalf("ordinary path=%q, want %q", got[0].Path, wantPath)
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(got[0].Path), "resource.txt"))
	if err != nil || string(b) != "resource" {
		t.Fatalf("relative resource: %q, %v", b, err)
	}
}
