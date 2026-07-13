package modes

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
	"terva.sh/terva/packages/tui"
)

// An unset recursive_file_suggest means RECURSIVE — the @-picker fuzzy-
// searches the whole tree by default, matching the web composer's @-stage
// (which has no flat mode at all). The flat directory-by-directory browse is
// the explicit opt-out. Pinned behaviorally: a default-constructed
// Interactive's picker must surface a nested file.
func TestFileSuggestDefaultsToRecursive(t *testing.T) {
	tmp := testsupport.TempDir(t)
	deep := filepath.Join(tmp, "src", "util")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "nested.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	i := NewInteractive(InteractiveConfig{
		Theme:   tui.Dark,
		Carrier: newFakeCarrier(),
		CWD:     tmp,
		// RecursiveFileSuggest deliberately nil: the default under test.
	})
	i.fileSuggest.SetCWD(i.cfg.CWD) // the render loop's per-frame seed
	nv, ok := i.fileSuggest.TabComplete("@src/util/nest")
	if !ok || nv != "@src/util/nested.go" {
		t.Fatalf("default picker did not tab-complete a nested path: (%q, %v)", nv, ok)
	}

	// The opt-out still opts out.
	off := false
	flat := NewInteractive(InteractiveConfig{
		Theme:                tui.Dark,
		Carrier:              newFakeCarrier(),
		CWD:                  tmp,
		RecursiveFileSuggest: &off,
	})
	flat.fileSuggest.SetCWD(flat.cfg.CWD)
	if nv, _ := flat.fileSuggest.TabComplete("@src/util/nest"); nv != "@src/util/nest" {
		t.Fatalf("flat opt-out completed a nested path anyway: %q", nv)
	}
}
