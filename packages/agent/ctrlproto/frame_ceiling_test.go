package ctrlproto_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// The WebSocket frame ceiling was declared twice — once in web/conn.go for the
// server's read limit, once in ctrlclient/ws.go for the client's — and kept
// equal by a comment asking a human to keep them in step.
//
// That is not a mechanism. The two are load-bearing in opposite directions: a
// CLIENT limit below the server's truncates a legitimate snapshot into a dead
// socket, and the browser sees only a closed connection with nothing naming the
// size. That is exactly how the previous 16 MiB ceiling was diagnosed, days
// after the fact, off a 19.6 MB character card.
//
// Both endpoints now read the protocol's constant. This scan is what stops a
// third one being typed: it reads the source for a hand-written frame ceiling
// anywhere outside this package.
func TestNobodyRedeclaresTheFrameCeiling(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	var offenders []string

	for _, rel := range []string{
		filepath.Join("packages", "agent", "web"),
		filepath.Join("packages", "agent", "ctrlproto", "ctrlclient"),
	} {
		dir := filepath.Join(root, rel)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		seen := 0
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			seen++
			path := filepath.Join(dir, e.Name())
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				vs, ok := n.(*ast.ValueSpec)
				if !ok {
					return true
				}
				for i, name := range vs.Names {
					if !strings.Contains(strings.ToLower(name.Name), "framebytes") {
						continue
					}
					if i >= len(vs.Values) {
						continue
					}
					// A literal is a second source of truth; a selector
					// (ctrlproto.MaxFrameBytes) is the shared one.
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok {
						offenders = append(offenders, rel+"/"+e.Name()+": "+name.Name+" = "+lit.Value)
					} else if bin, ok := vs.Values[i].(*ast.BinaryExpr); ok {
						if _, isLit := bin.X.(*ast.BasicLit); isLit {
							offenders = append(offenders, rel+"/"+e.Name()+": "+name.Name+" is a hand-typed expression")
						}
					}
				}
				return true
			})
		}
		if seen == 0 {
			t.Fatalf("scanned no non-test Go files under %s; the walk is broken", rel)
		}
	}

	for _, o := range offenders {
		t.Errorf("%s — the frame ceiling is ctrlproto.MaxFrameBytes. A second declaration is kept in "+
			"step by nobody, and a client below the server truncates a real snapshot into a dead socket.", o)
	}
}

// The derived upload bound must stay derived. Typed independently it drifts from
// the frame it has to fit inside, and a client pre-flights against a number the
// transport will not honour.
func TestTheUploadBoundStaysDerivedFromTheFrame(t *testing.T) {
	if want := ctrlproto.MaxFrameBytes / 4 * 3; ctrlproto.MaxUploadFileBytes != want {
		t.Errorf("MaxUploadFileBytes = %d, want %d (the frame discounted by base64 inflation)",
			ctrlproto.MaxUploadFileBytes, want)
	}
	if ctrlproto.MaxUploadFileBytes >= ctrlproto.MaxFrameBytes {
		t.Error("the upload bound is not below the frame that must carry it base64-encoded")
	}
}
