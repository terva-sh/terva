package config

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/filelock"
	"terva.sh/terva/packages/testsupport"
)

// .terva/config.json had four writers, byte-for-byte identical, and that
// sameness was the hazard rather than the reassurance.
//
// They edit DIFFERENT keys of ONE document whose whole purpose is preserving
// the keys the others own — disable_extensions, disable_mcp, provider/model,
// permissions — so a change to how that file is read, published or serialized
// has to land in all four or they start dropping each other's keys. Three
// separate tests each asserted "preserves unrelated fields" against one copy,
// and none of them could see the other two. One of the four lived in a
// different package entirely, so no compiler and no test forced them to agree.
//
// None took a lock, and all four wrote the same fixed "<path>.tmp" — the shared
// scratch name provider/auth/store.go and terva-mcp-bridge each document having
// fixed, because two writers truncate and fill the same file and the rename
// publishes a blend of two documents.
//
// The gate is about the WRITE, not the path: a caller that merely wants the
// path to show a user is fine, and a rule that banned that would be noise.
var projectConfigWriteVerbs = map[string]bool{
	"WriteFile": true, "Rename": true, "Create": true, "CreateTemp": true, "OpenFile": true,
}

// projectConfigWriters returns the name of every function that both resolves
// ProjectConfigPath and writes a file — the read-modify-write shape.
func projectConfigWriters(fset *token.FileSet, filename, src string) ([]string, error) {
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		var resolvesPath, writes bool
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				if fun.Name == "ProjectConfigPath" {
					resolvesPath = true
				}
			case *ast.SelectorExpr:
				pkg, ok := fun.X.(*ast.Ident)
				if !ok {
					return true
				}
				if fun.Sel.Name == "ProjectConfigPath" {
					resolvesPath = true
				}
				if pkg.Name == "os" && projectConfigWriteVerbs[fun.Sel.Name] {
					writes = true
				}
			}
			return true
		})
		if resolvesPath && writes {
			out = append(out, fn.Name.Name)
		}
	}
	sort.Strings(out)
	return out, nil
}

func TestOnlyMutateProjectConfigWritesTheProjectConfig(t *testing.T) {
	const root = "../../.."
	fset := token.NewFileSet()
	var offenders []string
	var scanned int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if testsupport.SkipScanDir(root, path, d) {
			return filepath.SkipDir
		}
		name := d.Name()
		if d.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		// Cheap pre-filter; parsing every file in the repo is the slow part.
		if !strings.Contains(string(b), "ProjectConfigPath") {
			return nil
		}
		fns, err := projectConfigWriters(fset, path, string(b))
		if err != nil {
			return err
		}
		for _, fn := range fns {
			if fn == "MutateProjectConfig" {
				continue
			}
			offenders = append(offenders, filepath.ToSlash(path)+": "+fn)
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
		t.Errorf("these read and write .terva/config.json themselves instead of going through "+
			"config.MutateProjectConfig, so they take no lock, share one temp name, and will drop "+
			"the keys the other writers own:\n  %s", strings.Join(offenders, "\n  "))
	}
}

// The teeth, on synthetic source: the classifier must see the read-modify-write
// shape and must NOT flag a function that only wants the path.
func TestTheProjectConfigGateDistinguishesAWriterFromAReader(t *testing.T) {
	const src = `package p

func writer(cwd string) error {
	path := config.ProjectConfigPath(cwd)
	b := []byte("{}")
	return os.WriteFile(path+".tmp", b, 0o644)
}

func renamer(cwd string) error {
	return os.Rename("x", config.ProjectConfigPath(cwd))
}

func reader(cwd string) string {
	return "your project config is at " + config.ProjectConfigPath(cwd)
}

func unrelatedWriter(path string) error {
	return os.WriteFile(path, nil, 0o644)
}
`
	got, err := projectConfigWriters(token.NewFileSet(), "synthetic.go", src)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"renamer", "writer"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("classifier picked %v, want %v", got, want)
	}
}

// The behaviour the four copies existed to provide, now asserted once against
// all of them: unrelated keys — including ones this build has never heard of —
// survive every setter.
func TestEverySetterPreservesTheKeysTheOthersOwn(t *testing.T) {
	cwd := testsupport.TempDir(t)
	path := ProjectConfigPath(cwd)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// A hand-written project file: one key from a newer terva, one a human added.
	seed := `{"some_future_key": {"nested": true}, "a_humans_note": "keep me"}`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SetProjectExtensionDisabled(cwd, "memory", true); err != nil {
		t.Fatal(err)
	}
	if err := SetProjectMCPDisabled(cwd, "playwright", true); err != nil {
		t.Fatal(err)
	}
	if err := SetProjectModel(cwd, "anthropic", "opus"); err != nil {
		t.Fatal(err)
	}
	// Standing in for workspace's setProjectPermissionRule, which is in another
	// package and now shares this same chokepoint.
	if err := MutateProjectConfig(cwd, func(doc map[string]any) {
		doc["permissions"] = []any{map[string]any{"tool": "bash", "decision": "deny"}}
	}); err != nil {
		t.Fatal(err)
	}

	var doc map[string]any
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the published document does not parse: %v\n%s", err, raw)
	}
	for _, key := range []string{"some_future_key", "a_humans_note", "disable_extensions", "disable_mcp", "provider", "model", "permissions"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("%q was dropped:\n%s", key, raw)
		}
	}

	// Clearing removes the key rather than leaving an empty value behind: the
	// resolver honours "" as a value, so the two are not the same document.
	if err := SetProjectModel(cwd, "", ""); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	// A FRESH map: json.Unmarshal MERGES into a non-nil one, so reusing doc
	// would carry the old provider key forward and the assertion below would
	// fire on the test's own bookkeeping rather than on what was published.
	doc = nil
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["provider"]; ok {
		t.Errorf("clearing the provider left the key behind:\n%s", raw)
	}
	if _, ok := doc["a_humans_note"]; !ok {
		t.Errorf("clearing the provider dropped an unrelated key:\n%s", raw)
	}
}

// Two setters that own different keys, run concurrently. This is the finding's
// exact scenario: the extensions dialog and the permissions pane, two panes of
// one TUI or two web sessions, and the second rename used to win outright.
func TestTwoProjectSettersDoNotLoseEachOthersKeys(t *testing.T) {
	cwd := testsupport.TempDir(t)

	const n = 12
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := SetProjectExtensionDisabled(cwd, "ext-"+itoa(i), true); err != nil {
				t.Error(err)
			}
		}(i)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := SetProjectMCPDisabled(cwd, "mcp-"+itoa(i), true); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()

	raw, err := os.ReadFile(ProjectConfigPath(cwd))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Extensions []string `json:"disable_extensions"`
		MCP        []string `json:"disable_mcp"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the published document does not parse — a shared temp name lets two writes blend: %v\n%s", err, raw)
	}
	if len(doc.Extensions) != n || len(doc.MCP) != n {
		t.Errorf("lost updates: %d/%d disabled extensions and %d/%d disabled MCP servers survived\n%s",
			len(doc.Extensions), n, len(doc.MCP), n, raw)
	}
}

// The fixed "<path>.tmp" is gone. With the lock in place a same-process race on
// the scratch file is unreachable, so this asserts the property directly rather
// than by racing: a write must not touch that name at all.
//
// It still matters. The lock degrades to the in-process mutex on a filesystem
// that cannot host a lockfile, and the fixed name is also reachable by anything
// else that ever wrote it — which is why provider/auth/store.go and
// terva-mcp-bridge each document having removed the same shared name.
func TestAWriteDoesNotTouchTheOldSharedTempName(t *testing.T) {
	cwd := testsupport.TempDir(t)
	path := ProjectConfigPath(cwd)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := path + ".tmp"
	const sentinel = `{"written_by":"somebody else"}`
	if err := os.WriteFile(stale, []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SetProjectModel(cwd, "anthropic", "opus"); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(stale)
	if err != nil {
		t.Fatalf("the write consumed the shared scratch name %s: %v", stale, err)
	}
	if string(got) != sentinel {
		t.Errorf("the write reused the shared scratch name, clobbering it:\n got %s\nwant %s", got, sentinel)
	}
	// And the real file was still published.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "opus") {
		t.Errorf("the config was not published: %s", raw)
	}
}

// The cross-process half, the same deterministic shape as the user config's:
// park the lock, prove the write BLOCKS, release, prove it RETURNS.
func TestProjectConfigWritesWaitForAnotherProcess(t *testing.T) {
	cwd := testsupport.TempDir(t)
	path := ProjectConfigPath(cwd)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	lk, err := filelock.Acquire(path + ".lock")
	if err != nil {
		t.Skipf("this filesystem cannot host a lockfile: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- SetProjectModel(cwd, "anthropic", "opus") }()

	select {
	case err := <-done:
		lk.Release()
		t.Fatalf("the project config was written (%v) while its lock was held elsewhere — "+
			"two terva instances still lose each other's edits", err)
	case <-time.After(150 * time.Millisecond):
	}

	lk.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the write never completed after the lock was released")
	}
}
