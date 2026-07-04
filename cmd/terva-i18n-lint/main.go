// Command terva-i18n-lint extracts terva's translation reference catalog
// from every i18n.T / i18n.TC / i18n.TN call in the source tree and writes
// it to packages/i18n/locales/en.json — the English-as-key inventory that
// `terva locale` uses as its coverage denominator and init template.
//
// It parses syntactically (not type-checked), so build-tag-gated files
// (terva_acp, connector variants) contribute their strings even though a
// given build excludes them — the catalog is the union across all builds.
//
// Usage:
//
//	terva-i18n-lint                 # rewrite packages/i18n/locales/en.json
//	terva-i18n-lint -check          # fail if en.json is stale (CI gate)
//	terva-i18n-lint -unwrapped DIR  # advisory: prose literals not yet wrapped
//
// It is a migration tool, not part of the shipped binary.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"terva.sh/terva/packages/i18n"
)

// unresolvedKeyed counts i18n.P/H calls whose English default the extractor
// couldn't resolve to a literal/const (a concatenation); a non-zero count is
// a hard error so a silently-dropped block can't ship untranslatable.
var unresolvedKeyed int

func main() {
	root := flag.String("root", "packages,cmd", "comma-separated directory trees to scan for i18n calls")
	out := flag.String("out", filepath.Join("packages", "i18n", "locales", "en.json"), "UI reference catalog to write")
	check := flag.Bool("check", false, "verify the references are current; non-zero exit if stale")
	unwrapped := flag.String("unwrapped", "", "advisory: list likely user-facing literals not wrapped in i18n.T under this dir")
	flag.Parse()

	if *unwrapped != "" {
		if err := reportUnwrapped(*unwrapped); err != nil {
			fmt.Fprintln(os.Stderr, "terva-i18n-lint:", err)
			os.Exit(2)
		}
		return
	}

	roots := splitRoots(*root)
	consts, err := stringConsts(roots)
	if err != nil {
		fmt.Fprintln(os.Stderr, "terva-i18n-lint:", err)
		os.Exit(2)
	}
	entries, keyed, err := extract(roots, consts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "terva-i18n-lint:", err)
		os.Exit(2)
	}
	if unresolvedKeyed > 0 {
		fmt.Fprintf(os.Stderr, "terva-i18n-lint: %d keyed P/H entr(y/ies) could not be extracted (see above); fix and re-run\n", unresolvedKeyed)
		os.Exit(2)
	}

	// One reference file per catalog: the UI en.json, then each keyed
	// catalog (prompts, help) under locales/<cat>/en.json.
	type refFile struct {
		path  string
		label string
		count int
		data  []byte
	}
	refs := []refFile{{*out, "strings", len(entries), mustMarshal(entries)}}
	for _, cat := range i18n.KeyedCatalogs() {
		m := keyed[cat]
		refs = append(refs, refFile{
			path:  filepath.Join("packages", "i18n", "locales", cat, "en.json"),
			label: cat, count: len(m), data: mustMarshal(m),
		})
	}

	if *check {
		stale := false
		for _, r := range refs {
			if existing, _ := os.ReadFile(r.path); !bytes.Equal(bytes.TrimSpace(existing), bytes.TrimSpace(r.data)) {
				fmt.Fprintf(os.Stderr, "%s is stale — run `go run ./cmd/terva-i18n-lint` and commit the result\n", r.path)
				stale = true
			}
		}
		if stale {
			os.Exit(1)
		}
		for _, r := range refs {
			fmt.Printf("%s is up to date (%d %s)\n", r.path, r.count, r.label)
		}
		return
	}

	for _, r := range refs {
		if err := os.WriteFile(r.path, r.data, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "terva-i18n-lint:", err)
			os.Exit(2)
		}
		fmt.Printf("wrote %s (%d %s)\n", r.path, r.count, r.label)
	}
}

func mustMarshal(m map[string]json.RawMessage) []byte {
	data, err := i18n.MarshalLocale(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "terva-i18n-lint:", err)
		os.Exit(2)
	}
	return data
}

// extract walks the roots and returns the UI reference plus the keyed
// catalogs. entries (UI): a singular source maps to itself; a TC
// context+source maps to the source; a TN pair maps to an object of its
// English one/other forms. keyed[catalog] (i18n.P → "prompts", i18n.H →
// "help"): each dotted key maps to its English default template.
func extract(roots []string, consts map[string]string) (entries map[string]json.RawMessage, keyed map[string]map[string]json.RawMessage, err error) {
	fset := token.NewFileSet()
	entries = map[string]json.RawMessage{}
	keyed = map[string]map[string]json.RawMessage{
		"prompts": {},
		"help":    {},
	}
	walk := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == "vendor" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return fmt.Errorf("%s: %w", path, perr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn := i18nFunc(call)
			switch fn {
			case "T", "M", "Errorf":
				// T translates at the call site; M marks a literal declared
				// elsewhere (a data table) for translation at its render site;
				// Errorf translates an error message. All contribute the same
				// singular source key (their first string-literal argument).
				if s, ok := litArg(call, 0); ok {
					putString(entries, s, s)
				}
			case "TC":
				if ctx, ok := litArg(call, 0); ok {
					if src, ok := litArg(call, 1); ok {
						putString(entries, i18n.ContextKey(ctx, src), src)
					}
				}
			case "TN":
				if one, ok := litArg(call, 1); ok {
					if other, ok := litArg(call, 2); ok {
						putPlural(entries, one, other)
					}
				}
			case "P", "H":
				// P → the model-facing prompt catalog, H → the operator-
				// facing help catalog. Both are dotted-key: arg0 the key,
				// arg1 the English default (a string literal or a named
				// const — large blocks are kept as documented consts).
				cat := "prompts"
				if fn == "H" {
					cat = "help"
				}
				if key, ok := litArg(call, 0); ok {
					if english, ok := stringOrConstArg(call, 1, consts); ok {
						putString(keyed[cat], key, english)
					} else {
						// A concatenation (`…`+f()+`…`) or non-const arg can't
						// be extracted, so the entry would silently vanish from
						// the reference and become untranslatable. Fail loudly:
						// rewrite it as one %s template.
						unresolvedKeyed++
						pos := fset.Position(call.Pos())
						fmt.Fprintf(os.Stderr, "terva-i18n-lint: %s:%d: i18n.%s(%q, …): english default is not a literal or resolvable const (string concatenation?) — use one %%s template so the block stays extractable\n", pos.Filename, pos.Line, fn, key)
					}
				}
			}
			return true
		})
		return nil
	}
	for _, root := range roots {
		if err := filepath.WalkDir(root, walk); err != nil {
			return entries, keyed, err
		}
	}
	return entries, keyed, nil
}

// stringConsts collects package-level `const/var NAME = "literal"` string
// declarations across the tree so an i18n.P call whose english default is a
// named const (large prompts are kept as documented consts, not inlined)
// still contributes that const's value to the prompt reference. Only a
// single string BasicLit resolves — concatenations and fmt-built values are
// skipped. On a name collision the last decl scanned wins; prompt-default
// consts are uniquely named, so this is not a concern in practice.
func stringConsts(roots []string) (map[string]string, error) {
	fset := token.NewFileSet()
	out := map[string]string{}
	walk := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == "vendor" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return fmt.Errorf("%s: %w", path, perr)
		}
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					if s, ok := staticString(vs.Values[i]); ok {
						out[name.Name] = s
					}
				}
			}
		}
		return nil
	}
	for _, root := range roots {
		if err := filepath.WalkDir(root, walk); err != nil {
			return out, err
		}
	}
	return out, nil
}

// splitRoots parses the comma-separated -root flag into a list of trees,
// trimming blanks and dropping empties.
func splitRoots(s string) []string {
	var out []string
	for r := range strings.SplitSeq(s, ",") {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	return out
}

// staticString evaluates a compile-time-constant string expression: a
// string literal, a parenthesised one, or a '+' concatenation of such
// (Go folds `"a" + "b"` in a const, and prompt defaults are sometimes
// written that way to wrap long lines). Anything non-constant fails.
func staticString(expr ast.Expr) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		return s, err == nil
	case *ast.ParenExpr:
		return staticString(e.X)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		l, ok := staticString(e.X)
		if !ok {
			return "", false
		}
		r, ok := staticString(e.Y)
		if !ok {
			return "", false
		}
		return l + r, true
	}
	return "", false
}

// stringOrConstArg returns the i-th argument's string value when it is a
// string literal or a resolvable named string const, else ok=false.
func stringOrConstArg(call *ast.CallExpr, i int, consts map[string]string) (string, bool) {
	if s, ok := litArg(call, i); ok {
		return s, true
	}
	if i < len(call.Args) {
		if id, ok := call.Args[i].(*ast.Ident); ok {
			if s, ok := consts[id.Name]; ok {
				return s, true
			}
		}
	}
	return "", false
}

// i18nFunc returns "T"/"TC"/"TN" when call is a selector call on the i18n
// package, else "".
func i18nFunc(call *ast.CallExpr) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "i18n" {
		return ""
	}
	switch sel.Sel.Name {
	case "T", "TC", "TN", "M", "Errorf", "P", "H":
		return sel.Sel.Name
	}
	return ""
}

// litArg returns the string value of the i-th argument when it is a string
// literal, else ok=false (a variable or concatenation can't be extracted).
func litArg(call *ast.CallExpr, i int) (string, bool) {
	if i >= len(call.Args) {
		return "", false
	}
	lit, ok := call.Args[i].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

func putString(m map[string]json.RawMessage, key, val string) {
	m[key] = i18n.MarshalValue(val)
}

func putPlural(m map[string]json.RawMessage, one, other string) {
	m[i18n.PluralKey(one, other)] = i18n.MarshalValue(map[string]string{"one": one, "other": other})
}

// reportUnwrapped lists prose-looking string literals in dir that are not
// arguments to an i18n.T/TC/TN call — an advisory migration checklist, not a
// gate (it is deliberately conservative but still noisy).
func reportUnwrapped(dir string) error {
	fset := token.NewFileSet()
	count := 0
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == "vendor" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		wrapped := map[token.Pos]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || i18nFunc(call) == "" {
				return true
			}
			for _, a := range call.Args {
				if lit, ok := a.(*ast.BasicLit); ok {
					wrapped[lit.Pos()] = true
				}
			}
			return true
		})
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || wrapped[lit.Pos()] {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil || !looksLikeProse(s) {
				return true
			}
			pos := fset.Position(lit.Pos())
			fmt.Printf("%s:%d: %s\n", pos.Filename, pos.Line, oneLine(s))
			count++
			return true
		})
		return nil
	})
	fmt.Fprintf(os.Stderr, "%d unwrapped prose literal(s) under %s\n", count, dir)
	return err
}

// looksLikeProse is the heuristic for "a sentence a user might read": has a
// space and at least a few letters, and isn't an import path or format-only
// token. It over-reports; a human triages the list.
func looksLikeProse(s string) bool {
	if !strings.Contains(s, " ") {
		return false
	}
	letters := 0
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			letters++
		}
	}
	if letters < 4 {
		return false
	}
	if strings.Contains(s, "/") && !strings.Contains(s, ". ") {
		return false // likely a path or import
	}
	return true
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 80 {
		return s[:77] + "..."
	}
	return s
}
