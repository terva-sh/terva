package testsupport

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// A comment that points at a file which does not exist sends a reader nowhere,
// and nothing here noticed for 70 of them.
//
// 63 came from one commit. a13efdf8 moved 28 shipped documents under
// docs/plans/archive/ and docs/proposals/archive/, and its own message records
// that it fixed inbound links and that the "full doc-to-doc link sweep is
// clean". Doc to doc. check-links validates documents and never reads Go, so
// every pointer in code kept naming the pre-archive path.
//
// This is worse than an ordinary stale comment for the reason TKT-01M1RRT6J
// gives: a pointer that resolves to nothing tells a reader not to look, and so
// suppresses the search that would correct it.
//
// The scan reads comment tokens through go/ast rather than matching lines,
// because a path inside a string literal is data the program uses and not a
// pointer a reader follows. TKT-01M2Q0X87G carries the repair this guards.
func TestCommentPointersResolve(t *testing.T) {
	// Most of what these comments point at — docs/plans, docs/proposals,
	// docs/reviews, scripts/release.sh — is held back from the published tree.
	// There a live pointer and a dead one look identical, because the target is
	// missing either way, so the gate cannot tell the class it exists to catch
	// from the enrolment decision the cut already made. It polices the source
	// tree, where that distinction is real.
	requireSourceTree(t)
	root := filepath.Join("..", "..")

	type offender struct{ file, path string }
	var offenders []offender

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
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.ParseComments)
		if err != nil {
			// A file the parser rejects is a compile failure, which another
			// gate reports better than this one would.
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		seen := map[string]bool{}
		for _, cg := range f.Comments {
			for _, m := range commentPointerRe.FindAllString(cg.Text(), -1) {
				if seen[m] || illustrativePointer[m] {
					continue
				}
				seen[m] = true
				if _, err := os.Stat(filepath.Join(root, m)); err != nil {
					offenders = append(offenders, offender{rel, m})
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	sort.Slice(offenders, func(i, j int) bool {
		if offenders[i].file != offenders[j].file {
			return offenders[i].file < offenders[j].file
		}
		return offenders[i].path < offenders[j].path
	})
	for _, o := range offenders {
		t.Errorf("%s: comment points at %s, which does not exist", o.file, o.path)
	}
	if len(offenders) > 0 {
		t.Logf("A moved document keeps its content: look under the archive/ " +
			"directory beside its old home before assuming the pointer is dead. " +
			"If the target is genuinely gone, name what replaced it rather than " +
			"deleting the sentence.")
	}
}

// commentPointerRe matches a repository path a reader could follow: one of the
// top-level source directories, then a known extension.
//
// It is deliberately narrow. A bare directory reference ("see packages/agent")
// and a path in a directory added later both slip past, and widening it to
// catch them would start matching ordinary prose that happens to contain a
// slash and a dot. A gate that fires on prose gets suppressed, and a suppressed
// gate catches nothing at all; this one is sized to stay credible.
var commentPointerRe = regexp.MustCompile(
	`\b(?:docs|packages|cmd|internal|e2e|examples|scripts)/[A-Za-z0-9_./-]+\.(?:go|md|json|ts|tsx|yml|yaml|sh)\b`)

// illustrativePointer holds paths that are examples inside a sentence rather
// than references. Both entries below appear in quoted speech: a sub-agent
// reporting "I wrote packages/foo/bar.go" in a comment explaining why the
// coordinator must read the leased worktree. Nobody is meant to open them.
//
// Keep this list short. A real pointer that someone finds inconvenient belongs
// in the repair, not here.
var illustrativePointer = map[string]bool{
	"packages/foo/bar.go": true,
}
