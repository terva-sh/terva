package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func TestParseArgsAdditionalDirectories(t *testing.T) {
	root := testsupport.TempDir(t)
	a, err := ParseArgs([]string{"--cwd", root, "--add-dir", root, "--add-dir", "."})
	if err != nil || len(a.AddDirs) != 2 || !filepath.IsAbs(a.AddDirs[1]) {
		t.Fatalf("additional directories: %+v, %v", a.AddDirs, err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, filepath.Join(root, "missing")} {
		if _, err := ParseArgs([]string{"--add-dir", path}); err == nil {
			t.Fatal("invalid additional directory accepted")
		}
	}
	if _, err := ParseArgs([]string{"--add-dir"}); err == nil {
		t.Fatal("missing directory value accepted")
	}
}

func TestResolveAdditionalDirectories(t *testing.T) {
	primary, peer, outside := testsupport.TempDir(t), testsupport.TempDir(t), testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "fixture-key")
	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5", CWD: primary, AddDirs: []string{peer}, Jail: true, NoExt: true, NoMCP: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Sandbox.CheckPath(filepath.Join(peer, "file")); err != nil {
		t.Fatal("Resolve lost grant", err)
	}
	if err := r.Sandbox.CheckPath(filepath.Join(outside, "file")); err == nil {
		t.Fatal("Resolve escaped grant")
	}
	if !strings.Contains(r.SystemPrompt, peer) {
		t.Fatal("model prompt does not name peer root")
	}
}
