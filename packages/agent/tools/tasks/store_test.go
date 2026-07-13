package tasks

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

func fixedClock() func() time.Time {
	t := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

// newBound returns a store bound to a real session in a temp dir, with a fixed
// clock for deterministic timestamps.
func newBound(t *testing.T) *Store {
	t.Helper()
	s := NewStore(NewDirFS(testsupport.TempDir(t)), "test-agent")
	s.now = fixedClock()
	if err := s.Rebind("sess-1"); err != nil {
		t.Fatalf("rebind: %v", err)
	}
	return s
}

func TestCreateDefaultsAndIDs(t *testing.T) {
	s := newBound(t)
	got, err := s.Create([]CreateSpec{
		{Title: "Patch the parser bug"},
		{Title: "Add regression test", ActiveForm: "Adding regression test"},
		{Title: "Run the suite", Status: StatusActive},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 created, got %d", len(got))
	}
	if got[0].ID != "task-1" || got[1].ID != "task-2" || got[2].ID != "task-3" {
		t.Errorf("ids: %q %q %q", got[0].ID, got[1].ID, got[2].ID)
	}
	if got[0].ActiveForm != "Patch the parser bug" {
		t.Errorf("active_form fallback to title: %q", got[0].ActiveForm)
	}
	if got[1].ActiveForm != "Adding regression test" {
		t.Errorf("explicit active_form: %q", got[1].ActiveForm)
	}
	if got[0].Status != StatusPending {
		t.Errorf("default status should be pending: %q", got[0].Status)
	}
	if got[2].Status != StatusActive {
		t.Errorf("explicit active status: %q", got[2].Status)
	}
	if got[0].Owner != "test-agent" {
		t.Errorf("owner default: %q", got[0].Owner)
	}
	if got[0].CreatedAt == "" || got[0].UpdatedAt == "" {
		t.Errorf("timestamps unset: %+v", got[0])
	}
}

func TestCreateRejectsBadInput(t *testing.T) {
	s := newBound(t)
	if _, err := s.Create(nil); err == nil {
		t.Error("empty batch should error")
	}
	if _, err := s.Create([]CreateSpec{{Title: "  "}}); err == nil {
		t.Error("blank title should error")
	}
	if _, err := s.Create([]CreateSpec{{Title: "ok", Status: "garbage"}}); err == nil {
		t.Error("invalid status should error")
	}
	if n := len(s.List()); n != 0 {
		t.Errorf("rejected creates must not mutate; have %d", n)
	}
}

func TestOneActiveInvariant(t *testing.T) {
	s := newBound(t)
	if _, err := s.Create([]CreateSpec{{Title: "A"}, {Title: "B"}, {Title: "C", Status: StatusBlocked}}); err != nil {
		t.Fatal(err)
	}
	active := StatusActive
	if _, deact, err := s.Update(UpdatePatch{ID: "task-1", Status: &active}); err != nil || deact != nil {
		t.Fatalf("activate A: err=%v deact=%v", err, deact)
	}
	_, deact, err := s.Update(UpdatePatch{ID: "task-2", Status: &active})
	if err != nil {
		t.Fatal(err)
	}
	if deact == nil || deact.ID != "task-1" {
		t.Fatalf("activating B should deactivate A; got %v", deact)
	}
	list := s.List()
	if list[0].Status != StatusPending {
		t.Errorf("task-1 should be pending, got %q", list[0].Status)
	}
	if list[1].Status != StatusActive {
		t.Errorf("task-2 should be active, got %q", list[1].Status)
	}
	if list[2].Status != StatusBlocked {
		t.Errorf("blocked task must be untouched, got %q", list[2].Status)
	}
	// Re-activating the already-active task must not demote anything.
	if _, deact, _ := s.Update(UpdatePatch{ID: "task-2", Status: &active}); deact != nil {
		t.Errorf("re-activating active task demoted %v", deact)
	}
}

func TestBatchTwoActiveLastWins(t *testing.T) {
	s := newBound(t)
	got, err := s.Create([]CreateSpec{
		{Title: "A", Status: StatusActive},
		{Title: "B", Status: StatusActive},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Status != StatusPending {
		t.Errorf("first active should be demoted, got %q", got[0].Status)
	}
	if got[1].Status != StatusActive {
		t.Errorf("last active should remain, got %q", got[1].Status)
	}
}

func TestUpdateRejectsUnknownAndEmptyID(t *testing.T) {
	s := newBound(t)
	if _, err := s.Create([]CreateSpec{{Title: "A"}}); err != nil {
		t.Fatal(err)
	}
	active := StatusActive
	if _, _, err := s.Update(UpdatePatch{ID: "task-99", Status: &active}); err == nil {
		t.Error("unknown id should error")
	}
	if _, _, err := s.Update(UpdatePatch{ID: "", Status: &active}); err == nil {
		t.Error("empty id should error")
	}
	if n := len(s.List()); n != 1 {
		t.Errorf("update must never create; have %d tasks", n)
	}
}

func TestStatusValidation(t *testing.T) {
	s := newBound(t)
	cases := []struct {
		status Status
		ok     bool
	}{
		{"", true},
		{StatusPending, true},
		{StatusActive, true},
		{StatusBlocked, true},
		{StatusDone, true},
		{StatusCancelled, true},
		{"Done", false},
		{"in_progress", false},
		{"garbage", false},
	}
	for _, c := range cases {
		_, err := s.Create([]CreateSpec{{Title: "x", Status: c.status}})
		if c.ok && err != nil {
			t.Errorf("status %q: unexpected error %v", c.status, err)
		}
		if !c.ok && err == nil {
			t.Errorf("status %q: expected rejection", c.status)
		}
	}
}

func TestEvidenceRoundTripAndReload(t *testing.T) {
	dir := testsupport.TempDir(t)
	s1 := NewStore(NewDirFS(dir), "test-agent")
	s1.now = fixedClock()
	if err := s1.Rebind("sess-x"); err != nil {
		t.Fatal(err)
	}
	if _, err := s1.Create([]CreateSpec{{Title: "A"}, {Title: "B"}}); err != nil {
		t.Fatal(err)
	}
	done := StatusDone
	ev := "cargo test parser::comments passed"
	if _, _, err := s1.Update(UpdatePatch{ID: "task-1", Status: &done, Evidence: &ev}); err != nil {
		t.Fatal(err)
	}

	// Reload via a fresh store over the same dir + session.
	s2 := NewStore(NewDirFS(dir), "test-agent")
	s2.now = fixedClock()
	if err := s2.Rebind("sess-x"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s1.List(), s2.List()) {
		t.Errorf("reload mismatch:\n s1=%+v\n s2=%+v", s1.List(), s2.List())
	}
	got := s2.List()
	if got[0].Status != StatusDone || got[0].Evidence != ev {
		t.Errorf("evidence/status not persisted: %+v", got[0])
	}
	// nextID preserved across reload.
	created, err := s2.Create([]CreateSpec{{Title: "C"}})
	if err != nil {
		t.Fatal(err)
	}
	if created[0].ID != "task-3" {
		t.Errorf("nextID not preserved across reload, got %q", created[0].ID)
	}
}

func TestTimestamps(t *testing.T) {
	dir := testsupport.TempDir(t)
	s := NewStore(NewDirFS(dir), "a")
	cur := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return cur }
	if err := s.Rebind("s"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create([]CreateSpec{{Title: "A"}, {Title: "B"}}); err != nil {
		t.Fatal(err)
	}
	createdAt := s.List()[0].CreatedAt
	active := StatusActive
	if _, _, err := s.Update(UpdatePatch{ID: "task-2", Status: &active}); err != nil {
		t.Fatal(err)
	}
	cur = cur.Add(time.Hour)
	if _, _, err := s.Update(UpdatePatch{ID: "task-1", Status: &active}); err != nil {
		t.Fatal(err)
	}
	list := s.List()
	if list[0].CreatedAt != createdAt {
		t.Errorf("CreatedAt must be stable: %q vs %q", list[0].CreatedAt, createdAt)
	}
	want := cur.UTC().Format(time.RFC3339)
	if list[0].UpdatedAt != want {
		t.Errorf("activated task UpdatedAt: %q want %q", list[0].UpdatedAt, want)
	}
	if list[1].UpdatedAt != want {
		t.Errorf("demoted task UpdatedAt must advance: %q want %q", list[1].UpdatedAt, want)
	}
}

func TestRebindSwitchAndNoSession(t *testing.T) {
	dir := testsupport.TempDir(t)
	s := NewStore(NewDirFS(dir), "a")
	s.now = fixedClock()
	if err := s.Rebind("sess-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create([]CreateSpec{{Title: "A-task"}}); err != nil {
		t.Fatal(err)
	}
	// Switch sessions: A flushed, B starts empty.
	if err := s.Rebind("sess-B"); err != nil {
		t.Fatal(err)
	}
	if n := len(s.List()); n != 0 {
		t.Errorf("sess-B should start empty, got %d", n)
	}
	if _, err := s.Create([]CreateSpec{{Title: "B-task"}}); err != nil {
		t.Fatal(err)
	}
	// Back to A: reload its task (no cross-session bleed).
	if err := s.Rebind("sess-A"); err != nil {
		t.Fatal(err)
	}
	la := s.List()
	if len(la) != 1 || la[0].Title != "A-task" {
		t.Errorf("sess-A reload wrong: %+v", la)
	}
	// No session: in-memory, no file written.
	if err := s.Rebind(""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create([]CreateSpec{{Title: "ephemeral"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tasks-.json")); !os.IsNotExist(err) {
		t.Errorf("tasks-.json must never be written")
	}
}

func TestRebindCarriesPreBindWork(t *testing.T) {
	dir := testsupport.TempDir(t)
	s := NewStore(NewDirFS(dir), "a")
	s.now = fixedClock()
	// Create while still in-memory (ordering guarantee violated / pre-session).
	if _, err := s.Create([]CreateSpec{{Title: "early"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Rebind("sess-1"); err != nil {
		t.Fatal(err)
	}
	got := s.List()
	if len(got) != 1 || got[0].Title != "early" {
		t.Errorf("pre-bind work not carried into first session: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "tasks-sess-1.json")); err != nil {
		t.Errorf("carried tasks should be persisted: %v", err)
	}
}

func TestRebindIdempotent(t *testing.T) {
	s := newBound(t)
	if _, err := s.Create([]CreateSpec{{Title: "A"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Rebind("sess-1"); err != nil {
		t.Fatal(err)
	}
	if n := len(s.List()); n != 1 {
		t.Errorf("re-binding same id should be a no-op; have %d tasks", n)
	}
}

func TestUpdatePatchesFields(t *testing.T) {
	s := newBound(t)
	if _, err := s.Create([]CreateSpec{{Title: "Old title", ActiveForm: "Old form", Note: "old note"}}); err != nil {
		t.Fatal(err)
	}
	newTitle := "New title"
	newNote := "new note"
	updated, _, err := s.Update(UpdatePatch{ID: "task-1", Title: &newTitle, Note: &newNote})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "New title" {
		t.Errorf("title not patched: %q", updated.Title)
	}
	if updated.Note != "new note" {
		t.Errorf("note not patched: %q", updated.Note)
	}
	// ActiveForm untouched (nil pointer) keeps its old value.
	if updated.Status != StatusPending {
		t.Errorf("status should be unchanged: %q", updated.Status)
	}
	if updated.ActiveForm != "Old form" {
		t.Errorf("active_form should be unchanged when not patched: %q", updated.ActiveForm)
	}
}

func TestUpdateEmptyActiveFormFallsBackToTitle(t *testing.T) {
	s := newBound(t)
	if _, err := s.Create([]CreateSpec{{Title: "Patch parser", ActiveForm: "Patching parser"}}); err != nil {
		t.Fatal(err)
	}
	empty := ""
	updated, _, err := s.Update(UpdatePatch{ID: "task-1", ActiveForm: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ActiveForm != "Patch parser" {
		t.Errorf("empty active_form should fall back to title, got %q", updated.ActiveForm)
	}
}

func TestUpdateBlankTitleIsNoOp(t *testing.T) {
	s := newBound(t)
	if _, err := s.Create([]CreateSpec{{Title: "A"}}); err != nil {
		t.Fatal(err)
	}
	// A blank title patch (e.g. a status-only update that incidentally carries
	// title:"") is a no-op, not an error (hardening #6).
	blank := "   "
	active := StatusActive
	updated, _, err := s.Update(UpdatePatch{ID: "task-1", Title: &blank, Status: &active})
	if err != nil {
		t.Fatalf("blank title patch should not error: %v", err)
	}
	if updated.Title != "A" {
		t.Errorf("blank title patch must leave title unchanged, got %q", updated.Title)
	}
	if updated.Status != StatusActive {
		t.Errorf("the rest of the patch should still apply, got status %q", updated.Status)
	}
}

func TestArchiveAllResetsCurrentAndKeepsIDsMonotonic(t *testing.T) {
	s := newBound(t)
	if _, err := s.Create([]CreateSpec{{Title: "A"}, {Title: "B"}, {Title: "C"}}); err != nil {
		t.Fatal(err)
	}
	gen, dropped, ok, err := s.Archive(false, "phase one")
	if err != nil || !ok {
		t.Fatalf("archive: ok=%v err=%v", ok, err)
	}
	if dropped != 0 {
		t.Errorf("no cap pressure, want dropped=0 got %d", dropped)
	}
	if gen.Seq != 1 || len(gen.Tasks) != 3 || gen.Label != "phase one" {
		t.Errorf("unexpected generation: %+v", gen)
	}
	if n := len(s.List()); n != 0 {
		t.Errorf("current list should be empty after archive-all, got %d", n)
	}
	// IDs are not rewound: the next task continues past the archived ones.
	created, err := s.Create([]CreateSpec{{Title: "D"}})
	if err != nil {
		t.Fatal(err)
	}
	if created[0].ID != "task-4" {
		t.Errorf("nextID must stay monotonic across archive, got %q", created[0].ID)
	}
	// A second archive gets the next seq.
	gen2, _, ok, _ := s.Archive(false, "")
	if !ok || gen2.Seq != 2 {
		t.Errorf("second generation seq should be 2, got %+v (ok=%v)", gen2, ok)
	}
}

func TestArchiveKeepOpen(t *testing.T) {
	s := newBound(t)
	if _, err := s.Create([]CreateSpec{{Title: "open"}, {Title: "fin"}, {Title: "gone"}}); err != nil {
		t.Fatal(err)
	}
	done, cancelled := StatusDone, StatusCancelled
	if _, _, err := s.Update(UpdatePatch{ID: "task-2", Status: &done}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Update(UpdatePatch{ID: "task-3", Status: &cancelled}); err != nil {
		t.Fatal(err)
	}
	gen, _, ok, err := s.Archive(true, "")
	if err != nil || !ok {
		t.Fatalf("archive keep_open: ok=%v err=%v", ok, err)
	}
	if len(gen.Tasks) != 2 {
		t.Errorf("only the two terminal tasks should be archived, got %d", len(gen.Tasks))
	}
	cur := s.List()
	if len(cur) != 1 || cur[0].ID != "task-1" || cur[0].Status != StatusPending {
		t.Errorf("the open task should remain current: %+v", cur)
	}
}

func TestArchiveNoOp(t *testing.T) {
	s := newBound(t)
	// Empty list: archive is a no-op, no generation.
	if _, _, ok, err := s.Archive(false, ""); ok || err != nil {
		t.Errorf("archiving empty list should be a no-op, got ok=%v err=%v", ok, err)
	}
	// keep_open with no terminal tasks: also a no-op, current untouched.
	if _, err := s.Create([]CreateSpec{{Title: "A"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok, _ := s.Archive(true, ""); ok {
		t.Error("keep_open with no terminal tasks should be a no-op")
	}
	if n := len(s.List()); n != 1 {
		t.Errorf("no-op archive must not mutate current, got %d", n)
	}
	if g := s.Generations(); len(g) != 0 {
		t.Errorf("no generation should be created on a no-op, got %d", len(g))
	}
}

func TestArchivePersistRoundTrip(t *testing.T) {
	dir := testsupport.TempDir(t)
	s1 := NewStore(NewDirFS(dir), "a")
	s1.now = fixedClock()
	if err := s1.Rebind("sess-arch"); err != nil {
		t.Fatal(err)
	}
	if _, err := s1.Create([]CreateSpec{{Title: "A"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s1.Archive(false, "gen-one"); err != nil {
		t.Fatal(err)
	}

	// Reload via a fresh store: generations and the gen counter survive.
	s2 := NewStore(NewDirFS(dir), "a")
	s2.now = fixedClock()
	if err := s2.Rebind("sess-arch"); err != nil {
		t.Fatal(err)
	}
	gens := s2.Generations()
	if len(gens) != 1 || gens[0].Seq != 1 || gens[0].Label != "gen-one" || len(gens[0].Tasks) != 1 {
		t.Fatalf("generations not persisted/reloaded: %+v", gens)
	}
	g, ok := s2.Generation(1)
	if !ok || g.Tasks[0].Title != "A" {
		t.Errorf("Generation(1) lookup after reload: %+v ok=%v", g, ok)
	}
	// nextGen preserved: the next archive is seq 2, not a reused 1.
	if _, err := s2.Create([]CreateSpec{{Title: "B"}}); err != nil {
		t.Fatal(err)
	}
	gen2, _, _, _ := s2.Archive(false, "")
	if gen2.Seq != 2 {
		t.Errorf("nextGen not preserved across reload, got seq %d", gen2.Seq)
	}
}

func TestArchiveGenerationsCap(t *testing.T) {
	s := newBound(t)
	// Archive MaxGenerations+2 times; each needs a task to be non-empty.
	for i := range MaxGenerations + 2 {
		if _, err := s.Create([]CreateSpec{{Title: "t"}}); err != nil {
			t.Fatal(err)
		}
		_, dropped, ok, err := s.Archive(false, "")
		if err != nil || !ok {
			t.Fatalf("archive %d: ok=%v err=%v", i, ok, err)
		}
		if i >= MaxGenerations && dropped != 1 {
			t.Errorf("archive %d past the cap should drop 1 oldest, got %d", i, dropped)
		}
	}
	gens := s.Generations()
	if len(gens) != MaxGenerations {
		t.Fatalf("retained generations should be capped at %d, got %d", MaxGenerations, len(gens))
	}
	// The oldest seqs were dropped FIFO; the newest is retained.
	if gens[len(gens)-1].Seq != MaxGenerations+2 {
		t.Errorf("newest generation seq wrong: %d", gens[len(gens)-1].Seq)
	}
	if gens[0].Seq != 3 { // seqs 1 and 2 dropped
		t.Errorf("oldest retained seq should be 3 after FIFO drop, got %d", gens[0].Seq)
	}
}

func TestLoadResilience(t *testing.T) {
	dir := testsupport.TempDir(t)
	// Legacy file: tasks present, no next_id.
	legacy := `{"tasks":[{"id":"task-5","title":"old","status":"pending","created_at":"","updated_at":""}]}`
	if err := os.WriteFile(filepath.Join(dir, "tasks-legacy.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewStore(NewDirFS(dir), "a")
	s.now = fixedClock()
	if err := s.Rebind("legacy"); err != nil {
		t.Fatal(err)
	}
	created, err := s.Create([]CreateSpec{{Title: "new"}})
	if err != nil {
		t.Fatal(err)
	}
	if created[0].ID != "task-6" {
		t.Errorf("nextID should derive past max suffix; got %q", created[0].ID)
	}
}

// TestSessionIDPathSafety: a hostile session id with traversal sequences must
// not write outside the data dir (hardening #1).
func TestSessionIDPathSafety(t *testing.T) {
	dir := testsupport.TempDir(t)
	s := NewStore(NewDirFS(dir), "a")
	s.now = fixedClock()
	if err := s.Rebind("sess/../../escape"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create([]CreateSpec{{Title: "x"}}); err != nil {
		t.Fatal(err)
	}
	// Nothing escaped into the tempdir's parent.
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(dir), "*escape*")); len(m) > 0 {
		t.Errorf("session id escaped the data dir: %v", m)
	}
	// A safe (hashed) file landed inside the data dir.
	inside, _ := filepath.Glob(filepath.Join(dir, "tasks-*.json"))
	if len(inside) != 1 {
		t.Fatalf("expected one hashed session file inside the data dir, got %v", inside)
	}
	if base := filepath.Base(inside[0]); strings.ContainsAny(base, `/\`) || strings.Contains(base, "..") {
		t.Errorf("unsafe session file name: %q", base)
	}
}

// TestRebindCorruptFileNoLeak: switching into a session whose file is corrupt
// must not show the previous session's tasks (hardening #2).
func TestRebindCorruptFileNoLeak(t *testing.T) {
	dir := testsupport.TempDir(t)
	s := NewStore(NewDirFS(dir), "a")
	s.now = fixedClock()
	if err := s.Rebind("good"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create([]CreateSpec{{Title: "good task"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tasks-bad.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	warn := s.Rebind("bad")
	if warn == nil {
		t.Errorf("expected a warning when the target file is corrupt")
	}
	if n := len(s.List()); n != 0 {
		t.Errorf("corrupt rebind leaked prior session tasks; got %d", n)
	}
	if bk, _ := filepath.Glob(filepath.Join(dir, "tasks-bad.json.corrupt-*")); len(bk) == 0 {
		t.Errorf("corrupt file should be moved aside")
	}
	// The new session starts clean.
	if _, err := s.Create([]CreateSpec{{Title: "fresh"}}); err != nil {
		t.Fatal(err)
	}
	if got := s.List(); len(got) != 1 || got[0].Title != "fresh" {
		t.Errorf("bad session should start fresh: %+v", got)
	}
}

// TestCreateSanitizesFields: control/newline/ANSI characters are stripped at
// ingress (hardening #3).
func TestCreateSanitizesFields(t *testing.T) {
	s := newBound(t)
	got, err := s.Create([]CreateSpec{{
		Title: "line1\nFAKE-TASK done injected\tend",
		Note:  "a\x1b[31mred\rb",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(got[0].Title, "\n\r\t") {
		t.Errorf("title not sanitized: %q", got[0].Title)
	}
	if got[0].Title != "line1 FAKE-TASK done injected end" {
		t.Errorf("unexpected sanitized title: %q", got[0].Title)
	}
	if strings.ContainsRune(got[0].Note, 0x1b) || strings.ContainsAny(got[0].Note, "\r\n") {
		t.Errorf("note not sanitized: %q", got[0].Note)
	}
}

// TestCreateCaps: overlong fields truncate and oversized batches are rejected
// (hardening #4).
func TestCreateCaps(t *testing.T) {
	s := newBound(t)
	long := strings.Repeat("x", MaxTitleLen+50)
	got, err := s.Create([]CreateSpec{{Title: long}})
	if err != nil {
		t.Fatal(err)
	}
	if r := []rune(got[0].Title); len(r) > MaxTitleLen+1 { // +1 for the ellipsis
		t.Errorf("title not truncated: %d runes", len(r))
	}
	big := make([]CreateSpec, MaxBatch+1)
	for i := range big {
		big[i] = CreateSpec{Title: "t"}
	}
	if _, err := s.Create(big); err == nil {
		t.Errorf("a batch over %d should be rejected", MaxBatch)
	}
}

func TestPerSessionCap(t *testing.T) {
	s := newBound(t)
	for k := range MaxTasksPerSession / MaxBatch {
		batch := make([]CreateSpec, MaxBatch)
		for i := range batch {
			batch[i] = CreateSpec{Title: "t"}
		}
		if _, err := s.Create(batch); err != nil {
			t.Fatalf("batch %d: %v", k, err)
		}
	}
	if _, err := s.Create([]CreateSpec{{Title: "over"}}); err == nil {
		t.Errorf("creating past the per-session cap should error")
	}
}
