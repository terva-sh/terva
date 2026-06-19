package extensions_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestExtensionsDependencyBoundary locks the phase 2-3 result of the
// extdriver extraction: the host integration layer no longer pulls the
// heavy core / provider / tui packages. The core.Tool wrapper moved to
// packages/agent/exttool and theme-option construction moved to the
// tui-aware host (agent.extensionThemeOptions), leaving extensions
// dependency-light over the wire driver. If a future change genuinely
// needs one of these here, update this test deliberately rather than
// letting the dependency creep back unnoticed. See
// docs/plans/extdriver-extraction.md.
func TestExtensionsDependencyBoundary(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "terva.sh/terva/packages/agent/extensions").Output()
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
				t.Errorf("extensions must not depend on %s — it was removed in the extdriver extraction (phases 2-3)", bad)
			}
		}
	}
}
