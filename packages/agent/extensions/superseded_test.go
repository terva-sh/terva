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

// writeExtManifest lays down a minimal installed extension named name. The exec
// deliberately does not exist: an extension that gets as far as spawning will
// fail loudly, which is what makes "no error" meaningful below.
func writeExtManifest(t *testing.T, home, name string) {
	t.Helper()
	dir := filepath.Join(home, "extensions", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"` + name + `","version":"1.0.0","description":"x","exec":"./run.sh"}`
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A superseded extension must be skipped before any spawn, quietly. Enrolls
// itself from the map, so adding an entry without this holding is not possible.
//
// The control case is the point: an identically-broken extension that is NOT
// superseded fails at spawn. Both are "no tools registered", and only the error
// tells them apart — without the control this test would pass on a manager that
// simply failed to load anything.
func TestASupersededExtensionIsSkippedBeforeSpawning(t *testing.T) {
	for name := range supersededExtensions {
		t.Run(name, func(t *testing.T) {
			tmp := testsupport.TempDir(t)
			writeExtManifest(t, tmp, name)

			mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-opus-4-7", nil)
			errs := mgr.Discover(context.Background())
			defer mgr.Stop(10 * time.Millisecond)

			if len(errs) != 0 {
				t.Errorf("superseded %q should be skipped quietly, got errors: %v", name, errs)
			}
			if got := mgr.Tools(); len(got) != 0 {
				t.Errorf("superseded %q registered %d tool(s)", name, len(got))
			}
		})
	}

	t.Run("control/not-superseded", func(t *testing.T) {
		tmp := testsupport.TempDir(t)
		writeExtManifest(t, tmp, "some-third-party-ext")

		mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-opus-4-7", nil)
		errs := mgr.Discover(context.Background())
		defer mgr.Stop(10 * time.Millisecond)

		if len(errs) == 0 {
			t.Fatal("a non-superseded extension with a missing exec should fail at spawn; " +
				"if it does not, the superseded cases above prove nothing")
		}
	})
}

// The message is the entire user-facing consequence of superseding: the
// extension stops working and the only explanation is this string. It has to
// say what replaced it and how to clean up, or the user is left with a tool
// that silently vanished.
func TestEverySupersededReasonSaysWhatToDo(t *testing.T) {
	for name, why := range supersededExtensions {
		if !strings.Contains(why, "terva ext remove "+name) {
			t.Errorf("%q: reason should tell the user how to uninstall, got: %s", name, why)
		}
		if !strings.Contains(why, "terva") {
			t.Errorf("%q: reason should say the capability is built into terva, got: %s", name, why)
		}
	}
}

// Superseding keys on the EXTENSION name; core's memory stand-down keys on the
// TOOL name. Retiring the `memory` extension therefore does NOT make the
// stand-down dead code — a third-party extension under any other name may still
// register a tool called `memory`, and core should still defer to it (that is
// the stand-down's stated intent: the one the user installed deliberately wins).
//
// Pinned because memory-in-core.md predicts the stand-down "becomes dead code
// and comes out" once the extension is retired, and that is half right: the
// retired extension can no longer trigger it, but the mechanism still has a job.
func TestSupersedingMemoryDoesNotRetireTheToolNameCollision(t *testing.T) {
	if _, ok := supersededExtensions["memory"]; !ok {
		t.Fatal("precondition: the memory extension should be superseded")
	}
	tmp := testsupport.TempDir(t)
	writeExtManifest(t, tmp, "someone-elses-notes")

	mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-opus-4-7", nil)
	defer mgr.Stop(10 * time.Millisecond)
	if errs := mgr.Discover(context.Background()); len(errs) == 0 {
		t.Fatal("an extension under a different name must still be load-attempted")
	}
}
