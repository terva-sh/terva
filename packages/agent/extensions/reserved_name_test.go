package extensions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// TestReservedExtensionNameLockstep pins the local literals against the core
// tool-group namespace they mirror — this package must not import core in
// production code (TestExtensionsDependencyBoundary), so the constants are
// duplicated and this test keeps them honest.
func TestReservedExtensionNameLockstep(t *testing.T) {
	if reservedExtensionName(core.CoreToolGroup) == "" {
		t.Errorf("core.CoreToolGroup (%q) must be a reserved extension name", core.CoreToolGroup)
	}
	// An MCP server's tool group is "mcp:<server>" (see build's MCP wiring);
	// the prefix must stay reserved.
	if reservedExtensionName("mcp:github") == "" {
		t.Error(`"mcp:github" must be a reserved extension name`)
	}
	for _, ok := range []string{"mail", "mcpish", "my-ext", "tasks2"} {
		if why := reservedExtensionName(ok); why != "" {
			t.Errorf("%q should be allowed, got: %s", ok, why)
		}
	}
}

// TestDiscoverRejectsReservedManifestName: an extension whose manifest names
// itself "core" would land its tools in the always-visible built-in group and
// bypass lazy hiding; an "mcp:"-prefixed name would share an activation group
// with a real MCP server. Both must be refused at load, before any tool is
// registered. Path-safe but semantically reserved — the sibling guard to
// TestDiscoverRejectsUnsafeManifestName.
func TestDiscoverRejectsReservedManifestName(t *testing.T) {
	for _, name := range []string{"core", "mcp:github"} {
		t.Run(name, func(t *testing.T) {
			tmp := testsupport.TempDir(t)
			extDir := filepath.Join(tmp, "extensions", "squatter")
			if err := os.MkdirAll(extDir, 0o755); err != nil {
				t.Fatal(err)
			}
			manifest := `{"name":"` + name + `","version":"1.0.0","description":"x","exec":"./run.sh"}`
			if err := os.WriteFile(filepath.Join(extDir, "extension.json"), []byte(manifest), 0o644); err != nil {
				t.Fatal(err)
			}

			mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-opus-4-7", nil)
			errs := mgr.Discover(context.Background())
			defer mgr.Stop(10 * time.Millisecond)

			if len(errs) == 0 {
				t.Fatalf("expected a discover error for reserved name %q, got none", name)
			}
			joined := ""
			for _, e := range errs {
				joined += e.Error() + "\n"
			}
			if !strings.Contains(joined, "reserved") {
				t.Errorf("discover error should say the name is reserved; got:\n%s", joined)
			}
			if len(mgr.All()) != 0 {
				t.Fatalf("reserved-named extension was loaded despite the guard")
			}
		})
	}
}
