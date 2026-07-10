package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// --- store round-trip + idempotence ---

func TestTrustStoreRoundTrip(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	// Missing file ⇒ empty store, no error.
	s, err := LoadTrustStore()
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if len(s.Trusted) != 0 {
		t.Fatalf("fresh store should be empty, got %v", s.Trusted)
	}

	repo := testsupport.TempDir(t)
	if err := TrustPath(repo, false); err != nil {
		t.Fatalf("trust: %v", err)
	}

	// Reloads with the entry.
	s2, err := LoadTrustStore()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if ok, _ := s2.IsTrusted(repo); !ok {
		t.Fatalf("round-tripped store should trust %q: %v", repo, s2.Trusted)
	}

	// 0600 file mode.
	st, err := os.Stat(TrustStorePath())
	if err != nil {
		t.Fatalf("stat store: %v", err)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("trusted.json mode = %v, want 0600", st.Mode().Perm())
	}
}

func TestTrustPathIdempotent(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	repo := testsupport.TempDir(t)

	if err := TrustPath(repo, false); err != nil {
		t.Fatal(err)
	}
	if err := TrustPath(repo, false); err != nil {
		t.Fatal(err)
	}
	s, _ := LoadTrustStore()
	if len(s.Trusted) != 1 {
		t.Fatalf("trusting the same dir twice should not duplicate: %v", s.Trusted)
	}

	// Untrust is idempotent too.
	if err := UntrustPath(repo); err != nil {
		t.Fatal(err)
	}
	if err := UntrustPath(repo); err != nil {
		t.Fatal(err)
	}
	s2, _ := LoadTrustStore()
	if len(s2.Trusted) != 0 {
		t.Fatalf("untrust should leave an empty store: %v", s2.Trusted)
	}
}

func TestTrustPromoteToParent(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	repo := testsupport.TempDir(t)
	if err := TrustPath(repo, false); err != nil {
		t.Fatal(err)
	}
	if err := TrustPath(repo, true); err != nil { // same dir, now as a parent
		t.Fatal(err)
	}
	s, _ := LoadTrustStore()
	if len(s.Trusted) != 1 {
		t.Fatalf("promoting should not duplicate the entry: %v", s.Trusted)
	}
	if !s.Trusted[0].Parent {
		t.Errorf("entry should be promoted to Parent:true: %+v", s.Trusted[0])
	}
}

// --- identity: canonicalization + parent-prefix + symlink ---

func TestIsTrustedCanonicalization(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	repo := testsupport.TempDir(t)
	if err := TrustPath(repo, false); err != nil {
		t.Fatal(err)
	}
	s, _ := LoadTrustStore()

	// Trailing slash, doubled separators, and a "." segment all
	// canonicalize to the same real path.
	sep := string(filepath.Separator)
	variants := []string{
		repo + sep,
		repo + sep + ".",
		filepath.Join(repo, "sub", ".."),
	}
	for _, v := range variants {
		if ok, _ := s.IsTrusted(v); !ok {
			t.Errorf("canonical variant %q should be trusted", v)
		}
	}

	// A sibling that merely shares a string prefix must NOT be trusted
	// (no-parent entry trusts only its own dir, and the separator guard
	// prevents "/a/b" matching "/a/bcd").
	sibling := repo + "-sibling"
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.IsTrusted(sibling); ok {
		t.Errorf("string-prefix sibling %q must not be trusted", sibling)
	}
}

func TestIsTrustedChildNotTrustedWithoutParent(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	repo := testsupport.TempDir(t)
	child := filepath.Join(repo, "nested")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	// Trust the parent dir WITHOUT the parent flag: the child must not
	// inherit trust.
	if err := TrustPath(repo, false); err != nil {
		t.Fatal(err)
	}
	s, _ := LoadTrustStore()
	if ok, _ := s.IsTrusted(child); ok {
		t.Errorf("child %q must not be trusted by a non-parent entry on %q", child, repo)
	}
}

func TestIsTrustedParentTrustsChildren(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	parent := testsupport.TempDir(t)
	child := filepath.Join(parent, "repo")
	grandchild := filepath.Join(child, "deeper")
	if err := os.MkdirAll(grandchild, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := TrustPath(parent, true); err != nil { // trust the parent
		t.Fatal(err)
	}
	s, _ := LoadTrustStore()
	for _, p := range []string{parent, child, grandchild} {
		if ok, _ := s.IsTrusted(p); !ok {
			t.Errorf("parent trust should cover %q", p)
		}
	}
	// A directory OUTSIDE the trusted parent stays untrusted.
	outside := testsupport.TempDir(t)
	if ok, _ := s.IsTrusted(outside); ok {
		t.Errorf("unrelated dir %q must not be trusted", outside)
	}
}

func TestIsTrustedSymlinkedDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is privileged on Windows CI")
	}
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	real := testsupport.TempDir(t)
	// A symlink pointing at the real trusted dir resolves to the same
	// canonical path, so it is trusted too.
	link := filepath.Join(testsupport.TempDir(t), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := TrustPath(real, false); err != nil {
		t.Fatal(err)
	}
	s, _ := LoadTrustStore()
	if ok, _ := s.IsTrusted(link); !ok {
		t.Errorf("symlink %q to trusted real dir %q should be trusted", link, real)
	}

	// And trusting via the symlink also trusts the real path (both
	// canonicalize identically).
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	if err := TrustPath(link, false); err != nil {
		t.Fatal(err)
	}
	s2, _ := LoadTrustStore()
	if ok, _ := s2.IsTrusted(real); !ok {
		t.Errorf("trusting via symlink should trust the real dir %q", real)
	}
}

// --- resolveTrust precedence ---

// On Windows, two case spellings of the same dir must match.
func TestIsTrustedCaseInsensitiveOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("case-insensitive matching is a Windows behavior")
	}
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	repo := testsupport.TempDir(t)
	if err := TrustPath(repo, false); err != nil {
		t.Fatal(err)
	}
	s, _ := LoadTrustStore()
	if ok, _ := s.IsTrusted(strings.ToUpper(repo)); !ok {
		t.Errorf("upper-cased spelling of %q should match on Windows", repo)
	}
}

// HasGatedProjectContent only fires when the dir actually ships gated
// content (so a plain repo never sees a trust notice).
func TestHasGatedProjectContent(t *testing.T) {
	plain := testsupport.TempDir(t)
	if HasGatedProjectContent(plain) {
		t.Errorf("plain dir %q should have no gated content", plain)
	}
	withSkills := testsupport.TempDir(t)
	if err := os.MkdirAll(filepath.Join(withSkills, ".terva", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !HasGatedProjectContent(withSkills) {
		t.Errorf("dir with .terva/skills should have gated content")
	}
	withExt := testsupport.TempDir(t)
	if err := os.MkdirAll(filepath.Join(withExt, ".terva", "extensions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !HasGatedProjectContent(withExt) {
		t.Errorf("dir with .terva/extensions should have gated content")
	}
}
