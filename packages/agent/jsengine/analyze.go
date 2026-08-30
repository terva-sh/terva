package jsengine

import (
	"fmt"
	"sort"

	"github.com/grafana/sobek/ast"
	"github.com/grafana/sobek/parser"
)

// This file answers one question before a script runs: which host bindings
// will it call? That turns an approval prompt from "run this script?" —
// which asks a human to read JavaScript under time pressure — into "this
// script will call write x2, read x5", which is a decision someone can
// actually make.
//
// The analysis is only worth having if it is honest about its own blind
// spots, so the result carries Complete alongside the counts. A script can
// defeat static analysis (eval, computed access to the global object,
// shadowing a binding name, aliasing a binding into a variable), and a
// walker can meet a node type it does not know. Every one of those cases
// clears Complete and records a reason. A caller may treat Complete counts
// as exhaustive; it must treat incomplete ones as a floor, never a ceiling.
//
// The cost of getting this wrong is asymmetric: over-reporting wastes an
// approval, under-reporting runs an unapproved call. Everything here is
// therefore biased toward declaring ignorance.

// BindingRefs reports the binding references found in a script.
type BindingRefs struct {
	// Calls counts direct call sites per binding name: read("a") counts
	// against "read".
	Calls map[string]int
	// Mentions counts references that are not direct calls — a binding
	// aliased or passed as a value. `const f = write` mentions write
	// without calling it, and f(...) later calls it under a name the
	// walker cannot attribute, so any mention also clears Complete.
	Mentions map[string]int
	// Complete reports that Calls is an exact account of the host calls
	// this script can make. When false, Calls is a lower bound and
	// Reasons says what defeated the analysis.
	Complete bool
	// Reasons lists what made the analysis incomplete, deduplicated and
	// sorted so the output is stable for tests and transcripts.
	Reasons []string
}

// Total returns the number of statically visible calls across all
// bindings — the figure an approval prompt leads with.
func (r BindingRefs) Total() int {
	n := 0
	for _, c := range r.Calls {
		n += c
	}
	return n
}

// globalReach are identifiers that reach the global object, from which any
// binding is retrievable under a name no walker can predict
// (globalThis["wr"+"ite"]). They have no legitimate use in a script whose
// entire capability set is its bindings, so any appearance is treated as
// defeating the analysis rather than as something to model.
var globalReach = map[string]bool{
	"globalThis": true,
	"window":     true,
	"self":       true,
	"eval":       true,
	"Function":   true,
}

// AnalyzeBindings reports which of the named bindings src references.
//
// src is parsed exactly as the synchronous profile compiles it — a plain
// script, not a module and not wrapped in an async function — so "does it
// parse here" and "will it run there" cannot disagree. A parse failure is
// returned as an error, matching what Run would report.
func AnalyzeBindings(name, src string, bindings []string) (BindingRefs, error) {
	refs := BindingRefs{Calls: map[string]int{}, Mentions: map[string]int{}}
	prog, err := parser.ParseFile(nil, name, src, 0)
	if err != nil {
		return refs, fmt.Errorf("script does not parse: %w", err)
	}
	w := &refWalker{
		names:  make(map[string]bool, len(bindings)),
		refs:   &refs,
		reason: map[string]bool{},
	}
	for _, b := range bindings {
		w.names[b] = true
		refs.Calls[b] = 0
	}
	w.walkStatements(prog.Body)

	refs.Complete = len(w.reasons) == 0
	sort.Strings(w.reasons)
	refs.Reasons = w.reasons
	return refs, nil
}

type refWalker struct {
	names   map[string]bool
	refs    *BindingRefs
	reasons []string
	// reason deduplicates: one `eval` and ten are the same finding.
	reason map[string]bool
}

func (w *refWalker) note(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if w.reason[msg] {
		return
	}
	w.reason[msg] = true
	w.reasons = append(w.reasons, msg)
}

// unknown is the load-bearing default arm of every switch below. sobek's
// AST is explicitly a work in progress ("may change in the future" in its
// own package doc), so a node type this walker has never seen is a matter
// of when, not if. Skipping it silently would drop whatever it contains —
// including calls — and still report a confident count, which is the one
// failure this analysis must not have.
func (w *refWalker) unknown(kind string, n any) {
	w.note("unrecognized %s node %T (analysis cannot see inside it)", kind, n)
}

func (w *refWalker) declare(name string) {
	if w.names[name] {
		w.note("%q is redeclared in the script, so a reference to it may not reach the host binding", name)
	}
}

// callTarget accounts for the one binding whose authority hides in its
// first argument: call(name, args) (§12.7). The tool name must be a string
// literal so the account can name what will be called; anything else — a
// computed name, a variable, an expression — is opaque for the same reason a
// bash command string is (§12.4), and defeats the analysis.
func (w *refWalker) callTarget(args []ast.Expression) {
	if len(args) == 0 {
		w.note("call() without a tool name cannot be accounted")
		return
	}
	lit, ok := args[0].(*ast.StringLiteral)
	if !ok {
		w.note("call(name, …) with a computed name cannot be accounted; pass the tool name as a string literal")
		return
	}
	name := string(lit.Value)
	if !w.names[name] {
		// A name outside the accounted set is like any other unknown
		// identifier — the runtime will refuse it, and it is not this tool's
		// reach to account for.
		return
	}
	w.refs.Calls[name]++
}

func (w *refWalker) reference(id *ast.Identifier, called bool) {
	name := string(id.Name)
	if globalReach[name] {
		w.note("%q reaches the global object or evaluates code, which can call any binding under any name", name)
		return
	}
	if !w.names[name] {
		return
	}
	if called {
		w.refs.Calls[name]++
		return
	}
	w.refs.Mentions[name]++
	w.note("%q is referenced without being called directly (aliased or passed as a value), so call counts are a lower bound", name)
}

// calleeIdentifier unwraps the optional-chaining wrappers so read?.() is
// attributed to read rather than dismissed as a non-identifier callee.
func calleeIdentifier(e ast.Expression) *ast.Identifier {
	for {
		switch n := e.(type) {
		case *ast.Identifier:
			return n
		case *ast.Optional:
			e = n.Expression
		case *ast.OptionalChain:
			e = n.Expression
		default:
			return nil
		}
	}
}

func (w *refWalker) walkStatements(list []ast.Statement) {
	for _, s := range list {
		w.walkStatement(s)
	}
}

func (w *refWalker) walkStatement(s ast.Statement) {
	if s == nil {
		return
	}
	switch n := s.(type) {
	case *ast.BadStatement, *ast.DebuggerStatement, *ast.EmptyStatement, *ast.BranchStatement:
		// No expressions reachable. BranchStatement carries only a label.
	case *ast.BlockStatement:
		w.walkStatements(n.List)
	case *ast.CaseStatement:
		w.walkExpression(n.Test)
		w.walkStatements(n.Consequent)
	case *ast.CatchStatement:
		w.walkBindingTarget(n.Parameter)
		w.walkStatement(n.Body)
	case *ast.DoWhileStatement:
		w.walkExpression(n.Test)
		w.walkStatement(n.Body)
	case *ast.ExpressionStatement:
		w.walkExpression(n.Expression)
	case *ast.ForInStatement:
		w.walkForInto(n.Into)
		w.walkExpression(n.Source)
		w.walkStatement(n.Body)
	case *ast.ForOfStatement:
		w.walkForInto(n.Into)
		w.walkExpression(n.Source)
		w.walkStatement(n.Body)
	case *ast.ForStatement:
		w.walkForLoopInitializer(n.Initializer)
		w.walkExpression(n.Test)
		w.walkExpression(n.Update)
		w.walkStatement(n.Body)
	case *ast.IfStatement:
		w.walkExpression(n.Test)
		w.walkStatement(n.Consequent)
		w.walkStatement(n.Alternate)
	case *ast.LabelledStatement:
		w.walkStatement(n.Statement)
	case *ast.ReturnStatement:
		w.walkExpression(n.Argument)
	case *ast.SwitchStatement:
		w.walkExpression(n.Discriminant)
		for _, c := range n.Body {
			w.walkStatement(c)
		}
	case *ast.ThrowStatement:
		w.walkExpression(n.Argument)
	case *ast.TryStatement:
		w.walkStatement(n.Body)
		if n.Catch != nil {
			w.walkStatement(n.Catch)
		}
		if n.Finally != nil {
			w.walkStatement(n.Finally)
		}
	case *ast.VariableStatement:
		w.walkBindings(n.List)
	case *ast.LexicalDeclaration:
		w.walkBindings(n.List)
	case *ast.WhileStatement:
		w.walkExpression(n.Test)
		w.walkStatement(n.Body)
	case *ast.WithStatement:
		// `with` rewrites identifier resolution at runtime against an
		// object the walker cannot evaluate, so every name in its body
		// becomes unattributable.
		w.note("the script uses `with`, which rebinds names at runtime and defeats static attribution")
		w.walkExpression(n.Object)
		w.walkStatement(n.Body)
	case *ast.FunctionDeclaration:
		w.walkFunctionLiteral(n.Function)
	case *ast.ClassDeclaration:
		w.walkClassLiteral(n.Class)
	case *ast.ImportDeclaration:
		// Module-only; unreachable in script mode, but a module surface
		// would import names this walker cannot follow.
		w.note("the script uses `import`, whose bindings come from outside the analysed source")
	case *ast.ExportDeclaration:
		if n.Variable != nil {
			w.walkStatement(n.Variable)
		}
		if n.LexicalDeclaration != nil {
			w.walkStatement(n.LexicalDeclaration)
		}
		if n.ClassDeclaration != nil {
			w.walkStatement(n.ClassDeclaration)
		}
		if n.HoistableDeclaration != nil && n.HoistableDeclaration.FunctionDeclaration != nil {
			w.walkStatement(n.HoistableDeclaration.FunctionDeclaration)
		}
		w.walkExpression(n.AssignExpression)
	default:
		w.unknown("statement", n)
	}
}

func (w *refWalker) walkExpression(e ast.Expression) {
	if e == nil {
		return
	}
	switch n := e.(type) {
	case *ast.BadExpression, *ast.BooleanLiteral, *ast.NullLiteral,
		*ast.NumberLiteral, *ast.RegExpLiteral, *ast.StringLiteral,
		*ast.ThisExpression, *ast.SuperExpression, *ast.MetaProperty,
		*ast.PrivateIdentifier:
		// Leaves with no identifier reference.
	case *ast.Identifier:
		w.reference(n, false)
	case *ast.ArrayLiteral:
		for _, v := range n.Value {
			w.walkExpression(v)
		}
	case *ast.ArrayPattern:
		for _, el := range n.Elements {
			w.walkExpression(el)
		}
		w.walkExpression(n.Rest)
	case *ast.ObjectPattern:
		for _, p := range n.Properties {
			w.walkProperty(p)
		}
		w.walkExpression(n.Rest)
	case *ast.AssignExpression:
		w.walkExpression(n.Left)
		w.walkExpression(n.Right)
	case *ast.AwaitExpression:
		w.walkExpression(n.Argument)
	case *ast.YieldExpression:
		w.walkExpression(n.Argument)
	case *ast.BinaryExpression:
		w.walkExpression(n.Left)
		w.walkExpression(n.Right)
	case *ast.BracketExpression:
		// Computed member access. If the object reaches the global scope
		// the property name may be built at runtime; reference() flags
		// the global identifier itself, so walking Left is enough.
		w.walkExpression(n.Left)
		w.walkExpression(n.Member)
	case *ast.CallExpression:
		if id := calleeIdentifier(n.Callee); id != nil {
			w.reference(id, true)
			if string(id.Name) == "call" {
				w.callTarget(n.ArgumentList)
			}
		} else {
			w.walkExpression(n.Callee)
		}
		for _, a := range n.ArgumentList {
			w.walkExpression(a)
		}
	case *ast.NewExpression:
		w.walkExpression(n.Callee)
		for _, a := range n.ArgumentList {
			w.walkExpression(a)
		}
	case *ast.ConditionalExpression:
		w.walkExpression(n.Test)
		w.walkExpression(n.Consequent)
		w.walkExpression(n.Alternate)
	case *ast.DotExpression:
		// n.Identifier is a property name, not a variable reference, so
		// it must not be counted: obj.read is not the read binding.
		w.walkExpression(n.Left)
	case *ast.PrivateDotExpression:
		w.walkExpression(n.Left)
	case *ast.SequenceExpression:
		for _, x := range n.Sequence {
			w.walkExpression(x)
		}
	case *ast.UnaryExpression:
		w.walkExpression(n.Operand)
	case *ast.ObjectLiteral:
		for _, p := range n.Value {
			w.walkProperty(p)
		}
	case *ast.TemplateLiteral:
		w.walkExpression(n.Tag)
		for _, x := range n.Expressions {
			w.walkExpression(x)
		}
	case *ast.FunctionLiteral:
		w.walkFunctionLiteral(n)
	case *ast.ClassLiteral:
		w.walkClassLiteral(n)
	case *ast.ArrowFunctionLiteral:
		w.walkParameterList(n.ParameterList)
		w.walkConciseBody(n.Body)
	case *ast.Binding:
		w.walkBinding(n)
	case *ast.Optional:
		w.walkExpression(n.Expression)
	case *ast.OptionalChain:
		w.walkExpression(n.Expression)
	case *ast.SpreadElement:
		w.walkExpression(n.Expression)
	case *ast.PropertyShort, *ast.PropertyKeyed:
		w.walkProperty(n.(ast.Property))
	case *ast.DynamicImportExpression:
		w.note("the script uses dynamic `import()`, which loads code the analysis cannot see")
	default:
		w.unknown("expression", n)
	}
}

func (w *refWalker) walkProperty(p ast.Property) {
	if p == nil {
		return
	}
	switch n := p.(type) {
	case *ast.PropertyShort:
		// `{read}` is shorthand for `{read: read}`, so the name IS a
		// variable reference here, unlike a keyed property's key.
		w.reference(&n.Name, false)
		w.walkExpression(n.Initializer)
	case *ast.PropertyKeyed:
		// A non-computed key is a literal name, never a reference.
		if n.Computed {
			w.walkExpression(n.Key)
		}
		w.walkExpression(n.Value)
	case *ast.SpreadElement:
		w.walkExpression(n.Expression)
	default:
		w.unknown("property", n)
	}
}

func (w *refWalker) walkBindings(list []*ast.Binding) {
	for _, b := range list {
		w.walkBinding(b)
	}
}

func (w *refWalker) walkBinding(b *ast.Binding) {
	if b == nil {
		return
	}
	w.walkBindingTarget(b.Target)
	w.walkExpression(b.Initializer)
}

func (w *refWalker) walkBindingTarget(t ast.BindingTarget) {
	if t == nil {
		return
	}
	switch n := t.(type) {
	case *ast.Identifier:
		// A declaration, not a reference: `const read = ...` shadows the
		// binding rather than calling it.
		w.declare(string(n.Name))
	case *ast.ObjectPattern:
		w.walkObjectPattern(n)
	case *ast.ArrayPattern:
		w.walkArrayPattern(n)
	case *ast.BadExpression:
		// A parse-recovery node with nothing to attribute.
	default:
		w.unknown("binding target", n)
	}
}

// A pattern in DECLARATION position binds names; the identical syntax in
// expression position references them. `const {read} = o` shadows the
// binding, while `({read} = o)` assigns to it. Walking a declaration
// pattern through the expression path would report a shorthand name as
// an alias, so patterns get their own mode below.
func (w *refWalker) walkObjectPattern(n *ast.ObjectPattern) {
	for _, p := range n.Properties {
		switch pp := p.(type) {
		case *ast.PropertyShort:
			// `const {read} = o` declares read. Its Initializer is the
			// default value, which IS an ordinary expression.
			w.declare(string(pp.Name.Name))
			w.walkExpression(pp.Initializer)
		case *ast.PropertyKeyed:
			if pp.Computed {
				w.walkExpression(pp.Key)
			}
			w.walkPatternValue(pp.Value)
		case *ast.SpreadElement:
			w.walkPatternValue(pp.Expression)
		default:
			w.unknown("pattern property", pp)
		}
	}
	w.walkPatternValue(n.Rest)
}

func (w *refWalker) walkArrayPattern(n *ast.ArrayPattern) {
	for _, el := range n.Elements {
		w.walkPatternValue(el)
	}
	w.walkPatternValue(n.Rest)
}

// walkPatternValue handles one slot of a destructuring pattern, which may
// be a nested pattern, a plain name, or a name with a default.
func (w *refWalker) walkPatternValue(e ast.Expression) {
	if e == nil {
		return
	}
	switch n := e.(type) {
	case *ast.Binding:
		w.walkBindingTarget(n.Target)
		w.walkExpression(n.Initializer)
	case *ast.Identifier:
		w.declare(string(n.Name))
	case *ast.ObjectPattern:
		w.walkObjectPattern(n)
	case *ast.ArrayPattern:
		w.walkArrayPattern(n)
	default:
		// An assignment-destructuring target such as ({a: o.x} = v) is a
		// reference rather than a declaration. walkExpression has its own
		// fail-closed default, so this stays honest.
		w.walkExpression(n)
	}
}

func (w *refWalker) walkParameterList(p *ast.ParameterList) {
	if p == nil {
		return
	}
	w.walkBindings(p.List)
	w.walkExpression(p.Rest)
}

func (w *refWalker) walkConciseBody(b ast.ConciseBody) {
	if b == nil {
		return
	}
	switch n := b.(type) {
	case *ast.BlockStatement:
		w.walkStatements(n.List)
	case *ast.ExpressionBody:
		w.walkExpression(n.Expression)
	default:
		w.unknown("arrow body", n)
	}
}

func (w *refWalker) walkFunctionLiteral(f *ast.FunctionLiteral) {
	if f == nil {
		return
	}
	if f.Name != nil {
		w.declare(string(f.Name.Name))
	}
	w.walkParameterList(f.ParameterList)
	if f.Body != nil {
		w.walkStatements(f.Body.List)
	}
}

func (w *refWalker) walkClassLiteral(c *ast.ClassLiteral) {
	if c == nil {
		return
	}
	if c.Name != nil {
		w.declare(string(c.Name.Name))
	}
	w.walkExpression(c.SuperClass)
	for _, el := range c.Body {
		w.walkClassElement(el)
	}
}

func (w *refWalker) walkClassElement(el ast.ClassElement) {
	if el == nil {
		return
	}
	switch n := el.(type) {
	case *ast.FieldDefinition:
		if n.Computed {
			w.walkExpression(n.Key)
		}
		w.walkExpression(n.Initializer)
	case *ast.MethodDefinition:
		if n.Computed {
			w.walkExpression(n.Key)
		}
		w.walkFunctionLiteral(n.Body)
	case *ast.ClassStaticBlock:
		if n.Block != nil {
			w.walkStatements(n.Block.List)
		}
	default:
		w.unknown("class element", n)
	}
}

func (w *refWalker) walkForInto(f ast.ForInto) {
	if f == nil {
		return
	}
	switch n := f.(type) {
	case *ast.ForIntoVar:
		w.walkBinding(n.Binding)
	case *ast.ForDeclaration:
		w.walkBindingTarget(n.Target)
	case *ast.ForIntoExpression:
		w.walkExpression(n.Expression)
	default:
		w.unknown("for-into", n)
	}
}

func (w *refWalker) walkForLoopInitializer(f ast.ForLoopInitializer) {
	if f == nil {
		return
	}
	switch n := f.(type) {
	case *ast.ForLoopInitializerExpression:
		w.walkExpression(n.Expression)
	case *ast.ForLoopInitializerVarDeclList:
		w.walkBindings(n.List)
	case *ast.ForLoopInitializerLexicalDecl:
		w.walkBindings(n.LexicalDeclaration.List)
	default:
		w.unknown("for-loop initializer", n)
	}
}
