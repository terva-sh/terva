package extdriver_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestDriverDependencyBoundary locks the whole point of the extraction:
// extdriver is the authoritative wire and must stay dependency-light, so
// it can back a small, optionally-importable conformance tool. If it
// ever imports core / provider / tui, a conformance binary would balloon
// to terva's full size and the boundary that keeps the wire portable is
// gone. See docs/plans/extdriver-extraction.md.
func TestDriverDependencyBoundary(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "terva.sh/terva/packages/agent/extdriver").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	forbidden := []string{
		"terva.sh/terva/packages/core",
		"terva.sh/terva/packages/provider",
		"terva.sh/terva/packages/tui",
	}
	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		for _, bad := range forbidden {
			if dep == bad {
				t.Errorf("extdriver must not depend on %s — it pulls the wire driver out of dependency-light territory", bad)
			}
		}
	}
}
