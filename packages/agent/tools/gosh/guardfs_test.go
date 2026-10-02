//go:build terva_gosh

package gosh

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"

	gobashfs "github.com/mark3labs/go-bash/fs"
)

// openOnPurpose are the FileSystem methods guardFS leaves to the mount.
// Each returns names or metadata and never a file's bytes, as the read
// tool's neighbours do.
var openOnPurpose = map[string]bool{
	"Stat": true, "Lstat": true, "ReadDir": true, "Realpath": true, "AllPaths": true,
}

// TestGuardFSCoversTheInterface fails when go-bash's FileSystem gains a
// method that guardFS neither guards nor lists as open.
//
// 🚨 guardFS embeds the interface, so a method it does not declare passes
// straight to the mount with no Sandbox check, and nothing else says so.
func TestGuardFSCoversTheInterface(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "guardfs.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		if star, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
			if id, ok := star.X.(*ast.Ident); ok && id.Name == "guardFS" {
				declared[fn.Name.Name] = true
			}
		}
	}
	iface := reflect.TypeOf((*gobashfs.FileSystem)(nil)).Elem()
	if iface.NumMethod() < 10 {
		t.Fatalf("the interface has %d methods, so the reflection read the wrong type", iface.NumMethod())
	}
	for i := 0; i < iface.NumMethod(); i++ {
		name := iface.Method(i).Name
		if !declared[name] && !openOnPurpose[name] {
			t.Errorf("guardFS does not guard FileSystem.%s: declare it on guardFS, or add it to openOnPurpose with a reason", name)
		}
		if declared[name] && openOnPurpose[name] {
			t.Errorf("FileSystem.%s is both guarded and listed as open", name)
		}
	}
}
