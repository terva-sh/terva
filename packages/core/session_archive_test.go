package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// seedSession writes a transcript with a meta row, some messages, and returns
// its id. Mirrors what NewSession + a few turns leave on disk.
func seedSession(t *testing.T, root, cwd, id, title string, messages int) string {
	t.Helper()
	dir := SessionsDir(root, cwd)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString(`{"type":"meta","meta":{"id":"` + id + `","title":"` + title + `","model":"claude-sonnet-4-5","provider":"anthropic"}}` + "\n")
	for i := 0; i < messages; i++ {
		b.WriteString(`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"hello there"}]}}` + "\n")
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The whole point of the subdirectory: an archived session leaves EVERY listing
// without leaving the disk, and no scan had to learn a filter to make that true.
func TestArchivedSessionDisappearsFromEveryListing(t *testing.T) {
	root, cwd := testsupport.TempDir(t), "/proj/one"
	seedSession(t, root, cwd, "20260101-120000-aaaaaaaa", "kept", 2)
	seedSession(t, root, cwd, "20260101-130000-bbbbbbbb", "archived", 3)

	if got := len(ListSessions(root, cwd)); got != 2 {
		t.Fatalf("ListSessions = %d, want 2 before archiving", got)
	}

	if _, err := ArchiveSession(root, cwd, "20260101-130000-bbbbbbbb"); err != nil {
		t.Fatalf("ArchiveSession: %v", err)
	}

	paths := ListSessions(root, cwd)
	if len(paths) != 1 {
		t.Fatalf("ListSessions = %d, want 1 after archiving", len(paths))
	}
	if strings.Contains(paths[0], "bbbbbbbb") {
		t.Error("the archived session is still listed")
	}
	// The other scans over the same directory, each of which skips directories
	// for its own reasons — this is what the design is buying.
	if len(DescribeSessions(root, cwd)) != 1 {
		t.Error("DescribeSessions still sees the archived session")
	}
	if len(BuildSessionTree(root, cwd)) != 1 {
		t.Error("BuildSessionTree still sees the archived session")
	}
	if FindSessionByID(root, cwd, "20260101-130000-bbbbbbbb") != "" {
		t.Error("FindSessionByID resolved an archived session; a resume would open it")
	}
	// And the transcript is genuinely gone from the sessions dir, not copied.
	if fileExists(filepath.Join(SessionsDir(root, cwd), "20260101-130000-bbbbbbbb.jsonl")) {
		t.Error("the original transcript is still in the sessions directory")
	}
}

// An archived row must carry the same title/model/message count the live picker
// shows, or the archive is a list of opaque ids.
func TestListArchivedSessionsDescribesTheTranscript(t *testing.T) {
	root, cwd := testsupport.TempDir(t), "/proj/two"
	seedSession(t, root, cwd, "20260101-120000-cccccccc", "the archived one", 4)

	a, err := ArchiveSession(root, cwd, "20260101-120000-cccccccc")
	if err != nil {
		t.Fatal(err)
	}
	if a.Title != "the archived one" || a.MessageCount != 4 {
		t.Errorf("archive result = %q/%d messages, want the summary read from the transcript", a.Title, a.MessageCount)
	}
	if a.Bytes <= 0 || a.Original <= 0 {
		t.Errorf("sizes = %d compressed / %d original, want both recorded", a.Bytes, a.Original)
	}

	list := ListArchivedSessions(root, cwd)
	if len(list) != 1 {
		t.Fatalf("ListArchivedSessions = %d, want 1", len(list))
	}
	if list[0].ID != "20260101-120000-cccccccc" {
		t.Errorf("id = %q, want the session id, not a path-derived stem", list[0].ID)
	}
	if list[0].Title != "the archived one" || list[0].MessageCount != 4 {
		t.Errorf("listed row = %q/%d, want the same summary the live picker renders", list[0].Title, list[0].MessageCount)
	}
	if list[0].ArchivedAt.IsZero() {
		t.Error("no archived-at stamp")
	}
}

// Round trip: what comes back is byte-identical to what went in. A transcript
// that survives archiving in a lossy way is worse than one that was deleted.
func TestArchiveRestoreRoundTripsExactly(t *testing.T) {
	root, cwd := testsupport.TempDir(t), "/proj/three"
	path := seedSession(t, root, cwd, "20260101-120000-dddddddd", "round trip", 5)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ArchiveSession(root, cwd, "20260101-120000-dddddddd"); err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreSession(root, cwd, "20260101-120000-dddddddd")
	if err != nil {
		t.Fatalf("RestoreSession: %v", err)
	}
	after, err := os.ReadFile(restored)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("the restored transcript differs from the archived one")
	}
	if len(ListSessions(root, cwd)) != 1 {
		t.Error("the restored session is not listed again")
	}
	if len(ListArchivedSessions(root, cwd)) != 0 {
		t.Error("the archive still holds a copy after restore")
	}
}

// The error sidecar is that session's data. Archiving the transcript and leaving
// the sidecar orphans a failure record against a session nothing lists.
func TestArchiveCarriesTheErrorSidecar(t *testing.T) {
	root, cwd := testsupport.TempDir(t), "/proj/four"
	path := seedSession(t, root, cwd, "20260101-120000-eeeeeeee", "with errors", 1)
	sidecar := ErrorLogPathFor(path)
	if err := os.WriteFile(sidecar, []byte(`{"type":"error","message":"boom"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := ArchiveSession(root, cwd, "20260101-120000-eeeeeeee"); err != nil {
		t.Fatal(err)
	}
	if fileExists(sidecar) {
		t.Error("the sidecar was left behind, orphaned against an archived session")
	}
	// It must not show up as an archived SESSION either — it shares the suffix.
	if got := len(ListArchivedSessions(root, cwd)); got != 1 {
		t.Fatalf("ListArchivedSessions = %d, want 1: the sidecar was counted as a session", got)
	}

	if _, err := RestoreSession(root, cwd, "20260101-120000-eeeeeeee"); err != nil {
		t.Fatal(err)
	}
	if !fileExists(sidecar) {
		t.Error("the sidecar did not come back with its transcript")
	}
}

// Restoring on top of a live session would destroy a transcript terva may have
// open, to recover an old one. Refused.
func TestRestoreRefusesToClobberALiveSession(t *testing.T) {
	root, cwd := testsupport.TempDir(t), "/proj/five"
	id := "20260101-120000-ffffffff"
	seedSession(t, root, cwd, id, "original", 2)
	if _, err := ArchiveSession(root, cwd, id); err != nil {
		t.Fatal(err)
	}
	// A new session lands on the same id (contrived, but the failure mode is
	// data loss, so the guard is not allowed to depend on ids being unique).
	seedSession(t, root, cwd, id, "live now", 9)

	if _, err := RestoreSession(root, cwd, id); err == nil {
		t.Fatal("restore overwrote a live session")
	}
	got := DescribeSessions(root, cwd)
	if len(got) != 1 || got[0].Title != "live now" {
		t.Error("the live transcript was damaged by the refused restore")
	}
}

// Archiving something that is not there, and restoring something that was never
// archived, are both clean refusals rather than silent successes.
func TestArchiveAndRestoreRefuseUnknownIDs(t *testing.T) {
	root, cwd := testsupport.TempDir(t), "/proj/six"
	if _, err := ArchiveSession(root, cwd, "20260101-120000-99999999"); err == nil {
		t.Error("archiving a session that does not exist reported success")
	}
	if _, err := RestoreSession(root, cwd, "20260101-120000-99999999"); err == nil {
		t.Error("restoring a session that was never archived reported success")
	}
	if IsArchived(root, cwd, "20260101-120000-99999999") {
		t.Error("IsArchived is true for a session that does not exist")
	}
}

// Compression is the reason for the gzip, so assert it actually pays on the
// shape of data this stores: JSONL repeats its keys on every line.
func TestArchivingCompressesTheTranscript(t *testing.T) {
	root, cwd := testsupport.TempDir(t), "/proj/seven"
	seedSession(t, root, cwd, "20260101-120000-77777777", "big", 400)

	a, err := ArchiveSession(root, cwd, "20260101-120000-77777777")
	if err != nil {
		t.Fatal(err)
	}
	if a.Bytes >= a.Original/2 {
		t.Errorf("compressed %d from %d bytes — under 2x on repetitive JSONL suggests it was stored, not deflated", a.Bytes, a.Original)
	}
}

// The archive is per-directory, exactly as sessions already are: archiving in
// one project must not touch or reveal another's.
func TestArchiveIsScopedToItsDirectory(t *testing.T) {
	root := testsupport.TempDir(t)
	seedSession(t, root, "/proj/a", "20260101-120000-aaaa1111", "in a", 1)
	seedSession(t, root, "/proj/b", "20260101-120000-bbbb2222", "in b", 1)

	if _, err := ArchiveSession(root, "/proj/a", "20260101-120000-aaaa1111"); err != nil {
		t.Fatal(err)
	}
	if got := len(ListArchivedSessions(root, "/proj/b")); got != 0 {
		t.Errorf("project b sees %d archived sessions from project a", got)
	}
	if got := len(ListSessions(root, "/proj/b")); got != 1 {
		t.Errorf("project b's live sessions changed: %d", got)
	}
}

// A future archive viewer reads through this seam and never learns that the
// archive is compressed.
func TestOpenArchivedSessionStreamsTheTranscript(t *testing.T) {
	root, cwd := testsupport.TempDir(t), "/proj/eight"
	path := seedSession(t, root, cwd, "20260101-120000-88888888", "viewer", 3)
	want, _ := os.ReadFile(path)
	if _, err := ArchiveSession(root, cwd, "20260101-120000-88888888"); err != nil {
		t.Fatal(err)
	}

	rc, err := OpenArchivedSession(root, cwd, "20260101-120000-88888888")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got := make([]byte, len(want)+16)
	n, _ := readFull(rc, got)
	if string(got[:n]) != string(want) {
		t.Error("the streamed transcript differs from the archived one")
	}
}

func readFull(r interface{ Read([]byte) (int, error) }, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
