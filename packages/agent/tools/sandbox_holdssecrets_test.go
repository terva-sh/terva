package tools

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func TestHoldsSecrets(t *testing.T) {
	ws := testsupport.TempDir(t)
	secret := filepath.Join(ws, "cfg", "terva")
	if err := os.MkdirAll(secret, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	sb := NewSandbox(ws)
	sb.AddSecretRoot(secret)
	for path, want := range map[string]bool{
		ws:                            true,  // the workspace holds the root
		filepath.Join(ws, "cfg"):      true,  // the parent of the root
		secret:                        true,  // the root itself
		filepath.Join(secret, "x"):    false, // inside: CheckPathRead refuses it already
		filepath.Join(ws, "src"):      false,
		filepath.Join(ws, "cfgx"):     false, // a sibling that shares a prefix
		filepath.Join(ws, "new", "d"): false, // a path that does not exist yet
	} {
		if got := sb.HoldsSecrets(path); got != want {
			t.Errorf("HoldsSecrets(%s) = %v, want %v", path, got, want)
		}
	}
	var nilSB *Sandbox
	if nilSB.HoldsSecrets(ws) {
		t.Error("a nil Sandbox claims to hold secrets")
	}
}
