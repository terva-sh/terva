package agent

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"terva.sh/terva/packages/envcompat"
	"terva.sh/terva/packages/testsupport"
)

// pinMigrateEnv points the envcompat resolvers at temp dirs (XDG for
// linux, $HOME for darwin — os.UserHomeDir honors $HOME — and
// %LOCALAPPDATA% for windows) and clears the explicit overrides,
// returning the base both "terva" and "zot" default dirs resolve
// under.
func pinMigrateEnv(t *testing.T) string {
	t.Helper()
	base := testsupport.TempDir(t)
	t.Setenv("XDG_STATE_HOME", base)
	switch runtime.GOOS {
	case "darwin":
		home := testsupport.TempDir(t)
		t.Setenv("HOME", home)
		base = filepath.Join(home, "Library", "Application Support")
	case "windows":
		t.Setenv("LOCALAPPDATA", base)
	}
	t.Setenv("TERVA_HOME", "")
	t.Setenv("ZOT_HOME", "")
	os.Unsetenv("TERVA_HOME")
	os.Unsetenv("ZOT_HOME")
	return base
}

func mkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeMigrateFile(t *testing.T, path, content string) {
	t.Helper()
	mkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPlanMigration(t *testing.T) {
	t.Run("zot dir only", func(t *testing.T) {
		base := pinMigrateEnv(t)
		mkdirAll(t, filepath.Join(base, "zot"))
		p := PlanMigration(testsupport.TempDir(t))
		if p.OldDir != filepath.Join(base, "zot") || p.NewDir != filepath.Join(base, "terva") {
			t.Errorf("plan = %q → %q, want zot → terva under %q", p.OldDir, p.NewDir, base)
		}
		if !p.UserDirApplicable() || p.AlreadyMigrated || p.NothingToDo() {
			t.Errorf("flags wrong: %+v", p)
		}
	})

	t.Run("no legacy dir", func(t *testing.T) {
		pinMigrateEnv(t)
		p := PlanMigration(testsupport.TempDir(t))
		if p.UserDirApplicable() {
			t.Errorf("no zot dir, but OldDir = %q", p.OldDir)
		}
	})

	t.Run("ZOT_HOME set", func(t *testing.T) {
		base := pinMigrateEnv(t)
		src := filepath.Join(base, "custom-zot")
		mkdirAll(t, src)
		t.Setenv("ZOT_HOME", src)
		p := PlanMigration(testsupport.TempDir(t))
		if p.OldDir != src || !p.OldFromEnv || p.EnvNote == "" {
			t.Errorf("want ZOT_HOME source with env note, got %+v", p)
		}
	})

	t.Run("TERVA_HOME pointing at the zot dir", func(t *testing.T) {
		base := pinMigrateEnv(t)
		zot := filepath.Join(base, "zot")
		mkdirAll(t, zot)
		t.Setenv("TERVA_HOME", zot)
		p := PlanMigration(testsupport.TempDir(t))
		if p.UserDirApplicable() {
			t.Errorf("old == new must skip the user-dir step, got OldDir=%q", p.OldDir)
		}
	})

	t.Run("project dirs", func(t *testing.T) {
		pinMigrateEnv(t)
		root := testsupport.TempDir(t)
		sub := filepath.Join(root, "a", "b")
		mkdirAll(t, filepath.Join(root, ".zot"))
		mkdirAll(t, sub)
		p := PlanMigration(sub)
		if p.ProjectOldDir != filepath.Join(root, ".zot") || !p.ProjectApplicable() {
			t.Errorf("want project .zot at %q, got %+v", root, p)
		}

		mkdirAll(t, filepath.Join(root, ".terva"))
		p = PlanMigration(sub)
		if !p.ProjectConflict || p.ProjectApplicable() {
			t.Errorf("both spellings present must be a conflict, got %+v", p)
		}
	})

	t.Run("nothing to do after migration", func(t *testing.T) {
		base := pinMigrateEnv(t)
		mkdirAll(t, filepath.Join(base, "terva"))
		if err := envcompat.SetZotFallbackDisabled(true); err != nil {
			t.Fatal(err)
		}
		p := PlanMigration(testsupport.TempDir(t))
		if !p.AlreadyMigrated || !p.NothingToDo() {
			t.Errorf("want NothingToDo, got %+v", p)
		}
	})
}

func TestCopyUserDataNoClobber(t *testing.T) {
	old := testsupport.TempDir(t)
	dest := testsupport.TempDir(t)

	writeMigrateFile(t, filepath.Join(old, "config.json"), "old-config")
	writeMigrateFile(t, filepath.Join(old, "sessions", "abc", "s1.jsonl"), "session")
	writeMigrateFile(t, filepath.Join(old, ".terva-migration-note-shown"), "sentinel")
	if err := os.Chmod(filepath.Join(old, "config.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A connector installed by symlink, target inside the old dir, and
	// one pointing elsewhere.
	writeMigrateFile(t, filepath.Join(old, "connectors", "real.json"), "manifest")
	if err := os.Symlink(filepath.Join(old, "connectors", "real.json"), filepath.Join(old, "connectors", "link.json")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(testsupport.TempDir(t), "outside.json")
	writeMigrateFile(t, outside, "outside")
	if err := os.Symlink(outside, filepath.Join(old, "connectors", "ext.json")); err != nil {
		t.Fatal(err)
	}
	// Pre-existing file at the destination must survive untouched.
	writeMigrateFile(t, filepath.Join(dest, "config.json"), "fresh-config")

	rep := CopyUserData(old, dest)
	if !rep.Clean() {
		t.Fatalf("unexpected errors: %v", rep.Errors)
	}
	if rep.FilesCopied != 2 { // sessions/abc/s1.jsonl + connectors/real.json
		t.Errorf("FilesCopied = %d, want 2", rep.FilesCopied)
	}
	if rep.SymlinksCopied != 2 {
		t.Errorf("SymlinksCopied = %d, want 2", rep.SymlinksCopied)
	}
	if len(rep.SkippedExisting) != 1 || rep.SkippedExisting[0] != "config.json" {
		t.Errorf("SkippedExisting = %v, want [config.json]", rep.SkippedExisting)
	}

	if b, _ := os.ReadFile(filepath.Join(dest, "config.json")); string(b) != "fresh-config" {
		t.Errorf("destination config clobbered: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dest, "sessions", "abc", "s1.jsonl")); string(b) != "session" {
		t.Errorf("session not copied: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dest, ".terva-migration-note-shown")); !os.IsNotExist(err) {
		t.Error("legacy one-shot sentinel must not be copied")
	}
	// Windows emulates unix permissions (0666/0444 only), so the
	// preserved-mode assertion is meaningful elsewhere only.
	if st, err := os.Stat(filepath.Join(dest, "sessions", "abc", "s1.jsonl")); err != nil || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o644) {
		t.Errorf("mode not preserved: %v %v", st, err)
	}
	// In-dir symlink target rewritten to the new location.
	if target, err := os.Readlink(filepath.Join(dest, "connectors", "link.json")); err != nil || target != filepath.Join(dest, "connectors", "real.json") {
		t.Errorf("in-dir symlink = %q, %v; want rewrite into dest", target, err)
	}
	// Outside target preserved verbatim.
	if target, err := os.Readlink(filepath.Join(dest, "connectors", "ext.json")); err != nil || target != outside {
		t.Errorf("outside symlink = %q, %v; want %q", target, err, outside)
	}

	// Second run: everything already there, nothing copied, no errors.
	rep2 := CopyUserData(old, dest)
	if !rep2.Clean() || rep2.FilesCopied != 0 || rep2.SymlinksCopied != 0 {
		t.Errorf("re-run not idempotent: %+v", rep2)
	}
}

func TestCopyUserDataSkipsIrregularFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix domain sockets")
	}
	// Not testsupport.TempDir(t): it embeds this test's long name, and sun_path
	// tops out around 104 bytes on darwin — bind fails with EINVAL.
	old, err := os.MkdirTemp("", "terva-sock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(old) })
	dest := testsupport.TempDir(t)
	writeMigrateFile(t, filepath.Join(old, "agents", "a1", "meta.json"), "meta")
	l, err := net.Listen("unix", filepath.Join(old, "agents", "a1", "in.sock"))
	if err != nil {
		t.Fatal(err)
	}
	// Leave the socket file behind, the way a finished swarm agent does.
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()

	rep := CopyUserData(old, dest)
	if !rep.Clean() {
		t.Fatalf("a dead socket must not dirty the copy: %v", rep.Errors)
	}
	if rep.FilesCopied != 1 {
		t.Errorf("FilesCopied = %d, want 1 (meta.json)", rep.FilesCopied)
	}
	if _, err := os.Lstat(filepath.Join(dest, "agents", "a1", "in.sock")); !os.IsNotExist(err) {
		t.Errorf("socket must not be recreated at the destination: %v", err)
	}
}

func TestCopyUserDataReportsErrors(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("chmod-based unreadable file needs non-root unix")
	}
	old := testsupport.TempDir(t)
	dest := testsupport.TempDir(t)
	writeMigrateFile(t, filepath.Join(old, "ok.json"), "ok")
	writeMigrateFile(t, filepath.Join(old, "locked.json"), "secret")
	if err := os.Chmod(filepath.Join(old, "locked.json"), 0o000); err != nil {
		t.Fatal(err)
	}

	rep := CopyUserData(old, dest)
	if rep.Clean() || len(rep.Errors) != 1 {
		t.Fatalf("want exactly one error, got %+v", rep)
	}
	if rep.FilesCopied != 1 {
		t.Errorf("walk must continue past the failure: FilesCopied = %d, want 1", rep.FilesCopied)
	}

	p := MigrationPlan{OldDir: old, NewDir: dest}
	if err := RemoveOldUserDir(p, rep); err == nil {
		t.Error("RemoveOldUserDir must refuse after a dirty copy")
	}
	if _, err := os.Stat(filepath.Join(old, "ok.json")); err != nil {
		t.Errorf("old dir must be intact after the refusal: %v", err)
	}
}

func TestRemoveOldUserDir(t *testing.T) {
	old := testsupport.TempDir(t)
	dest := testsupport.TempDir(t)
	writeMigrateFile(t, filepath.Join(old, "config.json"), "x")
	rep := CopyUserData(old, dest)
	p := MigrationPlan{OldDir: old, NewDir: dest}
	if err := RemoveOldUserDir(p, rep); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("old dir still present")
	}

	// Identity refusal: never delete the migration target.
	same := testsupport.TempDir(t)
	if err := RemoveOldUserDir(MigrationPlan{OldDir: same, NewDir: same}, MigrationCopyReport{}); err == nil {
		t.Error("must refuse when old == new")
	}
}

func TestRenameProjectDir(t *testing.T) {
	root := testsupport.TempDir(t)
	mkdirAll(t, filepath.Join(root, ".zot"))
	writeMigrateFile(t, filepath.Join(root, ".zot", "config.json"), "{}")
	p := PlanMigration(root)
	if err := RenameProjectDir(p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".terva", "config.json")); err != nil {
		t.Errorf(".terva/config.json missing after rename: %v", err)
	}

	// Conflict refusal.
	mkdirAll(t, filepath.Join(root, ".zot"))
	p = PlanMigration(root)
	if err := RenameProjectDir(p); err == nil {
		t.Error("must refuse when both .zot and .terva exist")
	}
}

func TestFinalizeMigration(t *testing.T) {
	pinMigrateEnv(t)
	if err := FinalizeMigration(); err != nil {
		t.Fatal(err)
	}
	if !envcompat.ZotFallbackDisabled() {
		t.Error("marker not visible after FinalizeMigration")
	}
	if p := PlanMigration(testsupport.TempDir(t)); !p.AlreadyMigrated {
		t.Error("next PlanMigration must see AlreadyMigrated")
	}
}
