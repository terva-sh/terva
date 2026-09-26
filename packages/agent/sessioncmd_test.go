package agent

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/session"
	"terva.sh/terva/packages/testsupport"
)

// lockCmdFixture puts this process in a temp cwd with a temp home, so
// `terva session ...` resolves the same bucket a real run in that directory
// would, and returns a session in it.
func lockCmdFixture(t *testing.T) (id, path string) {
	t.Helper()
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Skipf("cannot chdir here: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	// Resolve cwd the way the command does, so a symlinked temp dir (macOS
	// /var -> /private/var) buckets identically.
	realCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	s, err := session.NewSession(home, realCwd, "openai", "gpt-5", "test")
	if err != nil {
		t.Fatal(err)
	}
	path = s.Path
	// A message, or Close prunes the empty transcript and the bucket is bare.
	if err := s.AppendMessage(provider.Message{
		Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "u0"}},
	}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close() // closed, so the bucket holds a session nobody has open
	return session.SessionIDFromPath(path), path
}

func TestSessionCommandFallsThroughForEverythingElse(t *testing.T) {
	if handled, _ := runSessionCommand([]string{"doctor"}); handled {
		t.Error("session claimed a command that is not its own")
	}
	if handled, _ := runSessionCommand(nil); handled {
		t.Error("session claimed an empty argv")
	}
}

func TestSessionLockNeedsAReason(t *testing.T) {
	id, _ := lockCmdFixture(t)
	// The reason is the whole point of a claim record over a bare lock: it is
	// what the next person reads when they are refused.
	handled, err := runSessionCommand([]string{"session", "lock", id})
	if !handled || err == nil {
		t.Fatalf("a claim with no reason must be refused: handled=%v err=%v", handled, err)
	}
	if !strings.Contains(err.Error(), "--reason") {
		t.Errorf("the refusal must name the flag that fixes it: %v", err)
	}
}

func TestSessionLockThenUnlockRoundTrips(t *testing.T) {
	id, path := lockCmdFixture(t)

	if handled, err := runSessionCommand([]string{"session", "lock", id, "--reason", "held for review"}); !handled || err != nil {
		t.Fatalf("lock: handled=%v err=%v", handled, err)
	}
	claim, ok := session.SessionLockClaim(path)
	if !ok || claim.Reason != "held for review" {
		t.Fatalf("the claim did not record the reason: %+v", claim)
	}
	if claim.ExpiresAt == "" {
		t.Error("a claim must always carry an expiry")
	}
	// The claiming process has exited by now, and the claim must still bind.
	blocked, _, err := session.OpenSession(path)
	if !errors.Is(err, session.ErrSessionLocked) {
		t.Fatalf("the claim did not refuse an open: %v", err)
	}
	if blocked != nil {
		t.Fatal("a refused open returned a session handle anyway")
	}

	if handled, err := runSessionCommand([]string{"session", "unlock", id}); !handled || err != nil {
		t.Fatalf("unlock: handled=%v err=%v", handled, err)
	}
	reopened, _, err := session.OpenSession(path)
	if err != nil {
		t.Fatalf("an unlocked session must open: %v", err)
	}
	_ = reopened.Close()
}

func TestSessionLockRejectsABadDuration(t *testing.T) {
	id, _ := lockCmdFixture(t)
	_, err := runSessionCommand([]string{"session", "lock", id, "--reason", "r", "--for", "soon"})
	if err == nil || !strings.Contains(err.Error(), "--for") {
		t.Fatalf("a bad duration must be named: %v", err)
	}
}

func TestSessionLockRefusesAnIdThatEscapesTheBucket(t *testing.T) {
	lockCmdFixture(t)
	for _, bad := range []string{"../escape", "a/b", `a\b`} {
		if _, err := runSessionCommand([]string{"session", "lock", bad, "--reason", "r"}); err == nil {
			t.Errorf("%q was accepted as a session id", bad)
		}
	}
}

func TestSessionUnlockRefusesWhileATervaHoldsIt(t *testing.T) {
	id, path := lockCmdFixture(t)
	held, _, err := session.OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })

	_, err = runSessionCommand([]string{"session", "unlock", id})
	if err == nil {
		t.Fatal("unlock must refuse while a writer holds the session")
	}
	// The remedy has to be in the message: there is nothing the user can do
	// from here except stop the other terva.
	if !strings.Contains(err.Error(), "Stop that terva first") {
		t.Errorf("the refusal must name the remedy: %v", err)
	}
}

func TestSessionLocksListsAHeldSession(t *testing.T) {
	_, path := lockCmdFixture(t)
	held, _, err := session.OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })

	out := captureStdout(t, func() {
		if handled, err := runSessionCommand([]string{"session", "locks"}); !handled || err != nil {
			t.Fatalf("locks: handled=%v err=%v", handled, err)
		}
	})
	if !strings.Contains(out, filepath.Base(strings.TrimSuffix(path, ".jsonl"))) {
		t.Errorf("the held session was not listed:\n%s", out)
	}
	if !strings.Contains(out, "held") {
		t.Errorf("the listing must say it is held:\n%s", out)
	}
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prev := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stdout = prev
	return <-done
}
