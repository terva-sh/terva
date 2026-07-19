package workflow

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/grafana/sobek"
	"github.com/grafana/sobek/ast"
	"github.com/grafana/sobek/parser"
)

// Meta is the script's self-description — the same grammar and shape as a
// Claude Code workflow's `export const meta` block (docs/proposals/
// workflow-structured-swarm.md: compat at the script level, conversion
// not emulation).
type Meta struct {
	Name        string
	Description string
	WhenToUse   string
	Phases      []Phase
}

// Phase is one meta.phases entry.
type Phase struct {
	Title  string
	Detail string
	Model  string
}

// exportRe rewrites the module-only `export const meta` into plain
// script grammar. Scripts are executed as scripts (wrapped in an async
// function for top-level await/return), not ES modules, so the export
// keyword must go before any parse.
var exportRe = regexp.MustCompile(`(?m)^(\s*)export\s+(const\s+meta\b)`)

// deExport strips the export keyword from the meta declaration.
func deExport(src string) string {
	return exportRe.ReplaceAllString(src, "$1$2")
}

// extractMeta parses the (de-exported) source, finds the top-level
// `const meta = {...}` declaration, and evaluates ONLY its initializer
// in a bare VM — the body never runs during extraction. The meta block
// must be a pure literal (the same rule Claude Code enforces), which is
// exactly what makes evaluating it in isolation safe.
//
// The source is parsed in the SAME async-function wrapping the runner
// executes it under — script bodies use top-level await and top-level
// return, which are illegal outside that wrapper — so "does it parse
// here" and "will it run there" can never disagree.
func extractMeta(name, src string) (Meta, error) {
	var m Meta
	wrapped := "(async () => {\n" + src + "\n})()"
	prog, err := parser.ParseFile(nil, name, wrapped, 0)
	if err != nil {
		return m, fmt.Errorf("script does not parse: %w", err)
	}
	initSrc := ""
	for _, st := range unwrapAsyncBody(prog) {
		var bindings []*ast.Binding
		switch d := st.(type) {
		case *ast.VariableStatement:
			bindings = d.List
		case *ast.LexicalDeclaration:
			bindings = d.List
		default:
			continue
		}
		for _, b := range bindings {
			id, ok := b.Target.(*ast.Identifier)
			if !ok || id.Name.String() != "meta" || b.Initializer == nil {
				continue
			}
			initSrc = wrapped[b.Initializer.Idx0()-1 : b.Initializer.Idx1()-1]
		}
	}
	if initSrc == "" {
		return m, fmt.Errorf("script must begin with `export const meta = {name, description, ...}`")
	}
	vm := sobek.New()
	v, err := vm.RunString("(" + initSrc + ")")
	if err != nil {
		return m, fmt.Errorf("meta must be a pure object literal (no variables, calls, or interpolation): %w", err)
	}
	obj, ok := v.Export().(map[string]any)
	if !ok {
		return m, fmt.Errorf("meta must be an object literal")
	}
	m.Name, _ = obj["name"].(string)
	m.Description, _ = obj["description"].(string)
	m.WhenToUse, _ = obj["whenToUse"].(string)
	if phases, ok := obj["phases"].([]any); ok {
		for _, p := range phases {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			ph := Phase{}
			ph.Title, _ = pm["title"].(string)
			ph.Detail, _ = pm["detail"].(string)
			ph.Model, _ = pm["model"].(string)
			m.Phases = append(m.Phases, ph)
		}
	}
	if strings.TrimSpace(m.Name) == "" {
		return m, fmt.Errorf("meta.name is required")
	}
	return m, nil
}

// unwrapAsyncBody digs the script's statement list out of the
// `(async () => { ... })()` wrapper's AST. Falls back to the program
// body itself if the shape ever differs (a script with neither
// top-level await nor return parses fine unwrapped too).
func unwrapAsyncBody(prog *ast.Program) []ast.Statement {
	if len(prog.Body) == 1 {
		if es, ok := prog.Body[0].(*ast.ExpressionStatement); ok {
			if call, ok := es.Expression.(*ast.CallExpression); ok {
				if arrow, ok := call.Callee.(*ast.ArrowFunctionLiteral); ok {
					if block, ok := arrow.Body.(*ast.BlockStatement); ok {
						return block.List
					}
				}
			}
		}
	}
	return prog.Body
}
