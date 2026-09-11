package testsupport

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The egress guard is defense that exists only while something calls it.
// It shipped with zero importers and sat dead for two months while
// docs/permissions.md described the protection as live. Finding A.3 of
// docs/plans/terva-review-remediation.md caught that, reworded the doc to
// "staged, not yet wired", and asked for an import test "so it can't go
// silently dead again". The import test never arrived. The wiring did, and
// the same doc then went stale the other way, calling a package staged that
// three files were already using.
//
// These two gates close both directions. TestEgressGuardHasImporters fails
// when the last consumer goes away. TestEgressDocumentedConsumersMatchCode
// holds the documented list to the set the code really has, so neither an
// added nor a removed consumer can leave the doc lying.

const egressPkg = "terva.sh/terva/packages/egress"

// egressImporters returns every non-test Go file outside packages/egress
// that imports the guard, as repo-relative slash paths.
func egressImporters(t *testing.T) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// SkipScanDir is not housekeeping here, it is correctness. This
		// checkout keeps sibling worktrees, and a walk that descends into
		// one counts another branch's imports as consumers of this tree.
		// The doc-parity gate would then demand permissions.md list a file
		// that does not exist on this branch.
		if SkipScanDir(repoRoot, path, d) {
			return fs.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(repoRoot, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		// The package's own files do not count as consumers.
		if strings.HasPrefix(rel, "packages/egress/") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(src), `"`+egressPkg+`"`) {
			found = append(found, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the tree for egress importers: %v", err)
	}
	sort.Strings(found)
	return found
}

// TestEgressGuardHasImporters is the anti-rot gate. A guard nobody calls
// blocks nothing, and the failure is silent: every test in packages/egress
// still passes on dead code.
func TestEgressGuardHasImporters(t *testing.T) {
	if got := egressImporters(t); len(got) == 0 {
		t.Fatalf("no file outside packages/egress imports %s, so the SSRF guard is dead code and every claim about it is false.\n"+
			"Restore the consumer that went away, or delete the package and the protection it promises in docs/permissions.md.", egressPkg)
	}
}

// docGoPath matches a backtick-quoted Go file path in the documented list.
var docGoPath = regexp.MustCompile("`([^`]+\\.go)`")

// TestEgressDocumentedConsumersMatchCode is the anti-drift gate. The
// consumer list in docs/permissions.md sits between two markers so this
// test can compare it against the tree.
func TestEgressDocumentedConsumersMatchCode(t *testing.T) {
	const (
		startMark = "<!-- egress-consumers:start -->"
		endMark   = "<!-- egress-consumers:end -->"
	)
	doc := filepath.Join(repoRoot, "docs", "permissions.md")
	b, err := os.ReadFile(doc)
	if err != nil {
		t.Fatalf("read %s: %v", doc, err)
	}
	src := string(b)
	i, j := strings.Index(src, startMark), strings.Index(src, endMark)
	if i < 0 || j < 0 || j < i {
		t.Fatalf("docs/permissions.md must wrap its egress consumer list in %s and %s, so this gate can hold the list to the code", startMark, endMark)
	}
	var documented []string
	for _, m := range docGoPath.FindAllStringSubmatch(src[i:j], -1) {
		documented = append(documented, m[1])
	}
	sort.Strings(documented)

	got := egressImporters(t)
	if strings.Join(documented, "\n") != strings.Join(got, "\n") {
		t.Errorf("the egress consumer list in docs/permissions.md disagrees with the code.\n  documented: %v\n  in code:    %v\n"+
			"Correct the list between the markers. A consumer that is not documented understates the guard, and a documented one that is gone promises protection that no longer runs.", documented, got)
	}
}
