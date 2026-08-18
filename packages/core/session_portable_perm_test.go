package core

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// ImportSession and BranchSession create LIVE transcripts in the data home —
// same directory, same shape, same contents as one the session writer opens
// through privfs. They created them 0644 under a 0755 directory, so an imported
// or branched session was readable by every account on the machine while every
// session terva itself opened was 0600 under 0700.
//
// The privfs gate did not catch it: session_portable.go was exempt for its
// EXPORT, which legitimately writes where the user asked. One argued write
// sheltered two unargued ones, because the exemption covered the whole FILE.
func TestImportAndBranchWriteOwnerOnlySessions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}
	root := testsupport.TempDir(t)

	// A real session, exported, so the fixture is whatever the writer produces.
	parent, err := NewSession(testsupport.TempDir(t), "/original/cwd", "anthropic", "m", "0.0.0-test")
	if err != nil {
		t.Fatal(err)
	}
	_ = parent.AppendMessage(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "hi"}},
	})
	_ = parent.Close()

	exportPath, err := ExportSession(parent.Path, testsupport.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}

	imported, err := ImportSession(exportPath, root, "/my/cwd", "1.2.3")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	assertOwnerOnly(t, imported, "an imported session")

	branched, err := BranchSession(imported, root, "/my/cwd", "1.2.3", 0)
	if err != nil {
		t.Fatalf("branch: %v", err)
	}
	assertOwnerOnly(t, branched, "a branched session")

	// The containing directory too: a 0755 directory inside a 0700 tree is
	// wrong in the way that only shows once the tree is copied or repaired.
	dir := filepath.Dir(imported)
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("the sessions directory is %04o; every other writer creates it 0700", fi.Mode().Perm())
	}
}

func assertOwnerOnly(t *testing.T, path, what string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("%s is %04o — readable beyond its owner, while the session writer creates 0600", what, perm)
	}
}
