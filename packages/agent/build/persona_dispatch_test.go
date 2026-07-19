package build

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// dispatchablePersonas includes only personas with a non-empty good_for: the
// embedded crew's 7 review specialists qualify; the default Mieli (no good_for)
// does not.
func TestDispatchablePersonas(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("TERVA_PERSONA_NAME", "")
	ds := dispatchablePersonas()
	if len(ds) != 8 {
		names := make([]string, len(ds))
		for i, p := range ds {
			names[i] = p.Name
		}
		t.Fatalf("dispatchable: got %d %v, want 8 (Mieli excluded)", len(ds), names)
	}
	for _, p := range ds {
		if p.Name == "Mieli" {
			t.Error("Mieli has no good_for and must not be dispatchable")
		}
		if len(p.GoodFor) == 0 {
			t.Errorf("%s is in the roster but has empty good_for", p.Name)
		}
	}
}

// The roster names each specialist with specialty + good_for, and the
// auto-swarm addendum embeds both the base block and the roster.
func TestPersonaRosterAndAddendum(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("TERVA_PERSONA_NAME", "")
	roster := personaRoster(dispatchablePersonas())
	// Roster lists qualified refs (review-crew:vartija), not bare names.
	for _, want := range []string{"review-crew:vartija", "security review", "swarm_spawn"} {
		if !strings.Contains(roster, want) {
			t.Errorf("roster missing %q:\n%s", want, roster)
		}
	}
	if strings.Contains(roster, "mieli") {
		t.Error("roster must not list the non-dispatchable default Mieli")
	}
	add := autoSwarmAddendum()
	if !strings.Contains(add, AutoSwarmSystemAddendum) || !strings.Contains(add, roster) {
		t.Error("autoSwarmAddendum must contain both the base addendum and the roster")
	}
}

// ResolveDispatchPersona canonicalizes a known name, rejects paths, errors on
// unknown names, and passes blank through.
func TestResolveDispatchPersona(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("TERVA_PERSONA_NAME", "")
	// Resolves to the canonical qualified Ref, not the bare frontmatter name.
	if got, err := ResolveDispatchPersona("vartija"); err != nil || got != "review-crew:vartija" {
		t.Fatalf("vartija -> (%q, %v); want (review-crew:vartija, nil)", got, err)
	}
	if got, err := ResolveDispatchPersona("review-crew:vartija"); err != nil || got != "review-crew:vartija" {
		t.Fatalf("qualified vartija -> (%q, %v); want (review-crew:vartija, nil)", got, err)
	}
	for _, bad := range []string{"../evil.md", "/etc/x", "a/b", "x.md"} {
		if _, err := ResolveDispatchPersona(bad); err == nil {
			t.Errorf("path-like %q must be rejected", bad)
		}
	}
	if _, err := ResolveDispatchPersona("nope-not-real"); err == nil {
		t.Error("unknown name must error")
	}
	if got, err := ResolveDispatchPersona("   "); err != nil || got != "" {
		t.Fatalf("blank -> (%q, %v); want empty/no-error", got, err)
	}
}
