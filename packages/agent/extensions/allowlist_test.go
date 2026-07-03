package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// writeShellExtAt writes a minimal shell extension registering one
// command into home/extensions/<name>, returning the extension's dir
// (so a test can also hand it to LoadExplicit as an --ext path).
func writeShellExtAt(t *testing.T, home, name, cmd string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script extension; skip on windows")
	}
	dir := filepath.Join(home, "extensions", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	hello := fmt.Sprintf(`{"type":"hello","name":"%s","version":"1.0","capabilities":["commands"]}`, name)
	script := "#!/bin/sh\nprintf '%s\\n' '" + hello + "'\n" +
		"printf '%s\\n' '{\"type\":\"register_command\",\"name\":\"" + cmd + "\"}'\n" +
		"printf '%s\\n' '{\"type\":\"ready\"}'\n" +
		"while IFS= read -r line; do case \"$line\" in *'\"type\":\"shutdown\"'*) exit 0;; esac; done\n"
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	mfb, _ := json.Marshal(map[string]any{"name": name, "exec": "./run.sh"})
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), mfb, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestSetAllowedExtensionsScopesDiscovery: with a --extensions
// allowlist only the named installed extensions load, while an
// explicit --ext path bypasses it (pointing at a directory is already
// explicit consent — the extension-development flow).
func TestSetAllowedExtensionsScopesDiscovery(t *testing.T) {
	home := testsupport.TempDir(t)
	writeShellExtAt(t, home, "calendar", "calcmd")
	writeShellExtAt(t, home, "mail", "mailcmd")
	devDir := writeShellExtAt(t, testsupport.TempDir(t), "devtool", "devcmd")

	mgr := New(home, "", "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	mgr.SetAllowedExtensions([]string{"calendar"})
	if errs := mgr.LoadExplicit(context.Background(), []string{devDir}); len(errs) > 0 {
		t.Fatalf("load explicit: %v", errs)
	}
	if errs := mgr.Discover(context.Background()); len(errs) > 0 {
		t.Fatalf("discover: %v", errs)
	}
	defer mgr.Stop(2 * time.Second)
	mgr.WaitForReady(time.Second)

	if !mgr.HasCommand("calcmd") {
		t.Error("allowlisted extension failed to load")
	}
	if mgr.HasCommand("mailcmd") {
		t.Error("non-allowlisted extension loaded — the scoping leaked")
	}
	if !mgr.HasCommand("devcmd") {
		t.Error("explicit --ext path must bypass the allowlist")
	}
}

// TestAllowlistComposesWithDisableList: the allowlist is restrict-only —
// a name in both the allowlist and the disable list stays OFF.
func TestAllowlistComposesWithDisableList(t *testing.T) {
	home := testsupport.TempDir(t)
	writeShellExtAt(t, home, "calendar", "calcmd")
	writeShellExtAt(t, home, "mail", "mailcmd")

	mgr := New(home, "", "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	mgr.SetAllowedExtensions([]string{"calendar", "mail"})
	mgr.SetDisabledExtensions([]string{"mail"})
	if errs := mgr.Discover(context.Background()); len(errs) > 0 {
		t.Fatalf("discover: %v", errs)
	}
	defer mgr.Stop(2 * time.Second)
	mgr.WaitForReady(time.Second)

	if !mgr.HasCommand("calcmd") {
		t.Error("allowlisted+enabled extension failed to load")
	}
	if mgr.HasCommand("mailcmd") {
		t.Error("the disable list must still subtract inside the allowlist")
	}
}
