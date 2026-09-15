package tenant

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStoreIn(testsupport.TempDir(t))
}

// The same person signing in from a second browser must land in the same
// environment, or every login mints a new empty home.
func TestEnrolIsIdempotentBySubject(t *testing.T) {
	s := newTestStore(t)

	first, created, err := s.Enrol("sub-abc", "Ada")
	if err != nil || !created {
		t.Fatalf("first enrol: rec=%+v created=%v err=%v", first, created, err)
	}
	second, created, err := s.Enrol("sub-abc", "Ada Lovelace")
	if err != nil {
		t.Fatalf("second enrol: %v", err)
	}
	if created {
		t.Error("the second sign-in enrolled a NEW environment; it must find the existing one")
	}
	if second.ID != first.ID {
		t.Errorf("id changed across sign-ins: %q then %q", first.ID, second.ID)
	}
	if second.EnrolledAt != first.EnrolledAt {
		t.Error("EnrolledAt was rewritten on a return visit")
	}
	if second.Display != "Ada Lovelace" {
		t.Errorf("display did not refresh: %q", second.Display)
	}
}

func TestTwoSubjectsGetSeparateEnvironments(t *testing.T) {
	s := newTestStore(t)
	a, _, err := s.Enrol("sub-a", "A")
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := s.Enrol("sub-b", "B")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatalf("two subjects share one environment id %q", a.ID)
	}
	if a.Home("/srv/tenants") == b.Home("/srv/tenants") {
		t.Fatal("two subjects share one home")
	}
}

// Verifying the systemd foundation showed every tenant can read the NAMES of
// every other tenant's state directory. The id names that directory, so it must
// carry nothing about who the tenant is — not the subject, and not a value
// derived from it that a holder of a candidate subject could recompute.
func TestTheIDRevealsNothingAboutTheSubject(t *testing.T) {
	const subject = "8f14e45fceea167a5a36dedd4bea2543"

	s := newTestStore(t)
	rec, _, err := s.Enrol(subject, "someone@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rec.ID, subject) {
		t.Errorf("the id %q contains the subject verbatim", rec.ID)
	}

	// Not derived either: the same subject enrolled into a different registry
	// must produce a different id, or the id is a function of the subject and
	// anyone holding a guess can confirm it.
	other := newTestStore(t)
	again, _, err := other.Enrol(subject, "someone@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID == rec.ID {
		t.Errorf("the id is derived from the subject — the same subject minted %q twice", rec.ID)
	}
}

// The id is joined onto a filesystem path, so a registry that has been
// hand-edited or restored from a backup must fail at LOAD, not at the join.
func TestAMalformedIDIsRefusedAtLoad(t *testing.T) {
	dir := testsupport.TempDir(t)
	path := filepath.Join(dir, StoreName)

	// The control: a well-formed registry loads. Without this half, a Lookup
	// that failed for any other reason would read as the check working.
	good := `{"tenants":[{"id":"t-0123456789abcdef","subject":"s","enrolled_at":"2026-01-01T00:00:00Z"}]}`
	if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStoreAt(path).Lookup("s"); err != nil {
		t.Fatalf("CONTROL: a well-formed registry must load, got %v", err)
	}

	for _, bad := range []string{"../escape", "t-short", "", "/etc"} {
		body := `{"tenants":[{"id":"` + bad + `","subject":"s","enrolled_at":"2026-01-01T00:00:00Z"}]}`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := NewStoreAt(path).Lookup("s")
		if err == nil {
			t.Errorf("id %q loaded without complaint", bad)
			continue
		}
		if !strings.Contains(err.Error(), "malformed id") {
			t.Errorf("id %q was refused, but for the wrong reason: %v", bad, err)
		}
	}
}

func TestLookupOfAnUnknownSubjectSaysNotEnrolled(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Lookup("nobody"); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("want ErrNotEnrolled, got %v", err)
	}
	// The caller distinguishes "not enrolled" from "broken" to decide between
	// enrolling and refusing, so the sentinel has to survive a real enrolment
	// existing alongside.
	if _, _, err := s.Enrol("somebody", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lookup("nobody"); !errors.Is(err, ErrNotEnrolled) {
		t.Fatalf("want ErrNotEnrolled with another tenant present, got %v", err)
	}
}

func TestEnrolRefusesAnEmptySubject(t *testing.T) {
	s := newTestStore(t)
	if _, _, err := s.Enrol("", "nobody"); err == nil {
		t.Fatal("an empty subject enrolled an environment — every anonymous caller would share it")
	}
}

// D7 ships suspension before deletion. Suspending must leave the record — and
// therefore the home it names — entirely intact.
func TestSuspensionDoesNotDestroyTheEnrolment(t *testing.T) {
	s := newTestStore(t)
	rec, _, err := s.Enrol("sub-x", "X")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSuspended(rec.ID, true); err != nil {
		t.Fatal(err)
	}
	after, err := s.Lookup("sub-x")
	if err != nil {
		t.Fatalf("the enrolment vanished on suspend: %v", err)
	}
	if !after.Suspended {
		t.Error("suspension did not stick")
	}
	if after.ID != rec.ID || after.Home("/srv") != rec.Home("/srv") {
		t.Error("suspension moved the environment")
	}

	if err := s.SetSuspended(rec.ID, false); err != nil {
		t.Fatal(err)
	}
	if back, _ := s.Lookup("sub-x"); back.Suspended {
		t.Error("resuming did not clear suspension")
	}
	if err := s.SetSuspended("t-ffffffffffffffff", true); err == nil {
		t.Error("suspending an unknown environment reported success")
	}
}

// The registry maps real people to their environments; it must not be
// world-readable even if the supervisor's home is.
//
// Skipped on Windows rather than asserted there: Go reports a synthesised POSIX
// mode for a Windows file, so perm&0o077 is always non-zero and the finding
// would be a false positive every run. The ACL question is a different one and
// this test is not equipped to answer it.
func TestTheRegistryIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not the boundary on Windows")
	}
	dir := testsupport.TempDir(t)
	s := NewStoreIn(dir)
	if _, _, err := s.Enrol("sub", "who"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, StoreName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("registry mode %o is readable beyond its owner", perm)
	}
}

// D7's hardest rule: an unentitled sign-in is EVIDENCE, not an instruction. An
// IdP outage, a renamed group and a genuinely revoked role are identical from
// here, and the action they would otherwise trigger is irreversible.
func TestAnUnentitledSignInRecordsAndDestroysNothing(t *testing.T) {
	s := newTestStore(t)
	rec, _, err := s.Enrol("sub-a", "Ada")
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	if err := s.NoteUnentitled("sub-a", when); err != nil {
		t.Fatal(err)
	}

	after, err := s.Lookup("sub-a")
	if err != nil {
		t.Fatalf("the enrolment vanished: %v", err)
	}
	if after.UnentitledSince == nil {
		t.Error("the observation was not recorded")
	}
	// Everything that could cost the tenant work is untouched.
	if after.Suspended {
		t.Error("an unentitled sign-in SUSPENDED the environment — that is an action, and this must only record")
	}
	if after.ID != rec.ID {
		t.Error("the environment was re-keyed")
	}
	if after.Home("/srv") != rec.Home("/srv") {
		t.Error("the home moved")
	}
}

// Signing in with a role again clears the mark, so the field means "as of the
// last time we saw them" rather than "at some point in the past" — which is the
// difference between a useful panel and a misleading one.
func TestReturningWithARoleClearsTheMark(t *testing.T) {
	s := newTestStore(t)
	rec, _, err := s.Enrol("sub-a", "Ada")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.NoteUnentitled("sub-a", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearUnentitled(rec.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Lookup("sub-a")
	if after.UnentitledSince != nil {
		t.Error("the mark survived an entitled sign-in")
	}
}

// Recording an observation about someone with no enrolment would create one —
// enrolling by the back door, for a person who was just refused.
func TestNotingAnUnknownSubjectEnrolsNobody(t *testing.T) {
	s := newTestStore(t)
	if err := s.NoteUnentitled("a-stranger", time.Now()); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("refusing a stranger created %d enrolment(s): %+v", len(list), list)
	}
}

// The hot path must not rewrite the registry. Enrol takes the file lock and
// rewrites the whole file, so calling it per request made a bookkeeping field
// cost a serialised write on every /media/ fetch.
func TestLookupDoesNotWrite(t *testing.T) {
	dir := testsupport.TempDir(t)
	s := NewStoreIn(dir)
	if _, _, err := s.Enrol("sub-a", "Ada"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, StoreName)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	for range 5 {
		if _, err := s.Lookup("sub-a"); err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("Lookup rewrote the registry — the read path is taking the write path's lock")
	}
}

// Stale is what keeps the write rare without making last-seen useless.
func TestStaleTracksTheTouchInterval(t *testing.T) {
	now := time.Now()
	fresh := Record{LastSeenAt: now.Add(-TouchInterval / 2)}
	if fresh.Stale(now) {
		t.Error("a recently-seen record asked for a write")
	}
	old := Record{LastSeenAt: now.Add(-2 * TouchInterval)}
	if !old.Stale(now) {
		t.Error("a long-unseen record never asks for a write, so last-seen would freeze")
	}
}

// A field meaning "this never happened" must be ABSENT from the file, not
// present as a date. `omitempty` does nothing to a time.Time, so the first
// version wrote "unentitled_since":"0001-01-01T00:00:00Z" onto every record —
// in a file whose whole purpose is being read by an operator.
func TestAnUneventfulRecordCarriesNoPhantomTimestamp(t *testing.T) {
	dir := testsupport.TempDir(t)
	s := NewStoreIn(dir)
	if _, _, err := s.Enrol("sub-a", "Ada"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, StoreName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "unentitled_since") {
		t.Errorf("a tenant who has done nothing wrong carries an unentitled_since:\n%s", b)
	}
	if strings.Contains(string(b), "0001-01-01") {
		t.Errorf("a zero time reached the file as a date:\n%s", b)
	}

	// The control: when it HAS happened, it is written.
	if err := s.NoteUnentitled("sub-a", time.Now()); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(dir, StoreName))
	if !strings.Contains(string(b), "unentitled_since") {
		t.Errorf("a real observation was not written:\n%s", b)
	}
}
