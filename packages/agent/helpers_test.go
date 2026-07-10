package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/testsupport"
)

// Test fixtures are per-package in Go; this mirrors the copy in
// packages/agent/build rather than exporting a helper for tests alone.

// withTempHome points TERVA_HOME at a temp dir for config isolation.
func withTempHome(t *testing.T) string {
	t.Helper()
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	return home
}

func writeUserConfig(t *testing.T, home string, c config.Config) {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}
