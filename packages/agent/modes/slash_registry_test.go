package modes

import (
	"strings"
	"testing"
)

// Aliases dispatch identically to their canonical command — the exact
// drift the registry exists to prevent: before it, /perms, /telegram,
// and /tg were listed in the dispatch switch but rejected by the
// submit path's separate known-command check.
func TestSlashRegistryAliasesResolve(t *testing.T) {
	for alias, want := range map[string]string{
		"/perms":    "/permissions",
		"/telegram": "/connect",
		"/tg":       "/connect",
	} {
		spec, ok := lookupSlash(alias)
		if !ok {
			t.Errorf("lookupSlash(%q) not found", alias)
			continue
		}
		if spec.name != want {
			t.Errorf("lookupSlash(%q) = %q, want %q", alias, spec.name, want)
		}
		if !isKnownSlashCommand(alias + " some args") {
			t.Errorf("isKnownSlashCommand(%q ...) = false; aliases must dispatch from the editor", alias)
		}
	}
}

// Every name and alias in the registry must be unique — a collision
// would make dispatch order-dependent.
func TestSlashRegistryNamesUnique(t *testing.T) {
	seen := map[string]string{}
	for _, s := range slashRegistry {
		for _, n := range append([]string{s.name}, s.aliases...) {
			if prev, dup := seen[n]; dup {
				t.Errorf("%q registered by both %q and %q", n, prev, s.name)
			}
			seen[n] = s.name
			if !strings.HasPrefix(n, "/") {
				t.Errorf("%q must start with a slash", n)
			}
		}
		if !s.hidden && s.desc == "" {
			t.Errorf("%q is visible but has no description for the popup//help", s.name)
		}
	}
}

// The popup/help catalog derives from the registry: visible commands
// in order, hidden ones absent.
func TestSlashCatalogDerivesFromRegistry(t *testing.T) {
	cat := builtinSlashCatalog()
	if len(cat) == 0 || cat[0].Name != "/help" {
		t.Fatalf("catalog head = %v, want /help first", cat[:1])
	}
	for _, c := range cat {
		if c.Name == "/cd" {
			t.Fatal("hidden /cd leaked into the catalog")
		}
	}
	if _, ok := lookupSlash("/cd"); !ok {
		t.Fatal("/cd must stay dispatchable while hidden")
	}
}

// BuiltinSlashCommands is the exported, *Interactive-free accessor a
// non-TUI frontend (the ACP run mode) consumes to build a slash-command
// catalog. It must surface every visible command (in registry order),
// drop hidden internal verbs, and carry the argument hint declared on the
// spec.
func TestBuiltinSlashCommandsAccessor(t *testing.T) {
	cmds := BuiltinSlashCommands()
	if len(cmds) == 0 {
		t.Fatal("BuiltinSlashCommands returned nothing")
	}

	// Same shape as the internal catalog: visible commands in registry
	// order, /help first, hidden /cd absent.
	if cmds[0].Name != "/help" {
		t.Errorf("accessor head = %q, want /help first", cmds[0].Name)
	}
	if len(cmds) != len(builtinSlashCatalog()) {
		t.Errorf("accessor returned %d commands; internal catalog has %d (they must agree)",
			len(cmds), len(builtinSlashCatalog()))
	}

	byName := map[string]SlashCommandInfo{}
	for _, c := range cmds {
		if c.Name == "/cd" {
			t.Error("hidden /cd leaked into BuiltinSlashCommands")
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
			t.Errorf("%q missing from BuiltinSlashCommands", name)
			continue
		}
		if c.Hint != "" {
			t.Errorf("%q hint = %q; want empty (it takes no argument)", name, c.Hint)
		}
	}

	// An argument-taking command surfaces its hint, decoupled from the TUI.
	if c, ok := byName["/model"]; !ok {
		t.Error("/model missing from BuiltinSlashCommands")
	} else if c.Hint == "" {
		t.Error("/model should carry an argument hint")
	}
}

// cancelsTurn flows from the spec, including via aliases.
func TestSlashCancelsTurnFromSpec(t *testing.T) {
	for cmd, want := range map[string]bool{
		"/clear":    true,
		"/model":    true,
		"/cd":       true,
		"/help":     false,
		"/jump":     false,
		"/perms":    false,
		"/telegram": false,
	} {
		if got := slashCancelsTurn(cmd); got != want {
			t.Errorf("slashCancelsTurn(%q) = %v, want %v", cmd, got, want)
		}
	}
}
