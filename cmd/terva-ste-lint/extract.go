package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
)

// Text is one piece of model-visible tool text, with enough position to click.
//
// Rules is the policy this piece must satisfy. The zero value is rulesFull, so
// a producer that says nothing gets every rule.
type Text struct {
	File  string
	Line  int
	What  string // "Description()" or `schema "description"`
	Body  string
	Rules ruleSet
}

// jsonDescRe pulls description values out of a JSON schema that lives inside a
// Go string literal. The schemas are hand-written JSON, not marshalled structs,
// so this is the only way in for roughly half the corpus.
var jsonDescRe = regexp.MustCompile(`"description"\s*:\s*"((?:[^"\\]|\\.)*)"`)

// extractFile returns every piece of tool text in one Go file.
//
// It parses syntactically rather than type-checking, for the same reason
// terva-i18n-lint does: build-tag-gated files (terva_scripting's
// code_execution.go, the connector variants) must contribute their text even
// though the current build excludes them. A tool whose description is only
// compiled under a tag is still a tool the model reads.
func extractFile(fset *token.FileSet, rel, src string) ([]Text, error) {
	f, err := parser.ParseFile(fset, rel, src, 0)
	if err != nil {
		return nil, err
	}

	// Package-level string constants first: descList, slugDesc, optionsDesc and
	// friends are declared apart from the method that returns them, precisely so
	// two tool shapes cannot describe the same field differently. Resolving them
	// is what lets the lint see through `return descList`.
	//
	// Two passes, because a constant can be built FROM another constant --
	// askSchema splices slugDesc and optionsDesc into the middle of a JSON
	// literal. One pass leaves those as placeholders, which splits
	// `"description":"` from its value across two operands and hides the whole
	// ask tool from the lint. The second pass resolves the references; a third
	// would only matter for a chain three deep, which no schema has.
	type constDecl struct {
		expr ast.Expr
		pos  token.Pos
	}
	decls := map[string]constDecl{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
			continue
		}
		for _, s := range gd.Specs {
			vs, ok := s.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i < len(vs.Values) {
					decls[name.Name] = constDecl{expr: vs.Values[i], pos: vs.Values[i].Pos()}
				}
			}
		}
	}
	consts := map[string]string{}
	for pass := 0; pass < 2; pass++ {
		for name, d := range decls {
			if str, ok := flatten(d.expr, consts); ok {
				consts[name] = str
			}
		}
	}

	var out []Text
	seen := map[string]bool{}
	add := func(pos token.Pos, what, body string) {
		body = strings.TrimSpace(body)
		if body == "" {
			return
		}
		key := what + "\x00" + body
		if seen[key] {
			return
		}
		seen[key] = true
		p := fset.Position(pos)
		out = append(out, Text{File: rel, Line: p.Line, What: what, Body: body})
	}

	// Fully-resolved constants carrying a JSON schema. This is the only path
	// that sees a description whose value arrives from a different constant,
	// so it runs before the syntax walk rather than instead of it.
	for name, d := range decls {
		s, ok := consts[name]
		if !ok || !strings.Contains(s, `"description"`) {
			continue
		}
		for _, m := range jsonDescRe.FindAllStringSubmatch(s, -1) {
			add(d.pos, `schema "description"`, unescapeJSON(m[1]))
		}
	}

	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.FuncDecl:
			// A tool's Description() method: receiver, no params, one string result.
			if v.Recv == nil || v.Name.Name != "Description" || v.Body == nil {
				return true
			}
			if v.Type.Params != nil && len(v.Type.Params.List) > 0 {
				return true
			}
			ast.Inspect(v.Body, func(m ast.Node) bool {
				ret, ok := m.(*ast.ReturnStmt)
				if !ok {
					return true
				}
				for _, r := range ret.Results {
					if s, ok := flatten(r, consts); ok {
						add(r.Pos(), "Description()", s)
					}
				}
				return true
			})
			return true

		case *ast.KeyValueExpr:
			// Go-map schemas: {"description": "..."}.
			k, ok := v.Key.(*ast.BasicLit)
			if !ok || k.Kind != token.STRING {
				return true
			}
			if s, err := strconv.Unquote(k.Value); err != nil || s != "description" {
				return true
			}
			if s, ok := flatten(v.Value, consts); ok {
				add(v.Value.Pos(), `schema "description"`, s)
			}
			return true

		case *ast.CallExpr:
			// Standalone prompt injections: i18n.P("key", english, ...) at any
			// call site, not only inside a Description or a schema. The English
			// default is what ships; an operator's overlay is their prose, not
			// ours. Keys under promptKeyExemptPrefixes are a tier decision and
			// are skipped by name, never by file.
			//
			// i18n.H is deliberately NOT extracted here. Help panels are
			// usage-table genre: metavars (NAME, PATH, DUR) read to the caps
			// rule as shouting and a flag table reads to the length rule as an
			// 88-word sentence. Enrolling them was tried and produced only
			// misfires — noise that would sit in the baseline forever, not a
			// burn-down list.
			sel, ok := v.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "P" || len(v.Args) < 2 {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "i18n" {
				return true
			}
			keyLit, ok := v.Args[0].(*ast.BasicLit)
			if !ok || keyLit.Kind != token.STRING {
				return true
			}
			key, err := strconv.Unquote(keyLit.Value)
			if err != nil || promptKeyExempt(key) {
				return true
			}
			if s, ok := flatten(v.Args[1], consts); ok {
				add(v.Args[1].Pos(), `i18n.`+sel.Sel.Name+`(`+key+`)`, s)
			}
			return true

		case *ast.BasicLit:
			// Hand-written JSON schemas: the description values live inside one
			// string literal, so they have to come out by pattern.
			if v.Kind != token.STRING {
				return true
			}
			s, ok := unquoteAny(v.Value)
			if !ok || !strings.Contains(s, `"description"`) {
				return true
			}
			for _, m := range jsonDescRe.FindAllStringSubmatch(s, -1) {
				add(v.Pos(), `schema "description"`, unescapeJSON(m[1]))
			}
			return true
		}
		return true
	})
	return out, nil
}

// flatten reconstructs a string-concatenation chain. A non-literal operand — a
// call like shellName(), a reference like core.SceneStateName — becomes a
// neutral placeholder, so the sentence keeps its shape without the lint
// asserting anything about a value only known at run time.
func flatten(e ast.Expr, consts map[string]string) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		return unquoteAny(v.Value)
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		l, lok := flatten(v.X, consts)
		r, rok := flatten(v.Y, consts)
		// The placeholder is shaped like a code span on purpose: stripCode
		// removes it before any prose rule runs, so a runtime value never reads
		// as a shouted word or pads a sentence's word count.
		if !lok {
			l = "$VALUE"
		}
		if !rok {
			r = "$VALUE"
		}
		if !lok && !rok {
			return "", false
		}
		return l + r, true
	case *ast.Ident:
		if consts != nil {
			if s, ok := consts[v.Name]; ok {
				return s, true
			}
		}
		return "", false
	case *ast.ParenExpr:
		return flatten(v.X, consts)
	case *ast.CallExpr:
		// i18n.D("tool.x.description", english, args...) — the description
		// moved into the tools catalog, but the English source stays at the
		// call site as the default. That default is what ships and what this
		// lint governs; an operator's overlay is their prose, not ours.
		//
		// Without this the lint goes silently blind the moment a description
		// is wrapped, which is exactly what happened: TestExtractionFindsThe
		// CoreTools failed on read.go the same commit i18n.D landed.
		if english, ok := i18nDefaultArg(v); ok {
			return flatten(english, consts)
		}
		return "", false
	}
	return "", false
}

// i18nDefaultArg returns the english-default argument of an i18n.D/P/H call,
// which is always the second: the first is the dotted key.
func i18nDefaultArg(call *ast.CallExpr) (ast.Expr, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || len(call.Args) < 2 {
		return nil, false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "i18n" {
		return nil, false
	}
	switch sel.Sel.Name {
	case "D", "P", "H":
		return call.Args[1], true
	}
	return nil, false
}

func unquoteAny(lit string) (string, bool) {
	if s, err := strconv.Unquote(lit); err == nil {
		return s, true
	}
	// A raw string that Unquote rejects (it should not, but be forgiving).
	if len(lit) >= 2 && lit[0] == '`' && lit[len(lit)-1] == '`' {
		return lit[1 : len(lit)-1], true
	}
	return "", false
}

// unescapeJSON turns the escapes that survive a Go raw string back into the
// characters the model actually receives.
func unescapeJSON(s string) string {
	r := strings.NewReplacer(`\"`, `"`, `\n`, "\n", `\t`, "\t", `\\`, `\`)
	return r.Replace(s)
}
