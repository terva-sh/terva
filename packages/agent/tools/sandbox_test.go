package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func TestSandboxLockedBlocksOutside(t *testing.T) {
	root := testsupport.TempDir(t)
	outside := testsupport.TempDir(t)
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
	root := testsupport.TempDir(t)
	outside := testsupport.TempDir(t)
	sb := NewSandbox(root)
	if err := sb.CheckPath(filepath.Join(outside, "a.txt")); err != nil {
		t.Fatalf("unlocked should allow: %v", err)
	}
}

func TestSandboxCommandBanned(t *testing.T) {
	sb := NewSandbox(testsupport.TempDir(t))
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
	root := testsupport.TempDir(t)
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
	root := testsupport.TempDir(t)
	sub := filepath.Join(root, "pkg", "foo.go")
	outside := filepath.Join(testsupport.TempDir(t), "x.go")

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

// Reads are unconfined when jailed; writes are not. The read jail was
// withdrawn because bash was never path-jailed, so every refused read was one
// `cat` away — it confined nothing and cost turns.
func TestSandboxJailedReadsAreUnconfined(t *testing.T) {
	root := testsupport.TempDir(t)
	docs := testsupport.TempDir(t)
	docFile := filepath.Join(docs, "tui.md")
	os.WriteFile(docFile, []byte("# docs"), 0o644)
	outside := testsupport.TempDir(t)

	sb := NewSandbox(root)
	sb.Lock()

	for _, p := range []string{docFile, filepath.Join(outside, "x"), "/etc/hosts"} {
		if err := sb.CheckPathRead(p); err != nil {
			t.Errorf("CheckPathRead(%s) = %v, want reads unconfined", p, err)
		}
	}
	// The write jail is the half that still holds.
	if err := sb.CheckPath(docFile); err == nil {
		t.Error("a path outside the root must NOT be writable")
	}
	inside := filepath.Join(root, "f.txt")
	if err := sb.CheckPathRead(inside); err != nil {
		t.Errorf("jail root should be readable: %v", err)
	}
	if err := sb.CheckPath(inside); err != nil {
		t.Errorf("jail root should be writable: %v", err)
	}
}

// End-to-end through the tool: a jailed read reaches outside the root. This is
// the batch of eight refusals in B1 of the 2026-07-30 session-harness review,
// reduced to one call.
func TestReadToolReadsOutsideRootWhenLocked(t *testing.T) {
	root := testsupport.TempDir(t)
	docs := testsupport.TempDir(t)
	docFile := filepath.Join(docs, "rpc.md")
	os.WriteFile(docFile, []byte("rpc docs body"), 0o644)

	sb := NewSandbox(root)
	sb.Lock()
	tool := &ReadTool{CWD: root, Sandbox: sb}

	if _, err := tool.Execute(context.Background(),
		mustJSONRaw(t, map[string]any{"path": docFile}), nil); err != nil {
		t.Fatalf("jailed read outside the root should succeed: %v", err)
	}
}

// A secret root denies its whole tree; an exception carves back only matching
// files DIRECTLY inside it. Nested matches and non-matching siblings stay
// denied, and the denial binds bash as well as the file tools.
func TestSandboxSecretRootAndException(t *testing.T) {
	root := testsupport.TempDir(t)
	logs := testsupport.TempDir(t)
	mk := func(name string) string {
		p := filepath.Join(logs, name)
		os.WriteFile(p, []byte("x"), 0o644)
		return p
	}
	extLog := mk("ext-memory.log")
	botLog := mk("bot.log")
	mcpLog := mk("mcp-foo.log")

	sb := NewSandbox(root)
	sb.AddSecretRoot(logs)
	sb.AddSecretException(logs, "ext-*.log")
	sb.Lock()

	if err := sb.CheckPathRead(extLog); err != nil {
		t.Errorf("ext-*.log should be readable: %v", err)
	}
	for _, bad := range []string{botLog, mcpLog} {
		if err := sb.CheckPathRead(bad); err == nil {
			t.Errorf("%s should be denied (not ext-*.log)", filepath.Base(bad))
		}
	}
	// The dir itself is denied, so grep/glob cannot sweep it.
	if err := sb.CheckPathRead(logs); err == nil {
		t.Error("the secret dir itself should not be readable (no grep across it)")
	}
	// Non-recursive: a matching name in a subdir does not qualify.
	sub := filepath.Join(logs, "sub")
	os.MkdirAll(sub, 0o755)
	if err := sb.CheckPathRead(filepath.Join(sub, "ext-x.log")); err == nil {
		t.Error("the exception is non-recursive; a nested file should stay denied")
	}
	// Denied for writes too, exception or not.
	if err := sb.CheckPath(extLog); err == nil {
		t.Error("a secret-root file must not be writable")
	}
	// And bash sees the same denial.
	if err := sb.CheckCommand("cat " + botLog); err == nil {
		t.Error("bash must not read a secret-root file either")
	}
	if err := sb.CheckCommand("tail " + extLog); err != nil {
		t.Errorf("bash should reach the carved-out exception: %v", err)
	}
}

// A writable root is a narrow write grant outside the jail root: the granted
// directory (even one that does not exist yet — the first handoff creates it)
// accepts writes, its siblings stay jailed, and a symlink planted inside it
// cannot smuggle a write elsewhere because targets resolve before matching.
func TestSandboxWritableRootGrantsNarrowWrites(t *testing.T) {
	root := testsupport.TempDir(t)
	home := testsupport.TempDir(t)
	handoffs := filepath.Join(home, "handoffs")

	sb := NewSandbox(root)
	sb.AddWritableRoot(handoffs) // deliberately before the dir exists
	sb.Lock()

	if err := sb.CheckPath(filepath.Join(handoffs, "2026-08-02-x.md")); err != nil {
		t.Errorf("write into the granted dir should be allowed pre-creation: %v", err)
	}
	if err := sb.CheckPath(filepath.Join(handoffs, "nested", "y.md")); err != nil {
		t.Errorf("write into a granted subdir should be allowed: %v", err)
	}
	// The grant is the directory alone — siblings and the home root stay jailed.
	if err := sb.CheckPath(filepath.Join(home, "config.json")); err == nil {
		t.Error("a sibling of the granted dir must stay write-jailed")
	}
	if err := sb.CheckPath(home); err == nil {
		t.Error("the parent of the granted dir must stay write-jailed")
	}
	// A symlink inside the grant pointing outside resolves outside and is refused.
	outside := testsupport.TempDir(t)
	os.MkdirAll(handoffs, 0o755)
	link := filepath.Join(handoffs, "evil")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}
	if err := sb.CheckPath(filepath.Join(link, "z.md")); err == nil {
		t.Error("a symlink escape through the granted dir must be refused")
	}
	// Unlocked sandboxes are unchanged: everything allowed, grant or not.
	sb.Unlock()
	if err := sb.CheckPath(filepath.Join(home, "config.json")); err != nil {
		t.Errorf("unlocked write should be allowed: %v", err)
	}
}

// A writable root has to be WORKABLE, not merely writable. The write check
// honoured grants from the day they were added and the `cd` check never
// learned about them, so a granted directory accepted an edit and refused the
// build that would compile it. The agent that hit this did not stop at the
// refusal; it reached for `git -C`, `--prefix` and `sh -c 'cd …'` until
// something worked, which is the same write with the jail's reasoning removed.
func TestSandboxAllowsCDIntoWritableRoot(t *testing.T) {
	root := testsupport.TempDir(t)
	home := testsupport.TempDir(t)
	granted := filepath.Join(home, "worktrees")
	nested := filepath.Join(granted, "repo-abc", "worktrees", "feature")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	sb := NewSandbox(root)
	sb.AddWritableRoot(granted)
	sb.Lock()

	allowed := []string{
		"cd " + granted,
		"cd " + nested + " && go test ./...",
		"cd \"" + nested + "\" && npm test",
	}
	for _, c := range allowed {
		if err := sb.CheckCommand(c); err != nil {
			t.Errorf("expected %q to be allowed inside a writable grant: %v", c, err)
		}
	}

	// The grant is the granted tree alone. Its parent and siblings are not
	// part of it, and neither is the rest of the filesystem.
	blocked := []string{
		"cd " + home,
		"cd " + filepath.Join(home, "sessions"),
		"cd /etc",
	}
	for _, c := range blocked {
		if err := sb.CheckCommand(c); err == nil {
			t.Errorf("expected %q to stay jailed — the grant is one subtree, not its parent", c)
		}
	}
}

// The cd side must not become a way to launder a grant. A symlink planted
// inside a writable root resolves before it is matched, so it cannot carry the
// grant out to whatever it points at — the same rule the write side already
// held, now provably the same code path.
func TestSandboxCDCannotRideASymlinkOutOfAGrant(t *testing.T) {
	root := testsupport.TempDir(t)
	home := testsupport.TempDir(t)
	outside := testsupport.TempDir(t)
	granted := filepath.Join(home, "worktrees")
	if err := os.MkdirAll(granted, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(granted, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}

	sb := NewSandbox(root)
	sb.AddWritableRoot(granted)
	sb.Lock()

	if err := sb.CheckCommand("cd " + link); err == nil {
		t.Error("cd through a symlink out of a grant must be refused")
	}
	if err := sb.CheckPath(filepath.Join(link, "x.go")); err == nil {
		t.Error("a write through that same symlink must stay refused")
	}
}

func mustJSONRaw(t *testing.T, v any) []byte {
	t.Helper()
	return mustJSON(t, v)
}

// TestSandboxDestructiveTargetsMatchWholeArguments pins B2 of the 2026-07-30
// session-harness review. The banned list held the literal "rm -rf /", matched
// with strings.Contains, so it fired on `rm -rf /tmp/dol-upstream-analysis` —
// on every absolute path, in fact. `rm -rf ./x` passed and `rm -rf /tmp/x` did
// not, and the model simply switched to `mktemp -d` and deleted the same tree
// through a name the guard could not read.
func TestSandboxDestructiveTargetsMatchWholeArguments(t *testing.T) {
	sb := NewSandbox(testsupport.TempDir(t))
	sb.Lock()

	// Still banned: the target IS a root.
	for _, c := range []string{
		"rm -rf /",
		"rm -rf /*",
		"rm -rf ~",
		"rm -rf ~/",
		"rm -rf $HOME",
		"rm -fr /",
		"rm -r -f /",
		"rm --recursive --force /",
		"rm -rf '/'",
		`rm -rf "$HOME"`,
		"dd of=/dev/disk0 if=x",
	} {
		if err := sb.CheckCommand(c); err == nil {
			t.Errorf("expected %q to be banned", c)
		}
	}

	// Allowed: a path UNDER a root, which is the ordinary case the old
	// substring match broke.
	for _, c := range []string{
		"rm -rf /tmp/dol-upstream-analysis",
		"rm -rf /tmp/scratch && git clone https://example.invalid/x /tmp/scratch",
		"rm -rf ~/Library/Caches/terva-test",
		"rm -rf $HOME/scratch",
		"rm -rf ./build",
		"rm -f /tmp/one-file",
		"dd of=/tmp/image.bin if=/tmp/src",
	} {
		if err := sb.CheckCommand(c); err != nil {
			t.Errorf("expected %q to be allowed, got: %v", c, err)
		}
	}

	// A compound line is still decomposed, so a root deletion cannot hide
	// behind a harmless leading command.
	if err := sb.CheckCommand("ls && rm -rf /"); err == nil {
		t.Error("a root deletion after && must still be caught")
	}
}
