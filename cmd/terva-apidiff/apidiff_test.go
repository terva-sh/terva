package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// apiFixture builds a repository with the package at two commits, tagged
// `base` and `head`, and returns its path.
func apiFixture(t *testing.T, basePkg, headPkg map[string]string) string {
	t.Helper()
	dir := testsupport.TempDir(t)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	writeAll := func(files map[string]string) {
		t.Helper()
		pkgDir := filepath.Join(dir, "packages", "sample")
		if err := os.RemoveAll(pkgDir); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(pkgDir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(pkgDir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	git("init", "-q", "-b", "main")
	// A directory that exists and holds no Go source, for the vacuity guard.
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "readme.md"), []byte("prose\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeAll(basePkg)
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	git("tag", "base")
	writeAll(headPkg)
	git("add", "-A")
	// --allow-empty: some fixtures hand the same package to both refs, because
	// what they exercise is the census, not a diff.
	git("commit", "-q", "--allow-empty", "-m", "head")
	git("tag", "head")
	return dir
}

func names(syms []symbol) []string {
	out := make([]string, 0, len(syms))
	for _, s := range syms {
		out = append(out, s.Name)
	}
	return out
}

func has(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// The census must see what a consumer can reach and nothing else.
func TestAPIDiffCensusesTheReachableSurface(t *testing.T) {
	base := map[string]string{
		"a.go": `package sample

type Kept struct {
	Field  string
	hidden int
}

type Dropped struct{}

func (k Kept) Method() {}
func (d *dropped) Method() {}

type dropped struct{}

func Exported(a int) error   { return nil }
func unexported(a int) error { return nil }

const Version = "1"
`,
		"a_test.go": `package sample

func TestOnlySymbol() {}
`,
	}
	head := map[string]string{
		"a.go": `package sample

type Kept struct {
	Field string
	Added bool
}

func (k Kept) Method() {}

func Exported(a int, b string) error { return nil }

const Version = "1"
`,
		"b.go": `package sample

func Fresh() {}
`,
	}
	report, err := comparePkg(apiFixture(t, base, head), "packages/sample", "base", "head")
	if err != nil {
		t.Fatalf("compare: %v", err)
	}

	for _, want := range []string{"Dropped"} {
		if !has(names(report.Removed), want) {
			t.Errorf("removal of %s not reported; removed = %v", want, names(report.Removed))
		}
	}
	for _, want := range []string{"Fresh", "Kept.Added"} {
		if !has(names(report.Added), want) {
			t.Errorf("addition of %s not reported; added = %v", want, names(report.Added))
		}
	}
	var changed []string
	for _, c := range report.Changed {
		changed = append(changed, c.Symbol.Name)
	}
	if !has(changed, "Exported") {
		t.Errorf("the signature change to Exported was not reported; changed = %v", changed)
	}
	if !has(report.NewFiles, "packages/sample/b.go") {
		t.Errorf("new file not reported; new files = %v", report.NewFiles)
	}

	// Things a consumer cannot reach must not appear on either side, or the
	// report fills with noise that hides the findings that matter.
	all := append(append(names(report.Added), names(report.Removed)...), changed...)
	for _, unreachable := range []string{"unexported", "hidden", "dropped.Method", "TestOnlySymbol"} {
		if has(all, unreachable) {
			t.Errorf("%s is not part of the exported surface but was reported: %v", unreachable, all)
		}
	}
}

// An exported field is as breakable as the type holding it, and dropping one
// must not read as an unchanged struct.
func TestAPIDiffSeesAFieldRemoval(t *testing.T) {
	base := map[string]string{"a.go": `package sample

type Config struct {
	Host string
	Port int
}

type Store interface {
	Get(k string) string
	Put(k, v string)
}

func New() *Config { return nil }
`}
	head := map[string]string{"a.go": `package sample

type Config struct {
	Host string
}

type Store interface {
	Get(k string) string
}

func New() *Config { return nil }
`}
	report, err := comparePkg(apiFixture(t, base, head), "packages/sample", "base", "head")
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if !has(names(report.Removed), "Config.Port") {
		t.Fatalf("dropping an exported field was not reported as a removal: %v", names(report.Removed))
	}
	// An interface method is the same kind of break: every implementation and
	// every caller of it stops compiling.
	if !has(names(report.Removed), "Store.Put") {
		t.Fatalf("dropping an interface method was not reported as a removal: %v", names(report.Removed))
	}
	// And it is reported ONCE, against the field — not a second time as a
	// wholesale rewrite of the type it lives in.
	for _, c := range report.Changed {
		if c.Symbol.Name == "Config" {
			t.Errorf("the field removal was also reported as a change to Config itself (%s -> %s)", c.Was, c.Symbol.Sig)
		}
	}
}

// THE ONE THAT MATTERS. Every hand-rolled version of this check has at some
// point reported "no removals" while looking at nothing at all — a mis-split
// path list, a moved package, a ref resolving to an empty tree. A clean report
// and a vacuous one are indistinguishable downstream, so a census of nothing
// must be an error and never a verdict.
func TestAPIDiffRefusesToReportOnAnEmptyCensus(t *testing.T) {
	pkg := map[string]string{"a.go": `package sample

func Exported() {}
`}
	repo := apiFixture(t, pkg, pkg)

	for _, tc := range []struct{ name, path string }{
		{"path that exists at neither ref", "packages/nosuch"},
		{"directory holding no Go source", "docs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := comparePkg(repo, tc.path, "base", "head")
			if err == nil {
				t.Fatal("an empty census was reported as a clean comparison — that is the failure this guard exists for")
			}
			if !strings.Contains(err.Error(), "census of nothing") {
				t.Fatalf("the error does not name the problem, so it reads as an unrelated failure: %v", err)
			}
		})
	}
}

// Each side is guarded on its own. Disabling one used to be invisible because
// the other still fired: every fixture emptied both at once, so the pair tested
// as one guard and a mutation to either passed.
func TestAPIDiffGuardsEachSideSeparately(t *testing.T) {
	real := map[string]string{"a.go": "package sample\n\nfunc Exported() {}\n"}
	hidden := map[string]string{"a.go": "package sample\n\nfunc unexported() {}\n"}
	empty := map[string]string{}

	for _, tc := range []struct {
		name, want string
		base, head map[string]string
	}{
		{"absent at base only", "does not exist", empty, real},
		{"absent at head only", "moved or removed", real, empty},
		{"nothing exported at base only", "not one exported symbol", hidden, real},
		{"nothing exported at head only", "not one exported symbol", real, hidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := comparePkg(apiFixture(t, tc.base, tc.head), "packages/sample", "base", "head")
			if err == nil {
				t.Fatal("one side was empty and the comparison was reported as clean")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the error does not name this cause (want %q): %v", tc.want, err)
			}
			if !strings.Contains(err.Error(), "census of nothing") {
				t.Fatalf("the error does not say a census of nothing is not a report: %v", err)
			}
		})
	}
}

// The working tree is the head a cut actually asks about.
func TestAPIDiffComparesAgainstTheWorkingTree(t *testing.T) {
	pkg := map[string]string{"a.go": `package sample

func Exported() {}
`}
	repo := apiFixture(t, pkg, pkg)
	if err := os.WriteFile(filepath.Join(repo, "packages", "sample", "a.go"),
		[]byte("package sample\n\nfunc Exported(now bool) {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := comparePkg(repo, "packages/sample", "base", "")
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if len(report.Changed) != 1 || report.Changed[0].Symbol.Name != "Exported" {
		t.Fatalf("an uncommitted signature change was not seen: %+v", report.Changed)
	}
}

// A build-tag-gated file is part of the surface: some build reaches it, and a
// census that type-checked one configuration would miss it.
func TestAPIDiffIncludesBuildTaggedFiles(t *testing.T) {
	base := map[string]string{"a.go": "package sample\n\nfunc Always() {}\n"}
	head := map[string]string{
		"a.go": "package sample\n\nfunc Always() {}\n",
		"tagged.go": `//go:build never_set

package sample

func OnlyUnderATag() {}
`,
	}
	report, err := comparePkg(apiFixture(t, base, head), "packages/sample", "base", "head")
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if !has(names(report.Added), "OnlyUnderATag") {
		t.Fatalf("a symbol behind a build tag was left out of the census: %v", names(report.Added))
	}
}
