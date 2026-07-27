package build

import (
	"strings"
	"testing"
)

// The built-in persona runs silently; a user override is announced, with the
// flow's own note preserved after the announcement.
func TestMachinePersonaNotice(t *testing.T) {
	builtin := Persona{Name: "Seppä", Source: "embedded:personas/seppa.md"}
	if got := MachinePersonaNotice("seppa", builtin, "card is in good shape"); got != "card is in good shape" {
		t.Errorf("builtin persona must pass the note through unchanged, got %q", got)
	}
	if got := MachinePersonaNotice("seppa", builtin, ""); got != "" {
		t.Errorf("builtin persona with no note must stay empty, got %q", got)
	}

	override := Persona{Name: "Seppä", Source: "/home/u/.terva/personas/seppa.md"}
	got := MachinePersonaNotice("seppa", override, "card is in good shape")
	if !strings.Contains(got, `"seppa"`) || !strings.Contains(got, override.Source) {
		t.Errorf("override notice must name the stem and the shadowing source, got %q", got)
	}
	if !strings.HasSuffix(got, "card is in good shape") {
		t.Errorf("the flow's own note must survive after the announcement, got %q", got)
	}
	if bare := MachinePersonaNotice("seppa", override, ""); bare == "" || strings.Contains(bare, "\n") {
		t.Errorf("override with no flow note is the bare announcement, got %q", bare)
	}
}
