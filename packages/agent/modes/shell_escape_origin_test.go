package modes

// The shell escape ("!command") is the EDITOR's affordance and nobody else's.
// startShellEscape runs bash with no confirm gate and no audit record, so any
// programmatic caller that reaches it gets ungated command execution on the
// host.
//
// Before this was structural, `shellEscapeCommand` was parsed inside
// SubmitOrQueue — a choke point shared by the chat bridge and the auto-swarm
// recap injector — so a paired Telegram DM reading "!rm -rf ~" executed
// immediately, ahead of even the login check.
//
// This file keeps the STRUCTURAL half: one call site, so a future caller cannot
// silently inherit the escape. The behavioural half (a paired DM is prose)
// followed the bridge into the workspace, where the bridge now lives — see
// TestPairedDMCannotShellEscape in packages/agent/workspace. The TUI no longer
// has a chat.Host to test.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// --- structural guard -------------------------------------------------------

// shellEscapeCallSites returns the non-test .go files in this package that call
// shellEscapeCommand, parsed from source rather than grepped, so a rename or a
// call inside a string/comment cannot fool it. Test files are exempt: the
// parser has its own unit test, and a test calling it executes nothing.
func shellEscapeCallSites(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	var sites []string
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			if filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				id, ok := call.Fun.(*ast.Ident)
				if !ok || id.Name != "shellEscapeCommand" {
					return true
				}
				sites = append(sites, filepath.Base(name))
				return true
			})
		}
	}
	return sites
}

// TestShellEscapeHasOneCallSite: the escape is parsed only where a human
// keystroke is known. Any other caller — a chat DM, an extension's Submit, a
// sub-agent's recap — would inherit ungated shell execution.
func TestShellEscapeHasOneCallSite(t *testing.T) {
	const allowed = "interactive_input.go"
	sites := shellEscapeCallSites(t)
	if len(sites) == 0 {
		t.Fatal("no call sites found: the AST probe is broken, not the invariant")
	}
	for _, s := range sites {
		if s != allowed {
			t.Errorf("shellEscapeCommand called from %s; only %s (the editor key path) may. "+
				"startShellEscape has no confirm gate and no audit record — a programmatic "+
				"caller reaching it is ungated command execution on the host.", s, allowed)
		}
	}
	if len(sites) != 1 {
		t.Errorf("expected exactly 1 call site, found %d: %v", len(sites), sites)
	}
}
