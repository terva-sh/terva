package memory

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func boundStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := testsupport.TempDir(t)
	s := NewStore()
	if err := s.Rebind(dir); err != nil {
		t.Fatalf("Rebind: %v", err)
	}
	return s, dir
}

func TestStoreAddListRoundTrip(t *testing.T) {
	s, dir := boundStore(t)
	if err := s.Add("uses pnpm, not npm"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Add("tests live in crates/*/tests"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := s.List(); len(got) != 2 || got[0] != "uses pnpm, not npm" {
		t.Fatalf("List = %v", got)
	}

	// A second store over the same dir sees them — the file is the truth.
	other := NewStore()
	if err := other.Rebind(dir); err != nil {
		t.Fatal(err)
	}
	if got := other.List(); len(got) != 2 {
		t.Fatalf("reload from disk = %v, want 2 entries", got)
	}
}

// The file is meant to be hand-editable, so parsing must key off the bullet and
// tolerate anything else in the file.
func TestStoreToleratesHandEdits(t *testing.T) {
	s, dir := boundStore(t)
	if err := s.Add("one"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, projectFileName)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := string(b) + "\nsome prose a human typed\n- two\n## a heading\n"
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	got := s.List()
	if len(got) != 2 || got[1] != "two" {
		t.Fatalf("hand-edited file parsed as %v, want [one two]", got)
	}
}

// Control characters in a model-authored entry must not reach the file, or the
// injected block becomes a display-injection channel.
func TestStoreSanitizesEntries(t *testing.T) {
	s, _ := boundStore(t)
	if err := s.Add("line one\nline two\x1b[31m red"); err != nil {
		t.Fatal(err)
	}
	got := s.List()[0]
	if strings.ContainsAny(got, "\n\x1b") {
		t.Fatalf("entry kept control characters: %q", got)
	}
	if !strings.Contains(got, "line one line two") {
		t.Fatalf("entry = %q, want the newline collapsed to a space", got)
	}
}

func TestStoreReplaceAndRemove(t *testing.T) {
	s, _ := boundStore(t)
	for _, e := range []string{"alpha fact", "beta fact", "gamma fact"} {
		if err := s.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Replace("beta", "beta fact, revised"); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if got := s.List()[1]; got != "beta fact, revised" {
		t.Fatalf("after Replace = %q", got)
	}
	if err := s.Remove("alpha"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := s.List(); len(got) != 2 || got[0] != "beta fact, revised" {
		t.Fatalf("after Remove = %v", got)
	}
	// "fact" is in all of them — ambiguous, and the error must say so rather
	// than picking one.
	err := s.Remove("fact")
	if err == nil || !strings.Contains(err.Error(), "more specific") {
		t.Fatalf("ambiguous match error = %v", err)
	}
}

// F1: a cap refusal the model cannot act on without knowing what is stored cost
// a turn per refusal in a reviewed session — four of them — even though the
// success path returns exactly the listing that was withheld. Sizes ride along
// because the budget is in bytes: "remove a stale entry" is unactionable when
// every entry looks the same length.
func TestStoreBudgetErrorCarriesTheListing(t *testing.T) {
	s, _ := boundStore(t)
	// Fill until the store refuses, rather than filling to a computed offset
	// and assuming one more entry tips it. The offset version depended on
	// arithmetic that happened to work at a 6144-byte cap and silently stopped
	// guaranteeing overflow when the cap moved.
	//
	// The uniqueness marker must be unbounded for the same reason: a 26-letter
	// cycle was enough entries to fill 6 KiB and is not enough to fill 16 KiB,
	// so it started colliding with the duplicate check before reaching the
	// budget. Bytes bind before the count cap (TestBytesBindBeforeTheEntryCount),
	// so the first refusal here is the budget one.
	var err error
	for i := 0; i < MaxEntries; i++ {
		if err = s.Add(strings.Repeat("x", 300) + strconv.Itoa(i) + strings.Repeat("y", 90)); err != nil {
			break
		}
	}
	if err == nil {
		t.Fatal("expected a budget refusal")
	}
	msg := err.Error()
	if !strings.Contains(msg, "byte budget") {
		t.Fatalf("not the budget error: %q", msg)
	}
	if !strings.Contains(msg, LabelProject+" currently holds:") {
		t.Errorf("budget error withholds the listing:\n%s", msg)
	}
	if !strings.Contains(msg, "B) ") {
		t.Errorf("budget error withholds per-entry sizes:\n%s", msg)
	}
	// Every stored entry must be nameable from the error.
	for i, e := range s.List() {
		if !strings.Contains(msg, e) {
			t.Errorf("entry %d missing from the refusal: %q", i, e)
		}
	}
}

// F2: a replace-miss means the model reached for an entry it believed was there.
// The useful reply is what IS there.
func TestStoreReplaceMissListsCandidates(t *testing.T) {
	s, _ := boundStore(t)
	for _, e := range []string{"uses pnpm", "tests in crates"} {
		if err := s.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	err := s.Replace("some entry that drifted", "new text")
	if err == nil {
		t.Fatal("expected a miss")
	}
	msg := err.Error()
	if !strings.Contains(msg, "currently holds:") {
		t.Fatalf("miss withholds the listing:\n%s", msg)
	}
	for _, e := range []string{"uses pnpm", "tests in crates"} {
		if !strings.Contains(msg, e) {
			t.Errorf("candidate %q missing from the miss:\n%s", e, msg)
		}
	}

	// An EMPTY store says so instead of printing an empty list, which reads as
	// a rendering bug rather than a fact.
	empty := NewStore()
	if err := empty.Rebind(testsupport.TempDir(t)); err != nil {
		t.Fatal(err)
	}
	err = empty.Replace("anything", "x")
	if err == nil || !strings.Contains(err.Error(), "is empty") {
		t.Fatalf("empty-store miss = %v", err)
	}
}

func TestStoreRejectsDuplicates(t *testing.T) {
	s, _ := boundStore(t)
	if err := s.Add("Uses pnpm, not npm."); err != nil {
		t.Fatal(err)
	}
	// Exact after normalization (case, trailing punctuation).
	if err := s.Add("uses pnpm, not npm"); err == nil || !strings.Contains(err.Error(), "already saved") {
		t.Fatalf("normalized duplicate = %v", err)
	}
	// A genuinely distinct fact that shares vocabulary still goes through —
	// the threshold is deliberately conservative.
	if err := s.Add("pnpm workspaces are defined in pnpm-workspace.yaml at the root"); err != nil {
		t.Fatalf("distinct fact was blocked: %v", err)
	}
}

// The store is a cache over the file, never the truth. Two instances sharing a
// project must MERGE rather than the last writer clobbering the other's delta —
// the reason every mutation re-reads under the lock.
func TestStoreMergesConcurrentWriters(t *testing.T) {
	dir := testsupport.TempDir(t)
	a, b := NewStore(), NewStore()
	if err := a.Rebind(dir); err != nil {
		t.Fatal(err)
	}
	if err := b.Rebind(dir); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func(i int) { defer wg.Done(); _ = a.Add("from a " + string(rune('0'+i))) }(i)
		go func(i int) { defer wg.Done(); _ = b.Add("from b " + string(rune('0'+i))) }(i)
	}
	wg.Wait()

	final := NewStore()
	if err := final.Rebind(dir); err != nil {
		t.Fatal(err)
	}
	got := final.List()
	if len(got) != 20 {
		t.Fatalf("merged file holds %d entries, want 20 — a writer clobbered the other's delta:\n%v", len(got), got)
	}
}

// An unbound store (--no-session, no resolvable project) must work in memory and
// write nothing, rather than erroring or picking a directory of its own.
func TestUnboundStoreIsInMemory(t *testing.T) {
	s := NewStore()
	if err := s.Add("ephemeral"); err != nil {
		t.Fatalf("Add on an unbound store: %v", err)
	}
	if got := s.List(); len(got) != 1 {
		t.Fatalf("unbound List = %v", got)
	}
}

func TestUserStoreIsASeparateScope(t *testing.T) {
	dir := testsupport.TempDir(t)
	proj, user := NewStore(), NewUserStore()
	if err := proj.Rebind(dir); err != nil {
		t.Fatal(err)
	}
	if err := user.Rebind(dir); err != nil {
		t.Fatal(err)
	}
	if err := proj.Add("a repo fact"); err != nil {
		t.Fatal(err)
	}
	if err := user.Add("a person fact"); err != nil {
		t.Fatal(err)
	}
	if got := proj.List(); len(got) != 1 || got[0] != "a repo fact" {
		t.Errorf("project scope leaked: %v", got)
	}
	if got := user.List(); len(got) != 1 || got[0] != "a person fact" {
		t.Errorf("user scope leaked: %v", got)
	}
	if user.Label() != LabelUser || proj.Label() != LabelProject {
		t.Errorf("labels = %q / %q", user.Label(), proj.Label())
	}
	// Different files, so the two caps apply independently.
	for _, n := range []string{projectFileName, userFileName} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s not written: %v", n, err)
		}
	}
}

// byteLen is a test helper: the serialized size the caps are measured against.
func (s *Store) byteLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(serialize(s.header, s.entries))
}
