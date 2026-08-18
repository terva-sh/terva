package connproto

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// declaredFrameTypes discovers connproto's frame structs from source rather than
// from a list, by the naming convention the wire uses: a frame is sent either by
// the host or by the connector, and says so in its name.
func declaredFrameTypes(t *testing.T) map[string]bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "connproto.go", nil, 0)
	if err != nil {
		t.Fatalf("parse connproto.go: %v", err)
	}
	out := map[string]bool{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if _, ok := ts.Type.(*ast.StructType); !ok {
				continue
			}
			name := ts.Name.Name
			if strings.HasSuffix(name, "FromHost") || strings.HasSuffix(name, "FromConn") {
				out[name] = true
			}
		}
	}
	return out
}

// TestCorpusCoversEveryFrameType is why connproto's golden corpus can be trusted
// as a complete reference rather than a sample.
//
// connproto's corpus harness is a near-verbatim copy of extproto's, but it never
// received extproto's completeness census — and that census exists because
// extproto's corpus had already fallen behind the protocol once, quietly: the
// protocol-6 secret verbs shipped with no golden entry, so terva-sdk-rust had to
// source-read extproto.go to implement them and left a comment saying so.
// Nothing failed, because a table that names its own subjects cannot notice a
// subject that was never added.
//
// connproto's corpus is complete today. It had no way to stay complete: every
// entry is hand-written, and a new FromHost/FromConn frame would simply not
// appear. Now it fails here, on the commit that adds the type, naming itself.
func TestCorpusCoversEveryFrameType(t *testing.T) {
	declared := declaredFrameTypes(t)
	if len(declared) < 10 {
		t.Fatalf("discovered %d frame types in connproto.go; the scan is broken and this census "+
			"would pass over anything", len(declared))
	}

	covered := map[string]bool{}
	for _, tc := range goldenFrames {
		covered[reflect.TypeOf(tc.v).Name()] = true
	}
	if len(covered) == 0 {
		t.Fatal("the corpus yielded no type names; this census is passing vacuously")
	}

	var missing []string
	for name := range declared {
		if !covered[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("frame types with no entry in goldenFrames — add one, so connector SDKs in other "+
			"languages read the bytes here instead of reverse-engineering them out of connproto.go:\n  %s",
			strings.Join(missing, "\n  "))
	}

	// The reverse direction: an entry for a type that no longer exists publishes
	// a frame the protocol has retired, which is worse than publishing nothing.
	var stale []string
	for name := range covered {
		if !declared[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("goldenFrames pins types that connproto.go no longer declares: %v", stale)
	}
}

// TestFrameCensusCatchesANewFrame is the teeth. A complete corpus and a census
// that discovers nothing produce identical output, so drive the comparison with
// a type the corpus deliberately does not cover.
func TestFrameCensusCatchesANewFrame(t *testing.T) {
	declared := declaredFrameTypes(t)
	covered := map[string]bool{}
	for _, tc := range goldenFrames {
		covered[reflect.TypeOf(tc.v).Name()] = true
	}

	// Simulate the protocol gaining a frame nobody pinned.
	declared["SomethingBrandNewFromHost"] = true

	var missing []string
	for name := range declared {
		if !covered[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) != 1 || missing[0] != "SomethingBrandNewFromHost" {
		t.Fatalf("census did not flag an unpinned new frame type: got %v", missing)
	}

	// And pinning it must silence the census.
	covered["SomethingBrandNewFromHost"] = true
	missing = missing[:0]
	for name := range declared {
		if !covered[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		t.Fatalf("census still flags the frame after it is pinned: %v", missing)
	}
}
