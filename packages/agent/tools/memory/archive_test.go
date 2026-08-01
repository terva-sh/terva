package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/lore"
	"terva.sh/terva/packages/testsupport"
)

// pinnedClock is a fixed timestamp source. The swarm archive's id-collision
// guard passed without its guard until its clock was pinned; the same shape
// applies here, where "archived" ends up in the file and in the sort order.
func pinnedClock() func() time.Time {
	t0 := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	n := 0
	return func() time.Time {
		n++
		return t0.Add(time.Duration(n) * time.Second)
	}
}

func boundArchive(t *testing.T) (*Archive, string) {
	t.Helper()
	dir := testsupport.TempDir(t)
	a := NewArchive(ScopeProject, LabelProject)
	a.SetClock(pinnedClock())
	if err := a.Rebind(dir); err != nil {
		t.Fatalf("Rebind: %v", err)
	}
	return a, dir
}

// The write path is a struct in this package and the read path is
// lore.ParseEntry, which is the only thing making "no new parser" true. Nothing
// but this test stops them drifting: a renamed yaml tag on the write side would
// simply stop being read, and the symptom is an entry that silently never fires.
func TestArchiveFilesRoundTripThroughLore(t *testing.T) {
	a, dir := boundArchive(t)
	in := ArchiveEntry{
		Name:          "model catalog",
		Keys:          []string{"model", "catalog", "add a model"},
		SecondaryKeys: []string{"anthropic", "openai"},
		Logic:         lore.LogicAndAny,
		Order:         250,
		Text:          "Curated and speculative entries live in packages/provider/models.go.\n\nMergeCatalog promotes matching speculative ids.",
	}
	stored, err := a.Add(in)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, stored.ID+archiveFileExt))
	if err != nil {
		t.Fatal(err)
	}
	le, ok, err := lore.ParseEntry(string(raw), "test")
	if err != nil || !ok {
		t.Fatalf("lore could not read back what we wrote: ok=%v err=%v\n%s", ok, err, raw)
	}
	if le.Name != in.Name {
		t.Errorf("name = %q, want %q", le.Name, in.Name)
	}
	if strings.Join(le.Keys, "|") != strings.Join(in.Keys, "|") {
		t.Errorf("keys = %v, want %v", le.Keys, in.Keys)
	}
	if strings.Join(le.SecondaryKeys, "|") != strings.Join(in.SecondaryKeys, "|") {
		t.Errorf("secondary_keys = %v, want %v", le.SecondaryKeys, in.SecondaryKeys)
	}
	if le.Order != in.Order {
		t.Errorf("order = %d, want %d", le.Order, in.Order)
	}
	if le.Constant {
		t.Error("an archived entry was written as constant")
	}
	// And the body survives its blank line — the tier exists so entries can be
	// longer than one flattened line.
	if !strings.Contains(le.Content, "MergeCatalog promotes") {
		t.Errorf("body did not survive:\n%s", le.Content)
	}
	if !strings.Contains(le.Content, "\n\n") {
		t.Errorf("multi-line body was flattened:\n%q", le.Content)
	}

	// The reload must reconstruct the same entry, including the timestamp that
	// only exists because we wrote it.
	if err := a.Reload(); err != nil {
		t.Fatal(err)
	}
	got := a.List()
	if len(got) != 1 {
		t.Fatalf("reload found %d entries, want 1", len(got))
	}
	if got[0].Archived.IsZero() {
		t.Error("archived timestamp did not survive the round trip")
	}
	if got[0].Order != in.Order {
		t.Errorf("reloaded order = %d, want %d", got[0].Order, in.Order)
	}
}

// The whole design rests on this: an entry keyed on the QUESTION's vocabulary
// fires on a question phrased by someone who has not read the entry, and an
// entry keyed on its own ANSWER does not.
//
// Both halves are asserted, because the failure is silent — a spec that misses
// produces no output rather than wrong output, so a test that only proves the
// good spec works would pass just as happily if matching were broken open.
func TestArchivedEntryFiresOnTheQuestionNotTheAnswer(t *testing.T) {
	a, _ := boundArchive(t)
	body := "Curated and speculative entries live in packages/provider/models.go; MergeCatalog " +
		"promotes matching speculative ids, and anthropic is not in discoveryAuthoritative."

	if _, err := a.Add(ArchiveEntry{
		Name: "asks like a user", Keys: []string{"model", "catalog"}, Text: body,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Add(ArchiveEntry{
		Name: "asks like the source",
		Keys: []string{"MergeCatalog", "discoveryAuthoritative"},
		Text: "A second entry about the same subject, keyed on its own identifiers.",
	}); err != nil {
		t.Fatal(err)
	}

	question := []string{"add the new Opus model to the catalog"}
	block, fired := Recall(question, a)
	if !strings.Contains(block, "packages/provider/models.go") {
		t.Errorf("the question-keyed entry did not fire on the question:\n%s", block)
	}
	if strings.Contains(block, "keyed on its own identifiers") {
		t.Errorf("the answer-keyed entry fired on a question that contains none of its keys:\n%s", block)
	}
	if len(fired) != 1 || fired[0].Dropped {
		t.Errorf("trace = %+v, want exactly one kept entry", fired)
	}

	// And an unrelated turn recalls nothing at all: an archive that fires on
	// everything costs tail bytes every turn and teaches the model to ignore it.
	if block, _ := Recall([]string{"rename this CSS class"}, a); block != "" {
		t.Errorf("archived memory fired on an unrelated turn:\n%s", block)
	}
}

// Whole-word matching, unlike lore's substring default. Memory keys are ordinary
// English words, so "add" firing on "address" would be a routine occurrence
// rather than an edge case.
func TestRecallDoesNotFireOnSubstringsOfOtherWords(t *testing.T) {
	a, _ := boundArchive(t)
	if _, err := a.Add(ArchiveEntry{
		Name: "adding things", Keys: []string{"add", "test"}, Text: "how to add a thing",
	}); err != nil {
		t.Fatal(err)
	}
	if block, _ := Recall([]string{"update the mailing address in the latest draft"}, a); block != "" {
		t.Errorf("keys fired on substrings (address/latest):\n%s", block)
	}
	if block, _ := Recall([]string{"how do I add one of these"}, a); block == "" {
		t.Error("the key did not fire on a genuine whole-word match")
	}
}

// An archived entry marked constant would fire on EVERY turn from the uncached
// tail — paying per-turn forever for what the active tier gets from the cached
// prefix at a cache-hit rate. It is the one flag that inverts the tier's purpose,
// so a hand-edited file carrying it is refused with the verb that does the job.
func TestAConstantArchivedEntryIsRefused(t *testing.T) {
	a, dir := boundArchive(t)
	raw := "---\nname: always\nkeys: [x]\nconstant: true\n---\n\nsome fact\n"
	if err := os.WriteFile(filepath.Join(dir, "always.md"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Reload(); err != nil {
		t.Fatal(err)
	}
	if n := a.Len(); n != 0 {
		t.Fatalf("a constant archived entry was loaded (%d entries)", n)
	}
	probs := a.Problems()
	if len(probs) != 1 || !strings.Contains(probs[0], "constant") {
		t.Fatalf("problems = %v, want one naming `constant`", probs)
	}
	if !strings.Contains(probs[0], "Promote") {
		t.Errorf("the refusal does not name the verb that does what was wanted: %q", probs[0])
	}
}

// An entry with no keys can never activate, so storing one stores something
// unreachable by every path except an explicit search. Refused at the door, with
// the reason that actually matters — which vocabulary to key on.
func TestArchiveRefusesAnEntryWithNoKeys(t *testing.T) {
	a, _ := boundArchive(t)
	_, err := a.Add(ArchiveEntry{Name: "orphan", Text: "a fact nothing can reach"})
	if err == nil {
		t.Fatal("an entry with no keys was accepted")
	}
	if !strings.Contains(err.Error(), "never fire") {
		t.Errorf("refusal does not say what is wrong: %v", err)
	}
	if a.Len() != 0 {
		t.Error("the refused entry was stored anyway")
	}
}

// Two entries whose titles slug to the same stem must both survive under
// distinct ids. The id is the model's only handle on an archived entry, so a
// collision that overwrote would delete a memory while reporting success.
func TestArchiveMintsDistinctIDsOnACollision(t *testing.T) {
	a, _ := boundArchive(t)
	first, err := a.Add(ArchiveEntry{Name: "build gotchas", Keys: []string{"build"}, Text: "first fact about building"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Add(ArchiveEntry{Name: "Build Gotchas!", Keys: []string{"compile"}, Text: "an entirely different second fact"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatalf("both entries minted the id %q", first.ID)
	}
	if a.Len() != 2 {
		t.Fatalf("archive holds %d entries, want 2 — one overwrote the other", a.Len())
	}
	// Both remain addressable, which is the property the ids exist for.
	for _, id := range []string{first.ID, second.ID} {
		if _, err := a.Find(id); err != nil {
			t.Errorf("Find(%q): %v", id, err)
		}
	}
}

// The F1 lesson, applied to the new cap: a refusal the model cannot act on
// without knowing what is stored costs a turn per refusal. The byte cap here is
// also the session-start read cost, so the message says which budget it is.
func TestArchiveBudgetRefusalNamesWhatIsStored(t *testing.T) {
	a, dir := boundArchive(t)
	body := strings.Repeat("x", MaxArchiveEntryBytes-64)

	// Seed most of the way by writing files directly, then let Add push it over.
	// Filling entirely through Add would be more faithful but is quadratic — each
	// one re-reads the whole archive — and the refusal is what is under test, not
	// the write path, which every other test here exercises.
	//
	// Still "add until it refuses" rather than "add exactly one and assume it
	// tips": the store's own budget test used a computed offset and silently
	// stopped guaranteeing overflow the moment a cap moved.
	seeded := 0
	for n := 0; n < MaxArchiveBytes/MaxArchiveEntryBytes; n++ {
		raw := "---\nname: seed" + itoa(n) + "\nkeys: [seed" + itoa(n) + "]\n---\n\n" + body + itoa(n) + "\n"
		if seeded+len(raw) > MaxArchiveBytes-2*MaxArchiveEntryBytes {
			break
		}
		if err := os.WriteFile(filepath.Join(dir, "seed"+itoa(n)+".md"), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		seeded += len(raw)
	}
	if err := a.Reload(); err != nil {
		t.Fatal(err)
	}
	if a.Len() == 0 {
		t.Fatal("seeding produced no entries; this guard would pass vacuously")
	}

	var err error
	for i := 0; i < 10; i++ {
		// "add" in the body so these never collide with the seeded texts — the
		// duplicate check fires before the budget one and would mask it.
		_, err = a.Add(ArchiveEntry{
			Name: "bulk" + itoa(i), Keys: []string{"k" + itoa(i)}, Text: body + " add " + itoa(i),
		})
		if err != nil {
			break
		}
	}
	if err == nil {
		t.Fatal("the archive never refused; the byte cap is not binding")
	}
	msg := err.Error()
	if !strings.Contains(msg, "byte budget") {
		t.Fatalf("not the budget refusal: %q", msg)
	}
	if !strings.Contains(msg, "session start") {
		t.Errorf("the refusal does not say what the budget protects:\n%s", msg)
	}
	if !strings.Contains(msg, "holds:") {
		t.Errorf("the refusal withholds the listing:\n%s", msg)
	}
	for _, e := range a.List() {
		if !strings.Contains(msg, e.ID) {
			t.Errorf("entry %q missing from the refusal", e.ID)
			break
		}
	}
}

// A file that cannot be parsed is INERT: on disk, occupying the budget, never
// firing, with no other symptom whatsoever. Skipping it silently is how an entry
// stops working with nobody able to say why, so it is collected and surfaced.
func TestAnUnreadableArchiveFileIsReportedNotSwallowed(t *testing.T) {
	a, dir := boundArchive(t)
	if _, err := a.Add(ArchiveEntry{Name: "good", Keys: []string{"good"}, Text: "a readable fact"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.md"), []byte("---\nkeys: [oops\n---\n\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Reload(); err != nil {
		t.Fatalf("one bad file took the whole archive offline: %v", err)
	}
	if a.Len() != 1 {
		t.Errorf("the good entry did not survive its broken neighbour: %d entries", a.Len())
	}
	probs := a.Problems()
	if len(probs) != 1 || !strings.Contains(probs[0], "broken.md") {
		t.Fatalf("problems = %v, want one naming broken.md", probs)
	}
}

// Search is text-based, deliberately NOT scored through the retrieval spec. It
// exists for the case where the spec MISSED, and scoring it through the same
// spec would answer that case with the same silence.
func TestArchiveSearchFindsWhatTheKeysWouldMiss(t *testing.T) {
	a, _ := boundArchive(t)
	if _, err := a.Add(ArchiveEntry{
		Name: "worktree layout",
		Keys: []string{"zzz-a-key-nobody-would-type"},
		Text: "Implementation worktrees live under .claude/worktrees and the primary stays on sothr-main.",
	}); err != nil {
		t.Fatal(err)
	}
	// Keyed recall cannot reach it — that is the premise.
	if block, _ := Recall([]string{"where do worktrees live"}, a); block != "" {
		t.Fatalf("precondition failed: the entry fired on keys it does not have:\n%s", block)
	}
	hits := a.Search("worktrees", 5)
	if len(hits) != 1 {
		t.Fatalf("search found %d entries, want the one whose spec missed", len(hits))
	}
	if !strings.Contains(hits[0].Text, "sothr-main") {
		t.Errorf("search returned the wrong entry: %+v", hits[0])
	}
}

// An unbound archive (--no-session, no resolvable project) accepts entries for
// the session and writes nothing, matching Store rather than erroring or picking
// a directory of its own.
func TestUnboundArchiveIsInMemory(t *testing.T) {
	a := NewArchive(ScopeProject, LabelProject)
	a.SetClock(pinnedClock())
	e, err := a.Add(ArchiveEntry{Name: "ephemeral", Keys: []string{"eph"}, Text: "a fact for this session only"})
	if err != nil {
		t.Fatalf("Add on an unbound archive: %v", err)
	}
	if e.Path() != "" {
		t.Errorf("an unbound archive wrote to %q", e.Path())
	}
	if a.Len() != 1 {
		t.Fatalf("unbound archive holds %d entries", a.Len())
	}
	if block, _ := Recall([]string{"tell me about eph"}, a); !strings.Contains(block, "this session only") {
		t.Errorf("an unbound archive does not participate in recall:\n%s", block)
	}
}

// A budget drop and a spec that never matches look identical from outside — no
// output either way — and they need opposite fixes. The trace has to tell them
// apart.
func TestRecallTracesBudgetDropsSeparatelyFromMisses(t *testing.T) {
	a, _ := boundArchive(t)
	big := strings.Repeat("padding words that cost tokens. ", 200) // ~1600 tokens each
	for _, n := range []string{"one", "two", "three"} {
		if _, err := a.Add(ArchiveEntry{
			Name: n, Keys: []string{"shared"}, Text: n + " " + big,
		}); err != nil {
			t.Fatal(err)
		}
	}
	block, fired := Recall([]string{"tell me about the shared thing"}, a)
	if len(fired) != 3 {
		t.Fatalf("trace has %d entries, want all 3 that matched", len(fired))
	}
	kept, dropped := 0, 0
	for _, f := range fired {
		if f.Dropped {
			dropped++
		} else {
			kept++
		}
		if len(f.Keys) == 0 {
			t.Errorf("%s fired with no recorded trigger key", f.Ref)
		}
	}
	if dropped == 0 {
		t.Fatalf("the budget dropped nothing; %d entries of ~1600 tokens fit in %d", len(fired), recallTokenBudget)
	}
	if kept == 0 {
		t.Error("the budget dropped everything; at least the top-priority entry must survive")
	}
	if block == "" {
		t.Error("entries fired and were kept, but the block is empty")
	}
}

// The scope qualifies every id the model can see, because both archives are
// matched into ONE block and a bare id would be ambiguous exactly when both
// scopes hold something on the same subject.
func TestRecalledEntriesAreNamedByScopeAndID(t *testing.T) {
	proj := NewArchive(ScopeProject, LabelProject)
	proj.SetClock(pinnedClock())
	user := NewArchive(ScopeUser, LabelUser)
	user.SetClock(pinnedClock())
	for _, a := range []*Archive{proj, user} {
		if _, err := a.Add(ArchiveEntry{Name: "review", Keys: []string{"review"}, Text: "how " + a.Scope() + " reviews go"}); err != nil {
			t.Fatal(err)
		}
	}
	block, fired := Recall([]string{"walk me through a review"}, proj, user)
	for _, want := range []string{"[project:review]", "[user:review]"} {
		if !strings.Contains(block, want) {
			t.Errorf("block does not name %s:\n%s", want, block)
		}
	}
	if len(fired) != 2 {
		t.Fatalf("both scopes should have fired, trace = %+v", fired)
	}
	// One Select over both scopes, so they share the budget rather than each
	// spending it — which is the only reading of "the turn has a budget" that
	// means anything.
	for _, f := range fired {
		if !strings.Contains(f.Ref, ":") {
			t.Errorf("trace ref %q is not scope-qualified", f.Ref)
		}
	}
}

// Everywhere the model can SEE an archived entry names it "project:the-id" — the
// recall block, search results, the archive index. So that spelling has to be
// the one it can hand back.
//
// Written after the fact: the id the tail advertised was not the id Find
// accepted, and nothing failed. Every recall, promote and forget the model
// derived from what it had just read would have missed, and the reply would have
// been a perfectly clear "nothing matches" naming the id it was looking at.
func TestAnEntryIsAddressableByTheNameEverySurfaceShows(t *testing.T) {
	a, _ := boundArchive(t)
	stored, err := a.Add(ArchiveEntry{
		Name: "the deploy target", Keys: []string{"deploy"}, Text: "Deploys go to staging first.",
	})
	if err != nil {
		t.Fatal(err)
	}

	// What the model reads, on each surface.
	block, _ := Recall([]string{"let's deploy"}, a)
	for name, surface := range map[string]string{
		"recall block":  block,
		"archive index": RenderArchiveList(a.Label(), a.List()),
		"search":        RenderSearchResults(a.Label(), "deploy", a.Search("deploy", 5)),
	} {
		if !strings.Contains(surface, "["+stored.Ref()+"]") {
			t.Errorf("%s does not name the entry %q:\n%s", name, stored.Ref(), surface)
		}
	}

	// What it can hand back. Both forms, because the bare id is what a hand-edit
	// or a directory listing shows.
	for _, m := range []string{stored.Ref(), stored.ID} {
		if _, err := a.Find(m); err != nil {
			t.Errorf("Find(%q): %v", m, err)
		}
	}

	// But a prefix naming a DIFFERENT scope is not silently accepted — that
	// would resolve a user-scoped reference against the project archive and act
	// on the wrong entry.
	if _, err := a.Find("user:" + stored.ID); err == nil {
		t.Error("a reference to another scope resolved against this archive")
	}
}

// itoa avoids pulling strconv into the test file for one call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
