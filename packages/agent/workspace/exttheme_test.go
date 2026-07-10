package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/testsupport"
)

// TestExtensionThemeOptions covers the theme-option conversion that moved
// out of extensions into the tui-aware host: a theme-only extension's
// bundled theme.json becomes one tui.ThemeOption, attributed to the
// extension. (The wire layer stays theme-agnostic; it only exposes the
// extension's dir + name.)
func TestExtensionThemeOptions(t *testing.T) {
	tmp := testsupport.TempDir(t)
	extDir := filepath.Join(tmp, "extensions", "theme-only")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "extension.json"),
		[]byte(`{"name":"theme-only","version":"1.0.0","description":"theme only"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "theme.json"),
		[]byte(`{"name":"Theme Only","description":"theme from extension","colors":{"dark":{"accent":204}}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	mgr := extensions.New(tmp, "", "0.0.0-test", "anthropic", "claude-opus-4-7", nil)
	if errs := mgr.Discover(context.Background()); len(errs) > 0 {
		t.Fatalf("discover errors: %v", errs)
	}
	defer mgr.Stop(10 * time.Millisecond)

	opts := extensionThemeOptions(mgr)
	if len(opts) != 1 {
		t.Fatalf("theme options = %d, want 1", len(opts))
	}
	if opts[0].Label != "Theme Only" || opts[0].Path != filepath.Join(extDir, "theme.json") {
		t.Fatalf("unexpected theme option: %#v", opts[0])
	}
	if !strings.Contains(opts[0].Description, "from extension theme-only") {
		t.Fatalf("description missing extension source: %q", opts[0].Description)
	}
}

// TestExtensionThemeOptionsNilManager guards the callback's nil path so a
// run without an extension manager doesn't panic the theme picker.
func TestExtensionThemeOptionsNilManager(t *testing.T) {
	if opts := extensionThemeOptions(nil); opts != nil {
		t.Fatalf("nil manager should yield no options, got %#v", opts)
	}
}
