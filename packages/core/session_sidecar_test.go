package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// The sidecar lifecycle, held once for EVERY sidecar rather than once for the
// error log.
//
// These guards range over sessionSidecars deliberately. The drift they exist to
// stop is a new sidecar added to the table while one of the six lifecycle sites
// keeps its old hand-written list — delete, the empty-transcript prune, archive,
// restore, and the live and archived listing filters. Ranging means a row added
// to the table is immediately carried through all of them, and a site that
// forgot fails here instead of leaking a file in production.
//
// So: never rewrite these to name ".errors.jsonl". A guard that names one
// sidecar tests one sidecar, which is the situation the table replaced.

// writeLiveSidecars puts distinctive content in every sidecar belonging to
// transcriptPath and returns what it wrote, keyed by path.
func writeLiveSidecars(t *testing.T, transcriptPath string) map[string]string {
	t.Helper()
	want := map[string]string{}
	for _, p := range SessionSidecarPaths(transcriptPath) {
		body := "content of " + filepath.Base(p) + "\n"
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		want[p] = body
	}
	if len(want) == 0 {
		t.Fatal("sessionSidecars is empty, so these guards prove nothing")
	}
	return want
}

func TestSessionSidecarPathsDerivesFromTheTranscriptStem(t *testing.T) {
	if got := SessionSidecarPaths(""); got != nil {
		t.Errorf("SessionSidecarPaths(\"\") = %v, want nil", got)
	}
	paths := SessionSidecarPaths("/s/20260101-120000-aaaaaaaa.jsonl")
	if len(paths) != len(sessionSidecars) {
		t.Fatalf("got %d paths for %d sidecars", len(paths), len(sessionSidecars))
	}
	for _, p := range paths {
		if !strings.HasPrefix(p, "/s/20260101-120000-aaaaaaaa") {
			t.Errorf("sidecar %q does not share the transcript's stem, so it will not sort beside it", p)
		}
		if strings.HasSuffix(p, ".jsonl") && !isSessionSidecarName(filepath.Base(p)) {
			t.Errorf("sidecar %q ends in .jsonl but isSessionSidecarName does not claim it — a listing scan will treat it as a session", p)
		}
	}
}

// Archive and restore carry every sidecar. A sidecar left behind on archive
// orphans a record against a session no listing shows; one left behind on
// restore silently loses it.
func TestEverySidecarSurvivesAnArchiveRoundTrip(t *testing.T) {
	root, cwd := testsupport.TempDir(t), "/proj/one"
	const id = "20260101-120000-aaaaaaaa"
	path := seedSession(t, root, cwd, id, "archived", 2)
	want := writeLiveSidecars(t, path)

	if _, err := ArchiveSession(root, cwd, id); err != nil {
		t.Fatalf("ArchiveSession: %v", err)
	}

	dir := ArchiveDir(root, cwd)
	for _, p := range sessionSidecarPairs(path, dir, id) {
		if _, err := os.Stat(p.Live); !os.IsNotExist(err) {
			t.Errorf("live sidecar %s still present after archiving; it should have moved", filepath.Base(p.Live))
		}
		if _, err := os.Stat(p.Archived); err != nil {
			t.Errorf("archived sidecar %s missing after archiving: the record was orphaned", filepath.Base(p.Archived))
		}
	}

	if _, err := RestoreSession(root, cwd, id); err != nil {
		t.Fatalf("RestoreSession: %v", err)
	}
	for _, p := range sessionSidecarPairs(path, dir, id) {
		got, err := os.ReadFile(p.Live)
		if err != nil {
			t.Errorf("sidecar %s did not come back on restore: %v", filepath.Base(p.Live), err)
			continue
		}
		if string(got) != want[p.Live] {
			t.Errorf("sidecar %s restored with %q, want %q", filepath.Base(p.Live), got, want[p.Live])
		}
		if _, err := os.Stat(p.Archived); !os.IsNotExist(err) {
			t.Errorf("archived copy of %s survived the restore, so the next archive will find it in the way", filepath.Base(p.Archived))
		}
	}
}

// No sidecar is a session. Listing one surfaces a blank entry in /sessions and
// /continue, and the archived side shares the .jsonl.gz ending, so both scans
// need the filter.
func TestNoSidecarIsListedAsASession(t *testing.T) {
	root, cwd := testsupport.TempDir(t), "/proj/one"
	const id = "20260101-120000-aaaaaaaa"
	path := seedSession(t, root, cwd, id, "only session", 2)
	writeLiveSidecars(t, path)

	if got := ListSessions(root, cwd); len(got) != 1 {
		t.Fatalf("ListSessions = %d entries (%v), want 1 — a sidecar is being listed as a session", len(got), got)
	}

	if _, err := ArchiveSession(root, cwd, id); err != nil {
		t.Fatalf("ArchiveSession: %v", err)
	}
	if got := ListArchivedSessions(root, cwd); len(got) != 1 {
		t.Fatalf("ListArchivedSessions = %d entries, want 1 — an archived sidecar is being listed as a session", len(got))
	}
}

// The empty-transcript prune drops the sidecars with it. Decided in
// docs/proposals/session-state-sidecar.md: a never-sent session is still empty,
// so the sidecar is expendable — but it must be REMOVED, not left orphaned.
func TestEverySidecarGoesWithAPrunedEmptyTranscript(t *testing.T) {
	root := testsupport.TempDir(t)
	s, err := NewSession(root, root, "anthropic", "claude-sonnet-4-5", "test")
	if err != nil {
		t.Fatal(err)
	}
	want := writeLiveSidecars(t, s.Path)

	// No messages were appended, so Close prunes the fresh transcript.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Fatal("precondition: the empty transcript was not pruned, so this proves nothing about its sidecars")
	}
	for p := range want {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("sidecar %s outlived the transcript it belongs to", filepath.Base(p))
		}
	}
}
