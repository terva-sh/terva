package extensions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// writeProjectShellExt drops a shell-script extension under
// cwd/.terva/extensions/<name>. On spawn the script touches sentinelPath
// so a test can prove whether the subprocess ran at all (the headline
// RCE concern: an untrusted repo's extension must NEVER be spawned).
// Returns the project cwd to hand to New as the cwd argument.
func writeProjectShellExt(t *testing.T, name, sentinelPath string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script extension; skip on windows")
	}
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".terva", "extensions", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	hello := `{"type":"hello","name":"` + name + `","version":"1.0","capabilities":["commands"]}`
	// touch the sentinel FIRST thing (proves the process started), then
	// behave like a normal extension.
	body := `printf '%s\n' '{"type":"register_command","name":"projcmd"}'
printf '%s\n' '{"type":"ready"}'
while IFS= read -r line; do
  case "$line" in *'"type":"shutdown"'*) exit 0;; esac
done
`
	script := "#!/bin/sh\ntouch '" + sentinelPath + "'\nprintf '%s\\n' '" + hello + "'\n" + body
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	mfb, _ := json.Marshal(map[string]any{"name": name, "exec": "./run.sh"})
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), mfb, 0o644); err != nil {
		t.Fatal(err)
	}
	return cwd
}

// The headline RCE fix: an UNTRUSTED workspace's project-local extension
// is never discovered or spawned — no command registers and the spawn
// sentinel is never written. The control case (same dir, trusted) loads
// and spawns it.
func TestProjectExtensionGatedOnTrust(t *testing.T) {
	sentinel := filepath.Join(t.TempDir(), "spawned")
	cwd := writeProjectShellExt(t, "evil-proj-ext", sentinel)
	home := t.TempDir() // global extensions dir (empty here)

	// Untrusted (default): the manager must NOT search the project dir.
	mgr := New(home, cwd, "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	// SetProjectTrusted not called ⇒ default false (restricted).
	if errs := mgr.Discover(context.Background()); len(errs) > 0 {
		t.Fatalf("untrusted discover: %v", errs)
	}
	mgr.WaitForReady(500 * time.Millisecond)
	mgr.Stop(2 * time.Second)
	if mgr.HasCommand("projcmd") {
		t.Error("untrusted workspace spawned its project extension (command registered) — RCE gap")
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Error("untrusted workspace spawned its project extension (sentinel written) — RCE gap")
	}

	// Trusted control: SetProjectTrusted(true) before Discover loads it.
	sentinel2 := filepath.Join(t.TempDir(), "spawned2")
	cwd2 := writeProjectShellExt(t, "evil-proj-ext", sentinel2)
	mgr2 := New(home, cwd2, "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	mgr2.SetProjectTrusted(true)
	if errs := mgr2.Discover(context.Background()); len(errs) > 0 {
		t.Fatalf("trusted discover: %v", errs)
	}
	defer mgr2.Stop(2 * time.Second)
	mgr2.WaitForReady(time.Second)
	if !mgr2.HasCommand("projcmd") {
		t.Error("trusted workspace should spawn its project extension")
	}
	if _, err := os.Stat(sentinel2); err != nil {
		t.Errorf("trusted workspace should have spawned the extension (sentinel missing): %v", err)
	}
}

// User/global extensions ($TERVA_HOME/extensions) load regardless of the
// project's trust verdict — they are code the user installed themselves.
func TestGlobalExtensionLoadsUntrusted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script extension; skip on windows")
	}
	home := t.TempDir()
	gdir := filepath.Join(home, "extensions", "global-ext")
	if err := os.MkdirAll(gdir, 0o755); err != nil {
		t.Fatal(err)
	}
	hello := `{"type":"hello","name":"global-ext","version":"1.0","capabilities":["commands"]}`
	body := `printf '%s\n' '{"type":"register_command","name":"globalcmd"}'
printf '%s\n' '{"type":"ready"}'
while IFS= read -r line; do
  case "$line" in *'"type":"shutdown"'*) exit 0;; esac
done
`
	script := "#!/bin/sh\nprintf '%s\\n' '" + hello + "'\n" + body
	if err := os.WriteFile(filepath.Join(gdir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	mfb, _ := json.Marshal(map[string]any{"name": "global-ext", "exec": "./run.sh"})
	if err := os.WriteFile(filepath.Join(gdir, "extension.json"), mfb, 0o644); err != nil {
		t.Fatal(err)
	}

	// Untrusted project cwd, but the global extension must still load.
	mgr := New(home, t.TempDir(), "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	// trust left at the default (false / restricted)
	if errs := mgr.Discover(context.Background()); len(errs) > 0 {
		t.Fatalf("discover: %v", errs)
	}
	defer mgr.Stop(2 * time.Second)
	mgr.WaitForReady(time.Second)
	if !mgr.HasCommand("globalcmd") {
		t.Error("global ($TERVA_HOME) extension should load even in an untrusted workspace")
	}
}
