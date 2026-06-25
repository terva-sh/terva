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
