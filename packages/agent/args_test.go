package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func TestParseArgsCWDDefaultsToGetwd(t *testing.T) {
	a, err := ParseArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	if a.CWD != wd {
		t.Fatalf("default cwd = %q, want %q", a.CWD, wd)
	}
}

func TestParseArgsCWDAbsolutizesExistingDir(t *testing.T) {
	dir := testsupport.TempDir(t)
	// Drive a relative path through to confirm it's resolved to absolute.
	parent := filepath.Dir(dir)
	rel := filepath.Base(dir)
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(parent); err != nil {
		t.Fatal(err)
	}
	// Restore the original cwd so sibling tests in this package aren't
	// affected (Go runs them in the same process).
	t.Cleanup(func() { _ = os.Chdir(orig) })

	a, err := ParseArgs([]string{"--cwd", rel})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !filepath.IsAbs(a.CWD) {
		t.Fatalf("cwd not absolutized: %q", a.CWD)
	}
	// EvalSymlinks both sides: macOS /tmp is a symlink, so a raw string
	// compare would spuriously fail.
	got, _ := filepath.EvalSymlinks(a.CWD)
	want, _ := filepath.EvalSymlinks(dir)
	if got != want {
		t.Fatalf("cwd = %q, want %q", got, want)
	}
}

func TestParseArgsCWDRejectsMissingDir(t *testing.T) {
	missing := filepath.Join(testsupport.TempDir(t), "does-not-exist")
	_, err := ParseArgs([]string{"--cwd", missing})
	if err == nil {
		t.Fatal("expected error for missing --cwd dir, got nil")
	}
	if !strings.Contains(err.Error(), "--cwd") {
		t.Fatalf("error should mention --cwd: %v", err)
	}
}

func TestParseArgsContextFileRepeatablePreservesOrder(t *testing.T) {
	a, err := ParseArgs([]string{"--context-file", "a.md", "--context-file", "b.md"})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.ContextFiles) != 2 || a.ContextFiles[0] != "a.md" || a.ContextFiles[1] != "b.md" {
		t.Fatalf("context files = %v, want [a.md b.md]", a.ContextFiles)
	}
}

func TestParseArgsContextFileRequiresValue(t *testing.T) {
	_, err := ParseArgs([]string{"--context-file"})
	if err == nil {
		t.Fatal("expected error when --context-file has no value")
	}
}

func TestParseArgsCWDRejectsFile(t *testing.T) {
	f := filepath.Join(testsupport.TempDir(t), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ParseArgs([]string{"--cwd", f})
	if err == nil {
		t.Fatal("expected error for --cwd pointing at a file, got nil")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("error should say not a directory: %v", err)
	}
}

func TestParseArgsSwarmWorktreesFlag(t *testing.T) {
	// Absent: pointer stays nil so config decides.
	a, err := ParseArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.SwarmWorktrees != nil {
		t.Fatalf("SwarmWorktrees = %v; want nil when flag absent", *a.SwarmWorktrees)
	}
	// Present: pointer is non-nil and true.
	a, err = ParseArgs([]string{"--swarm-worktrees"})
	if err != nil {
		t.Fatal(err)
	}
	if a.SwarmWorktrees == nil || !*a.SwarmWorktrees {
		t.Fatalf("SwarmWorktrees = %v; want a non-nil true", a.SwarmWorktrees)
	}
}

func TestResolveSwarmWorktrees(t *testing.T) {
	tru := true
	fls := false
	cases := []struct {
		name      string
		flag, cfg *bool
		want      bool
	}{
		{"both nil -> off", nil, nil, false},
		{"config on, no flag", nil, &tru, true},
		{"config off, no flag", nil, &fls, false},
		{"flag on overrides config off", &tru, &fls, true},
		{"flag off overrides config on", &fls, &tru, false},
		{"flag on, no config", &tru, nil, true},
	}
	for _, c := range cases {
		if got := resolveSwarmWorktrees(c.flag, c.cfg); got != c.want {
			t.Errorf("%s: got %v; want %v", c.name, got, c.want)
		}
	}
}

func TestParseArgsTemperatureAllowsZero(t *testing.T) {
	args, err := ParseArgs([]string{"--temperature", "0"})
	if err != nil {
		t.Fatalf("ParseArgs returned %v", err)
	}
	if args.Temperature == nil || *args.Temperature != 0 {
		t.Fatalf("Temperature = %v; want 0", args.Temperature)
	}
}

func TestParseArgsTemperatureRejectsOutOfRange(t *testing.T) {
	if _, err := ParseArgs([]string{"--temperature", "2.1"}); err == nil {
		t.Fatal("ParseArgs accepted out-of-range temperature")
	}
}

func TestParseArgsExtensionsAndMCPAllowlists(t *testing.T) {
	a, err := ParseArgs([]string{
		"--extensions", "calendar, index",
		"--extensions", "memory",
		"--mcp", "git,jira ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.WithExtensions) != 3 || a.WithExtensions[0] != "calendar" ||
		a.WithExtensions[1] != "index" || a.WithExtensions[2] != "memory" {
		t.Fatalf("WithExtensions = %v, want [calendar index memory]", a.WithExtensions)
	}
	if len(a.WithMCP) != 2 || a.WithMCP[0] != "git" || a.WithMCP[1] != "jira" {
		t.Fatalf("WithMCP = %v, want [git jira]", a.WithMCP)
	}
	// Both flags require a value.
	if _, err := ParseArgs([]string{"--extensions"}); err == nil {
		t.Error("--extensions without a value should error")
	}
	if _, err := ParseArgs([]string{"--mcp"}); err == nil {
		t.Error("--mcp without a value should error")
	}
}
