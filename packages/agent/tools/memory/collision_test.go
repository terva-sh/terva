package memory

import (
	"os"
	"strings"
	"testing"
)

// The suffix stays; the silence does not. Add must hand the caller the name it
// could not have, so the tool can say what it did.
func TestArchiveReportsTheCollidedName(t *testing.T) {
	a, _ := boundArchive(t)

	first, err := a.Add(ArchiveEntry{
		Name: "build gotchas", Keys: []string{"build"},
		Text: "the first fact, about how the build resolves its tags",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := first.CollidedWith(); got != "" {
		t.Errorf("a free name must not report a collision, got %q", got)
	}

	second, err := a.Add(ArchiveEntry{
		Name: "Build Gotchas!", Keys: []string{"compile"},
		Text: "an entirely separate second fact concerning linker flags on windows",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := second.CollidedWith(); got != first.ID {
		t.Errorf("CollidedWith() = %q, want the taken id %q", got, first.ID)
	}
	if second.ID == first.ID {
		t.Fatalf("both entries took the id %q", first.ID)
	}
	// The property the suffix exists for is untouched: both are still stored.
	if a.Len() != 2 {
		t.Errorf("archive holds %d entries, want 2", a.Len())
	}
}

// collidedWith describes one Add call, not the stored entry. It must not
// survive a round trip through the file, and it must never reach frontmatter.
func TestArchiveCollisionIsNotPersisted(t *testing.T) {
	a, _ := boundArchive(t)

	if _, err := a.Add(ArchiveEntry{
		Name: "build gotchas", Keys: []string{"build"}, Text: "first fact about the build",
	}); err != nil {
		t.Fatal(err)
	}
	second, err := a.Add(ArchiveEntry{
		Name: "build gotchas", Keys: []string{"compile"}, Text: "a different fact about linker flags",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.CollidedWith() == "" {
		t.Fatal("the second add should have reported a collision")
	}

	if err := a.Reload(); err != nil {
		t.Fatal(err)
	}
	for _, e := range a.List() {
		if got := e.CollidedWith(); got != "" {
			t.Errorf("entry %q carried a collision across a reload: %q", e.ID, got)
		}
	}

	// And nothing about it reached the file.
	found, err := a.Find(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(found.Path())
	if err != nil {
		t.Fatalf("read %s: %v", found.Path(), err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "collid") {
		t.Errorf("the transient collision leaked into %s:\n%s", found.Path(), raw)
	}
}
