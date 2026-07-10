package modes

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/slash"
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

// The registry is the JOIN of the neutral catalog (packages/agent/slash)
// and this package's handler map. The two tables must not drift: every
// spec needs a handler, every handler needs a spec, and the join must
// leave no dispatchable row without a run func.
func TestSlashHandlersMatchCatalog(t *testing.T) {
	specs := slash.Registry()
	byName := map[string]bool{}
	for _, s := range specs {
		byName[s.Name] = true
		if slashHandlers[s.Name] == nil {
			t.Errorf("catalog entry %s has no handler in slashHandlers", s.Name)
		}
	}
	for name := range slashHandlers {
		if !byName[name] {
			t.Errorf("handler %s has no catalog entry in slash.Registry()", name)
		}
	}
	for _, s := range slashRegistry {
		if s.run == nil {
			t.Errorf("joined registry row %s has nil run", s.name)
		}
	}
}

// The catalog groups related commands under divider headers, in a
// deliberate display order: /help leads ungrouped, then session,
// context, model, safety, integrations, and system. Guards against
// new commands being appended at the end of the registry instead of
// slotted into their group.
func TestSlashCatalogGroupOrder(t *testing.T) {
	wantHeaders := []string{
		"session", "context & skills", "model & account",
		"permissions & trust", "agents & integrations", "system",
	}
	cat := builtinSlashCatalog()
	if len(cat) == 0 || cat[0].Header || cat[0].Name != "/help" {
		t.Fatalf("catalog must open with the ungrouped /help, got %+v", cat[0])
	}
	var headers []string
	for _, c := range cat {
		if c.Header {
			headers = append(headers, c.Name)
		}
	}
	if len(headers) != len(wantHeaders) {
		t.Fatalf("group headers = %v, want %v", headers, wantHeaders)
	}
	for i := range headers {
		if headers[i] != wantHeaders[i] {
			t.Fatalf("group header %d = %q, want %q (full: %v)", i, headers[i], wantHeaders[i], headers)
		}
	}
	// Every visible command after /help belongs to a group; a groupless
	// spec would visually attach to whatever group precedes it.
	for _, s := range slashRegistry {
		if s.hidden || s.name == "/help" {
			continue
		}
		if s.group == "" {
			t.Errorf("%s has no display group", s.name)
		}
	}
}

// Filtering the popup keeps group dividers only for groups that still
// have matches, so a narrow prefix like "/se" doesn't strand empty
// headers in the list.
func TestSlashMenuFilterPrunesEmptyGroups(t *testing.T) {
	s := newSlashSuggester()
	got := s.matches("/se")
	if len(got) == 0 {
		t.Fatal("expected matches for /se")
	}
	for i, c := range got {
		if !c.Header {
			continue
		}
		if i+1 >= len(got) || got[i+1].Header {
			t.Fatalf("orphan group header %q in filtered popup: %+v", c.Name, got)
		}
	}
	// /sessions and /session live in the session group; /settings in
	// system. Both groups must surface with their headers when browsing.
	names := map[string]bool{}
	for _, c := range got {
		if !c.Header {
			names[c.Name] = true
		}
	}
	for _, want := range []string{"/sessions", "/session", "/settings"} {
		if !names[want] {
			t.Errorf("%s missing from /se matches: %+v", want, got)
		}
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
