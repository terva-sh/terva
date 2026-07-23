package extdriver

import (
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// TestExtDataDir: the writable data dir is $TERVA_HOME/ext-data/<name>,
// keyed by name (stable across install location), and empty when there's
// no terva home so the caller falls back to the install dir.
func TestExtDataDir(t *testing.T) {
	home := testsupport.TempDir(t)
	mgr := New(home, "", "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})

	if got, want := mgr.extDataDir("todos"), filepath.Join(home, "ext-data", "todos"); got != want {
		t.Errorf("extDataDir = %q, want %q", got, want)
	}
	if got := mgr.extDataDir(""); got != "" {
		t.Errorf("empty name should yield empty dir, got %q", got)
	}

	noHome := New("", "/some/cwd", "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	if got := noHome.extDataDir("todos"); got != "" {
		t.Errorf("no terva home should yield empty dir (fall back to install), got %q", got)
	}
}

// TestDataDirFallsBackToInstallDirOnly pins the compatibility path the docs now
// warn about: hello_ack's data_dir is $TERVA_HOME/ext-data/<name> in every
// normal deployment, and equals extension_dir ONLY when there is no terva home
// or the state dir cannot be created.
//
// The distinction is load-bearing on a managed install where the install tree
// is read-only: an extension that writes to extension_dir because it once
// observed the two being equal fails there with a read-only-filesystem error.
// The docs used to recommend exactly that (state "inside that directory
// itself"), which is why this is pinned rather than assumed.
func TestDataDirFallsBackToInstallDirOnly(t *testing.T) {
	home := testsupport.TempDir(t)
	withHome := New(home, "", "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	installDir := filepath.Join(home, "extensions", "todos")

	dd := withHome.extDataDir("todos")
	if dd == installDir {
		t.Fatalf("data_dir must not equal the install dir when a terva home exists: %q", dd)
	}
	if want := filepath.Join(home, "ext-data", "todos"); dd != want {
		t.Errorf("data_dir = %q, want %q", dd, want)
	}

	// No terva home: extDataDir yields "", which is the signal spawn() uses to
	// keep dataDir at ext.Dir — the pre-ext-data colocated layout.
	noHome := New("", "/some/cwd", "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	if got := noHome.extDataDir("todos"); got != "" {
		t.Errorf("no terva home must yield %q so the caller falls back to the install dir, got %q", "", got)
	}
}
