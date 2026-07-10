package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/testsupport"
)

func writeCtxFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadStartupContextFilesOrderAndDedup(t *testing.T) {
	dir := testsupport.TempDir(t)
	writeCtxFile(t, filepath.Join(dir, "a.md"), "AAA")
	writeCtxFile(t, filepath.Join(dir, "b.md"), "BBB")

	// config provides a.md; flags provide b.md then a.md again (a dup).
	out, err := readStartupContextFiles(dir,
		[]string{filepath.Join(dir, "a.md")},
		[]string{"b.md", "a.md"})
	if err != nil {
		t.Fatal(err)
	}
	ia, ib := strings.Index(out, "AAA"), strings.Index(out, "BBB")
	if ia < 0 || ib < 0 {
		t.Fatalf("missing content:\n%s", out)
	}
	if ia > ib {
		t.Fatalf("config file should precede flag file:\n%s", out)
	}
	if n := strings.Count(out, "AAA"); n != 1 {
		t.Fatalf("duplicate not deduped: AAA appears %d times", n)
	}
}

func TestReadStartupContextFilesRelativeFlagResolvesAgainstCwd(t *testing.T) {
	dir := testsupport.TempDir(t)
	writeCtxFile(t, filepath.Join(dir, "sub", "c.md"), "CCC")
	out, err := readStartupContextFiles(dir, nil, []string{"sub/c.md"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "CCC") {
		t.Fatalf("relative flag not loaded:\n%s", out)
	}
	if !strings.Contains(out, filepath.Join(dir, "sub", "c.md")) {
		t.Fatalf("expected absolute path heading:\n%s", out)
	}
}

func TestReadStartupContextFilesMissingErrors(t *testing.T) {
	dir := testsupport.TempDir(t)
	_, err := readStartupContextFiles(dir, nil, []string{"nope.md"})
	if err == nil {
		t.Fatal("expected error for missing context file")
	}
}

func TestReadStartupContextFilesDirectoryErrors(t *testing.T) {
	dir := testsupport.TempDir(t)
	sub := filepath.Join(dir, "adir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := readStartupContextFiles(dir, []string{sub}, nil)
	if err == nil {
		t.Fatal("expected error when a context file is a directory")
	}
}

func TestReadStartupContextFilesEmptySkipped(t *testing.T) {
	dir := testsupport.TempDir(t)
	writeCtxFile(t, filepath.Join(dir, "blank.md"), "   \n\t  ")
	out, err := readStartupContextFiles(dir, []string{filepath.Join(dir, "blank.md")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Fatalf("whitespace-only file should be skipped, got:\n%s", out)
	}
}

func TestReadStartupContextFilesOversizeErrors(t *testing.T) {
	dir := testsupport.TempDir(t)
	big := filepath.Join(dir, "big.md")
	writeCtxFile(t, big, strings.Repeat("x", maxContextFileBytes+1))
	_, err := readStartupContextFiles(dir, []string{big}, nil)
	if err == nil {
		t.Fatal("expected error for oversize context file")
	}
}

// TestReadStartupContextFilesFlagAbsolutePathAccepted: the --context-file flag
// is user-supplied and TRUSTED, so an absolute path passed via flagFiles is
// loaded normally — the project-layer containment check does not touch it.
func TestReadStartupContextFilesFlagAbsolutePathAccepted(t *testing.T) {
	cwd := testsupport.TempDir(t)
	outside := filepath.Join(testsupport.TempDir(t), "team-rules.md")
	writeCtxFile(t, outside, "RULES")

	out, err := readStartupContextFiles(cwd, nil, []string{outside})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "RULES") {
		t.Fatalf("absolute --context-file not loaded:\n%s", out)
	}
	if !strings.Contains(out, outside) {
		t.Fatalf("expected absolute path heading for flag file:\n%s", out)
	}
}

func TestReadStartupContextFilesNoneReturnsEmpty(t *testing.T) {
	out, err := readStartupContextFiles(testsupport.TempDir(t), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Fatalf("expected empty, got:\n%s", out)
	}
}

// Config-layer context_files are ambient coding context: like AGENTS.md
// (whose gate drops its user-global layer too), they must stay out of
// chat/play. Only an explicit per-run --context-file injects there — running
// a roleplay inside a repo must not narrate the repo's architecture notes
// into the scene.
func TestResolve_ConfigContextFilesGatedInImmersive(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("OPENAI_API_KEY", "test-key")
	writeCtxFile(t, filepath.Join(home, "preamble.md"), "Coding house rules.")
	if err := config.SaveConfig(config.Config{Provider: "openai", Model: "gpt-5", ContextFiles: []string{"preamble.md"}}); err != nil {
		t.Fatal(err)
	}
	dir := testsupport.TempDir(t)

	hasCtx := func(t *testing.T, args Args) bool {
		t.Helper()
		args.CWD = dir
		r, err := Resolve(args, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range r.SystemSegments {
			if s.Source == "context-files" {
				return true
			}
		}
		return false
	}

	if !hasCtx(t, Args{}) {
		t.Error("coding mode should inject config context_files")
	}
	if hasCtx(t, Args{Experience: ExperienceChat}) {
		t.Error("--chat must not inject config context_files (ambient coding context)")
	}
	if hasCtx(t, Args{Experience: ExperiencePlay}) {
		t.Error("--play must not inject config context_files")
	}

	// An explicit per-run file is deliberate and injects in every mode.
	note := filepath.Join(dir, "notes.md")
	writeCtxFile(t, note, "Meeting notes to talk through.")
	if !hasCtx(t, Args{Experience: ExperienceChat, ContextFiles: []string{note}}) {
		t.Error("--chat --context-file should still inject the explicit file")
	}
}
