package external

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/chat"
)

// writeHelperManifest writes a connector.json whose exec is the test
// binary in helper mode (see TestHelperConnector).
func writeHelperManifest(t *testing.T, dir, name, mode string) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return writeManifestJSON(t, dir, map[string]any{
		"name":    name,
		"version": "9.9.9",
		"exec":    exe,
		"args":    []string{"-test.run=^TestHelperConnector$", mode},
	})
}

func writeManifestJSON(t *testing.T, dir string, m map[string]any) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "connector.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestServiceConfiguredProbe(t *testing.T) {
	tervaHome := t.TempDir()

	yes := writeHelperManifest(t, t.TempDir(), "fake", "happy")
	svc, err := NewService(yes, false)
	if err != nil {
		t.Fatal(err)
	}
	if !svc.Configured(tervaHome) {
		t.Error("configured probe exit 0 should report configured")
	}

	no := writeHelperManifest(t, t.TempDir(), "fake", "configured-no")
	svc, err = NewService(no, false)
	if err != nil {
		t.Fatal(err)
	}
	if svc.Configured(tervaHome) {
		t.Error("configured probe exit 1 should report unconfigured")
	}
}

func TestServiceStatusText(t *testing.T) {
	tervaHome := t.TempDir()
	path := writeHelperManifest(t, t.TempDir(), "fake", "happy")
	svc, err := NewService(path, true)
	if err != nil {
		t.Fatal(err)
	}
	text, err := svc.StatusText(tervaHome)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"fake connector (external, dev)", "fake status line", "unpaired"} {
		if !strings.Contains(text, want) {
			t.Errorf("status text missing %q:\n%s", want, text)
		}
	}

	if err := savePairing(tervaHome, "fake", "42"); err != nil {
		t.Fatal(err)
	}
	text, _ = svc.StatusText(tervaHome)
	if !strings.Contains(text, "user id 42") {
		t.Errorf("status text missing pairing:\n%s", text)
	}
}

func TestServicePairingRoundTrip(t *testing.T) {
	tervaHome := t.TempDir()
	path := writeHelperManifest(t, t.TempDir(), "fake", "happy")
	svc, err := NewService(path, false)
	if err != nil {
		t.Fatal(err)
	}
	_, pairing, err := svc.NewConnector(tervaHome, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pairing.AllowedUserID != "" {
		t.Errorf("fresh pairing = %q, want unpaired", pairing.AllowedUserID)
	}
	if err := pairing.Save("99"); err != nil {
		t.Fatal(err)
	}
	_, pairing, err = svc.NewConnector(tervaHome, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pairing.AllowedUserID != "99" {
		t.Errorf("persisted pairing = %q, want 99", pairing.AllowedUserID)
	}
}

func TestLinkAndReset(t *testing.T) {
	tervaHome := t.TempDir()
	devPath := writeHelperManifest(t, t.TempDir(), "fake", "happy")

	linked, err := Link(tervaHome, devPath)
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	if linked != ManifestPath(tervaHome, "fake") {
		t.Errorf("link path = %s", linked)
	}
	if target, err := os.Readlink(linked); err != nil || target != devPath {
		t.Errorf("readlink = %s, %v; want %s", target, err, devPath)
	}

	if _, err := Link(tervaHome, devPath); err == nil || !strings.Contains(err.Error(), "already installed") {
		t.Errorf("second Link err = %v, want already installed", err)
	}

	// Reset must clear pairing AND remove the symlink (it's the
	// uninstall verb for linked connectors)...
	if err := savePairing(tervaHome, "fake", "42"); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(linked, false)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := svc.Reset(tervaHome)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(removed, "pairing.json") || !strings.Contains(removed, "connector.json") {
		t.Errorf("removed = %q, want pairing + link", removed)
	}
	if _, err := os.Lstat(linked); !os.IsNotExist(err) {
		t.Error("symlink still present after reset")
	}
	// ...but never touch the author's manifest.
	if _, err := os.Stat(devPath); err != nil {
		t.Errorf("reset must not delete the dev manifest: %v", err)
	}
}

func TestResetKeepsInstalledManifest(t *testing.T) {
	tervaHome := t.TempDir()
	// A real (non-symlink) install under $TERVA_HOME/connectors.
	dir := filepath.Join(ConnectorsDir(tervaHome), "fake")
	path := writeHelperManifest(t, dir, "fake", "happy")
	if err := savePairing(tervaHome, "fake", "42"); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(path, false)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := svc.Reset(tervaHome)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(removed, "connector.json") {
		t.Errorf("reset removed an installed manifest: %q", removed)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("installed manifest must survive reset: %v", err)
	}
	if loadPairing(tervaHome, "fake") != "" {
		t.Error("pairing must be cleared by reset")
	}
}

func TestDiscover(t *testing.T) {
	tervaHome := t.TempDir()
	exe, _ := os.Executable()

	// Good connector.
	writeHelperManifest(t, filepath.Join(ConnectorsDir(tervaHome), "alpha"), "alpha", "happy")
	// Manifest name disagrees with its directory: refused loudly.
	writeManifestJSON(t, filepath.Join(ConnectorsDir(tervaHome), "beta"),
		map[string]any{"name": "notbeta", "exec": exe})
	// Disabled: skipped quietly.
	writeManifestJSON(t, filepath.Join(ConnectorsDir(tervaHome), "gamma"),
		map[string]any{"name": "gamma", "exec": exe, "enabled": false})
	// Stray non-connector dir: ignored.
	if err := os.MkdirAll(filepath.Join(ConnectorsDir(tervaHome), "alpha", "data"), 0o755); err != nil {
		t.Fatal(err)
	}

	svcs, errs := Discover(tervaHome)
	if len(svcs) != 1 || svcs[0].Name != "alpha" {
		t.Errorf("discovered = %+v, want [alpha]", svcs)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "does not match directory") {
		t.Errorf("errs = %v, want one name-mismatch error", errs)
	}
}

func TestRegisterDiscoveredKeepsCompiledIn(t *testing.T) {
	tervaHome := t.TempDir()
	chat.Register(chat.Service{Name: "conflict-x", Configured: func(string) bool { return false }})
	writeHelperManifest(t, filepath.Join(ConnectorsDir(tervaHome), "conflict-x"), "conflict-x", "happy")

	errs := RegisterDiscovered(tervaHome)
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "compiled into terva") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a conflict error, got %v", errs)
	}
	svc, ok := chat.Lookup("conflict-x")
	if !ok || svc.Dev {
		t.Errorf("compiled-in service must win the conflict: %+v", svc)
	}
}

func TestRegisterManifestDev(t *testing.T) {
	path := writeHelperManifest(t, t.TempDir(), "devconn-x", "happy")
	name, err := RegisterManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if name != "devconn-x" {
		t.Errorf("name = %q", name)
	}
	svc, ok := chat.Lookup("devconn-x")
	if !ok || !svc.Dev {
		t.Errorf("dev service not registered: %+v, ok=%v", svc, ok)
	}
	// A dev connector outranks compiled-in defaults for this run:
	// passing --connector-manifest is explicit intent.
	if got := chat.DefaultServiceName(); got != "devconn-x" {
		t.Errorf("DefaultServiceName = %q, want the dev connector", got)
	}
}

func TestManifestValidation(t *testing.T) {
	dir := t.TempDir()
	for name, m := range map[string]map[string]any{
		"missing name": {"exec": "/bin/true"},
		"missing exec": {"name": "x"},
		"path in name": {"name": "a/b", "exec": "/bin/true"},
	} {
		path := writeManifestJSON(t, filepath.Join(dir, strings.ReplaceAll(name, " ", "-")), m)
		if _, _, err := LoadManifest(path); err == nil {
			t.Errorf("%s: LoadManifest accepted an invalid manifest", name)
		}
	}
}
