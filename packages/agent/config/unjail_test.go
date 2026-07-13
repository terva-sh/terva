package config

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// scratchHome points $TERVA_HOME at a temp dir so the tests never touch the
// real store.
func scratchHome(t *testing.T) string {
	t.Helper()
	dir := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", dir)
	return dir
}

func TestUnjailStoreRoundTrip(t *testing.T) {
	scratchHome(t)
	dir := testsupport.TempDir(t)

	if ok, err := IsPathUnjailed(dir); err != nil || ok {
		t.Fatalf("fresh store: unjailed=%v err=%v, want jailed by default", ok, err)
	}
	if err := UnjailPath(dir, false); err != nil {
		t.Fatalf("UnjailPath: %v", err)
	}
	ok, err := IsPathUnjailed(dir)
	if err != nil || !ok {
		t.Fatalf("after UnjailPath: unjailed=%v err=%v, want unjailed", ok, err)
	}

	if err := RejailPath(dir); err != nil {
		t.Fatalf("RejailPath: %v", err)
	}
	if ok, _ := IsPathUnjailed(dir); ok {
		t.Error("RejailPath left the directory unjailed")
	}
}

// A plain entry covers its own directory only. Descendants stay jailed unless
// the user explicitly said --parent: unjailing a repo must not silently unjail
// every repo checked out beneath it.
func TestUnjailIsNotInheritedWithoutParent(t *testing.T) {
	scratchHome(t)
	root := testsupport.TempDir(t)
	child := filepath.Join(root, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := UnjailPath(root, false); err != nil {
		t.Fatal(err)
	}
	if ok, _ := IsPathUnjailed(child); ok {
		t.Error("a non-parent entry leaked to a descendant")
	}

	if err := UnjailPath(root, true); err != nil {
		t.Fatal(err)
	}
	if ok, _ := IsPathUnjailed(child); !ok {
		t.Error("--parent did not cover the descendant")
	}
}

// The sibling-prefix trap: "/a/b" must not match "/a/bcd".
func TestUnjailParentDoesNotMatchSiblingPrefix(t *testing.T) {
	scratchHome(t)
	base := testsupport.TempDir(t)
	ab := filepath.Join(base, "b")
	abcd := filepath.Join(base, "bcd")
	for _, d := range []string{ab, abcd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := UnjailPath(ab, true); err != nil {
		t.Fatal(err)
	}
	if ok, _ := IsPathUnjailed(abcd); ok {
		t.Errorf("unjailing %s also unjailed the sibling %s", ab, abcd)
	}
}

func TestUnjailAddIsIdempotentAndPromotes(t *testing.T) {
	scratchHome(t)
	dir := testsupport.TempDir(t)

	var s UnjailStore
	if !s.Add(dir, false) {
		t.Fatal("first Add reported no change")
	}
	if s.Add(dir, false) {
		t.Error("re-adding the same scope reported a change")
	}
	if !s.Add(dir, true) {
		t.Error("promoting to --parent reported no change")
	}
	if len(s.Unjailed) != 1 {
		t.Errorf("store has %d entries, want 1 (promotion must not duplicate)", len(s.Unjailed))
	}
}

// A corrupt store is a hard error, never an empty one. Silently reading it as
// empty would be the safe direction here (stay jailed) but it would also hide a
// broken file forever — and the same tolerance in trusted.json would fail OPEN.
// Both stores refuse, so neither can teach the habit.
func TestUnjailCorruptStoreIsAnError(t *testing.T) {
	home := scratchHome(t)
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(UnjailStorePath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadUnjailStore(); err == nil {
		t.Fatal("LoadUnjailStore accepted a corrupt file")
	}
	// And the resolver must not report "unjailed" off the back of it.
	ok, err := IsPathUnjailed(testsupport.TempDir(t))
	if ok {
		t.Error("a corrupt store reported a path as unjailed")
	}
	if err == nil {
		t.Error("a corrupt store did not surface an error")
	}
}

// The store is written 0600: it records a decision that widens a sandbox.
func TestUnjailStoreIsPrivate(t *testing.T) {
	scratchHome(t)
	if err := UnjailPath(testsupport.TempDir(t), false); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(UnjailStorePath())
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("unjailed.json mode = %o, want 600", perm)
	}
}

// Unjail and trust are different decisions and must not be entangled: trusting
// a repo must not unjail it, and unjailing must not trust it.
func TestUnjailAndTrustAreIndependent(t *testing.T) {
	scratchHome(t)
	dir := testsupport.TempDir(t)

	if err := TrustPath(dir, false); err != nil {
		t.Fatal(err)
	}
	if ok, _ := IsPathUnjailed(dir); ok {
		t.Error("trusting a directory also unjailed it")
	}

	other := testsupport.TempDir(t)
	if err := UnjailPath(other, false); err != nil {
		t.Fatal(err)
	}
	store, err := LoadTrustStore()
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := store.IsTrusted(other); ok {
		t.Error("unjailing a directory also trusted it")
	}
}
