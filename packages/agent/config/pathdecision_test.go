package config

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// pathStoreDriver is one path-keyed decision store, reduced to the operations
// both of them have. The two stores keep their own Load/Save/Add/Remove on
// purpose — they are dull and they read as what they are in the file that owns
// them — so something has to hold them to the same behaviour. This is it.
type pathStoreDriver struct {
	name   string
	add    func(path string, parent bool) bool
	remove func(path string) bool
	covers func(path string) bool
	// plant appends an entry directly, the way a hand-edited file would hold
	// one: display path only, no recorded canonical Real.
	plant func(display, real string, parent bool)
	count func() int
}

func trustDriver() pathStoreDriver {
	s := &TrustStore{}
	return pathStoreDriver{
		name:   "trusted.json",
		add:    s.Add,
		remove: s.Remove,
		covers: func(p string) bool { ok, _ := s.IsTrusted(p); return ok },
		plant: func(display, real string, parent bool) {
			s.Trusted = append(s.Trusted, TrustEntry{Path: display, Real: real, Parent: parent})
		},
		count: func() int { return len(s.Trusted) },
	}
}

func unjailDriver() pathStoreDriver {
	s := &UnjailStore{}
	return pathStoreDriver{
		name:   "unjailed.json",
		add:    s.Add,
		remove: s.Remove,
		covers: func(p string) bool { ok, _ := s.IsUnjailed(p); return ok },
		plant: func(display, real string, parent bool) {
			s.Unjailed = append(s.Unjailed, UnjailEntry{Path: display, Real: real, Parent: parent})
		},
		count: func() int { return len(s.Unjailed) },
	}
}

// 🔑 trusted.json and unjailed.json must answer "is this the same directory?"
// identically, forever.
//
// They are two stores of the same shape holding two DIFFERENT decisions — trust
// says "load this project's code", unjail says "let tools write outside this
// directory" — and they are deliberately not one code path. What they cannot be
// is subtly different about which directory an entry names: a parent entry that
// covers descendants in one and not the other, or a hand-edited entry one
// honours and the other ignores, is a sandbox that widens or narrows by
// accident.
//
// Each scenario carries what the answer should BE, so this pins behaviour as
// well as agreement — two stores that are identically wrong would otherwise
// pass.
func TestTheTwoPathStoresAgree(t *testing.T) {
	root := testsupport.TempDir(t)
	dir := filepath.Join(root, "project")
	child := filepath.Join(dir, "nested", "deep")
	sibling := filepath.Join(root, "projectile") // shares a prefix, is not a child
	for _, d := range []string{child, sibling} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	scenarios := []struct {
		what string
		want string
		run  func(d pathStoreDriver) string
	}{
		{
			what: "an exact entry covers its own directory and nothing below it",
			want: "added=true self=true child=false",
			run: func(d pathStoreDriver) string {
				a := d.add(dir, false)
				return fmt.Sprintf("added=%v self=%v child=%v", a, d.covers(dir), d.covers(child))
			},
		},
		{
			what: "a parent entry covers descendants",
			want: "self=true child=true",
			run: func(d pathStoreDriver) string {
				d.add(dir, true)
				return fmt.Sprintf("self=%v child=%v", d.covers(dir), d.covers(child))
			},
		},
		{
			what: "a parent entry does not cover a prefix-sharing sibling",
			want: "sibling=false",
			run: func(d pathStoreDriver) string {
				d.add(dir, true)
				return fmt.Sprintf("sibling=%v", d.covers(sibling))
			},
		},
		{
			what: "adding the same scope twice changes nothing",
			want: "first=true again=false entries=1",
			run: func(d pathStoreDriver) string {
				a := d.add(dir, false)
				b := d.add(dir, false)
				return fmt.Sprintf("first=%v again=%v entries=%d", a, b, d.count())
			},
		},
		{
			what: "promoting to parent updates in place rather than appending",
			want: "promoted=true entries=1 child=true",
			run: func(d pathStoreDriver) string {
				d.add(dir, false)
				p := d.add(dir, true)
				return fmt.Sprintf("promoted=%v entries=%d child=%v", p, d.count(), d.covers(child))
			},
		},
		{
			what: "a different spelling of the same directory is the same entry",
			want: "again=false entries=1",
			run: func(d pathStoreDriver) string {
				d.add(dir, false)
				again := d.add(filepath.Join(dir, "nested", ".."), false)
				return fmt.Sprintf("again=%v entries=%d", again, d.count())
			},
		},
		{
			what: "a hand-edited entry carrying only a display path still matches",
			want: "covers=true removed=true entries=0",
			run: func(d pathStoreDriver) string {
				d.plant(dir, "", false)
				c := d.covers(dir)
				r := d.remove(dir)
				return fmt.Sprintf("covers=%v removed=%v entries=%d", c, r, d.count())
			},
		},
		{
			what: "remove drops every entry for the directory, not just the first",
			want: "removed=true entries=0 covers=false",
			run: func(d pathStoreDriver) string {
				d.plant(dir, CanonicalTrustPath(dir), false)
				d.plant(dir, "", true)
				r := d.remove(dir)
				return fmt.Sprintf("removed=%v entries=%d covers=%v", r, d.count(), d.covers(dir))
			},
		},
		{
			what: "removing a directory that was never added changes nothing",
			want: "removed=false entries=0",
			run: func(d pathStoreDriver) string {
				r := d.remove(dir)
				return fmt.Sprintf("removed=%v entries=%d", r, d.count())
			},
		},
		{
			what: "the empty path is never added and never covered",
			want: "added=false covered=false entries=0",
			run: func(d pathStoreDriver) string {
				a := d.add("", false)
				return fmt.Sprintf("added=%v covered=%v entries=%d", a, d.covers(""), d.count())
			},
		},
		{
			what: "an empty store covers nothing",
			want: "covered=false",
			run: func(d pathStoreDriver) string {
				return fmt.Sprintf("covered=%v", d.covers(dir))
			},
		},
	}

	// Vacuity floor: a table that lost its rows would pass every assertion below
	// while asserting nothing.
	if len(scenarios) < 8 {
		t.Fatalf("only %d scenarios; the table is too thin to hold two stores together", len(scenarios))
	}

	for _, sc := range scenarios {
		t.Run(sc.what, func(t *testing.T) {
			for _, mk := range []func() pathStoreDriver{trustDriver, unjailDriver} {
				d := mk()
				if got := sc.run(d); got != sc.want {
					t.Errorf("%s: %s\n got: %s\nwant: %s", d.name, sc.what, got, sc.want)
				}
			}
		})
	}
}

// The two predicates on their own, because each carries a floor that no
// caller can reach: Add, Remove and both resolve walks canonicalize the query
// and bail on "" before either predicate is asked. Reached only from here —
// which is the point. An entry with no path matching a query with no path is
// "every directory is trusted" spelled as a pair of empty strings, and a floor
// nothing exercises is a floor that quietly stops working.
func TestThePathIdentityPredicatesRefuseTheEmptyPath(t *testing.T) {
	dir := CanonicalTrustPath(testsupport.TempDir(t))

	for _, tc := range []struct {
		what string
		d    pathDecision
		real string
	}{
		{"an empty entry against an empty query", pathDecision{}, ""},
		{"an empty PARENT entry against an empty query", pathDecision{Parent: true}, ""},
		{"a real entry against an empty query", pathDecision{Real: dir}, ""},
		{"an empty entry against a real query", pathDecision{}, dir},
	} {
		if samePathDecision(tc.d, tc.real) {
			t.Errorf("samePathDecision matched %s", tc.what)
		}
		if coversPathDecision(tc.d, tc.real) {
			t.Errorf("coversPathDecision matched %s", tc.what)
		}
	}

	// The positive direction too, so this is not a predicate that only ever
	// says no — which would pass every assertion above.
	if !samePathDecision(pathDecision{Real: dir}, dir) {
		t.Error("samePathDecision does not match a directory against itself")
	}
	if !coversPathDecision(pathDecision{Real: dir}, dir) {
		t.Error("coversPathDecision does not match a directory against itself")
	}
	child := filepath.Join(dir, "nested")
	if !coversPathDecision(pathDecision{Real: dir, Parent: true}, CanonicalTrustPath(child)) {
		t.Error("a parent entry does not cover its child")
	}
}

// identityCallers names the functions allowed to call the path-identity
// primitives, with why. Everything else asks through samePathDecision /
// coversPathDecision.
//
// This is the rule the extraction exists to hold, and a list of names is not
// enough to hold it — so the call sites are found by scanning, and a call from
// anywhere else fails here. The alternative is what was there before: the same
// comparison written out at six sites across two files, which is what the note
// on canonicalEntryReal warns will drift.
// Two, and the list started longer: the entries below for samePathDecision,
// findPathDecision and the primitives themselves were written from what the
// rule looks like rather than from what it does, and the stale-licence check
// deleted them on the first run. A licence nobody uses is a licence waiting to
// permit the wrong thing.
var identityCallers = map[string]string{
	"entryReal":          "the one place a stored entry becomes a canonical match key",
	"coversPathDecision": "the one place a Parent entry is allowed to reach descendants",
}

// The identity primitives are called from the shared rule and nowhere else.
func TestThePathIdentityRuleHasOneImplementation(t *testing.T) {
	const guarded = "canonicalEntryReal"
	const guardedParent = "trustPathContains"

	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	scanned, calls := 0, 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", name, perr)
		}
		scanned++
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				id, ok := call.Fun.(*ast.Ident)
				if !ok || (id.Name != guarded && id.Name != guardedParent) {
					return true
				}
				calls++
				seen[fn.Name.Name]++
				if _, allowed := identityCallers[fn.Name.Name]; allowed {
					return true
				}
				t.Errorf("%s:%d — %s calls %s directly.\n"+
					"  Ask through samePathDecision (exact identity) or coversPathDecision (identity or a "+
					"covering parent). This comparison decides whether a directory is trusted and whether "+
					"it runs outside the sandbox; it existed at six sites across two files, and a seventh "+
					"is how the two stores start disagreeing about what the same directory is.",
					name, fset.Position(call.Pos()).Line, fn.Name.Name, id.Name)
				return true
			})
		}
	}

	// Vacuity floors. A walk that parsed nothing, or found no call at all, would
	// report a clean package either way.
	if scanned < 3 {
		t.Fatalf("only %d source files scanned in package config; the walk is broken", scanned)
	}
	if calls < 2 {
		t.Fatalf("found %d calls to the identity primitives; they are called at least twice, so the scan is broken", calls)
	}

	// A licence for a caller that no longer exists would silently re-permit a
	// direct call by that name later.
	for name, why := range identityCallers {
		if seen[name] == 0 {
			var have []string
			for k := range seen {
				have = append(have, k)
			}
			sort.Strings(have)
			t.Errorf("identityCallers permits %s (%s), but nothing by that name calls the primitives; "+
				"drop the entry. Present: %v", name, why, have)
		}
	}
}

// 🪤 The store goes in the GLOBAL home, and in project-scoped mode that is not
// $TERVA_HOME. Both saves used to create $TERVA_HOME — a directory the file
// never lands in — and got away with it because privfs.WriteFile makes its own
// target; the line was dead from the moment these stores moved to privfs.
//
// Pinned because the wrong-directory version WORKED, which is how it survived
// in both copies: nothing failed, a stray directory just appeared.
func TestAPathStoreSaveTouchesOnlyTheHomeItWritesTo(t *testing.T) {
	root := testsupport.TempDir(t)
	projectHome := filepath.Join(root, "repo", ".terva")
	globalHome := filepath.Join(root, "userhome")

	t.Setenv("TERVA_HOME", projectHome)
	prev := PinnedGlobalHome
	PinnedGlobalHome = globalHome
	t.Cleanup(func() { PinnedGlobalHome = prev })

	if TrustStorePath() != filepath.Join(globalHome, trustFileName) {
		t.Fatalf("trust store resolved to %q; the premise of this test is gone", TrustStorePath())
	}

	for _, tc := range []struct {
		what string
		save func() error
		file string
	}{
		{"trust", func() error { return TrustPath(root, false) }, TrustStorePath()},
		{"unjail", func() error { return UnjailPath(root, false) }, UnjailStorePath()},
	} {
		t.Run(tc.what, func(t *testing.T) {
			if err := os.RemoveAll(projectHome); err != nil {
				t.Fatal(err)
			}
			if err := tc.save(); err != nil {
				t.Fatalf("save: %v", err)
			}
			if _, err := os.Stat(tc.file); err != nil {
				t.Errorf("the store did not land at %s: %v", tc.file, err)
			}
			if _, err := os.Stat(projectHome); err == nil {
				t.Errorf("saving to the global home created %s, which the file never goes in", projectHome)
			}
		})
	}
}
