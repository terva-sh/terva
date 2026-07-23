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

// TestManifestNameCollisionSilentlyDropsOne pins what actually happens when
// two extensions declare the same manifest name from differently-named
// directories — a case the docs used to gloss as "a project-local extension
// with the same name wins".
//
// Two separate rules are in play:
//
//   - Discover dedups by DIRECTORY basename, so both directories are queued.
//   - Driver.Load then claims the MANIFEST name atomically, and the loser
//     returns nil — silently. No error, no stderr line, nothing in the
//     discover result to say an extension was dropped.
//
// Because the queued loads run in parallel goroutines, WHICH copy wins is not
// deterministic. Neither can spawn here (exec is missing), so exactly one
// error surfaces: the claim winner's spawn failure. A second error would mean
// both spawned; zero would mean neither was queued.
func TestManifestNameCollisionSilentlyDropsOne(t *testing.T) {
	tmp := testsupport.TempDir(t)
	write := func(dir, name string) {
		d := filepath.Join(tmp, "extensions", dir)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		manifest := `{"name":"` + name + `","version":"1.0.0","description":"x","exec":"./does-not-exist"}`
		if err := os.WriteFile(filepath.Join(d, "extension.json"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("alpha", "collide")
	write("beta", "collide")

	mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-opus-4-7", nil)
	errs := mgr.Discover(context.Background())
	defer mgr.Stop(10 * time.Millisecond)

	if len(errs) != 1 {
		joined := ""
		for _, e := range errs {
			joined += "\n  " + e.Error()
		}
		t.Fatalf("expected exactly one surviving load (the manifest-name claim drops the other "+
			"silently), got %d:%s\nif this is now 2, the name claim no longer dedups; if 0, "+
			"discovery no longer queues both directories. Either way update the "+
			"'Layout & discovery' section of docs/extensions.md.", len(errs), joined)
	}
	// The survivor is whichever goroutine claimed the name first — deliberately
	// not asserted, because it is a race. Pinning one would encode a bug.
	if got := errs[0].Error(); !strings.Contains(got, "alpha") && !strings.Contains(got, "beta") {
		t.Errorf("the surviving load should be one of the two colliding dirs, got: %s", got)
	}
}
