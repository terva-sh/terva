package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/agent/tools/memory"
	"terva.sh/terva/packages/testsupport"
)

func archiveFixture(t *testing.T) *memory.Archive {
	t.Helper()
	a := memory.NewArchive(memory.ScopeProject, memory.LabelProject)
	if _, err := a.Add(memory.ArchiveEntry{
		Name: "cutting a release", Keys: []string{"release", "tag"},
		Text: "Run `just release-status` first; it is the oracle.",
	}); err != nil {
		t.Fatal(err)
	}
	return a
}

// The pane's archived rows carry the retrieval spec and the entry's own Ref.
// Keys, because the archive fails SILENTLY — an entry keyed on the answer's
// vocabulary never fires and produces nothing to notice — and Ref, because
// that is what an action addresses.
func TestMemoryScopeCarriesTheArchiveOntoTheWire(t *testing.T) {
	store, arch := memory.NewStore(), archiveFixture(t)
	got := memoryScope(store, arch, nil)

	// The active tier's fill fraction must be EXACTLY the store's own budget,
	// unchanged by the presence of an archive. That fraction is what refuses the
	// model's next write, and inflating it with a tier that costs nothing would
	// report the opposite of the truth.
	wantBytes, wantMax, _ := store.Budget()
	if got.Bytes != wantBytes || got.MaxBytes != wantMax {
		t.Errorf("active fill = %d/%d, want the store's own %d/%d", got.Bytes, got.MaxBytes, wantBytes, wantMax)
	}
	if got.ArchivedBytes != arch.Bytes() || got.ArchivedBytes == 0 {
		t.Errorf("ArchivedBytes = %d, want the archive's own %d", got.ArchivedBytes, arch.Bytes())
	}

	if len(got.Archived) != 1 {
		t.Fatalf("archived entries on the wire = %d, want 1", len(got.Archived))
	}
	e := got.Archived[0]
	if e.Ref != "project:cutting-a-release" {
		t.Errorf("Ref = %q, want the scope-qualified id an action can send back", e.Ref)
	}
	if len(e.Keys) != 2 {
		t.Errorf("triggers did not reach the pane: %v", e.Keys)
	}
	if e.Title != "cutting a release" {
		t.Errorf("Title = %q", e.Title)
	}
	if got.ArchivedMaxBytes != memory.MaxArchiveBytes {
		t.Errorf("ArchivedMaxBytes = %d, want the archive's own cap", got.ArchivedMaxBytes)
	}
}

// The last turn's trace is joined onto the entries it names. Fired and
// budget-dropped are carried separately because they are indistinguishable from
// outside — neither reached the model — and need opposite fixes.
func TestMemoryScopeJoinsTheActivationTrace(t *testing.T) {
	arch := archiveFixture(t)
	ref := arch.List()[0].Ref()

	idx := recallIndex([]memory.RecallFired{{Ref: ref, Keys: []string{"release"}, Dropped: true}})
	got := memoryScope(nil, arch, idx)
	if len(got.Archived) != 1 {
		t.Fatalf("want 1 entry, got %d", len(got.Archived))
	}
	e := got.Archived[0]
	if !e.Fired || !e.DroppedForBudget {
		t.Errorf("trace lost: fired=%v dropped=%v", e.Fired, e.DroppedForBudget)
	}
	if len(e.MatchedKeys) != 1 || e.MatchedKeys[0] != "release" {
		t.Errorf("matched keys = %v", e.MatchedKeys)
	}

	// An entry the trace does not name is not fired — and must not inherit the
	// flags of one that is.
	plain := memoryScope(nil, arch, recallIndex([]memory.RecallFired{{Ref: "project:something-else"}}))
	if plain.Archived[0].Fired || plain.Archived[0].DroppedForBudget {
		t.Errorf("an unmatched entry was marked as fired: %+v", plain.Archived[0])
	}
}

// Unreadable archive files ride the wire. Such a file is INERT — present,
// counted against the budget, unable to fire, with no other symptom — so the
// pane is the only place anyone learns it exists.
func TestUnreadableArchiveFilesReachTheWire(t *testing.T) {
	arch := memory.NewArchive(memory.ScopeProject, memory.LabelProject)
	dir := testsupport.TempDir(t)
	if err := arch.Rebind(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.md"), []byte("---\nkeys: [oops\n---\n\nbody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := arch.Reload(); err != nil {
		t.Fatal(err)
	}
	got := memoryScope(nil, arch, nil)
	if len(got.Problems) != 1 {
		t.Fatalf("problems on the wire = %v, want the unreadable file", got.Problems)
	}
}

// The archive verbs resolve the same scope argument the store verbs do. They are
// resolved by SEPARATE calls, so a scope that means "project" to one and
// something else to the other would let a forget land in the wrong archive.
func TestArchiveScopeArgAgreesWithTheStoreScopeArg(t *testing.T) {
	mt := &tools.MemoryTool{
		Project:        memory.NewStore(),
		User:           memory.NewUserStore(),
		ProjectArchive: memory.NewArchive(memory.ScopeProject, memory.LabelProject),
		UserArchive:    memory.NewArchive(memory.ScopeUser, memory.LabelUser),
	}
	for _, tc := range []struct {
		scope string
		want  *memory.Archive
	}{
		{"", mt.ProjectArchive},
		{memory.ScopeProject, mt.ProjectArchive},
		{memory.ScopeUser, mt.UserArchive},
		{"USER", mt.UserArchive},
	} {
		if got := memoryArchiveArg(mt, map[string]string{"scope": tc.scope}); got != tc.want {
			t.Errorf("scope %q resolved to the wrong archive", tc.scope)
		}
		// And the store agrees about which scope that was, for every value the
		// archive accepts.
		store, err := memoryScopeArg(mt, map[string]string{"scope": tc.scope})
		if err != nil {
			t.Errorf("scope %q: store refused what the archive accepted: %v", tc.scope, err)
			continue
		}
		wantUser := tc.want == mt.UserArchive
		if (store == mt.User) != wantUser {
			t.Errorf("scope %q: store and archive disagree about the scope", tc.scope)
		}
	}
}
