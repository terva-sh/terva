package ctrlproto

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// methodValuesByName maps each `MethodX Method = "x.y"` constant NAME to its
// wire VALUE. The capability tables are keyed by value while the census
// enumerates names, and bridging them by hand is exactly the drift this file
// exists to catch.
func methodValuesByName(t *testing.T) map[string]Method {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "methods.go", nil, 0)
	if err != nil {
		t.Fatalf("parse methods.go: %v", err)
	}
	out := map[string]Method{}
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
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "Method" {
				continue
			}
			for i, n := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				out[n.Name] = Method(v)
			}
		}
	}
	// A parse that quietly finds nothing would make the census vacuous — the
	// same guard methodConstants carries, for the same reason.
	if len(out) < 10 {
		t.Fatalf("found only %d Method values; the parse is not seeing them", len(out))
	}
	return out
}

// Every Method must be classified in exactly one of the three capability
// tables. The default in Capabilities() is deliberately the most restrictive
// answer, so an omission FAILS SAFE — but it also fails silently, and a verb
// nobody classified is indistinguishable from one deliberately marked
// write+spend. This makes the omission loud.
//
// It enrolls new constants automatically: methodConstants parses methods.go, so
// adding a Method and forgetting this file turns the suite red rather than
// quietly shipping a verb no restricted role can reach and nobody meant to
// restrict.
func TestEveryMethodIsClassified(t *testing.T) {
	consts := methodConstants(t)

	// The tables are keyed by VALUE (Method), the constants are NAMES. Bridge
	// them through the same source parse the group census uses.
	byName := methodValuesByName(t)

	var unclassified, doubled []string
	for name := range consts {
		m, ok := byName[name]
		if !ok {
			t.Errorf("%s has no value in the parse; the bridge is broken", name)
			continue
		}
		n := 0
		if readOnlyMethods[m] {
			n++
		}
		if writeOnlyMethods[m] {
			n++
		}
		if spendingMethods[m] {
			n++
		}
		switch {
		case n == 0:
			unclassified = append(unclassified, name)
		case n > 1:
			doubled = append(doubled, name)
		}
	}
	sort.Strings(unclassified)
	sort.Strings(doubled)
	if len(unclassified) > 0 {
		t.Errorf("%d method(s) are in no capability table — classify each in capability.go as read-only, write-only, or spending:\n  %s",
			len(unclassified), strings.Join(unclassified, "\n  "))
	}
	if len(doubled) > 0 {
		t.Errorf("%d method(s) appear in more than one capability table, so the answer depends on lookup order:\n  %s",
			len(doubled), strings.Join(doubled, "\n  "))
	}
}

// 🚨 A read-only verb must never also be a spending one. This is the assertion
// that protects the whole point of the mask: the corpus already contains verbs
// whose names read as harmless and whose handlers reach a provider
// (sessions.generate_title, suggest.reply, sidechat.ask, the doctors), so the
// classification being wrong in THIS direction is the failure that hands a
// read-only caller the operator's subscription.
func TestNoReadOnlyMethodSpends(t *testing.T) {
	for m := range readOnlyMethods {
		if spendingMethods[m] {
			t.Errorf("%q is classified read-only AND spending", m)
		}
		if caps := m.Capabilities(); caps&CapSpend != 0 {
			t.Errorf("%q is read-only but Capabilities() reports spend", m)
		}
		if caps := m.Capabilities(); caps&CapWrite != 0 {
			t.Errorf("%q is read-only but Capabilities() reports write", m)
		}
	}
}

// An unknown verb — one added to the wire but never classified — must come back
// maximally restricted, not permissively.
func TestUnknownMethodIsMaximallyRestricted(t *testing.T) {
	m := Method("nobody.classified.this")
	caps := m.Capabilities()
	if caps&CapWrite == 0 || caps&CapSpend == 0 {
		t.Fatalf("unclassified verb reported %b; want write+spend so it is unreachable by a restricted role", caps)
	}
	if m.Permits(CapRead) {
		t.Error("a read-only caller was permitted an unclassified verb")
	}
}

func TestPermitsIsSubsetNotIntersection(t *testing.T) {
	// prompt spends; a caller with read+write must still be refused.
	if MethodPrompt.Permits(CapRead | CapWrite) {
		t.Error("prompt was permitted to a caller with no spend capability")
	}
	if !MethodPrompt.Permits(capAll) {
		t.Error("prompt was refused to an unrestricted caller")
	}
	// subscribe only reads; every caller holding read may have it.
	if !MethodSubscribe.Permits(CapRead) {
		t.Error("subscribe was refused to a read-only caller")
	}
}
