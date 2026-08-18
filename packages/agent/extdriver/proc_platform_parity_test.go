package extdriver

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// procPair is one platform-split process-control pair in the tree.
//
// Both were written unix-first and given a Windows file that satisfied the
// compiler and nothing else: isolation was a no-op, so a subprocess's
// background children were orphaned, and the graceful stage either did not
// exist (tools/bash_windows.go killed outright) or could not work
// (extdriver sent syscall.SIGTERM, which Windows does not implement).
//
// A no-op with a comment reads as a considered decision. These were not — the
// unix files carry a PAIRING note saying every teardown must signal the GROUP
// "otherwise a daemon-style extension's background children are orphaned",
// which is not a unix-specific claim.
var procPairs = []struct {
	dir, unix, windows string
	// isolate is the function whose Windows body must actually do something.
	isolate string
}{
	{filepath.Join("..", "extdriver"), "proc_unix.go", "proc_windows.go", "isolateExtensionProcess"},
	{filepath.Join("..", "tools"), "bash_unix.go", "bash_windows.go", "setProcessGroup"},
}

// Every function the rest of the package CALLS must exist on both halves of a
// platform pair. A Windows file missing one does not fail the build until
// someone builds for Windows, which on a unix-only development machine is
// never.
//
// Scoped to the shared contract rather than to every declaration: a private
// helper used only inside the unix file (signalExtensionGroup) is an
// implementation detail, and demanding a Windows twin for it would be
// demanding a copy of the unix design.
func TestEachProcessPairDeclaresTheSameFunctions(t *testing.T) {
	for _, p := range procPairs {
		unix := funcsIn(t, filepath.Join(p.dir, p.unix))
		win := funcsIn(t, filepath.Join(p.dir, p.windows))
		called := calledElsewhere(t, p.dir, p.unix, p.windows)

		shared := 0
		for fn := range unix {
			if !called[fn] {
				continue // an internal helper of the unix implementation
			}
			shared++
			if !win[fn] {
				t.Errorf("%s declares %s, the rest of the package calls it, and %s does not "+
					"declare it — the Windows build has no equivalent", p.unix, fn, p.windows)
			}
		}
		if shared < 2 {
			t.Fatalf("%s: found %d shared functions; the scan is not reading the package", p.unix, shared)
		}
	}
}

// calledElsewhere returns the function names referenced by files in dir other
// than the two platform files themselves.
func calledElsewhere(t *testing.T, dir, unix, windows string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || name == unix || name == windows {
			continue
		}
		f, perr := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, 0)
		if perr != nil {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok {
				out[id.Name] = true
			}
			return true
		})
	}
	return out
}

// The isolation function must not be an empty body on Windows.
//
// This is the specific regression: `func setProcessGroup(_ *exec.Cmd) {}` and
// `func isolateExtensionProcess(_ *exec.Cmd) {}` both compiled, both read as
// deliberate, and both meant every background child of a tool call or a
// daemon-style extension outlived the thing that spawned it.
func TestWindowsProcessIsolationIsNotANoOp(t *testing.T) {
	for _, p := range procPairs {
		path := filepath.Join(p.dir, p.windows)
		body := funcBody(t, path, p.isolate)
		if body == nil {
			t.Errorf("%s does not declare %s", p.windows, p.isolate)
			continue
		}
		if len(body.List) == 0 {
			t.Errorf("%s: %s has an empty body. A subprocess that leads no group cannot be torn "+
				"down as one, so its background children are orphaned — which is what the unix "+
				"file's PAIRING note says must not happen.", p.windows, p.isolate)
		}
	}
}

// The Windows teardown must reach the GROUP, not just the lead process.
// p.Signal(syscall.SIGTERM) is unimplemented on Windows — it returns an error
// and does nothing — so a file that calls only that has a graceful stage in
// name only.
func TestWindowsTeardownAddressesTheGroup(t *testing.T) {
	for _, p := range procPairs {
		src := readSourceFile(t, filepath.Join(p.dir, p.windows))
		if !strings.Contains(src, "CTRL_BREAK_EVENT") {
			t.Errorf("%s never sends CTRL_BREAK_EVENT — the console equivalent of SIGTERM and the "+
				"only signal that reaches a whole process group on Windows", p.windows)
		}
		if strings.Contains(src, "syscall.SIGTERM") {
			t.Errorf("%s signals syscall.SIGTERM, which Windows does not implement: the call errors "+
				"and the process is never asked to stop", p.windows)
		}
	}
}

func funcsIn(t *testing.T, path string) map[string]bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	out := map[string]bool{}
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
			out[fd.Name.Name] = true
		}
	}
	return out
}

func funcBody(t *testing.T, path, name string) *ast.BlockStmt {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == name {
			return fd.Body
		}
	}
	return nil
}

func readSourceFile(t *testing.T, path string) string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	// Rendering from the AST rather than reading bytes keeps a match off a
	// comment that merely MENTIONS the constant.
	var names []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.Ident:
			names = append(names, v.Name)
		case *ast.SelectorExpr:
			if id, ok := v.X.(*ast.Ident); ok {
				names = append(names, id.Name+"."+v.Sel.Name)
			}
		}
		return true
	})
	sort.Strings(names)
	return strings.Join(names, "\n")
}
