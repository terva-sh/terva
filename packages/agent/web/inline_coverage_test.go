//go:build terva_web

package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/attach"
	"terva.sh/terva/packages/testsupport"
)

// The join between two tables in two packages that nothing was comparing.
//
// attach decides what a file's type IS; this package decides what may RENDER.
// Both are hand-maintained maps, and a type in the first with no entry in the
// second is a player that silently becomes a download — invisible to every test
// either package has, because each one is internally consistent.
//
// That gap has already shipped twice. macOS answered audio/x-wav where this
// allowlist says audio/wav, so a shared .wav rendered nowhere on any host; and
// .aac derived to audio/aac, which this allowlist did not carry at all. Neither
// was a mistake anyone could see by reading one file.
//
// So the extensions enroll themselves from attach's own pinned table, and each
// one is pushed through the REAL derivation rather than compared literal to
// literal — which is what makes this catch a derivation that changes its answer,
// not just a map that changes its contents. The x-wav bug would have failed
// here before it was found.

// declinedInline names the derivable types this route deliberately will not
// render, each with the reason. An entry here is a decision; its absence is the
// failure above.
//
// Both are container formats no browser plays natively, so an inline response
// would render a dead player where a download works. They stay derivable because
// deriving a type and deciding what may render are different questions: a .mov
// is still a video in the manifest the model reads.
var declinedInline = map[string]string{
	"video/quicktime":  "no browser plays QuickTime natively; an inline response is a dead player",
	"video/x-matroska": "no browser plays Matroska natively; an inline response is a dead player",
}

// sniffProof is a body that http.DetectContentType cannot identify, so the only
// thing that can answer for these files is the extension table under test. A
// body that sniffed would let the derivation pass for a reason the deployment
// this guards (a container with no system MIME database) does not have.
const sniffProof = "\x00\x01\x02 not any known format \x03\x04\x05"

func TestEveryDerivableMediaTypeCanBeRenderedOrIsDeclined(t *testing.T) {
	store := attach.NewStoreAt(testsupport.TempDir(t))

	var undecided []string
	for _, ext := range pinnedMediaExtensions(t) {
		// The real derivation: stage a file with this extension and ask the
		// store what it decided the type is.
		ref, err := store.Stage("ses_1", "probe"+ext, strings.NewReader(sniffProof))
		if err != nil {
			t.Fatalf("stage %s: %v", ext, err)
		}
		switch {
		case ref.Mime == "":
			t.Errorf("%s derives no type at all — it is in attach's pinned table", ext)
		case inlineTypes[ref.Mime]:
		case declinedInline[ref.Mime] != "":
		default:
			undecided = append(undecided, ext+" -> "+ref.Mime)
		}
	}
	sort.Strings(undecided)
	if len(undecided) > 0 {
		t.Errorf("derivable media types with no rendering decision:\n  %s\n"+
			"Each must be added to inlineTypes (the browser can play it) or to declinedInline "+
			"with a reason (it cannot). Left out, it becomes a download with nothing saying why.",
			strings.Join(undecided, "\n  "))
	}
}

// A declined type that is no longer derivable is a reason kept for a case that
// cannot happen — the same rot the allow-list exists to prevent, one level up.
func TestNoStaleInlineDeclensions(t *testing.T) {
	store := attach.NewStoreAt(testsupport.TempDir(t))

	derivable := map[string]bool{}
	for _, ext := range pinnedMediaExtensions(t) {
		ref, err := store.Stage("ses_1", "probe"+ext, strings.NewReader(sniffProof))
		if err != nil {
			t.Fatalf("stage %s: %v", ext, err)
		}
		derivable[ref.Mime] = true
	}
	for mime, reason := range declinedInline {
		if !derivable[mime] {
			t.Errorf("declinedInline names %q (%q), which nothing derives any more — drop the entry", mime, reason)
		}
		if reason == "" {
			t.Errorf("declinedInline names %q with no reason", mime)
		}
	}
}

// A type in both tables is a contradiction: rendered and declined at once.
func TestNoTypeIsBothInlineAndDeclined(t *testing.T) {
	for mime := range declinedInline {
		if inlineTypes[mime] {
			t.Errorf("%q is in inlineTypes AND declinedInline — one of them is a lie", mime)
		}
	}
}

// pinnedMediaExtensions reads attach's mediaTypes map from source.
//
// From SOURCE, and not from a list written here, because a second hand-written
// list would have exactly the drift problem this file exists to close. Reading
// the map's keys means an extension added over there enrolls itself here and
// fails until someone decides what it renders as.
//
// Only the keys are taken; the values come from the live derivation above, so
// this cannot pass by agreeing with a literal that the code no longer produces.
func pinnedMediaExtensions(t *testing.T) []string {
	t.Helper()
	path := filepath.Join(repoRootFromWeb(t), "packages", "agent", "attach", "attach.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read attach.go: %v", err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
	if err != nil {
		t.Fatalf("parse attach.go: %v", err)
	}
	var exts []string
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "mediaTypes" || len(vs.Values) != 1 {
			return true
		}
		lit, ok := vs.Values[0].(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			k, ok := kv.Key.(*ast.BasicLit)
			if !ok || k.Kind != token.STRING {
				continue
			}
			exts = append(exts, strings.Trim(k.Value, `"`))
		}
		return false
	})
	if len(exts) == 0 {
		t.Fatal("parsed no entries from attach's mediaTypes — the guard would pass vacuously")
	}
	sort.Strings(exts)
	return exts
}

// repoRootFromWeb walks up to the module root, so the test does not care where
// `go test` was invoked from.
func repoRootFromWeb(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}
