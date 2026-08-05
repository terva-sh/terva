package core

import (
	"crypto/sha256"
	"os"
	"runtime"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func readOnlyTestSession(t *testing.T) string {
	t.Helper()
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "openai-codex", "gpt-5.6-terra", "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "one"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "two"}}},
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "three"}}},
	} {
		if err := s.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return s.Path
}

func readOnlyDigest(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

// ReadSessionMessages must replay a session it has no write access to.
//
// This is the property that distinguishes it from OpenSession, and it is the
// one worth testing: an assertion that the bytes are unchanged passes for
// OpenSession too (its Close only removes a file that is BOTH freshFile and
// empty, so a resumed session is never deleted) and would therefore be a guard
// that cannot fail. Read-only permission can fail: OpenSession's
// O_APPEND|O_WRONLY is refused, and a dump built on it would be unable to
// inspect a session the user does not own.
func TestReadSessionMessagesWorksWithoutWriteAccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits do not govern write access on Windows")
	}
	path := readOnlyTestSession(t)
	before := readOnlyDigest(t, path)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	// Mode bits do not bind every caller: root ignores them outright, which is
	// how CI runs. There the chmod above is decoration, OpenSession succeeds, and
	// the second half of this test would report a regression that has not
	// happened. Probe whether this process is actually denied the write rather
	// than assuming the chmod bought one — asking the question directly beats
	// checking uid, because it is the property the test depends on rather than a
	// proxy for it.
	if f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0); err == nil {
		_ = f.Close()
		t.Skip("this process can write a 0400 file (running as root?), so read-only permission cannot distinguish the two loaders here")
	}

	msgs, err := ReadSessionMessages(path)
	if err != nil {
		t.Fatalf("read-only load failed on a read-only file: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("want the 3 seeded messages, got %d", len(msgs))
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("session file is gone after a read-only load: %v", err)
	}
	if after := readOnlyDigest(t, path); after != before {
		t.Error("reading the session rewrote it")
	}

	// The other half: without this, the test above could pass on a build where
	// ReadSessionMessages had quietly gone back to taking a write handle,
	// because the temp file happened to be writable after all.
	if s, _, err := OpenSession(path); err == nil {
		_ = s.Close()
		t.Error("OpenSession succeeded on a read-only file, so this test no longer distinguishes the two loaders")
	}
}

// The read-only path and the read-write path must replay to the SAME
// transcript. Two loaders that can disagree is how a dump ends up showing a
// prompt no session would ever send -- which is exactly the failure the mode
// was added to rule out, so it cannot be introduced by the mode itself.
func TestReadSessionMessagesMatchesOpenSession(t *testing.T) {
	path := readOnlyTestSession(t)

	readOnly, err := ReadSessionMessages(path)
	if err != nil {
		t.Fatal(err)
	}
	s, readWrite, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	// Append before closing so this exercises a real write cycle rather than
	// an open/close that touches nothing.
	if err := s.AppendMessage(provider.Message{Role: provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "keep"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if len(readOnly) != len(readWrite) {
		t.Fatalf("loaders disagree on length: read-only %d, OpenSession %d", len(readOnly), len(readWrite))
	}
	for i := range readOnly {
		if readOnly[i].Role != readWrite[i].Role {
			t.Errorf("message %d role: read-only %q, OpenSession %q", i, readOnly[i].Role, readWrite[i].Role)
		}
		if got, want := readOnlyTextOf(readOnly[i]), readOnlyTextOf(readWrite[i]); got != want {
			t.Errorf("message %d text: read-only %q, OpenSession %q", i, got, want)
		}
	}
}

func readOnlyTextOf(m provider.Message) string {
	out := ""
	for _, c := range m.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			out += tb.Text
		}
	}
	return out
}
