package ctrlproto

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// ctrlproto is the largest of terva's three control surfaces and had the
// weakest wire pinning of the three:
//
//	surface     wire types   golden entries   completeness census
//	connproto           24               45   yes
//	extproto            49               89   yes
//	ctrlproto          145                8   NO
//
// connproto's frame_census_test.go says why the other two have one: extproto's
// corpus had already fallen behind the protocol once, silently, and
// terva-sdk-rust had to source-read extproto.go to implement the protocol-6
// secret verbs. "A table that names its own subjects cannot notice a subject
// that was never added."
//
// The TypeScript mirror shows the same drift here, and types.ts is honest about
// it: the verb vocabulary is checked 145/145, PARAMS are checked only for the 41
// verbs VerbParams maps, and RESULT and EVENT shapes are unchecked — responses
// are cast, not validated. verbs.test.ts names a bug found that way
// (NextSceneParams.world, sent by the client, absent from the mirror, working
// only because send() took `unknown`).
//
// WHY THIS CENSUS PINS SCHEMAS, NOT BYTES. The sibling surfaces pin example
// frames: a golden JSON document per frame type. Copying that here would mean
// ~250 fixtures (276 json-tagged structs), each needing invented representative
// values, and — because they cannot all be written at once — a gap list seeded
// with well over a hundred entries. This repo already shows where that ends:
// notForwarded's 15 entries are 13 restatements of "web-only today", which is a
// rubber stamp rather than a decision record.
//
// So this census pins the SHAPE instead, derived mechanically from source: every
// wire struct's JSON field set, and every verb's params/result types. Nothing is
// invented, nothing is sampled, there is no gap list, and it is complete from
// the first commit. It also pins the thing an SDK author in another language
// actually needs — the schema, not one example of it. The existing golden FRAMES
// in wire_test.go keep doing what they are good at: proving the encoder's exact
// bytes for a representative few.
//
// WHAT IT CATCHES, precisely — because a guard that overstates its reach is how
// types.ts came to claim the Go golden frames pinned it while never having read
// a Go file:
//
//   - A json tag RENAMED, or DROPPED (which silently switches the wire name to
//     the Go field name — `title` becomes `Title`, and case matters on the
//     wire). Neither is visible to the compiler; both were invisible here.
//   - A field ADDED, REMOVED, or RETYPED, and omitempty gained or lost. Also
//     invisible to the compiler, and the exact class the TS mirror is unguarded
//     against for every result and event shape.
//   - A NEW VERB landing, or an existing verb's params/result type changing
//     shape — as review VISIBILITY, not as detection.
//
// What it does NOT add: swapping one params STRUCT for another in a dispatch
// entry is already a compile error, because the handler passes that value
// straight to a controller method with a fixed signature. The census records
// such a change; the compiler is what stops it.
//
// So the value here is concentrated in field-level drift on types nobody
// type-checks across the language boundary. That is where the known bug was.

// --- rendering Go type expressions ---

// exprString renders a type expression the way a wire reader cares about it.
func exprString(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + exprString(t.X)
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + exprString(t.Elt)
		}
		return "[" + exprString(t.Len) + "]" + exprString(t.Elt)
	case *ast.MapType:
		return "map[" + exprString(t.Key) + "]" + exprString(t.Value)
	case *ast.SelectorExpr:
		return exprString(t.X) + "." + t.Sel.Name
	case *ast.InterfaceType:
		if t.Methods == nil || len(t.Methods.List) == 0 {
			return "any"
		}
		return "interface{...}"
	case *ast.BasicLit:
		return t.Value
	case *ast.Ellipsis:
		return "..." + exprString(t.Elt)
	case *ast.FuncType:
		return "func"
	case *ast.ChanType:
		return "chan " + exprString(t.Value)
	default:
		return fmt.Sprintf("?%T", e)
	}
}

// packageFiles lists the package's non-test Go sources.
func packageFiles(t *testing.T) []string {
	t.Helper()
	all, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package sources: %v", err)
	}
	var out []string
	for _, p := range all {
		if !strings.HasSuffix(p, "_test.go") {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	if len(out) < 20 {
		t.Fatalf("found only %d non-test sources; the glob is not seeing the package", len(out))
	}
	return out
}

// --- the struct schema census ---

// wireField is one field as it appears on the wire.
type wireField struct {
	Name  string // the JSON name, or "<embedded>"
	Type  string
	Flags string // "omitempty", "string", … from the json tag
}

// wireStructs maps every exported struct carrying at least one json tag to its
// wire fields, in declaration order.
//
// Declaration order, not sorted: the order is how a person reads the type, and
// reordering fields is not a wire change — so a diff that only reorders is
// visible but harmless, while a sorted rendering would hide the reorder and
// show nothing.
func wireStructs(t *testing.T) map[string][]wireField {
	t.Helper()
	out := map[string][]wireField{}

	for _, path := range packageFiles(t) {
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !ts.Name.IsExported() {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					continue
				}
				fields, tagged := structWireFields(st)
				// Only types that actually travel: a struct with no json tag
				// anywhere is an internal shape, not a wire contract.
				if tagged {
					out[ts.Name.Name] = fields
				}
			}
		}
	}
	if len(out) < 250 {
		t.Fatalf("found only %d json-tagged wire structs; the parse is not seeing them", len(out))
	}
	return out
}

// structWireFields renders one struct's fields, and reports whether any carried
// a json tag.
func structWireFields(st *ast.StructType) ([]wireField, bool) {
	var out []wireField
	tagged := false
	for _, fld := range st.Fields.List {
		typeStr := exprString(fld.Type)

		jsonName, flags, skip, has := jsonTag(fld)
		if has {
			tagged = true
		}
		if skip {
			continue // json:"-" never reaches the wire
		}

		if len(fld.Names) == 0 {
			// Embedded: encoding/json inlines its fields.
			name := jsonName
			if name == "" {
				name = "<embedded>"
			}
			out = append(out, wireField{Name: name, Type: typeStr, Flags: flags})
			continue
		}
		for _, n := range fld.Names {
			if !n.IsExported() {
				continue // unexported fields never marshal
			}
			name := jsonName
			if name == "" {
				name = n.Name // encoding/json falls back to the Go name
			}
			out = append(out, wireField{Name: name, Type: typeStr, Flags: flags})
		}
	}
	return out, tagged
}

// jsonTag pulls the json name and options off a field.
func jsonTag(fld *ast.Field) (name, flags string, skip, has bool) {
	if fld.Tag == nil {
		return "", "", false, false
	}
	raw := strings.Trim(fld.Tag.Value, "`")
	v, ok := reflect.StructTag(raw).Lookup("json")
	if !ok {
		return "", "", false, false
	}
	parts := strings.Split(v, ",")
	name = parts[0]
	if name == "-" && len(parts) == 1 {
		return "", "", true, true
	}
	if len(parts) > 1 {
		flags = strings.Join(parts[1:], ",")
	}
	return name, flags, false, true
}

// --- the verb index ---

// verbWire is a verb's params and result type names ("-" when it has none).
type verbWire struct {
	Params string
	Result string
}

// verbWireTypes maps each verb to the params and result types its dispatch
// entry binds, read from the four shapes' function literals:
//
//	do  (…, func(c C, ctx, f Frame) error)              — neither
//	get (…, func(c C, ctx, f Frame) (R, error))         — result only
//	act (…, func(c C, ctx, f Frame, p P) error)         — params only
//	ask (…, func(c C, ctx, f Frame, p P) (R, error))    — both
func verbWireTypes(t *testing.T) map[string]verbWire {
	t.Helper()
	values := methodValuesByName(t)

	f, err := parser.ParseFile(token.NewFileSet(), "dispatch_table.go", nil, 0)
	if err != nil {
		t.Fatalf("parse dispatch_table.go: %v", err)
	}

	out := map[string]verbWire{}
	ast.Inspect(f, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			return true
		}
		verb, ok := values[key.Name]
		if !ok {
			return true
		}
		call, ok := kv.Value.(*ast.CallExpr)
		if !ok {
			return true
		}
		for _, a := range call.Args {
			lit, ok := a.(*ast.FuncLit)
			if !ok {
				continue
			}
			w := verbWire{Params: "-", Result: "-"}

			// Flatten the parameter list; the 4th entry (after receiver, ctx,
			// frame) is the params struct when the shape carries one.
			var params []ast.Expr
			if lit.Type.Params != nil {
				for _, p := range lit.Type.Params.List {
					reps := len(p.Names)
					if reps == 0 {
						reps = 1
					}
					for i := 0; i < reps; i++ {
						params = append(params, p.Type)
					}
				}
			}
			if len(params) >= 4 {
				w.Params = exprString(params[3])
			}
			// (R, error) carries a result; (error) alone does not.
			if lit.Type.Results != nil && len(lit.Type.Results.List) == 2 {
				w.Result = exprString(lit.Type.Results.List[0].Type)
			}
			out[verb] = w
			break
		}
		return true
	})
	if len(out) < 140 {
		t.Fatalf("resolved only %d verb wire signatures; the parse is not seeing the table", len(out))
	}
	return out
}

// --- the goldens ---

// TestWireVerbIndexIsPinned pins every verb's params and result type.
func TestWireVerbIndexIsPinned(t *testing.T) {
	verbs := verbWireTypes(t)

	lines := make([]string, 0, len(verbs))
	for verb, w := range verbs {
		lines = append(lines, fmt.Sprintf("%s\tparams=%s\tresult=%s", verb, w.Params, w.Result))
	}
	sort.Strings(lines)
	got := []byte(strings.Join(lines, "\n") + "\n")

	compareGolden(t, filepath.Join("testdata", "wire_verbs.txt"), got,
		"A verb's params or result type changed, or a verb was added.\n"+
			"Every line is a wire contract three front ends depend on (ctrlclient, "+
			"the TS mirror, and any third-party SDK).")
}

// TestWireStructSchemaIsPinned pins every wire struct's JSON field set.
func TestWireStructSchemaIsPinned(t *testing.T) {
	structs := wireStructs(t)

	names := make([]string, 0, len(structs))
	for n := range structs {
		names = append(names, n)
	}
	sort.Strings(names)

	var b strings.Builder
	for _, n := range names {
		b.WriteString(n)
		b.WriteString("\n")
		for _, f := range structs[n] {
			if f.Flags != "" {
				fmt.Fprintf(&b, "\t%s\t%s\t%s\n", f.Name, f.Type, f.Flags)
				continue
			}
			fmt.Fprintf(&b, "\t%s\t%s\n", f.Name, f.Type)
		}
	}

	compareGolden(t, filepath.Join("testdata", "wire_types.txt"), []byte(b.String()),
		"A wire struct's JSON shape changed.\n"+
			"A renamed or retyped field breaks every client that reads it; an added "+
			"field is additive and safe; a removed one is not.")
}

// TestEveryVerbTypeIsPinned is the census property itself: a type a verb names
// must appear in the schema golden. This is what stops the two files drifting
// apart — a verb gaining a params struct that nothing describes is exactly the
// hole terva-sdk-rust fell into on extproto.
func TestEveryVerbTypeIsPinned(t *testing.T) {
	verbs := verbWireTypes(t)
	structs := wireStructs(t)

	checked := 0
	for verb, w := range verbs {
		for label, typ := range map[string]string{"params": w.Params, "result": w.Result} {
			if typ == "-" {
				continue
			}
			// Strip the decorations a wire reader sees through.
			bare := strings.TrimPrefix(strings.TrimPrefix(typ, "[]"), "*")
			// A type from another package (core.WireUsage) is pinned by its own
			// package, not here.
			if strings.Contains(bare, ".") {
				continue
			}
			checked++
			if _, ok := structs[bare]; !ok {
				t.Errorf("%s %s is %q, which no pinned wire struct describes. "+
					"Either it is not a struct (then this census cannot describe the verb's "+
					"shape at all), or it carries no json tag and silently marshals by Go "+
					"field name.", verb, label, bare)
			}
		}
	}
	if checked < 100 {
		t.Fatalf("only %d verb types were checked; the parse is not seeing them", checked)
	}
}

// compareGolden diffs got against a golden file, honouring -update.
func compareGolden(t *testing.T, path string, got []byte, why string) {
	t.Helper()
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update): %v", err)
	}
	if bytes.Equal(got, want) {
		return
	}
	t.Errorf("%s\n\nIf the change is intended, rerun with -update and let the diff "+
		"be reviewed.\n\n%s", why, firstDiff(got, want))
}

// firstDiff reports the first differing line with a little context, because
// dumping two 1500-line documents into a failure message helps nobody.
func firstDiff(got, want []byte) string {
	g := strings.Split(string(got), "\n")
	w := strings.Split(string(want), "\n")
	for i := 0; i < len(g) || i < len(w); i++ {
		var gl, wl string
		if i < len(g) {
			gl = g[i]
		}
		if i < len(w) {
			wl = w[i]
		}
		if gl != wl {
			return fmt.Sprintf("first difference at line %d:\n  got:  %q\n  want: %q", i+1, gl, wl)
		}
	}
	return "(documents differ only in length)"
}
