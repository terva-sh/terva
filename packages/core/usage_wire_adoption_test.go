package core

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// UsageToWire's own doc says it is exported "because the workspace kept a
// byte-identical private twin of it, and a twin is how a field gets added to
// one wire path and not the other". That happened again, in rpc.go: two
// hand-built maps carrying five of the nine fields.
//
// It matters most exactly there. The RPC driver persists no session, so its
// event stream is the ONLY place a caller can observe usage — and what the
// prompt cache was worth, how much of the output was reasoning, and how much
// was image data were all absent from it.
//
// The scan refuses a map literal that spells the wire's own key names. Those
// keys belong to WireUsage; a literal using them is a second encoder of the
// same record.
func TestNobodyHandBuildsAUsageMap(t *testing.T) {
	root := filepath.Join("..", "..")
	// The wire key names, read off the struct tags so a renamed field cannot
	// leave this scan hunting for a string nothing emits.
	keys := wireUsageJSONKeys(t)
	if len(keys) < 5 {
		t.Fatalf("read %d json tags off WireUsage; the reflection is broken", len(keys))
	}

	var offenders []string
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if testsupport.SkipScanDir(root, path, d) {
				return filepath.SkipDir
			}
			// testdata holds fake binaries that stand in for terva itself
			// (worker/testdata/cmd/faketerva). Emitting the wire shape by hand
			// is what they are FOR — a fake that imported the real converter
			// would agree with it by construction and test nothing. Skipped
			// here rather than in testsupport.SkipScanDir, because ten other
			// guards consult that predicate and whether they want testdata is
			// each guard's own question, not this one's.
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		// wire.go DEFINES the encoding.
		if rel == filepath.Join("packages", "core", "wire.go") {
			return nil
		}
		scanned++
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			hits := 0
			for _, elt := range cl.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				lit, ok := kv.Key.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if keys[strings.Trim(lit.Value, `"`)] {
					hits++
				}
			}
			// Three or more of the wire's own key names in one literal is an
			// encoder, not a coincidence.
			if hits >= 3 {
				offenders = append(offenders, rel)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 200 {
		t.Fatalf("scanned only %d files; the walk is broken and this proves nothing", scanned)
	}
	for _, o := range offenders {
		t.Errorf("%s builds a usage map by hand — use core.UsageToWire. The last two carried five of "+
			"nine fields, so cache value, reasoning split and image output were invisible on that path.", o)
	}
}

// And the converter itself must carry every field, by reflection, so a tenth
// added to provider.Usage is covered by having been added.
func TestUsageToWireCarriesEveryField(t *testing.T) {
	// A usage record with every numeric field distinct and non-zero, so a
	// dropped one shows up as a missing key rather than a coincidental match.
	u := provider.Usage{
		InputTokens: 11, OutputTokens: 22, CacheReadTokens: 33, CacheWriteTokens: 44,
		CostUSD: 5.5, CacheSavedUSD: 6.6, ReasoningTokens: 77, ReasoningTokensKnown: true,
		ImageOutputTokens: 88,
	}
	b, err := json.Marshal(UsageToWire(u))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}

	w := reflect.ValueOf(UsageToWire(u))
	for i := 0; i < w.NumField(); i++ {
		if w.Field(i).IsZero() {
			t.Errorf("UsageToWire leaves %s zero for a fully-populated usage record",
				w.Type().Field(i).Name)
		}
	}
	for key := range wireUsageJSONKeys(t) {
		if _, ok := got[key]; !ok {
			t.Errorf("the encoded form has no %q — a consumer reading this path cannot see it", key)
		}
	}
}

// wireUsageJSONKeys reads the wire key names off WireUsage's struct tags.
func wireUsageJSONKeys(t *testing.T) map[string]bool {
	t.Helper()
	typ := reflect.TypeOf(WireUsage{})
	out := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		out[strings.Split(tag, ",")[0]] = true
	}
	return out
}
