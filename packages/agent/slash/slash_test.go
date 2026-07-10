package slash

import (
	"strings"
	"testing"
)

// Builtin is the exported, handler-free accessor every non-TUI frontend
// (the ACP run mode, a future web palette) consumes to build a
// slash-command catalog. It must surface every visible command (in
// display order), drop hidden internal verbs, and carry the argument
// hint declared on the spec. Ported from the modes accessor test when
// the catalog was extracted.
func TestBuiltinAccessor(t *testing.T) {
	cmds := Builtin()
	if len(cmds) == 0 {
		t.Fatal("Builtin returned nothing")
	}
	if cmds[0].Name != "/help" {
		t.Errorf("accessor head = %q, want /help first", cmds[0].Name)
	}

	visible := 0
	for _, s := range Registry() {
		if !s.Hidden {
			visible++
		}
	}
	if len(cmds) != visible {
		t.Errorf("Builtin returned %d commands; registry has %d visible specs", len(cmds), visible)
	}

	byName := map[string]Info{}
	for _, c := range cmds {
		if c.Name == "/cd" {
			t.Error("hidden /cd leaked into Builtin")
		}
		if !strings.HasPrefix(c.Name, "/") {
			t.Errorf("command %q must start with a slash", c.Name)
		}
		if c.Desc == "" {
			t.Errorf("command %q has no description", c.Name)
		}
		byName[c.Name] = c
	}

	// The headless-safe agent-control commands the ACP catalog curates
	// from must be present, with no spurious hint (they take no argument).
	for _, name := range []string{"/clear", "/compact"} {
		c, ok := byName[name]
		if !ok {
			t.Errorf("%q missing from Builtin", name)
			continue
		}
		if c.Hint != "" {
			t.Errorf("%q hint = %q; want empty (it takes no argument)", name, c.Hint)
		}
	}

	// An argument-taking command surfaces its hint.
	if c, ok := byName["/model"]; !ok {
		t.Error("/model missing from Builtin")
	} else if c.Hint == "" {
		t.Error("/model should carry an argument hint")
	}
}

// Registry returns a copy: callers mutating the returned slice must not
// corrupt the catalog other surfaces read.
func TestRegistryReturnsCopy(t *testing.T) {
	a := Registry()
	name := a[0].Name
	a[0].Name = "/mutated"
	if got := Registry()[0].Name; got != name {
		t.Fatalf("Registry copy leaked a mutation: %q", got)
	}
}
