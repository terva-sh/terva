package config

import (
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

// config.json is a whole-file JSON document that every setter loads in full,
// changes one field of, and writes back in full. Two writers that are not
// serialized against each other lose one of the two edits — A loads, B loads, A
// writes {A′,B}, B writes {A,B′} — and the atomic rename that keeps a reader
// from ever seeing a torn file is exactly what stops anyone noticing.
//
// MutateConfig exists to be the one safe path, and its own comment said so. Ten
// production setters went around it anyway, including two in THIS package and
// all five of the settings store's — the web/TUI settings surface, which is
// precisely the "N sessions" the comment named. A permission rule approved in
// one session and a theme changed in another raced, and the rule lost.
//
// Two rules, enforced separately because they fail differently:
//
//   - Outside this package: nothing may call config.SaveConfig. It replaces the
//     whole document, so in production it is only ever half of a read-modify-write.
//     Tests use it to seed a config from nothing, which is fine and is why it
//     stays exported.
//   - Inside this package: saveConfigAt is reachable only from SaveConfig and
//     MutateConfigAt. A third caller would be a new bypass with no import to
//     notice it.
const repoRoot = "../../.."

// scanForSaveConfigCallers returns "path:line" for every non-test Go file that
// calls config.SaveConfig, given a root to walk. Split out so the teeth test
// below can drive it over a synthetic tree.
func scanForSaveConfigCallers(root string, skipDir func(root, path string, d fs.DirEntry) bool) ([]string, int, error) {
	var found []string
	var scanned int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if skipDir(root, path, d) {
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
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "config.SaveConfig(") {
				found = append(found, filepath.ToSlash(path)+":"+itoa(i+1))
			}
		}
		return nil
	})
	sort.Strings(found)
	return found, scanned, err
}

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

func TestNoProductionCodeReadsAndWritesTheConfigWithoutMutateConfig(t *testing.T) {
	found, scanned, err := scanForSaveConfigCallers(repoRoot, testsupport.SkipScanDir)
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 500 {
		t.Fatalf("only %d production Go files were scanned; the walk is broken and this gate proves nothing", scanned)
	}
	if len(found) > 0 {
		t.Errorf("production code calls config.SaveConfig, which replaces the whole document — "+
			"use config.MutateConfig so the read and the write happen under one lock:\n  %s",
			strings.Join(found, "\n  "))
	}
}

// The teeth: a synthetic tree with one offending file. Without this, a scan that
// silently matched nothing — a wrong pattern, a walk that skipped everything —
// would read as a clean repository.
func TestTheSaveConfigGateCatchesANewCaller(t *testing.T) {
	root := testsupport.TempDir(t)
	write := func(name, body string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("clean.go", "package p\n\nfunc f() error { return config.MutateConfig(func(c *config.Config) {}) }\n")
	write("pkg/offender.go", "package q\n\nfunc g() error {\n\tc, _ := config.LoadConfig()\n\tc.Theme = \"x\"\n\treturn config.SaveConfig(c)\n}\n")
	// A test file is allowed to seed a config outright.
	write("pkg/offender_test.go", "package q\n\nfunc h() { _ = config.SaveConfig(config.Config{}) }\n")

	never := func(string, string, fs.DirEntry) bool { return false }
	found, scanned, err := scanForSaveConfigCallers(root, never)
	if err != nil {
		t.Fatal(err)
	}
	if scanned != 2 {
		t.Fatalf("scanned %d files, want the 2 non-test ones", scanned)
	}
	if len(found) != 1 || !strings.HasSuffix(found[0], "pkg/offender.go:6") {
		t.Fatalf("the gate did not name exactly the offending line: %v", found)
	}
}

// The intra-package half. saveConfigAt is the actual write; a third caller
// inside this package would be a bypass no import graph could reveal.
func TestOnlyTheTwoDeclaredWritersCallSaveConfigAt(t *testing.T) {
	allowed := map[string]bool{"SaveConfig": true, "MutateConfigAt": true}
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var callers []string
	var sawAllowed int
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				id, ok := call.Fun.(*ast.Ident)
				if !ok || id.Name != "saveConfigAt" {
					return true
				}
				if allowed[fn.Name.Name] {
					sawAllowed++
					return true
				}
				callers = append(callers, fn.Name.Name+" ("+name+":"+itoa(fset.Position(call.Pos()).Line)+")")
				return true
			})
		}
	}
	if sawAllowed < 2 {
		t.Fatalf("found %d calls from the declared writers, expected both SaveConfig and MutateConfigAt; "+
			"the scan is broken and this gate proves nothing", sawAllowed)
	}
	sort.Strings(callers)
	if len(callers) > 0 {
		t.Errorf("saveConfigAt is called from outside SaveConfig/MutateConfigAt, which is a read-modify-write "+
			"with no lock around it:\n  %s", strings.Join(callers, "\n  "))
	}
}

// The behavioural half of the in-process claim: N concurrent setters, each
// touching a DIFFERENT field, and all N survive. Under the old shape — load,
// edit, save, no shared lock — the last writer's copy wins and the others are
// silently gone.
func TestConcurrentSettersDoNotLoseEachOthersFields(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	const n = 24
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := MutateConfig(func(c *Config) {
				c.FavoriteModels = append(c.FavoriteModels, "model-"+itoa(i))
			}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()

	c, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.FavoriteModels) != n {
		t.Errorf("%d of %d concurrent edits survived: %v", len(c.FavoriteModels), n, c.FavoriteModels)
	}
}

// The cross-process claim, which the mutex above cannot make. A second terva
// instance is a second flock holder, and inside one process two Acquire calls
// take two file descriptions — so this is the real thing, not a simulation of it.
//
// Park the lock, prove the mutation BLOCKS, release, prove it RETURNS. The
// blocking half is what rules out a no-op: a MutateConfig that never took the
// lock would sail through step one and the test would pass having asserted
// nothing.
func TestMutateConfigWaitsForAnotherProcessHoldingTheLock(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)

	lk, err := filelock.Acquire(configLockPath(home))
	if err != nil {
		t.Skipf("this filesystem cannot host a lockfile: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- MutateConfig(func(c *Config) { c.Theme = "after-the-lock" })
	}()

	select {
	case err := <-done:
		lk.Release()
		t.Fatalf("MutateConfig completed (%v) while the config lock was held elsewhere — "+
			"it is not taking the cross-process lock, so two terva instances still lose each other's edits", err)
	case <-time.After(150 * time.Millisecond):
		// Blocked, as it must be.
	}

	lk.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("MutateConfig never completed after the lock was released")
	}

	c, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.Theme != "after-the-lock" {
		t.Errorf("theme = %q, want the value written once the lock was free", c.Theme)
	}
}

// SetGlobalUserName writes the GLOBAL home even under project scoping, so it
// needs the same locking against a different file. This pins that it goes
// through the mutating path rather than its own load/save pair.
func TestSetGlobalUserNamePreservesTheRestOfTheGlobalConfig(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	if err := SaveConfig(Config{Provider: "anthropic", Model: "opus", Theme: "dark"}); err != nil {
		t.Fatal(err)
	}
	if err := SetGlobalUserName("  Drew  "); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.UserName != "Drew" {
		t.Errorf("user name = %q, want %q", c.UserName, "Drew")
	}
	if c.Provider != "anthropic" || c.Model != "opus" || c.Theme != "dark" {
		t.Errorf("the rest of the config did not survive: %+v", c)
	}
}
