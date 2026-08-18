package connsdk

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// A connector that SEALS its state must also DECLARE it.
//
// Sealing is Config.Secrets-independent: SealedState.Save works fine on its own,
// writes real ciphertext, and looks completely healthy. What the declaration
// buys is everything AFTER the write. Config.secretsDecl is the only producer of
// hello.Secrets, connhost fires OnSecrets only when that field is non-nil, and
// Proxy.recordSecrets is the only production caller of the component registry's
// Record. With no declaration terva holds ciphertext it can never re-seal:
// `terva secret rotate --revoke` skips the file, and secretcmd's own advice for
// that case — "it never registered a recipient... start it once and re-run to
// restore access" — is unfollowable, because starting a connector that does not
// declare changes nothing. The operator loops forever.
//
// terva-discord-connector shipped in exactly that state: it constructed a
// SealedState in one commit and the declaration path landed in a later one that
// touched no file under cmd/. Nothing failed, because nothing was watching.
//
// The scan enrolls by SHAPE, not by a list of connector names: a binary added
// tomorrow is covered the day it is written. That distinction is the whole
// lesson of packages/agent/build/host_census_test.go — "a list of names is not a
// census — it cannot fail when a host is ADDED".
const repoRoot = "../../.."

// connectorFacts is what one Go package says about its own sealed state.
type connectorFacts struct {
	// seals is true when the package constructs a connsdk.SealedState.
	seals bool
	// declares is true when it passes Secrets: in a connsdk.Config literal.
	declares bool
	// sealsAt / declaresAt are for the failure message.
	sealsAt    string
	declaresAt string
}

// inspectPackage folds every non-test Go file in dir into one verdict. Split out
// from the walk so TestTheDeclarationGateCatchesAConnectorThatOnlySeals can
// drive it with source that does not exist in the tree.
func inspectPackage(t *testing.T, fset *token.FileSet, files map[string]string) connectorFacts {
	t.Helper()
	var facts connectorFacts
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		f, err := parser.ParseFile(fset, name, files[name], 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		local := connsdkLocalName(f)
		if local == "" {
			continue // does not import connsdk at all
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := lit.Type.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != local {
				return true
			}
			where := name + ":" + strconv.Itoa(fset.Position(lit.Pos()).Line)
			switch sel.Sel.Name {
			case "SealedState":
				facts.seals = true
				facts.sealsAt = where
			case "Config":
				for _, el := range lit.Elts {
					kv, ok := el.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Secrets" {
						facts.declares = true
						facts.declaresAt = where
					}
				}
			}
			return true
		})
	}
	return facts
}

// connsdkLocalName returns the identifier this file refers to connsdk by, or ""
// when it does not import it. Resolved rather than assumed: an aliased import
// would otherwise read as "no SealedState here" and the gate would pass by
// failing to look.
func connsdkLocalName(f *ast.File) string {
	const path = "terva.sh/terva/packages/agent/connsdk"
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != path {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "connsdk"
	}
	return ""
}

func TestAConnectorThatSealsAlsoDeclares(t *testing.T) {
	fset := token.NewFileSet()
	cmdDir := filepath.Join(repoRoot, "cmd")
	entries, err := os.ReadDir(cmdDir)
	if err != nil {
		t.Fatal(err)
	}
	var scanned, sealing int
	var undeclared []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(cmdDir, e.Name())
		files, err := readGoPackage(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			continue
		}
		scanned++
		facts := inspectPackage(t, fset, files)
		if !facts.seals {
			continue
		}
		sealing++
		if !facts.declares {
			undeclared = append(undeclared, e.Name()+": constructs connsdk.SealedState at "+facts.sealsAt+
				" but never passes Secrets: in its connsdk.Config — terva can never re-seal this connector's file")
		}
	}
	if scanned < 4 {
		t.Fatalf("only %d packages under cmd/ were scanned; the walk is broken and this gate proves nothing", scanned)
	}
	if sealing < 2 {
		t.Fatalf("found %d connectors constructing a connsdk.SealedState, expected at least the discord and telegram "+
			"binaries; the literal matcher is broken and this gate proves nothing", sealing)
	}
	sort.Strings(undeclared)
	if len(undeclared) > 0 {
		t.Errorf("a connector seals its state without declaring it:\n  %s", strings.Join(undeclared, "\n  "))
	}
}

// readGoPackage returns the non-test Go sources in dir, keyed by path.
func readGoPackage(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		files[filepath.Join(dir, name)] = string(b)
	}
	return files, nil
}

// The gate's teeth, driven with synthetic packages rather than by mutating the
// tree — so this proves the classifier distinguishes the two shapes, not merely
// that today's tree happens to be clean.
func TestTheDeclarationGateCatchesAConnectorThatOnlySeals(t *testing.T) {
	const sealsOnly = `package main

import "terva.sh/terva/packages/agent/connsdk"

var state = connsdk.SealedState{Name: "x", Paths: []string{"/bot_token"}}

func main() {
	connsdk.Main(connsdk.Config{Name: "x", Configured: func() bool { return true }})
}
`
	const sealsAndDeclares = `package main

import "terva.sh/terva/packages/agent/connsdk"

var state = connsdk.SealedState{Name: "x", Paths: []string{"/bot_token"}}

func main() {
	connsdk.Main(connsdk.Config{Name: "x", Secrets: &state})
}
`
	// An aliased import must still be seen; resolving the local name is what
	// stops the gate from passing because it looked for the wrong identifier.
	const aliased = `package main

import sdk "terva.sh/terva/packages/agent/connsdk"

var state = sdk.SealedState{Name: "x", Paths: []string{"/bot_token"}}

func main() { sdk.Main(sdk.Config{Name: "x"}) }
`
	// A connector that holds no sealed state at all is not in scope: the rule is
	// "seals ⇒ declares", not "every connector declares". packages/agent/ext's
	// connector adapter cannot declare — Extension.Connector takes no secrets
	// argument — and must not be dragged in by an over-broad rule.
	const neither = `package main

import "terva.sh/terva/packages/agent/connsdk"

func main() { connsdk.Main(connsdk.Config{Name: "x"}) }
`
	cases := []struct {
		name            string
		src             string
		seals, declares bool
	}{
		{"seals only", sealsOnly, true, false},
		{"seals and declares", sealsAndDeclares, true, true},
		{"aliased import, seals only", aliased, true, false},
		{"no sealed state", neither, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			facts := inspectPackage(t, token.NewFileSet(), map[string]string{"main.go": tc.src})
			if facts.seals != tc.seals {
				t.Errorf("seals = %v, want %v", facts.seals, tc.seals)
			}
			if facts.declares != tc.declares {
				t.Errorf("declares = %v, want %v", facts.declares, tc.declares)
			}
		})
	}
}
