package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/testsupport"
)

// userNameResolved is Args-shaped policy and stays in agent; its test stays
// with it. The config package keeps only the tests for what it owns.

func writeProjectConfig(t *testing.T, dir, body string) {
	t.Helper()
	zdir := filepath.Join(dir, ".terva")
	if err := os.MkdirAll(zdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(zdir, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The interactive name prompt must honor the same precedence Resolve uses, so it
// never fires (and clobbers) when a trusted project's user_name is set.
func TestUserNameResolved_HonorsTrustedProjectAndGlobal(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t)) // fresh home: no global user_name
	repo := testsupport.TempDir(t)
	writeProjectConfig(t, repo, `{"user_name":"ProjectName"}`)

	// A trusted project's user_name makes the name resolvable (no prompt).
	if !userNameResolved(build.Args{CWD: repo, Trust: true}) {
		t.Error("trusted project user_name should count as resolved")
	}
	// Untrusted + no global: the project override is dropped, nothing resolves.
	if userNameResolved(build.Args{CWD: repo, Trust: false}) {
		t.Error("untrusted project user_name must NOT count as resolved")
	}
	// --as always resolves, regardless of trust/config.
	if !userNameResolved(build.Args{CWD: repo, As: "Ravi"}) {
		t.Error("--as should count as resolved")
	}

	// With a global user_name set, even an untrusted project resolves to it.
	if err := config.SaveConfig(config.Config{UserName: "GlobalName"}); err != nil {
		t.Fatal(err)
	}
	if !userNameResolved(build.Args{CWD: repo, Trust: false}) {
		t.Error("global user_name should count as resolved even when untrusted")
	}

	// No --as, no project user_name, no global → unresolved (would prompt).
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	bare := testsupport.TempDir(t)
	if userNameResolved(build.Args{CWD: bare, Trust: true}) {
		t.Error("no --as, no project, no global → must be unresolved (prompt)")
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
