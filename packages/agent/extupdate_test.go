package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// gitAvailable lets the whole suite no-op on machines without git
// (the goreleaser cross-build is one). All these tests genuinely need
// the git binary; the agent runtime itself never requires it.
func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping extension-update tests")
	}
}

// initBareRepo creates a bare repo at path. Used as the "remote" for
// the clone-and-pull tests.
func initBareRepo(t *testing.T, path string) {
	t.Helper()
	mustRun(t, "", "git", "init", "--bare", "--initial-branch=main", path)
}

// initWorkRepo creates a working repo at path with one commit so it
// has a real default branch. Returns the path. We commit a single
// README so the worktree isn't empty.
func initWorkRepo(t *testing.T, path string) {
	t.Helper()
	mustRun(t, "", "git", "init", "--initial-branch=main", path)
	configRepo(t, path)
	writeFile(t, filepath.Join(path, "README.md"), "v1\n")
	mustRun(t, path, "git", "add", ".")
	mustRun(t, path, "git", "commit", "-q", "-m", "init")
}

func configRepo(t *testing.T, dir string) {
	t.Helper()
	mustRun(t, dir, "git", "config", "user.email", "test@example.com")
	mustRun(t, dir, "git", "config", "user.name", "Test")
	mustRun(t, dir, "git", "config", "commit.gpgsign", "false")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustRun(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// makeFakeTervaHome scaffolds $TERVA_HOME/extensions/ and returns the path.
func makeFakeTervaHome(t *testing.T) string {
	t.Helper()
	home := testsupport.TempDir(t)
	if err := os.MkdirAll(filepath.Join(home, "extensions"), 0o755); err != nil {
		t.Fatalf("mkdir extensions: %v", err)
	}
	return home
}

// extWithManifest creates a directory with an extension.json so
// updateOneExtension treats it as a real extension. content is the
// JSON to write; pass "" for the default minimal manifest.
func extWithManifest(t *testing.T, root, name, content string) string {
	t.Helper()
	dir := filepath.Join(root, "extensions", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if content == "" {
		content = `{"name":"` + name + `","version":"1.0.0","exec":"./x","enabled":true}`
	}
	writeFile(t, filepath.Join(dir, "extension.json"), content)
	return dir
}

// ----- tests -----

func TestUpdateAllExtensions_NoExtensionsDirectory(t *testing.T) {
	// Brand new $TERVA_HOME with no extensions/ directory at all.
	// Must not panic and must print nothing alarming.
	home := testsupport.TempDir(t)
	updateAllExtensions(home) // no-op
}

func TestUpdateAllExtensions_EmptyDirectory(t *testing.T) {
	home := makeFakeTervaHome(t)
	updateAllExtensions(home) // no-op, no panic
}

func TestUpdateOneExtension_NoManifest(t *testing.T) {
	home := makeFakeTervaHome(t)
	dir := filepath.Join(home, "extensions", "noman")
	_ = os.MkdirAll(dir, 0o755)
	got := updateOneExtension(dir, "noman")
	if got != "skipped" {
		t.Fatalf("want skipped, got %s", got)
	}
}

func TestUpdateOneExtension_Disabled(t *testing.T) {
	home := makeFakeTervaHome(t)
	dir := extWithManifest(t, home, "off",
		`{"name":"off","version":"1.0.0","exec":"./x","enabled":false}`)
	got := updateOneExtension(dir, "off")
	if got != "skipped" {
		t.Fatalf("want skipped, got %s", got)
	}
}

func TestUpdateOneExtension_NotAGitCheckout(t *testing.T) {
	home := makeFakeTervaHome(t)
	dir := extWithManifest(t, home, "plain", "")
	got := updateOneExtension(dir, "plain")
	if got != "skipped" {
		t.Fatalf("want skipped, got %s", got)
	}
}

func TestUpdateOneExtension_UpToDate(t *testing.T) {
	gitAvailable(t)
	tmp := testsupport.TempDir(t)
	home := makeFakeTervaHome(t)

	// Set up a remote and clone it as the "installed" extension.
	remote := filepath.Join(tmp, "remote.git")
	initBareRepo(t, remote)

	source := filepath.Join(tmp, "source")
	initWorkRepo(t, source)
	mustRun(t, source, "git", "remote", "add", "origin", remote)
	mustRun(t, source, "git", "push", "-u", "origin", "main")

	extDir := filepath.Join(home, "extensions", "up2date")
	mustRun(t, "", "git", "clone", "-q", remote, extDir)
	configRepo(t, extDir)
	// Overwrite the cloned README with a manifest in addition.
	writeFile(t, filepath.Join(extDir, "extension.json"),
		`{"name":"up2date","version":"1.0.0","exec":"./x","enabled":true}`)

	got := updateOneExtension(extDir, "up2date")
	if got != "up-to-date" {
		t.Fatalf("want up-to-date, got %s", got)
	}
}

func TestUpdateOneExtension_PullsNewCommit(t *testing.T) {
	gitAvailable(t)
	tmp := testsupport.TempDir(t)
	home := makeFakeTervaHome(t)

	remote := filepath.Join(tmp, "remote.git")
	initBareRepo(t, remote)

	source := filepath.Join(tmp, "source")
	initWorkRepo(t, source)
	writeFile(t, filepath.Join(source, "extension.json"),
		`{"name":"pulls","version":"1.0.0","exec":"./x","enabled":true}`)
	mustRun(t, source, "git", "add", "extension.json")
	mustRun(t, source, "git", "commit", "-q", "-m", "add manifest")
	mustRun(t, source, "git", "remote", "add", "origin", remote)
	mustRun(t, source, "git", "push", "-u", "origin", "main")

	extDir := filepath.Join(home, "extensions", "pulls")
	mustRun(t, "", "git", "clone", "-q", remote, extDir)
	configRepo(t, extDir)

	// Push a new commit to the remote so pulling sees something new.
	writeFile(t, filepath.Join(source, "NEW.md"), "added later\n")
	mustRun(t, source, "git", "add", "NEW.md")
	mustRun(t, source, "git", "commit", "-q", "-m", "add NEW")
	mustRun(t, source, "git", "push", "origin", "main")

	got := updateOneExtension(extDir, "pulls")
	if got != "updated" {
		t.Fatalf("want updated, got %s", got)
	}
	if _, err := os.Stat(filepath.Join(extDir, "NEW.md")); err != nil {
		t.Fatalf("expected NEW.md to be pulled in: %v", err)
	}
}

func TestUpdateOneExtension_StashesDirtyWorktree(t *testing.T) {
	gitAvailable(t)
	tmp := testsupport.TempDir(t)
	home := makeFakeTervaHome(t)

	remote := filepath.Join(tmp, "remote.git")
	initBareRepo(t, remote)
	source := filepath.Join(tmp, "source")
	initWorkRepo(t, source)
	writeFile(t, filepath.Join(source, "extension.json"),
		`{"name":"dirty","version":"1.0.0","exec":"./x","enabled":true}`)
	mustRun(t, source, "git", "add", "extension.json")
	mustRun(t, source, "git", "commit", "-q", "-m", "add manifest")
	mustRun(t, source, "git", "remote", "add", "origin", remote)
	mustRun(t, source, "git", "push", "-u", "origin", "main")

	extDir := filepath.Join(home, "extensions", "dirty")
	mustRun(t, "", "git", "clone", "-q", remote, extDir)
	configRepo(t, extDir)

	// Simulate runtime state: an untracked file the extension wrote.
	writeFile(t, filepath.Join(extDir, "runtime.json"), `{"state":"hi"}`)

	// Advance the remote.
	writeFile(t, filepath.Join(source, "NEW.md"), "added later\n")
	mustRun(t, source, "git", "add", "NEW.md")
	mustRun(t, source, "git", "commit", "-q", "-m", "add NEW")
	mustRun(t, source, "git", "push", "origin", "main")

	got := updateOneExtension(extDir, "dirty")
	if got != "updated" {
		t.Fatalf("want updated, got %s", got)
	}
	// Runtime file must still be there after the pull+stash cycle.
	if _, err := os.Stat(filepath.Join(extDir, "runtime.json")); err != nil {
		t.Fatalf("runtime.json was clobbered by the update: %v", err)
	}
	// And the pulled commit must be present too.
	if _, err := os.Stat(filepath.Join(extDir, "NEW.md")); err != nil {
		t.Fatalf("expected NEW.md to be pulled in: %v", err)
	}
}

func TestUpdateOneExtension_DivergedFails(t *testing.T) {
	gitAvailable(t)
	tmp := testsupport.TempDir(t)
	home := makeFakeTervaHome(t)

	remote := filepath.Join(tmp, "remote.git")
	initBareRepo(t, remote)
	source := filepath.Join(tmp, "source")
	initWorkRepo(t, source)
	writeFile(t, filepath.Join(source, "extension.json"),
		`{"name":"div","version":"1.0.0","exec":"./x","enabled":true}`)
	mustRun(t, source, "git", "add", "extension.json")
	mustRun(t, source, "git", "commit", "-q", "-m", "add manifest")
	mustRun(t, source, "git", "remote", "add", "origin", remote)
	mustRun(t, source, "git", "push", "-u", "origin", "main")

	extDir := filepath.Join(home, "extensions", "div")
	mustRun(t, "", "git", "clone", "-q", remote, extDir)
	configRepo(t, extDir)

	// Local diverging commit (committed, not just dirty).
	writeFile(t, filepath.Join(extDir, "LOCAL.md"), "local commit\n")
	mustRun(t, extDir, "git", "add", "LOCAL.md")
	mustRun(t, extDir, "git", "commit", "-q", "-m", "local")

	// Conflicting remote commit on the same path.
	writeFile(t, filepath.Join(source, "LOCAL.md"), "remote commit\n")
	mustRun(t, source, "git", "add", "LOCAL.md")
	mustRun(t, source, "git", "commit", "-q", "-m", "remote")
	mustRun(t, source, "git", "push", "origin", "main")

	got := updateOneExtension(extDir, "div")
	if got != "failed" {
		t.Fatalf("want failed (diverged), got %s", got)
	}
}

func TestUpdateOneExtension_BadRemoteFails(t *testing.T) {
	gitAvailable(t)
	tmp := testsupport.TempDir(t)
	home := makeFakeTervaHome(t)

	// Init a repo with an "origin" that points at a non-existent path.
	extDir := filepath.Join(home, "extensions", "bad")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustRun(t, extDir, "git", "init", "--initial-branch=main")
	configRepo(t, extDir)
	writeFile(t, filepath.Join(extDir, "README.md"), "hi\n")
	mustRun(t, extDir, "git", "add", ".")
	mustRun(t, extDir, "git", "commit", "-q", "-m", "init")
	writeFile(t, filepath.Join(extDir, "extension.json"),
		`{"name":"bad","version":"1.0.0","exec":"./x","enabled":true}`)
	mustRun(t, extDir, "git", "add", "extension.json")
	mustRun(t, extDir, "git", "commit", "-q", "-m", "manifest")
	mustRun(t, extDir, "git", "remote", "add", "origin",
		filepath.Join(tmp, "does-not-exist.git"))

	// Tracking branch is needed for `git pull` to even attempt fetch.
	// Without one, `git pull` errors out with "no tracking info" which
	// is also a perfectly valid failure for our purposes.
	got := updateOneExtension(extDir, "bad")
	if got != "failed" {
		t.Fatalf("want failed, got %s", got)
	}
}

func TestUpdateAllExtensions_MixedSet(t *testing.T) {
	gitAvailable(t)
	tmp := testsupport.TempDir(t)
	home := makeFakeTervaHome(t)

	// 1 plain non-git extension (skipped)
	extWithManifest(t, home, "plain", "")
	// 1 disabled
	extWithManifest(t, home, "off",
		`{"name":"off","version":"1.0.0","exec":"./x","enabled":false}`)
	// 1 healthy git clone
	remote := filepath.Join(tmp, "remote.git")
	initBareRepo(t, remote)
	source := filepath.Join(tmp, "source")
	initWorkRepo(t, source)
	writeFile(t, filepath.Join(source, "extension.json"),
		`{"name":"ok","version":"1.0.0","exec":"./x","enabled":true}`)
	mustRun(t, source, "git", "add", "extension.json")
	mustRun(t, source, "git", "commit", "-q", "-m", "manifest")
	mustRun(t, source, "git", "remote", "add", "origin", remote)
	mustRun(t, source, "git", "push", "-u", "origin", "main")
	extDir := filepath.Join(home, "extensions", "ok")
	mustRun(t, "", "git", "clone", "-q", remote, extDir)
	configRepo(t, extDir)

	// Must not panic, must not propagate any error to the caller.
	updateAllExtensions(home)
}

func TestSummariseGitError_PrefersErrorOverHint(t *testing.T) {
	out := "hint: try git pull\nfatal: refusing to merge unrelated histories\nhint: see git-merge"
	got := summariseGitError(out, exec.Command("false").Run())
	if !strings.Contains(got, "fatal:") {
		t.Fatalf("want fatal line, got %q", got)
	}
}

func TestRunGit_HonoursContextTimeout(t *testing.T) {
	gitAvailable(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// `git rev-parse HEAD` in a non-repo errors quickly; we just want
	// to confirm we don't hang.
	_, _ = runGit(ctx, testsupport.TempDir(t), "rev-parse", "HEAD")
}
