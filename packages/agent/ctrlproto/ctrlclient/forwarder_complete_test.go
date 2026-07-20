package ctrlclient

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ctrlproto's capability verbs hang off optional controller interfaces
// (CardsController, CastController, …). Two things implement them: the real
// daemon-side Workspace, and this package's Service — the wire forwarder that
// lets a Go client (the TUI, `terva attach`) drive a remote daemon through the
// SAME typed interface.
//
// Service's completeness against that surface used to be enforced one hand-placed
// `var _ ctrlproto.XController = (*Service)(nil)` at a time, which gave three
// silently-different behaviors: an asserted controller (Cards) breaks the build
// when a method is added and unforwarded (good); a forwarded-but-unasserted one
// (ModelParams, before this test) would have added the method silently and
// 404'd at runtime; and a never-forwarded one (Cast/Note/User/…) is simply
// unreachable from a Go client with nothing recording whether that was a
// decision.
//
// This test closes those gaps: every *Controller interface in ctrlproto must be
// in EXACTLY ONE of two buckets — forwarded by Service (with the compile-time
// `var _` assertion, which itself proves the implementation), or on the
// notForwarded allow-list below with a reason. A new controller enrolls itself
// (the list is read from source), so it cannot be silently skipped: adding one
// fails this test until its author decides, on the record, which bucket it is in.

// notForwarded is the deliberate allow-list: controllers a Go client does not
// drive, each with the reason. Everything here is a Stage/play surface that
// lives only in the web client today; a future "TUI Stage" would move the
// relevant ones up to real forwarders (and off this list).
var notForwarded = map[string]string{
	"CastController":         "Stage/play cast is web-only today; a Go client does not direct actors. Most likely of these to graduate to a forwarder — if the regular TUI grows a cast surface on the swarm-spawn machinery.",
	"DoctorController":       "The LLM card doctor is a Stage card-craft control; web-only. Revisit for a TUI Stage.",
	"SuggestController":      "Reply-suggestion drafting is a Stage composer aid; web-only. Revisit for a TUI Stage.",
	"DirectController":       "Directed authorship (post an approved character/narrator line into the transcript) is a Stage composer commit; web-only. Revisit for a TUI Stage.",
	"WorldController":        "World lore (session-scoped shared lorebook, Worlds L1) is edited from the Stage steering drawer; web-only. Revisit for a TUI Stage.",
	"DraftController":        "Discarding an unpromoted draft on navigate-away is a Stage front-end cleanup; web-only. Revisit for a TUI Stage.",
	"NoteController":         "Author's note is a Stage/play immersion control; web-only. Revisit for a TUI Stage.",
	"VariantsController":     "Message-scoped variant cleanup (prune/drop) is a Stage editing control; web-only. Revisit for a TUI Stage.",
	"UserController":         "User-persona bind is a Stage/play immersion control; web-only. Revisit for a TUI Stage.",
	"UserPersonasController": "Saved user personas are a Stage identity library; web-only. Revisit for a TUI Stage.",
	"ContinueController":     "turn.continue is a Stage revision verb; web-only. Revisit for a TUI Stage.",
	"ReplayController":       "Session replay/transport is a web player feature; no Go client drives it. Revisit for a TUI Stage.",
}

// controllerInterfaces returns every `type XController interface { … }` declared
// in the ctrlproto package source (the parent directory), read from source so a
// newly-added controller enrolls itself.
func controllerInterfaces(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("../*.go")
	if err != nil {
		t.Fatalf("glob ctrlproto sources: %v", err)
	}
	out := map[string]bool{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
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
				if !ok {
					continue
				}
				if _, ok := ts.Type.(*ast.InterfaceType); !ok {
					continue
				}
				if strings.HasSuffix(ts.Name.Name, "Controller") {
					out[ts.Name.Name] = true
				}
			}
		}
	}
	// A parser that quietly finds nothing would make this test vacuous.
	if len(out) < 8 {
		t.Fatalf("found only %d controller interfaces; the parse is not seeing them", len(out))
	}
	return out
}

// assertedControllers returns every controller named by a
// `var _ ctrlproto.XController = …` compile-time assertion in service.go. That
// assertion is what proves Service implements the interface — the package would
// not compile otherwise — so its presence is the forwarding guarantee.
func assertedControllers(t *testing.T) map[string]bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "service.go", nil, 0)
	if err != nil {
		t.Fatalf("parse service.go: %v", err)
	}
	out := map[string]bool{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "_" {
				continue
			}
			sel, ok := vs.Type.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "ctrlproto" {
				continue
			}
			if strings.HasSuffix(sel.Sel.Name, "Controller") {
				out[sel.Sel.Name] = true
			}
		}
	}
	return out
}

// Every ctrlproto controller must be in exactly one bucket: forwarded by Service
// (asserted), or explicitly not-forwarded with a reason. A controller in neither
// is the silent gap this test exists to prevent; a controller in both is a
// contradiction (the allow-list claims it is web-only while Service forwards it).
func TestEveryControllerIsForwardedOrExempt(t *testing.T) {
	controllers := controllerInterfaces(t)
	asserted := assertedControllers(t)

	var uncategorized, contradictory []string
	for name := range controllers {
		_, isForwarded := asserted[name]
		_, isExempt := notForwarded[name]
		switch {
		case isForwarded && isExempt:
			contradictory = append(contradictory, name)
		case !isForwarded && !isExempt:
			uncategorized = append(uncategorized, name)
		}
	}
	sort.Strings(uncategorized)
	sort.Strings(contradictory)

	for _, name := range uncategorized {
		t.Errorf("%s is neither forwarded by ctrlclient nor on the notForwarded allow-list.\n"+
			"    Either add a forwarder method plus `var _ ctrlproto.%s = (*Service)(nil)` in service.go,\n"+
			"    or add %q to notForwarded with the reason a Go client does not drive it.", name, name, name)
	}
	for _, name := range contradictory {
		t.Errorf("%s is both forwarded by Service (asserted in service.go) and listed in notForwarded — remove it from notForwarded.", name)
	}
}

// The allow-list may not name a controller that no longer exists (renamed or
// deleted), or one that is actually forwarded — a stale exemption would quietly
// re-open the gap for its real successor.
func TestNotForwardedListIsCurrent(t *testing.T) {
	controllers := controllerInterfaces(t)
	asserted := assertedControllers(t)
	for name := range notForwarded {
		if !controllers[name] {
			t.Errorf("notForwarded lists %q, which is not a controller interface in ctrlproto (renamed or removed?)", name)
		}
		if asserted[name] {
			t.Errorf("notForwarded lists %q, but service.go also asserts it — the exemption is stale.", name)
		}
	}
	// Any Service assertion must name a real controller too (guards a typo'd
	// or orphaned `var _` from silently passing the completeness check).
	for name := range asserted {
		if !controllers[name] {
			t.Errorf("service.go asserts ctrlproto.%s, which is not a controller interface (renamed or removed?)", name)
		}
	}
}
