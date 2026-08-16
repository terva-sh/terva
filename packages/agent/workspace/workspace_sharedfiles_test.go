package workspace

// The read side of share_file: shared.list and shared.fetch, the verbs a
// non-web carrier needs because it has no HTTP route to resolve a handle with.
//
// What matters here is scope and honesty. A listing is bound to one session, it
// reports what is actually ON DISK rather than what the transcript remembers,
// and a fetch refuses anything it cannot deliver instead of half-delivering it.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/attach"
	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/testsupport"
)

// wantCode asserts the wire error code, the way every other workspace test
// does: the code is the stable machine string a client branches on, while the
// message is localized prose that must never be asserted against.
func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var ce *ctrlproto.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a wire error, so a client sees no code at all", err)
	}
	if ce.Code != code {
		t.Errorf("error code = %q, want %q", ce.Code, code)
	}
}

// publishInto shares one file through the registered tool and returns its id.
// Going through the TOOL rather than the store directly keeps these tests on
// the same path a real turn takes.
func publishInto(t *testing.T, w *Workspace, s *wsSession, name, body string) string {
	t.Helper()
	src := filepath.Join(testsupport.TempDir(t), name)
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := deriveShare(t, w, s, build.Args{})
	if tool == nil {
		t.Fatal("share_file was not registered")
	}
	res, err := tool.Execute(context.Background(), []byte(`{"path":`+quote(src)+`}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Shared) != 1 {
		t.Fatalf("Shared = %+v, want one record", res.Shared)
	}
	return res.Shared[0].ID
}

// The listing is what the drawer is built from: everything this session handed
// over, described the way the card describes it, plus the path that makes a
// local action possible.
func TestSharedFilesListsWhatTheSessionPublished(t *testing.T) {
	w, s, store := shareTestWorkspace(t, "s1")
	id := publishInto(t, w, s, "report.pdf", "%PDF-1.4 body")

	files, err := w.SharedFiles(context.Background(), "s1")
	if err != nil {
		t.Fatalf("SharedFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("listed %d files, want 1: %+v", len(files), files)
	}
	got := files[0]
	if got.ID != id || got.Name != "report.pdf" || got.Kind != "document" {
		t.Errorf("entry = %+v, want the published report", got)
	}
	if got.Size != int64(len("%PDF-1.4 body")) {
		t.Errorf("size = %d, want %d", got.Size, len("%PDF-1.4 body"))
	}
	// The path is the whole reason a local client can act on a row rather than
	// only look at it.
	if !strings.HasPrefix(got.Path, store.SessionDir("s1")) {
		t.Errorf("path = %q, want it under this session's dir", got.Path)
	}
	if got.ExpiresAt == "" {
		t.Error("no expiry — the drawer cannot tell a live row from a doomed one")
	}
}

// A session that shared nothing has an empty drawer, not a broken one. Never
// having produced a deliverable is an ordinary state.
func TestSharedFilesIsEmptyNotAnErrorForASessionThatSharedNothing(t *testing.T) {
	w, _, _ := shareTestWorkspace(t, "s1")

	files, err := w.SharedFiles(context.Background(), "s1")
	if err != nil {
		t.Fatalf("SharedFiles on a quiet session: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("listed %+v, want nothing", files)
	}
}

// The scoping claim. A share belongs to the conversation that produced it, and
// a listing that leaked across sessions would hand someone another chat's
// deliverables — the same boundary the publisher enforces on the way in.
func TestSharedFilesDoesNotCrossSessions(t *testing.T) {
	w, s, _ := shareTestWorkspace(t, "s1")
	other := newTestSession()
	other.id, other.ws, other.cwd = "s2", w, s.cwd

	publishInto(t, w, s, "mine.txt", "s1 body")
	publishInto(t, w, other, "theirs.txt", "s2 body")

	files, err := w.SharedFiles(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name != "mine.txt" {
		t.Fatalf("s1 listed %+v, want only its own file", files)
	}
}

// Newest first: the ids are random, so there is no order to read out of them,
// and the useful question about a share is nearly always what just arrived.
func TestSharedFilesListsNewestFirst(t *testing.T) {
	w, s, store := shareTestWorkspace(t, "s1")
	first := publishInto(t, w, s, "older.txt", "old")
	second := publishInto(t, w, s, "newer.txt", "new")

	// Publication can land inside one filesystem timestamp tick, which would
	// make the order a coin flip rather than a property. Age the first file
	// explicitly so the assertion is about the sort and not about timing.
	ref, err := store.Resolve("s1", first)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(ref.Path, old, old); err != nil {
		t.Fatal(err)
	}

	files, err := w.SharedFiles(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("listed %d, want 2", len(files))
	}
	if files[0].ID != second || files[1].ID != first {
		t.Errorf("order = %q, %q; want the newer one first", files[0].Name, files[1].Name)
	}
}

// The fetch is the only way a remote client reaches the content at all, so it
// has to return the actual bytes with enough about them to write or render.
func TestSharedFileFetchReturnsTheBytes(t *testing.T) {
	w, s, _ := shareTestWorkspace(t, "s1")
	id := publishInto(t, w, s, "notes.txt", "the body")

	got, err := w.SharedFileFetch(context.Background(), "s1", ctrlproto.SharedFileRef{ID: id})
	if err != nil {
		t.Fatalf("SharedFileFetch: %v", err)
	}
	if string(got.Data) != "the body" {
		t.Errorf("data = %q, want the published body", got.Data)
	}
	if got.ID != id || got.Name != "notes.txt" {
		t.Errorf("content = %+v, want it to name the file it carries", got)
	}
}

// Fetching across sessions must fail the way an unknown id fails: one answer
// for expired, never-shared, and someone else's, so a caller cannot use the
// difference to confirm an id it guessed.
func TestSharedFileFetchDoesNotCrossSessions(t *testing.T) {
	w, s, _ := shareTestWorkspace(t, "s1")
	other := newTestSession()
	other.id, other.ws, other.cwd = "s2", w, s.cwd
	id := publishInto(t, w, other, "theirs.txt", "s2 body")

	_, err := w.SharedFileFetch(context.Background(), "s1", ctrlproto.SharedFileRef{ID: id})
	if err == nil {
		t.Fatal("s1 fetched s2's shared file")
	}
	// The same answer an unknown id gets.
	wantCode(t, err, ctrlproto.CodeNotFound)
}

// An id the store never minted is not found rather than a crash or an empty
// success. A swept file arrives here too, which is the common case.
func TestSharedFileFetchRefusesAnUnknownID(t *testing.T) {
	w, _, _ := shareTestWorkspace(t, "s1")

	_, err := w.SharedFileFetch(context.Background(), "s1", ctrlproto.SharedFileRef{ID: "shr_deadbeef"})
	if err == nil {
		t.Fatal("an unknown id fetched something")
	}
	wantCode(t, err, ctrlproto.CodeNotFound)
}

// The frame carries no id at all: a bad request, not a not-found. The caller
// asked nothing, which is a different mistake from asking for the wrong thing.
func TestSharedFileFetchRefusesAnEmptyID(t *testing.T) {
	w, _, _ := shareTestWorkspace(t, "s1")

	_, err := w.SharedFileFetch(context.Background(), "s1", ctrlproto.SharedFileRef{})
	if err == nil {
		t.Fatal("an empty id fetched something")
	}
	wantCode(t, err, ctrlproto.CodeBadRequest)
}

// A control frame is read into memory whole at both ends, so a file past the
// bound is refused BEFORE it is read — the failure must not be the thing it is
// protecting against. The caller still has the path and the web route, and the
// message says so.
func TestSharedFileFetchRefusesAFileTooLargeForAFrame(t *testing.T) {
	w, s, store := shareTestWorkspace(t, "s1")
	id := publishInto(t, w, s, "big.bin", "small for now")

	// Grow the file past the bound on disk. Publishing a real 8 MiB file would
	// make this test slow to prove a bound that is checked by stat.
	ref, err := store.Resolve("s1", id)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(ref.Path, os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(ctrlproto.MaxSharedFetchBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	_, err = w.SharedFileFetch(context.Background(), "s1", ctrlproto.SharedFileRef{ID: id})
	if err == nil {
		t.Fatal("an oversized file came back over the control plane")
	}
	wantCode(t, err, ctrlproto.CodeBadRequest)
	if !strings.Contains(err.Error(), "big.bin") {
		t.Errorf("error %q does not name the file that was refused", err)
	}
}

// The listing reports the STORE, not the transcript. The two diverge by design
// — the sweeper reaps bytes while the card that named them stays — and a drawer
// offering actions on a file that is gone is a drawer full of dead promises.
func TestSharedFilesReflectsTheStoreAfterASweep(t *testing.T) {
	w, s, store := shareTestWorkspace(t, "s1")
	publishInto(t, w, s, "doomed.txt", "body")

	// Age the file past the outbound TTL and sweep, exactly as the daemon's
	// background sweeper would.
	refs, err := store.List("s1")
	if err != nil || len(refs) != 1 {
		t.Fatalf("List = %+v, %v", refs, err)
	}
	old := time.Now().Add(-2 * attach.ShareTTL)
	if err := os.Chtimes(refs[0].Path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Sweep(time.Now(), attach.ShareTTL, attach.ShareCapBytes, attach.ShareGrace); err != nil {
		t.Fatal(err)
	}

	files, err := w.SharedFiles(context.Background(), "s1")
	if err != nil {
		t.Fatalf("SharedFiles after a sweep: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("listed %+v after the bytes were swept, want nothing", files)
	}
}
