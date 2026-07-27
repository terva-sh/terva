package workspace

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// The surface registry lives in three hand-maintained places that nothing
// tied together: surfaceList() decides what a client's tab strip OFFERS,
// surface(id) decides what a fetch SERVES, and surfaceAction(id, …) decides
// what a click REACHES. They drift independently — "taskboard" was served for
// several releases while absent from the list, which read as "the feature
// doesn't exist" from every client (fixed alongside the task-board rebuild
// bug). These tests parse workspace_surfaces.go itself, in the style of
// ctrlclient's forwarder_complete_test.go, so a new surface enrolls in all
// three relations by existing.
//
// The extension-panel surfaces are dynamic (ids minted at runtime from
// extPanels) and are deliberately outside this: the relations here are for
// the STATIC ids the switch and the if-chain name.

type surfaceRegistry struct {
	listed     map[string]bool // SurfaceMeta ID literals in surfaceList
	actionable map[string]bool // …of those, metas whose Actions field is the literal true
	served     map[string]bool // case ids in surface(id)'s switch
	routed     map[string]bool // id == "…" comparisons in surfaceAction
}

func parseSurfaceRegistry(t *testing.T) surfaceRegistry {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "workspace_surfaces.go", nil, 0)
	if err != nil {
		t.Fatalf("parse workspace_surfaces.go: %v", err)
	}
	reg := surfaceRegistry{
		listed: map[string]bool{}, actionable: map[string]bool{},
		served: map[string]bool{}, routed: map[string]bool{},
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		switch fd.Name.Name {
		case "surfaceList":
			record := func(cl *ast.CompositeLit) {
				var id string
				var actionsTrue bool
				for _, elt := range cl.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := kv.Key.(*ast.Ident)
					if !ok {
						continue
					}
					switch key.Name {
					case "ID":
						if lit, ok := kv.Value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
							id, _ = strconv.Unquote(lit.Value)
						}
					case "Actions":
						if v, ok := kv.Value.(*ast.Ident); ok && v.Name == "true" {
							actionsTrue = true
						}
					}
				}
				if id != "" {
					reg.listed[id] = true
					if actionsTrue {
						reg.actionable[id] = true
					}
				}
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				cl, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				if isSurfaceMeta(cl.Type) {
					record(cl)
					return true
				}
				// The seed slice: []ctrlproto.SurfaceMeta{{…}, {…}} — its element
				// literals carry no type of their own.
				if at, ok := cl.Type.(*ast.ArrayType); ok && isSurfaceMeta(at.Elt) {
					for _, elt := range cl.Elts {
						if inner, ok := elt.(*ast.CompositeLit); ok {
							record(inner)
						}
					}
				}
				return true
			})
		case "surface":
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				cc, ok := n.(*ast.CaseClause)
				if !ok {
					return true
				}
				for _, e := range cc.List {
					if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						id, _ := strconv.Unquote(lit.Value)
						reg.served[id] = true
					}
				}
				return true
			})
		case "surfaceAction":
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				be, ok := n.(*ast.BinaryExpr)
				if !ok || be.Op != token.EQL {
					return true
				}
				x, xok := be.X.(*ast.Ident)
				lit, lok := be.Y.(*ast.BasicLit)
				if xok && lok && x.Name == "id" && lit.Kind == token.STRING {
					id, _ := strconv.Unquote(lit.Value)
					reg.routed[id] = true
				}
				return true
			})
		}
	}
	// A walk that quietly matched nothing would make every relation below pass.
	if len(reg.listed) < 10 || len(reg.served) < 10 || len(reg.routed) < 5 {
		t.Fatalf("parse saw %d listed / %d served / %d routed surface ids; the extraction is not seeing them",
			len(reg.listed), len(reg.served), len(reg.routed))
	}
	return reg
}

func isSurfaceMeta(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "SurfaceMeta"
}

func missingFrom(set map[string]bool, from map[string]bool) []string {
	var out []string
	for id := range from {
		if !set[id] {
			out = append(out, id)
		}
	}
	return out
}

func TestEveryServedSurfaceIsListed(t *testing.T) {
	reg := parseSurfaceRegistry(t)
	if gone := missingFrom(reg.listed, reg.served); len(gone) > 0 {
		t.Errorf("served by surface(id) but never offered by surfaceList — a client can fetch these but no tab will ever show them (the taskboard bug class): %s",
			strings.Join(gone, ", "))
	}
}

func TestEveryListedSurfaceIsServed(t *testing.T) {
	reg := parseSurfaceRegistry(t)
	if gone := missingFrom(reg.served, reg.listed); len(gone) > 0 {
		t.Errorf("offered by surfaceList but not served by surface(id) — a dead tab: %s",
			strings.Join(gone, ", "))
	}
}

func TestEveryActionRouteIsCoherent(t *testing.T) {
	reg := parseSurfaceRegistry(t)
	// A routed id must be a listed surface (else the route is unreachable
	// through any client that trusts the list).
	if gone := missingFrom(reg.listed, reg.routed); len(gone) > 0 {
		t.Errorf("surfaceAction routes ids surfaceList never offers: %s", strings.Join(gone, ", "))
	}
	// A meta advertising Actions: true must have a route, or every click 404s.
	// (providers advertises via an expression — canLogin() — because its
	// actions are served by the auth verb group, not surfaceAction; the
	// literal-true scope keeps that legal without an exemption list.)
	if gone := missingFrom(reg.routed, reg.actionable); len(gone) > 0 {
		t.Errorf("listed with Actions: true but surfaceAction has no route — every click on these answers not-found: %s",
			strings.Join(gone, ", "))
	}
}
