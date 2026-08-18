package build

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// slug.ValidID exists, in its own words, so that "a '../../etc' id must not
// resolve to a real path". The card stores call it twelve times. The world and
// group stores hand-rolled `id == "" || id != filepath.Base(id)` five times
// instead — and groupstore.go ALREADY IMPORTED slug while doing it.
//
// The two predicates are not equivalent, in the direction that matters:
// filepath.Base("..") is "..", so the hand-rolled check ACCEPTS ".." while
// slug.ValidID rejects it. workspace_worlddoctor.go hands a ctrlproto param
// straight to WorldStore.Get, which then joined it onto the library dir — so
// `id: ".."` read $TERVA_HOME/world.json, one directory above the library.
//
// The FIRST test here passed for the wrong reason and is worth saying so: it
// asserted only that an error came back, and a not-found error from reading a
// path that does not exist satisfies that just as well as a rejected id. So the
// escape is now PLANTED — a real file one directory above the library — and the
// assertion is that the store does not return its contents.
func TestWorldStoreCannotReadOneDirectoryAboveTheLibrary(t *testing.T) {
	home := testsupport.TempDir(t)
	lib := filepath.Join(home, "worlds")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	// $TERVA_HOME/world.json — the exact file the finding names, one level up
	// from the library, planted so a successful escape is observable.
	planted := `{"id":"escaped","name":"NOT-IN-THE-LIBRARY"}`
	if err := os.WriteFile(filepath.Join(home, worldJSONName), []byte(planted), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, worldCoverName), []byte("PNG"), 0o644); err != nil {
		t.Fatal(err)
	}

	ws := &WorldStore{dir: lib}
	doc, err := ws.Get("..")
	if err == nil {
		t.Fatalf("WorldStore.Get(\"..\") escaped the library and returned %+v", doc)
	}
	if strings.Contains(err.Error(), "NOT-IN-THE-LIBRARY") {
		t.Fatalf("the escaped document leaked through the error: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid world id") {
		t.Errorf("Get(\"..\") failed for the wrong reason (%v) — a not-found error is not a rejection, "+
			"and this assertion is what stops the test passing when the fence is gone", err)
	}
	if p := ws.CoverPath(".."); p != "" {
		t.Errorf("CoverPath(\"..\") = %q — this result is handed straight to http.ServeFile", p)
	}
	if err := ws.Delete(".."); err == nil {
		t.Fatal("Delete(\"..\") was accepted; it removes the directory it resolves to")
	} else if !strings.Contains(err.Error(), "invalid world id") {
		t.Errorf("Delete(\"..\") failed for the wrong reason: %v", err)
	}
	// The planted files must still be there.
	if _, err := os.Stat(filepath.Join(home, worldJSONName)); err != nil {
		t.Errorf("the file above the library was deleted: %v", err)
	}
}

// The escapes below are driven through every id-taking method of both stores.
// Each asserts the FENCE fired, by name — not merely that some error came back.
func TestLibraryStoresRejectEveryEscapingID(t *testing.T) {
	escapes := []string{
		"..",    // the one the hand-rolled predicate accepted
		"../..", //
		"a/b",   // a separator
		`a\b`,   // the Windows separator, which filepath.Base does not split on POSIX
		"",      // empty
		"   ",   // whitespace only, after the stores' TrimSpace
		"foo/../../bar",
	}
	fenced := func(t *testing.T, what string, err error, noun string) {
		t.Helper()
		if err == nil {
			t.Errorf("%s was accepted", what)
			return
		}
		if !strings.Contains(err.Error(), "invalid "+noun+" id") {
			t.Errorf("%s failed for the wrong reason (%v); the fence did not fire", what, err)
		}
	}

	for _, id := range escapes {
		t.Run("world/"+strconv.Quote(id), func(t *testing.T) {
			dir := testsupport.TempDir(t)
			ws := &WorldStore{dir: dir}
			_, err := ws.Get(id)
			fenced(t, "WorldStore.Get("+strconv.Quote(id)+")", err, "world")
			fenced(t, "WorldStore.Delete("+strconv.Quote(id)+")", ws.Delete(id), "world")
			if p := ws.CoverPath(id); p != "" {
				t.Errorf("WorldStore.CoverPath(%q) = %q, want \"\" — this result is handed to http.ServeFile", id, p)
			}
		})
		t.Run("group/"+strconv.Quote(id), func(t *testing.T) {
			dir := testsupport.TempDir(t)
			gs := &GroupStore{dir: dir}
			_, err := gs.Get(id)
			fenced(t, "GroupStore.Get("+strconv.Quote(id)+")", err, "group")
			fenced(t, "GroupStore.Delete("+strconv.Quote(id)+")", gs.Delete(id), "group")
		})
	}
}

// The complement. Without it, a fence that refused everything would satisfy the
// test above while breaking every library in the product.
func TestLibraryStoresStillAcceptAnOrdinaryID(t *testing.T) {
	dir := testsupport.TempDir(t)
	ws := &WorldStore{dir: dir}
	// A missing world is a not-found error from the READ, not a rejected id.
	_, err := ws.Get("a-normal-id")
	if err == nil {
		t.Fatal("expected a not-found error for a world that was never saved")
	}
	if strings.Contains(err.Error(), "invalid world id") {
		t.Errorf("an ordinary id was rejected by the fence: %v", err)
	}
	gs := &GroupStore{dir: dir}
	if _, err := gs.Get("a-normal-id"); err != nil && strings.Contains(err.Error(), "invalid group id") {
		t.Errorf("an ordinary id was rejected by the fence: %v", err)
	}
}

// And the weaker predicate must not come back. This is the shape that regressed:
// someone reaching for a containment check and writing the obvious-looking one
// rather than importing the fence. A behavioural table can only cover the stores
// it knows about; this covers the mistake itself, wherever it is made next.
func TestNothingHandRollsTheWeakerIDFence(t *testing.T) {
	const root = "../../.."
	// The predicate is `X != filepath.Base(X)` — the SAME identifier on both
	// sides. That precision is deliberate, and was earned: a looser needle
	// flagged extcmd.go's `mn != filepath.Base(out)`, which compares two
	// different things and is not a containment check at all. Comment lines are
	// skipped for the same reason — this file's own prose quotes the pattern,
	// and slug.ValidID's doc comment names it to explain why it is wrong.
	pattern := regexp.MustCompile(`(\w+)\s*!=\s*filepath\.Base\(\s*(\w+)\s*\)`)
	var offenders []string
	var scanned int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if testsupport.SkipScanDir(root, path, d) {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if m := pattern.FindStringSubmatch(line); m != nil && m[1] == m[2] {
				offenders = append(offenders, filepath.ToSlash(path)+":"+strconv.Itoa(i+1))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 500 {
		t.Fatalf("only %d production Go files were scanned; the walk is broken and this gate proves nothing", scanned)
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("`x != filepath.Base(x)` is not a containment check — filepath.Base(\"..\") is \"..\", so it "+
			"accepts the one id that escapes. Use slug.ValidID:\n  %s", strings.Join(offenders, "\n  "))
	}
}
