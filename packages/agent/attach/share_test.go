package attach

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/privfs"
	"terva.sh/terva/packages/testsupport"
)

func newTestShareStore(t *testing.T) *Store {
	t.Helper()
	return NewShareStoreAt(filepath.Join(testsupport.TempDir(t), "shared"))
}

// srcFile writes a file OUTSIDE the store, standing in for something in the
// agent's workspace.
func srcFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(testsupport.TempDir(t), name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPublishDescribesWhatItWrote(t *testing.T) {
	s := newTestShareStore(t)
	ref, err := s.Publish("ses_1", srcFile(t, "report.pdf", "%PDF-1.4 body"), "")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if got, err := os.ReadFile(ref.Path); err != nil || string(got) != "%PDF-1.4 body" {
		t.Fatalf("published bytes = %q, %v; want the source body", got, err)
	}
	if ref.Name != "report.pdf" {
		t.Errorf("Name = %q, want report.pdf (the source's base)", ref.Name)
	}
	if ref.Size != int64(len("%PDF-1.4 body")) {
		t.Errorf("Size = %d, want %d", ref.Size, len("%PDF-1.4 body"))
	}
	if !strings.Contains(ref.Mime, "pdf") {
		t.Errorf("Mime = %q, want a pdf type", ref.Mime)
	}
	if ref.Kind != "document" {
		t.Errorf("Kind = %q, want document", ref.Kind)
	}
	// A shr_ id is how a share turning up in a log or a wire frame identifies
	// itself; the inbound store mints att_.
	if !strings.HasPrefix(ref.ID, "shr_") {
		t.Errorf("ID = %q, want a shr_ prefix", ref.ID)
	}
}

// The whole reason Publish copies rather than hardlinks: an agent that edits the
// file afterwards must not retroactively change what it already handed the user.
// A link would share the inode and this test would read "rewritten".
func TestPublishCopiesSoALaterEditCannotRewriteIt(t *testing.T) {
	s := newTestShareStore(t)
	src := srcFile(t, "notes.txt", "original")
	ref, err := s.Publish("ses_1", src, "")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if err := os.WriteFile(src, []byte("rewritten"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(ref.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Errorf("shared bytes = %q after the source was rewritten, want the original", got)
	}
}

// Truncating the source in place is the case a hardlink would ALSO leak, and it
// is the one an editor is most likely to produce.
func TestPublishSurvivesTheSourceBeingTruncated(t *testing.T) {
	s := newTestShareStore(t)
	src := srcFile(t, "export.csv", "a,b,c\n1,2,3\n")
	ref, err := s.Publish("ses_1", src, "")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if err := os.Truncate(src, 0); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(ref.Path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != int64(len("a,b,c\n1,2,3\n")) {
		t.Errorf("shared size = %d after the source was truncated, want %d", fi.Size(), len("a,b,c\n1,2,3\n"))
	}
}

// The name argument relabels the file for the user, and is a caller-supplied
// string like any other: it decides a label, never a location.
func TestPublishRelabelsWithoutLettingTheNameEscape(t *testing.T) {
	s := newTestShareStore(t)
	ref, err := s.Publish("ses_1", srcFile(t, "tmp0001.bin", "x"), "../../etc/passwd")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if ref.Name != "passwd" {
		t.Errorf("Name = %q, want the base alone", ref.Name)
	}
	if dir := filepath.Dir(ref.Path); dir != s.SessionDir("ses_1") {
		t.Errorf("Path lives in %q, want the session dir %q", dir, s.SessionDir("ses_1"))
	}
}

func TestPublishRejectsADirectory(t *testing.T) {
	s := newTestShareStore(t)
	dir := testsupport.TempDir(t)

	_, err := s.Publish("ses_1", dir, "")
	if !errors.Is(err, ErrNotRegular) {
		t.Fatalf("Publish(dir) error = %v, want ErrNotRegular", err)
	}
}

func TestPublishRejectsAMissingSource(t *testing.T) {
	s := newTestShareStore(t)
	_, err := s.Publish("ses_1", filepath.Join(testsupport.TempDir(t), "nope.txt"), "")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Publish(missing) error = %v, want os.ErrNotExist", err)
	}
}

// Rejected whole, and nothing partial left behind — a half-copied deliverable
// with a working download link is worse than a refusal.
func TestPublishRejectsOverLimitAndLeavesNothing(t *testing.T) {
	s := newTestShareStore(t)
	s.maxBytes = 4

	_, err := s.Publish("ses_1", srcFile(t, "big.bin", "123456789"), "")
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Publish(over limit) error = %v, want ErrTooLarge", err)
	}
	if entries, err := os.ReadDir(s.SessionDir("ses_1")); err == nil && len(entries) != 0 {
		t.Errorf("session dir holds %d entries after a rejected publish, want 0", len(entries))
	}
}

// Shares are the agent's output about the user's work; on a shared host they are
// no more another local account's business than the inbound area is.
func TestPublishPrivatePermissions(t *testing.T) {
	skipOnWindows(t)
	s := newTestShareStore(t)
	ref, err := s.Publish("ses_1", srcFile(t, "secret.txt", "hello"), "")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	fi, err := os.Stat(ref.Path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != privfs.FileMode {
		t.Errorf("file mode = %v, want %v", perm, privfs.FileMode)
	}
	di, err := os.Stat(s.SessionDir("ses_1"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != privfs.DirMode {
		t.Errorf("dir mode = %v, want %v", perm, privfs.DirMode)
	}
}

// Resolve is what the download route calls, so a share must not be reachable by
// naming it from another session.
func TestPublishedFileResolvesOnlyInItsOwnSession(t *testing.T) {
	s := newTestShareStore(t)
	ref, err := s.Publish("ses_1", srcFile(t, "chart.png", "\x89PNG\r\n\x1a\n"), "")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if got, err := s.Resolve("ses_1", ref.ID); err != nil || got.Path != ref.Path {
		t.Fatalf("Resolve in its own session = %+v, %v; want the published ref", got, err)
	}
	if _, err := s.Resolve("ses_2", ref.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve from another session error = %v, want ErrNotFound", err)
	}
}

// The type a media file gets must not depend on which machine the daemon runs
// on. Go's built-in table carries no audio or video entries whatsoever, so
// mime.TypeByExtension answers these only from the SYSTEM database — absent in a
// scratch container, and differently spelled where it exists (macOS says
// audio/x-wav). Both make a clip that plays on one host download on another.
//
// This is the test that would have caught it: it passes on a host with no
// /etc/mime.types at all, which is the deployment that was broken.
func TestMediaTypesDoNotDependOnTheHost(t *testing.T) {
	s := newTestShareStore(t)

	for _, tc := range []struct{ name, mime, kind string }{
		{"a.mp3", "audio/mpeg", "audio"},
		{"a.wav", "audio/wav", "audio"},   // NOT audio/x-wav, which is what macOS says
		{"a.flac", "audio/flac", "audio"}, // NOT audio/x-flac
		{"a.ogg", "audio/ogg", "audio"},
		{"a.m4a", "audio/mp4", "audio"},
		{"a.mp4", "video/mp4", "video"},
		{"a.webm", "video/webm", "video"},
		{"a.mov", "video/quicktime", "video"},
		// Still answered by Go's own table, so these prove the fallback survives.
		{"a.png", "image/png", "image"},
		{"a.pdf", "application/pdf", "document"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A body that sniffs as nothing in particular, so only the extension
			// path can produce the answer.
			ref, err := s.Publish("ses_1", srcFile(t, tc.name, "not a real media file"), "")
			if err != nil {
				t.Fatal(err)
			}
			if ref.Mime != tc.mime {
				t.Errorf("Mime = %q, want %q", ref.Mime, tc.mime)
			}
			if ref.Kind != tc.kind {
				t.Errorf("Kind = %q, want %q", ref.Kind, tc.kind)
			}
		})
	}
}

// The two areas are the same type with different retention, and the sweeper each
// one starts must apply its OWN policy. A share still young at the inbound TTL
// is exactly the case that would break if the constants were read globally.
func TestEachStoreSweepsOnItsOwnPolicy(t *testing.T) {
	share := newTestShareStore(t)
	inbox := newTestStore(t)

	if share.policy.TTL != ShareTTL {
		t.Errorf("share TTL = %v, want %v", share.policy.TTL, ShareTTL)
	}
	if inbox.policy.TTL != TTL {
		t.Errorf("stage TTL = %v, want %v", inbox.policy.TTL, TTL)
	}

	shared, err := share.Publish("ses_1", srcFile(t, "keepme.txt", "deliverable"), "")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	uploaded, err := inbox.Stage("ses_1", "dropped.txt", strings.NewReader("upload"))
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	// Two days on: past the inbound TTL, well inside the share TTL.
	old := time.Now().Add(-48 * time.Hour)
	for _, p := range []string{shared.Path, uploaded.Path} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := share.Sweep(time.Now(), share.policy.TTL, share.policy.CapBytes, share.policy.Grace); err != nil {
		t.Fatal(err)
	}
	if _, err := inbox.Sweep(time.Now(), inbox.policy.TTL, inbox.policy.CapBytes, inbox.policy.Grace); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(shared.Path); err != nil {
		t.Errorf("the shared file was swept at 48h, but its TTL is %v", ShareTTL)
	}
	if _, err := os.Stat(uploaded.Path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the staged upload survived 48h, but its TTL is %v", TTL)
	}
}

// The deadline a client renders against, and it has to be the SAME decision the
// sweeper makes — a card that goes inert while the bytes are still downloadable
// is as wrong as one that offers bytes that are gone, just in the other
// direction. Both read the file's own mtime and add the store's TTL.
func TestPublishedRefCarriesTheSweepersOwnDeadline(t *testing.T) {
	root := testsupport.TempDir(t)
	store := NewShareStoreAt(root)
	src := filepath.Join(testsupport.TempDir(t), "report.pdf")
	if err := os.WriteFile(src, []byte("%PDF-1.4"), 0o600); err != nil {
		t.Fatal(err)
	}

	ref, err := store.Publish("ses_1", src, "")
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if ref.ExpiresAt.IsZero() {
		t.Fatal("published ref carries no expiry — the panel cannot say when the download stops working")
	}

	// The file's own modification time plus the outbound TTL, which is what
	// Sweep compares against. Derived from disk rather than from time.Now(), so
	// a coarse-timestamp filesystem cannot put the two a second apart.
	info, err := os.Stat(ref.Path)
	if err != nil {
		t.Fatal(err)
	}
	if want := info.ModTime().Add(ShareTTL); !ref.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want mtime+ShareTTL = %v", ref.ExpiresAt, want)
	}

	// And the sweeper agrees: one instant before, the file survives; one after,
	// it is taken. This is the assertion that would fail if the two ever came
	// apart, which a hand-written constant could not tell us.
	if res, err := store.Sweep(ref.ExpiresAt.Add(-time.Second), ShareTTL, ShareCapBytes, 0); err != nil || res.Expired != 0 {
		t.Errorf("swept %d file(s) a second BEFORE the advertised expiry (err=%v)", res.Expired, err)
	}
	if res, err := store.Sweep(ref.ExpiresAt.Add(time.Second), ShareTTL, ShareCapBytes, 0); err != nil || res.Expired != 1 {
		t.Errorf("swept %d file(s) a second AFTER the advertised expiry, want 1 (err=%v)", res.Expired, err)
	}
}

// The inbound store answers with its own, much shorter, TTL from the same code.
// One derivation, two policies — not two derivations that happen to agree.
func TestStagedRefCarriesTheInboundDeadline(t *testing.T) {
	store := NewStoreAt(testsupport.TempDir(t))
	ref, err := store.Stage("ses_1", "notes.txt", strings.NewReader("hi"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(ref.Path)
	if err != nil {
		t.Fatal(err)
	}
	if want := info.ModTime().Add(TTL); !ref.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %v, want mtime+TTL = %v", ref.ExpiresAt, want)
	}
	if ShareTTL == TTL {
		t.Fatal("this test is vacuous while the two TTLs are equal")
	}
}

// A store with no policy must not claim everything expired at the epoch, which
// is what a bare `mod.Add(0)` would produce and what every reader would render
// as "gone".
func TestRefWithoutAPolicyHasNoExpiry(t *testing.T) {
	store := &Store{root: testsupport.TempDir(t), label: "test", idPrefix: "t_", maxBytes: 1 << 20}
	ref, err := store.Stage("ses_1", "notes.txt", strings.NewReader("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if !ref.ExpiresAt.IsZero() {
		t.Errorf("ExpiresAt = %v on a policy-less store, want the zero time (unknown, not expired)", ref.ExpiresAt)
	}
}
