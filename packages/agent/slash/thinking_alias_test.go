package slash

import "testing"

// The naming rule, and the promise that comes with it.
//
// terva calls this control "thinking" everywhere a user meets it — the status
// bar, /settings, --help — and "reasoning" everywhere in code and config
// (Model.DefaultReasoning, reasoning_summary, the wire). "/reasoning" was the
// one user-facing surface still spelled the internal way, so the canonical name
// moved to /thinking.
//
// What must NOT happen is the rename taking the old spelling with it. Anyone's
// muscle memory, every doc written before today, and any script driving the TUI
// all say /reasoning. Renaming a shipped command is only safe while the old
// name still dispatches.
func TestThinkingKeepsItsOldSpellingsAsAliases(t *testing.T) {
	var spec *Spec
	for i := range registry {
		if registry[i].Name == "/thinking" {
			spec = &registry[i]
			break
		}
	}
	if spec == nil {
		t.Fatal("no /thinking command; the canonical name moved without this guard following")
	}
	for _, want := range []string{"/reasoning", "/think"} {
		found := false
		for _, a := range spec.Aliases {
			if a == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s no longer dispatches to /thinking — a shipped spelling was withdrawn, not aliased", want)
		}
	}
}

// A command with no handler is a command that parses and then does nothing, and
// the join is by NAME (slashHandlers[s.Name] in modes/slash_registry.go) — so
// renaming a spec without renaming its handler key fails silently at runtime
// rather than at build. This is the shape that catches it: every canonical name
// is unique and non-empty, and no alias shadows another command's canonical
// name (which would make the shadowed command unreachable).
func TestSlashNamesAndAliasesDoNotCollide(t *testing.T) {
	if len(registry) == 0 {
		t.Fatal("empty registry — this guard would pass vacuously")
	}
	canonical := map[string]bool{}
	for _, s := range registry {
		if s.Name == "" {
			t.Error("a spec has an empty Name")
			continue
		}
		if canonical[s.Name] {
			t.Errorf("%s is declared twice", s.Name)
		}
		canonical[s.Name] = true
	}
	for _, s := range registry {
		for _, a := range s.Aliases {
			if canonical[a] {
				t.Errorf("%s is an alias of %s but also a command in its own right; the alias wins and shadows it", a, s.Name)
			}
		}
	}
}
