package resultcap

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// spillFile writes a file that stands in for a kept spill and returns its path.
func spillFile(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("output\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func exists(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Stat(p)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return err == nil
}

// A session's spills stay while it runs and go when it ends. A file kept after
// the end goes at once, and a file of another session stays.
func TestSpillsRemoveWhatTheSessionKept(t *testing.T) {
	dir := testsupport.TempDir(t)
	var mine, theirs Spills
	ctx := WithSpills(context.Background(), &mine)
	a := spillFile(t, dir, "a.log")
	b := spillFile(t, dir, "b.log")
	other := spillFile(t, dir, "other.log")
	Track(ctx, a)
	Track(ctx, b)
	Track(ctx, "") // a cut with no spill records nothing
	Track(WithSpills(context.Background(), &theirs), other)

	if !exists(t, a) || !exists(t, b) {
		t.Fatal("a tracked spill went before its session ended")
	}
	mine.Remove()
	if exists(t, a) || exists(t, b) {
		t.Fatal("a tracked spill outlived its session")
	}
	if !exists(t, other) {
		t.Fatal("one session's end removed another session's spill")
	}

	late := spillFile(t, dir, "late.log")
	Track(ctx, late)
	if exists(t, late) {
		t.Fatal("a spill kept after its session ended was left behind")
	}
}

// A call with no session in its context records its spill for the process.
// RemoveUnowned removes those and leaves the set open, so a later call in the
// same process still keeps its spill until the next removal.
func TestUnownedSpillsGoWithTheProcess(t *testing.T) {
	dir := testsupport.TempDir(t)
	first := spillFile(t, dir, "first.log")
	Track(context.Background(), first)
	if !exists(t, first) {
		t.Fatal("an unowned spill went before the process ended")
	}
	RemoveUnowned()
	if exists(t, first) {
		t.Fatal("RemoveUnowned left an unowned spill")
	}
	second := spillFile(t, dir, "second.log")
	Track(context.Background(), second)
	if !exists(t, second) {
		t.Fatal("a spill kept after RemoveUnowned went at once")
	}
	RemoveUnowned()
}

// The path Cap returns is the one a caller tracks. Tracked against a session,
// the spill of a cut MCP or extension result goes when the session ends.
func TestCapSpillGoesWithTheSession(t *testing.T) {
	useTempDir(t)
	var s Spills
	ctx := WithSpills(context.Background(), &s)
	_, path := Cap([]provider.Content{provider.TextBlock{Text: numbered(2000, 64)}}, "mcp")
	if path == "" {
		t.Fatal("Cap kept no spill for a cut result")
	}
	Track(ctx, path)
	s.Remove()
	if exists(t, path) {
		t.Fatal("the spill of a cut result outlived its session")
	}
}
