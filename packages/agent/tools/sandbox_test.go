package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSandboxLockedBlocksOutside(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "a.txt")
	os.WriteFile(outsideFile, []byte("secret"), 0o644)

	sb := NewSandbox(root)
	sb.Lock()

	if err := sb.CheckPath(outsideFile); err == nil {
		t.Fatal("expected outside path to be blocked")
	}
	inside := filepath.Join(root, "ok.txt")
	if err := sb.CheckPath(inside); err != nil {
		t.Fatalf("inside path blocked unexpectedly: %v", err)
	}
}

func TestSandboxUnlockedAllows(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	sb := NewSandbox(root)
	if err := sb.CheckPath(filepath.Join(outside, "a.txt")); err != nil {
		t.Fatalf("unlocked should allow: %v", err)
	}
}

func TestSandboxCommandBanned(t *testing.T) {
	sb := NewSandbox(t.TempDir())
	sb.Lock()
	cases := []string{
		"sudo apt-get install foo",
		"rm -rf /",
		"cd /etc && ls",
		"cd .. && rm foo",
	}
	for _, c := range cases {
		if err := sb.CheckCommand(c); err == nil {
			t.Fatalf("expected %q to be banned", c)
		}
	}
	// Allowed:
	for _, c := range []string{"ls", "go test ./...", "cd subdir && ls"} {
		if err := sb.CheckCommand(c); err != nil {
			t.Fatalf("expected %q to be allowed: %v", c, err)
		}
	}
}

// TestSandboxAllowsCDIntoSubdir is the regression for the false-positive
// jail error: a `cd` into a subdirectory of the sandbox root, spelled as
// an absolute path, must be allowed. The old guard rejected any `cd /...`
// outright, which wasted turns and nudged the model toward breaking out.
func TestSandboxAllowsCDIntoSubdir(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "packages", "provider")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	sb := NewSandbox(root)
	sb.Lock()

	allowed := []string{
		"cd " + sub + " && go build ./...",
		"cd " + root + " && go build ./...",
		"cd " + root, // bare cd to root
		"cd packages/provider && go build",
		"cd \"" + sub + "\" && ls", // quoted absolute path
	}
	for _, c := range allowed {
		if err := sb.CheckCommand(c); err != nil {
			t.Fatalf("expected %q to be allowed: %v", c, err)
		}
	}

	blocked := []string{
		"cd /etc",
		"cd / && ls",
		"cd ..",                    // parent of root escapes
		"cd " + filepath.Dir(root), // explicit parent
	}
	for _, c := range blocked {
		if err := sb.CheckCommand(c); err == nil {
			t.Fatalf("expected %q to be blocked", c)
		}
	}
}

// TestSandboxDisplayPath: tool results / errors should present paths
// relative to the sandbox root when jailed, so the model isn't biased
// toward absolute paths.
func TestSandboxDisplayPath(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "pkg", "foo.go")
	outside := filepath.Join(t.TempDir(), "x.go")

	sb := NewSandbox(root)

	// Unlocked: returns the given form verbatim.
	if got := sb.DisplayPath(sub, "pkg/foo.go"); got != "pkg/foo.go" {
		t.Fatalf("unlocked DisplayPath = %q; want verbatim", got)
	}

	sb.Lock()
	if got := sb.DisplayPath(sub, sub); got != "./pkg/foo.go" {
		t.Fatalf("DisplayPath(abs inside) = %q; want ./pkg/foo.go", got)
	}
	if got := sb.DisplayPath(root, root); got != "." {
		t.Fatalf("DisplayPath(root) = %q; want .", got)
	}
	// Outside root: fall back to the given form (don't fabricate a path).
	if got := sb.DisplayPath(outside, "x.go"); got != "x.go" {
		t.Fatalf("DisplayPath(outside) = %q; want given fallback", got)
	}
}

func TestReadToolRejectsOutsideWhenLocked(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "a.txt")
	os.WriteFile(outsideFile, []byte("x"), 0o644)

	sb := NewSandbox(root)
	sb.Lock()
	tool := &ReadTool{CWD: root, Sandbox: sb}

	_, err := tool.Execute(context.Background(),
		mustJSONRaw(t, map[string]any{"path": outsideFile}), nil)
	if err == nil {
		t.Fatal("expected sandbox error")
	}
}

// A read-only root is readable by the read-side check but never
// writable, and paths outside every root are still blocked for reads.
func TestSandboxReadOnlyRoot(t *testing.T) {
	root := t.TempDir()
	docs := t.TempDir()
	docFile := filepath.Join(docs, "tui.md")
	os.WriteFile(docFile, []byte("# docs"), 0o644)
	outside := t.TempDir()

	sb := NewSandbox(root)
	sb.AddReadOnlyRoot(docs)
	sb.Lock()

	if err := sb.CheckPathRead(docFile); err != nil {
		t.Errorf("read-only root should be readable: %v", err)
	}
	if err := sb.CheckPath(docFile); err == nil {
		t.Error("read-only root must NOT be writable")
	}
	if err := sb.CheckPathRead(filepath.Join(outside, "x")); err == nil {
		t.Error("path outside all roots should be blocked even for reads")
	}
	inside := filepath.Join(root, "f.txt")
	if err := sb.CheckPathRead(inside); err != nil {
		t.Errorf("jail root should be readable: %v", err)
	}
	if err := sb.CheckPath(inside); err != nil {
		t.Errorf("jail root should be writable: %v", err)
	}
}

// End-to-end: the read tool can read a file in a read-only root while
// jailed — the property that makes the shipped docs usable under /jail.
func TestReadToolReadsReadOnlyRootWhenLocked(t *testing.T) {
	root := t.TempDir()
	docs := t.TempDir()
	docFile := filepath.Join(docs, "rpc.md")
	os.WriteFile(docFile, []byte("rpc docs body"), 0o644)

	sb := NewSandbox(root)
	sb.AddReadOnlyRoot(docs)
	sb.Lock()
	tool := &ReadTool{CWD: root, Sandbox: sb}

	if _, err := tool.Execute(context.Background(),
		mustJSONRaw(t, map[string]any{"path": docFile}), nil); err != nil {
		t.Fatalf("jailed read of a read-only root should succeed: %v", err)
	}
}

// A read-only glob exposes only matching files DIRECTLY inside its dir;
// non-matching files, the dir itself, and nested matches stay blocked,
// and matches are never writable.
func TestSandboxReadOnlyGlob(t *testing.T) {
	root := t.TempDir()
	logs := t.TempDir()
	mk := func(name string) string {
		p := filepath.Join(logs, name)
		os.WriteFile(p, []byte("x"), 0o644)
		return p
	}
	extLog := mk("ext-memory.log")
	botLog := mk("bot.log")
	mcpLog := mk("mcp-foo.log")

	sb := NewSandbox(root)
	sb.AddReadOnlyGlob(logs, "ext-*.log")
	sb.Lock()

	if err := sb.CheckPathRead(extLog); err != nil {
		t.Errorf("ext-*.log should be readable: %v", err)
	}
	for _, bad := range []string{botLog, mcpLog} {
		if err := sb.CheckPathRead(bad); err == nil {
			t.Errorf("%s should be blocked (not ext-*.log)", filepath.Base(bad))
		}
	}
	// The dir itself isn't a root, so grep/glob across logs/ is denied.
	if err := sb.CheckPathRead(logs); err == nil {
		t.Error("the logs dir itself should not be readable (no grep across it)")
	}
	// Non-recursive: a matching name in a subdir doesn't qualify.
	sub := filepath.Join(logs, "sub")
	os.MkdirAll(sub, 0o755)
	if err := sb.CheckPathRead(filepath.Join(sub, "ext-x.log")); err == nil {
		t.Error("glob is non-recursive; nested file should be blocked")
	}
	// Read-only: a match is never writable.
	if err := sb.CheckPath(extLog); err == nil {
		t.Error("read-only glob match must not be writable")
	}
}

func mustJSONRaw(t *testing.T, v any) []byte {
	t.Helper()
	return mustJSON(t, v)
}
