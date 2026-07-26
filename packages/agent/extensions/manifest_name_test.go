package extensions

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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
// Both copies here SPAWN, which is load-bearing rather than incidental. This
// test used to point both manifests at a missing exec and assert that exactly
// one error came back — the winner's spawn failure. That made it flaky, and it
// failed in CI for the first time in July 2026: a failed spawn ROLLS THE NAME
// CLAIM BACK (extdriver.Driver.Load) to keep the name retryable, so the window
// in which the loser sees a claim at all lasts only as long as the winner's
// doomed spawn attempt. On a loaded runner the winner could claim, fail, and
// roll back before the loser even reached the check — whereupon the loser
// claimed cleanly, failed too, and two errors came back. The assertion was
// racing an implementation detail it did not name.
//
// With a spawn that succeeds the claim is permanent, so the drop is observable
// without a race, and the assertions get stronger: no error at all (that is
// what "silently" means), exactly one tracked extension, and no orphaned
// second subprocess. WHICH copy wins is still deliberately not asserted —
// pinning that would encode a bug.
func TestManifestNameCollisionSilentlyDropsOne(t *testing.T) {
	tmp := testsupport.TempDir(t)
	// Each copy appends its directory name here as its very first act, so the
	// file counts SPAWNS rather than survivors. That distinction is the whole
	// assertion: d.ext is keyed by name, so a second copy that spawned anyway
	// would overwrite the first in the map and still leave Count() == 1, while
	// its subprocess ran on untracked. Counting tracked extensions cannot see
	// that; counting starts can.
	marker := filepath.Join(tmp, "spawns.txt")
	// Skips on windows (/bin/sh), like every other spawn test here.
	writeSpawnableExtension(t, filepath.Join(tmp, "extensions"), "alpha", "collide", marker)
	writeSpawnableExtension(t, filepath.Join(tmp, "extensions"), "beta", "collide", marker)

	mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	stop := sync.OnceFunc(func() { mgr.Stop(2 * time.Second) })
	defer stop()
	errs := mgr.Discover(context.Background())

	if len(errs) != 0 {
		joined := ""
		for _, e := range errs {
			joined += "\n  " + e.Error()
		}
		t.Fatalf("dropping the duplicate name must be SILENT, got %d error(s):%s\n"+
			"update the 'Layout & discovery' section of docs/extensions.md if this is "+
			"now meant to report.", len(errs), joined)
	}
	if c := mgr.Count(); c != 1 {
		t.Fatalf("tracked extensions = %d, want 1; if 2 the manifest-name claim no longer "+
			"dedups, if 0 discovery no longer queues both directories. Either way update "+
			"the 'Layout & discovery' section of docs/extensions.md.", c)
	}
	for _, e := range mgr.All() {
		if e.Manifest.Name != "collide" {
			t.Errorf("survivor is named %q; want %q", e.Manifest.Name, "collide")
		}
	}

	// Stop first: once it returns, anything that was going to start has
	// started and written its line, so the file is final and reading it is
	// not a race of its own.
	mgr.WaitForReady(testsupport.ExtReadyGrace)
	stop()
	started, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("no copy spawned at all: %v", err)
	}
	if lines := strings.Fields(string(started)); len(lines) != 1 {
		t.Errorf("%d copies spawned (%v); want exactly 1 — the dropped copy must never "+
			"reach exec, or its subprocess runs untracked", len(lines), lines)
	}
}

// writeSpawnableExtension writes an extension that actually starts and speaks
// hello, in directory `dir` but claiming manifest name `name` — the two kept
// apart so a collision can be built from differently-named directories.
func writeSpawnableExtension(t *testing.T, root, dir, name, marker string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("spawnable extension uses /bin/sh; skip on windows")
	}
	d := filepath.Join(root, dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	// Records the spawn, says hello, then blocks on stdin until the manager
	// closes it. Staying alive is the point: a process that exited would free
	// the name claim and reintroduce the race this test exists to remove.
	// The append is one short line, so O_APPEND keeps two racing copies from
	// interleaving into a single mangled one.
	script := "#!/bin/sh\n" +
		"printf '%s\\n' '" + dir + "' >> '" + marker + "'\n" +
		`printf '%s\n' '{"type":"hello","name":"` + name + `","version":"0.0.1","capabilities":[]}'` + "\n" +
		"while IFS= read -r line; do\n" +
		"  case \"$line\" in\n" +
		"    *'\"type\":\"shutdown\"'*) printf '%s\\n' '{\"type\":\"shutdown_ack\"}'; exit 0 ;;\n" +
		"  esac\n" +
		"done\n"
	if err := os.WriteFile(filepath.Join(d, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"` + name + `","version":"0.0.1","description":"x","exec":"./run.sh"}`
	if err := os.WriteFile(filepath.Join(d, "extension.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}
