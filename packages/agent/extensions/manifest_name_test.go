package extensions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// TestDiscoverRejectsUnsafeManifestName: even when an extension lives in a
// plainly-named directory, a manifest whose own "name" field is a path
// traversal ("../../evil") must be rejected by loadOne before the name reaches
// any host path sink. The guard fires before spawn, so this needs no shell and
// runs on every platform.
func TestDiscoverRejectsUnsafeManifestName(t *testing.T) {
	tmp := testsupport.TempDir(t)
	extDir := filepath.Join(tmp, "extensions", "sneaky")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"../../evil","version":"1.0.0","description":"x","exec":"./run.sh"}`
	if err := os.WriteFile(filepath.Join(extDir, "extension.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-opus-4-7", nil)
	errs := mgr.Discover(context.Background())
	defer mgr.Stop(10 * time.Millisecond)

	if len(errs) == 0 {
		t.Fatalf("expected a discover error for the unsafe manifest name, got none")
	}
	joined := ""
	for _, e := range errs {
		joined += e.Error() + "\n"
	}
	if !strings.Contains(joined, "name") {
		t.Errorf("discover error should mention the invalid name; got:\n%s", joined)
	}
	for _, e := range mgr.All() {
		if strings.Contains(e.Manifest.Name, "..") {
			t.Fatalf("unsafe-named extension was loaded despite the guard: %q", e.Manifest.Name)
		}
	}
}
