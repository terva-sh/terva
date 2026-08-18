package testsupport

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// walksOutsideTheRepo lists the test files that call filepath.Walk/WalkDir over
// something that is NOT this repository's source tree, with the reason. A walk
// over a temp dir, an install output, or an embedded FS has no nested checkouts
// to blunder into, so SkipScanDir is not merely unnecessary there — it would be
// wrong, since those trees legitimately contain names it prunes.
//
// Everything else that walks must consult SkipScanDir. Adding an entry here is
// a claim that the walk cannot reach repository source; check before adding.
var walksOutsideTheRepo = map[string]string{
	filepath.Join("examples", "embed_test.go"):                   "walks the embedded FS roots, not a directory on disk",
	filepath.Join("packages", "testsupport", "repowalk.go"):      "defines the predicate",
	filepath.Join("packages", "testsupport", "repowalk_test.go"): "tests the predicate against synthetic trees",
}

// TestEveryRepoWalkConsultsSkipScanDir enforces the adoption that SkipScanDir's
// own doc comment demands: "Every guard test that walks the tree looking for a
// banned pattern must consult it."
//
// That sentence was true as documentation and false as fact. Four guards walked
// repository source with their own skip lists or none at all — docs_test.go
// carried a hand-written four-name list that knew about .claude but nothing
// about a checkout dropped anywhere else, golden_pin_test.go and
// interactive_config_live_test.go had no pruning whatever. The failure mode is
// the one SkipScanDir was written for: a walk descends into a worktree under
// .claude/worktrees and reports every file in somebody else's branch as an
// offender, naming real paths in a checkout whose own sources are clean.
//
// A rule stated only in prose is a rule that drifts. This is the check.
func TestEveryRepoWalkConsultsSkipScanDir(t *testing.T) {
	offenders := scanForUnguardedWalks(t, filepath.Join("..", ".."))
	if len(offenders) > 0 {
		t.Errorf("these tests walk the tree without consulting testsupport.SkipScanDir "+
			"(add the call, or list the file in walksOutsideTheRepo with a reason if it cannot reach repo source):\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

// TestWalkAdoptionGateCatchesANewWalk is the teeth. A green adoption gate reads
// the same whether every walker adopted the predicate or the scan simply
// matched nothing, so plant a walker that does not consult it and assert the
// scan says so.
func TestWalkAdoptionGateCatchesANewWalk(t *testing.T) {
	root := TempDir(t)
	pkg := filepath.Join(root, "packages", "newguard")
	if err := os.MkdirAll(pkg, 0o700); err != nil {
		t.Fatal(err)
	}
	src := "package newguard\n\nimport (\n\t\"path/filepath\"\n\t\"testing\"\n)\n\n" +
		"func TestSomething(t *testing.T) {\n\t_ = filepath.WalkDir(\".\", nil)\n}\n"
	bad := filepath.Join(pkg, "guard_test.go")
	if err := os.WriteFile(bad, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	found := scanForUnguardedWalks(t, root)
	want := filepath.Join("packages", "newguard", "guard_test.go")
	if !contains(found, want) {
		t.Fatalf("adoption gate missed a walker that ignores SkipScanDir; got %v", found)
	}

	// Adopting the predicate must silence it, or the gate is unpassable noise.
	fixed := strings.Replace(src, "_ = filepath.WalkDir(\".\", nil)",
		"_ = filepath.WalkDir(\".\", nil) // SkipScanDir", 1)
	if err := os.WriteFile(bad, []byte(fixed), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := scanForUnguardedWalks(t, root); len(got) != 0 {
		t.Fatalf("gate still reports an offender after adoption: %v", got)
	}
}

// scanForUnguardedWalks is the body of the adoption gate, over an arbitrary
// root so the teeth test can drive it against a scratch tree.
func scanForUnguardedWalks(t *testing.T, root string) []string {
	t.Helper()
	var offenders []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if SkipScanDir(root, path, d) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if _, ok := walksOutsideTheRepo[rel]; ok {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(data)
		if !strings.Contains(src, "filepath.WalkDir(") && !strings.Contains(src, "filepath.Walk(") {
			return nil
		}
		if strings.Contains(src, "SkipScanDir") {
			return nil
		}
		offenders = append(offenders, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(offenders)
	return offenders
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
