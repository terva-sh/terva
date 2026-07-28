package mode

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// The keystone guard.
//
// Every per-mode table in the tree — build's modeSurface, build's modePosture,
// and whatever comes next — proves itself exhaustive by ranging over All().
// That makes All() the single point where the whole scheme can fail silently:
// a constant declared below but missing from the roster is not caught by any of
// those tables, because none of them is ever told to look for it. The tables
// would keep passing while covering nine of ten modes.
//
// So the roster is checked against the declarations themselves rather than
// against a second hand-written list, which would have the same problem one
// level up. Adding a Mode constant and forgetting the roster fails here; a
// roster entry for a constant that no longer exists fails here too.
func TestAllCoversEveryDeclaredConstant(t *testing.T) {
	declared := declaredModeConstants(t)

	inRoster := map[string]bool{}
	for _, m := range All() {
		inRoster[string(m)] = true
	}

	var missing, stale []string
	for name, value := range declared {
		if !inRoster[value] {
			missing = append(missing, name+" ("+value+")")
		}
	}
	byValue := map[string]bool{}
	for _, v := range declared {
		byValue[v] = true
	}
	for _, m := range All() {
		if !byValue[string(m)] {
			stale = append(stale, string(m))
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)

	if len(missing) > 0 {
		t.Errorf("declared but not in All(): %s\n"+
			"Every per-mode table proves itself exhaustive against All(), so a mode missing here "+
			"is a mode every one of those tables silently stops checking.", strings.Join(missing, ", "))
	}
	if len(stale) > 0 {
		t.Errorf("in All() but not declared: %s — drop the roster entry", strings.Join(stale, ", "))
	}
	if got, want := len(All()), len(declared); got != want {
		t.Errorf("All() has %d entries, %d constants are declared", got, want)
	}
}

// declaredModeConstants parses this package's own source for every constant of
// type Mode, returning name -> value. It reads the const block rather than
// using reflection because an unreferenced constant leaves no runtime trace —
// which is exactly the state a forgotten roster entry creates.
func declaredModeConstants(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "mode.go", nil, 0)
	if err != nil {
		t.Fatalf("parse mode.go: %v", err)
	}
	out := map[string]string{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			id, ok := vs.Type.(*ast.Ident)
			if !ok || id.Name != "Mode" {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				out[name.Name] = strings.Trim(lit.Value, `"`)
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("parsed no Mode constants — the guard would pass vacuously")
	}
	return out
}

// All returns a copy, so a caller ranging over the modes to check its own table
// cannot change what the next caller's table is checked against.
func TestAllReturnsACopy(t *testing.T) {
	first := All()
	if len(first) == 0 {
		t.Fatal("All() is empty")
	}
	first[0] = "clobbered"
	if second := All(); second[0] == "clobbered" {
		t.Error("All() hands out the package's own slice — one caller's range can corrupt the roster for every other")
	}
}

// Values are the wire/flag spellings and are load-bearing: --print sets "print",
// and a mode's string is what reaches the audit log and the stderr notes.
func TestValuesAreStable(t *testing.T) {
	for _, c := range []struct {
		m    Mode
		want string
	}{
		{Interactive, "interactive"},
		{Print, "print"},
		{JSON, "json"},
		{RPC, "rpc"},
		{ACP, "acp"},
		{SwarmAgent, "swarm-agent"},
		{Web, "web"},
		{Attach, "attach"},
		{Replay, "replay"},
		{Bot, "bot"},
	} {
		if string(c.m) != c.want {
			t.Errorf("mode value = %q, want %q", string(c.m), c.want)
		}
	}
}
