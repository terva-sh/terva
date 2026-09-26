package session

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/sessionlock"
	"terva.sh/terva/packages/testsupport"
)

func lockTestSession(t *testing.T) *Session {
	t.Helper()
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "openai", "gpt-5", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(mvMsg(provider.RoleUser, "u0")); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestASecondOpenOfALiveSessionIsRefused(t *testing.T) {
	s := lockTestSession(t)
	t.Cleanup(func() { _ = s.Close() })

	_, _, err := OpenSession(s.Path)
	if !errors.Is(err, ErrSessionLocked) {
		t.Fatalf("opening a held session must be refused, got %v", err)
	}
	if !strings.Contains(err.Error(), "is writing to this session") {
		t.Errorf("the refusal must say what the holder is doing: %s", err)
	}
	if !strings.Contains(err.Error(), "lapses at") {
		t.Errorf("the refusal must carry the expiry: %s", err)
	}
}

func TestClosingASessionReleasesItForTheNextOpener(t *testing.T) {
	s := lockTestSession(t)
	path := s.Path
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := SessionLockClaim(path); ok {
		t.Error("the claim outlived the session that made it")
	}
	reopened, msgs, err := OpenSession(path)
	if err != nil {
		t.Fatalf("a closed session must reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if len(msgs) != 1 {
		t.Errorf("reopened with %d messages, want 1", len(msgs))
	}
}

// TestReadersIgnoreAHeldSession is decision 0007's property: the transcript is
// never locked, so inspection never waits on a writer and never needs write
// permission on its subject.
func TestReadersIgnoreAHeldSession(t *testing.T) {
	s := lockTestSession(t)
	t.Cleanup(func() { _ = s.Close() })

	before := fileDigest(t, s.Path)

	if msgs, err := ReadSessionMessages(s.Path); err != nil || len(msgs) != 1 {
		t.Fatalf("ReadSessionMessages on a held session: %d msgs, %v", len(msgs), err)
	}
	if _, meta, err := ReadSessionMeta(s.Path); err != nil || meta.Provider != "openai" {
		t.Fatalf("ReadSessionMeta on a held session: %+v %v", meta, err)
	}
	if got := ListSessions(lockRoot(s.Path), lockRoot(s.Path)); len(got) != 1 {
		t.Errorf("ListSessions saw %d sessions on a held bucket, want 1", len(got))
	}
	if after := fileDigest(t, s.Path); after != before {
		t.Error("reading a held session changed the transcript")
	}
}

// TestNoLockArtifactIsMistakenForASession sweeps every scanner that walks a
// bucket. A new file in that directory is one filter away from being listed as
// a session, pruned as an empty one, or offered in a picker.
func TestNoLockArtifactIsMistakenForASession(t *testing.T) {
	s := lockTestSession(t)
	dir := lockRoot(s.Path)
	id := SessionIDFromPath(s.Path)

	for _, p := range sessionlock.ArtifactPaths(s.Path) {
		if _, err := os.Stat(p); err != nil {
			continue // the guard exists; the record may not yet
		}
		if isSessionTranscriptName(filepath.Base(p)) {
			t.Errorf("%s reads as a transcript name", filepath.Base(p))
		}
	}
	_ = s.Close()

	if got := ListSessions(dir, dir); len(got) != 1 || SessionIDFromPath(got[0]) != id {
		t.Errorf("ListSessions returned %d entries, want just the session", len(got))
	}
	if got := DescribeSessions(dir, dir); len(got) != 1 {
		t.Errorf("DescribeSessions returned %d entries, want 1", len(got))
	}
	if got := ListSessionsAcrossProjects(dir); len(got) != 1 {
		t.Errorf("ListSessionsAcrossProjects returned %d entries, want 1", len(got))
	}
	if got := SessionsMatching(dir, func(SessionSummary) bool { return true }); len(got) != 1 {
		t.Errorf("SessionsMatching returned %d entries, want 1", len(got))
	}
}

// TestPruneEmptySessionsSkipsALockedTranscript covers a data-loss bug that
// predates the lock. PruneEmptySessions runs at every CLI start, and a session
// is meta-only for the whole window between NewSession and its first message.
//
// Written to fail without the guard: drop the SessionIsLocked check in
// PruneEmptySessions and this test removes a live session's transcript.
func TestPruneEmptySessionsSkipsALockedTranscript(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "openai", "gpt-5", "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	// No message yet: exactly the state a terva sits in at its prompt.
	if !sessionHasNoMessages(s.Path) {
		t.Fatal("setup: the session should be meta-only here")
	}

	PruneEmptySessions(dir, dir) // a second terva starting up

	if _, err := os.Stat(s.Path); err != nil {
		t.Fatalf("a live session's transcript was pruned out from under it: %v", err)
	}
	// And it must still be writable through the handle that survived.
	if err := s.AppendMessage(mvMsg(provider.RoleUser, "u0")); err != nil {
		t.Fatalf("append after prune: %v", err)
	}
	if msgs, err := ReadSessionMessages(s.Path); err != nil || len(msgs) != 1 {
		t.Fatalf("the turn did not reach disk: %d msgs, %v", len(msgs), err)
	}
}

func TestPruneEmptySessionsStillRemovesAnUnheldStub(t *testing.T) {
	// The guard must not turn the prune into a no-op.
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "openai", "gpt-5", "test")
	if err != nil {
		t.Fatal(err)
	}
	path := s.Path
	s.freshFile = false // stop Close removing it, so the prune is what does
	_ = s.Close()

	PruneEmptySessions(dir, dir)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("an abandoned meta-only transcript survived the prune")
	}
}

func TestPruneSweepsLockArtifactsLeftWithoutATranscript(t *testing.T) {
	// Where a downgrade's litter is collected: an older binary does not know
	// these files and leaves them behind when it deletes a session.
	dir := testsupport.TempDir(t)
	bucket := SessionsDir(dir, dir)
	if err := os.MkdirAll(bucket, 0o700); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(bucket, "20260101-120000-abcd1234.jsonl")
	for _, p := range sessionlock.ArtifactPaths(orphan) {
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	PruneEmptySessions(dir, dir)

	for _, p := range sessionlock.ArtifactPaths(orphan) {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s survived with no transcript beside it", filepath.Base(p))
		}
	}
}

func TestAPrunedEmptySessionTakesItsLockArtifactsWithIt(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "openai", "gpt-5", "test")
	if err != nil {
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close() // fresh and empty, so Close removes the transcript

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("setup: Close should have removed the empty transcript")
	}
	for _, p := range sessionlock.ArtifactPaths(path) {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s was orphaned by Close", filepath.Base(p))
		}
	}
}

// TestTheRenameHandleStillWritesToALockedSession pins the exemption. Renaming
// takes no lock on purpose: an flock is held per open file description, so the
// live session's own process would be refused by its own lock.
func TestTheRenameHandleStillWritesToALockedSession(t *testing.T) {
	s := lockTestSession(t)
	t.Cleanup(func() { _ = s.Close() })

	if err := RenameSession(s.Path, "renamed while held"); err != nil {
		t.Fatalf("rename must work on a session its own process holds: %v", err)
	}
	_, meta, err := ReadSessionMeta(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "renamed while held" {
		t.Errorf("title = %q, want the rename to have landed", meta.Title)
	}
}

func TestTheErrorSidecarStillWritesToALockedSession(t *testing.T) {
	s := lockTestSession(t)
	t.Cleanup(func() { _ = s.Close() })
	if err := s.LogError("a provider failure"); err != nil {
		t.Fatalf("recording an error must not be gated on the lock: %v", err)
	}
}

func TestArchivingASessionAnotherProcessHoldsIsRefused(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "openai", "gpt-5", "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.AppendMessage(mvMsg(provider.RoleUser, "u0")); err != nil {
		t.Fatal(err)
	}

	_, err = ArchiveSession(dir, dir, SessionIDFromPath(s.Path))
	if !errors.Is(err, ErrSessionLocked) {
		t.Fatalf("archiving a held session must be refused, got %v", err)
	}
	if _, statErr := os.Stat(s.Path); statErr != nil {
		t.Fatal("the refused archive removed the transcript anyway")
	}
}

func TestArchivingAClosedSessionTakesItsLockArtifacts(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "openai", "gpt-5", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(mvMsg(provider.RoleUser, "u0")); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	id := SessionIDFromPath(path)
	_ = s.Close()

	if _, err := ArchiveSession(dir, dir, id); err != nil {
		t.Fatalf("archive: %v", err)
	}
	for _, p := range sessionlock.ArtifactPaths(path) {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s survived the archive", filepath.Base(p))
		}
	}
	// And restoring lands on a session nothing claims.
	if _, err := RestoreSession(dir, dir, id); err != nil {
		t.Fatalf("restore: %v", err)
	}
	reopened, _, err := OpenSession(path)
	if err != nil {
		t.Fatalf("a restored session must be openable: %v", err)
	}
	_ = reopened.Close()
}

func TestAnExplicitClaimRefusesAnOpenAndSurvivesTheProcessThatMadeIt(t *testing.T) {
	s := lockTestSession(t)
	path := s.Path
	_ = s.Close()

	if err := ClaimSession(path, "held for review", "human:sothr", 3600); err != nil {
		t.Fatalf("ClaimSession: %v", err)
	}
	// ClaimSession returned, so its handle is gone. The claim must not be.
	if _, _, err := OpenSession(path); !errors.Is(err, ErrSessionLocked) {
		t.Fatalf("an explicit claim must refuse an open, got %v", err)
	}
	st, ok := DescribeSessionLock(path)
	if !ok || st.Held {
		t.Fatalf("a claim with no process behind it must not read as held: %+v", st)
	}
	if err := UnlockSession(path); err != nil {
		t.Fatalf("UnlockSession: %v", err)
	}
	reopened, _, err := OpenSession(path)
	if err != nil {
		t.Fatalf("an unlocked session must open: %v", err)
	}
	_ = reopened.Close()
}

func TestUnlockRefusesASessionThisProcessHasOpen(t *testing.T) {
	s := lockTestSession(t)
	t.Cleanup(func() { _ = s.Close() })
	if err := UnlockSession(s.Path); !errors.Is(err, ErrSessionLocked) {
		t.Fatalf("unlock must refuse while a writer holds the session, got %v", err)
	}
}

func fileDigest(t *testing.T, path string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

// lockRoot recovers the root a lockTestSession was made under. NewSession was
// called with root == cwd, and SessionsDir appends sessions/<cwdhash>, so the
// root is two directories above the transcript.
func lockRoot(transcriptPath string) string {
	return filepath.Dir(filepath.Dir(filepath.Dir(transcriptPath)))
}

// TestRetainKeepsAnEmptyTranscriptThroughAHandover covers the bug the ACP
// reorder exposed. Close prunes a still-empty session so that opening terva and
// quitting at the prompt leaves no litter. That rule is wrong for a handover:
// a caller closing one handle in order to reopen the same transcript must not
// have the file deleted in between.
func TestRetainKeepsAnEmptyTranscriptThroughAHandover(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "openai", "gpt-5", "test")
	if err != nil {
		t.Fatal(err)
	}
	path := s.Path

	s.Retain()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a retained transcript was pruned on close: %v", err)
	}
	reopened, _, err := OpenSession(path)
	if err != nil {
		t.Fatalf("the handover target could not be reopened: %v", err)
	}
	_ = reopened.Close()
}

func TestWithoutRetainAnEmptySessionIsStillPruned(t *testing.T) {
	// Retain must not become the default: the anti-litter rule is the reason
	// Close prunes at all.
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "openai", "gpt-5", "test")
	if err != nil {
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("an abandoned empty session survived close")
	}
}
