package permissions

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/testsupport"
)

// Tests for the trust policy layer (trust_cli.go): the store and the on-disk
// state are config's, the per-launch resolution is this package's.
// The store, paths and on-disk state are tested in packages/agent/config.

func TestMovedDirNotTrusted(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	base := testsupport.TempDir(t)
	orig := filepath.Join(base, "before")
	moved := filepath.Join(base, "after")
	if err := os.MkdirAll(orig, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := config.TrustPath(orig, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(orig, moved); err != nil {
		t.Fatal(err)
	}
	s, _ := config.LoadTrustStore()
	// The real path changed, so the moved dir is NOT trusted (the safe
	// failure — a moved/re-cloned repo re-prompts).
	if ok, _ := s.IsTrusted(moved); ok {
		t.Errorf("moved dir %q should not inherit the old path's trust", moved)
	}
}

func TestResolveTrustPrecedence(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	repo := testsupport.TempDir(t)
	other := testsupport.TempDir(t)

	empty := config.TrustStore{Version: config.TrustStoreVersion}
	withRepo := config.TrustStore{Version: config.TrustStoreVersion}
	withRepo.Add(repo, false)

	cases := []struct {
		name   string
		cwd    string
		forced bool
		store  config.TrustStore
		want   config.TrustState
	}{
		{"default untrusted", repo, false, empty, config.TrustRestricted},
		{"--trust one-shot trusts", repo, true, empty, config.TrustGranted},
		{"store entry trusts", repo, false, withRepo, config.TrustGranted},
		{"store entry does not trust other dir", other, false, withRepo, config.TrustRestricted},
		{"--trust wins even off-store", other, true, withRepo, config.TrustGranted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveTrust(c.cwd, c.forced, c.store); got != c.want {
				t.Errorf("resolveTrust = %v, want %v", got, c.want)
			}
		})
	}
}

// --trust must NOT persist anything to the store.
func TestTrustFlagDoesNotPersist(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	repo := testsupport.TempDir(t)

	if got := ResolveTrustState(repo, true); got != config.TrustGranted {
		t.Fatalf("--trust should grant for the run, got %v", got)
	}
	// The store file must not have been created/written by the flag.
	if _, err := os.Stat(config.TrustStorePath()); !os.IsNotExist(err) {
		t.Errorf("--trust must not write trusted.json (stat err = %v)", err)
	}
	s, _ := config.LoadTrustStore()
	if len(s.Trusted) != 0 {
		t.Errorf("--trust must not add a store entry: %v", s.Trusted)
	}
}
