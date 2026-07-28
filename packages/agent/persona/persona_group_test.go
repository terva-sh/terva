package persona

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// A persona's `group` is the shelf a roster files it under. Unlike Namespace it
// is NOT part of the ref, which is the whole reason it is the field a user may
// assign: renaming a group can never invalidate a --persona flag or a session's
// recorded identity.

func TestPersonaGroupParsesAndRoundTrips(t *testing.T) {
	p, err := Parse("---\nname: Scratch\ngroup: My crew\n---\nbody", "scratch.md")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.Group != "My crew" {
		t.Fatalf("Group = %q, want %q", p.Group, "My crew")
	}
	raw, err := Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), "group: My crew") {
		t.Fatalf("marshal dropped the group:\n%s", raw)
	}
	back, err := Parse(string(raw), "scratch.md")
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if back.Group != p.Group {
		t.Fatalf("round-trip Group = %q, want %q", back.Group, p.Group)
	}
}

func TestPersonaWithoutAGroupWritesNoGroupKey(t *testing.T) {
	// omitempty, so an author who never chose a shelf does not find one added
	// to their file the first time the editor saves it.
	raw, err := Marshal(Persona{Name: "Scratch", Charter: "body"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "group:") {
		t.Fatalf("marshal invented a group key:\n%s", raw)
	}
}

func TestGroupLabelFallsBackToTheNamespace(t *testing.T) {
	// The fallback is what makes a bundle self-organising: an extension shipping
	// five personas gets a shelf without anyone writing `group:` five times.
	cases := []struct {
		name  string
		p     Persona
		want  string
		notes string
	}{
		{"declared wins", Persona{Group: "Review", Namespace: "websearch"}, "Review", "an explicit group beats the directory it happens to live in"},
		{"namespace fills in", Persona{Namespace: "websearch"}, "websearch", ""},
		{"neither", Persona{}, "", "ungrouped, so a roster can render flat"},
		{"blank group is not a group", Persona{Group: "   ", Namespace: "websearch"}, "websearch", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.GroupLabel(); got != tc.want {
				t.Fatalf("GroupLabel() = %q, want %q — %s", got, tc.want, tc.notes)
			}
		})
	}
}

// TestEveryBuiltinPersonaHasAGroup keeps the shipped roster tidy: a new built-in
// that forgets `group:` would land in the ungrouped bucket at the bottom of
// every roster, which is the kind of thing nobody notices until a screenshot.
func TestEveryBuiltinPersonaHasAGroup(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("TERVA_PERSONA_NAME", "")

	want := map[string]string{
		"Dramaturgi": "Stage", "Kartoittaja": "Stage", "Kertoja": "Stage",
		"Seppä": "Stage", "Toimittaja": "Stage",
		"Mieli":      "Coding",
		"KUSANAGI-2": "Deliberation", "MAGATAMA-3": "Deliberation", "YATA-1": "Deliberation",
		"Arkkitehti": "Review", "Huoltaja": "Review", "Kirjuri": "Review",
		"Koestaja": "Review", "Luotain": "Review", "Luotsi": "Review", "Vartija": "Review",
	}

	seen := map[string]bool{}
	for _, p := range listEmbedded() {
		seen[p.Name] = true
		got := p.GroupLabel()
		if got == "" {
			t.Errorf("built-in %q has no group — add `group:` to its frontmatter", p.Name)
			continue
		}
		if w, ok := want[p.Name]; !ok {
			t.Errorf("built-in %q is new here; add it to this test's table (it groups as %q)", p.Name, got)
		} else if got != w {
			t.Errorf("built-in %q groups as %q, want %q", p.Name, got, w)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("built-in %q vanished from the roster; drop it from this test's table", name)
		}
	}
}
