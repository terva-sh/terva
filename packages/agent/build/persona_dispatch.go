package build

import (
	"fmt"
	"os"
	"strings"

	"terva.sh/terva/packages/agent/persona"
	"terva.sh/terva/packages/i18n"
)

// personaRosterTripwire is the number of dispatchable personas past which an
// always-injected roster starts to cost selection accuracy (the researched
// band is ~20–30; see docs/proposals/Persona-swarm-dispatch.md). Crossing it is
// the signal to add an on-demand Persona-search tool (phase 2b) rather than
// inject the whole roster; we log it so it surfaces.
const personaRosterTripwire = 30

// dispatchablePersonas returns the personas eligible for swarm dispatch: those
// carrying a non-empty good_for tag set. Filtering on good_for keeps the roster
// small, curated, and low-confusability (distinct tags) — which is what keeps
// an injected manifest reliable as the crew grows.
func dispatchablePersonas() []persona.Persona {
	all := persona.All()
	out := make([]persona.Persona, 0, len(all))
	for _, p := range all {
		if len(p.GoodFor) > 0 {
			out = append(out, p)
		}
	}
	return out
}

// DispatchablePersonaNames is the canonical names of the dispatchable personas,
// for the swarm_spawn `Persona` schema enum (validation + the model only sees
// real specialists). The human-readable roster (what each is good for) rides the
// nudge; this is just the value set.
func DispatchablePersonaNames() []string {
	ps := dispatchablePersonas()
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, p.Ref())
	}
	return names
}

// personaRoster renders the dispatchable personas as a compact block the
// coordinator reads to pick a specialist. Tier 1 of progressive disclosure:
// name + specialty + good_for only — the charter resolves in the sub-agent.
// Returns "" when nothing is dispatchable.
func personaRoster(ps []persona.Persona) string {
	if len(ps) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(i18n.P("swarm.roster.intro", "Specialist personas you can dispatch — pass the name as swarm_spawn's `persona` parameter to boot a sub-agent as that specialist:"))
	sb.WriteByte('\n')
	for _, p := range ps {
		fmt.Fprintf(&sb, "- %s", p.Ref())
		if spec := strings.TrimSpace(p.Specialty); spec != "" {
			fmt.Fprintf(&sb, " — %s", spec)
		}
		if len(p.GoodFor) > 0 {
			fmt.Fprintf(&sb, " — good for: %s", strings.Join(p.GoodFor, ", "))
		}
		if p.FromExtension() {
			fmt.Fprintf(&sb, " (via %s)", strings.TrimPrefix(p.Origin(), "ext:"))
		}
		sb.WriteByte('\n')
	}
	sb.WriteString(i18n.P("swarm.roster.footer", "Dispatch a persona only by a name from this list, picking the one whose focus matches the sub-task; omit `persona` for a general-purpose sub-agent."))
	return sb.String()
}

// autoSwarmAddendum is the auto-swarm system block plus, when dispatchable
// personas exist, the Persona roster. Used at BOTH injection sites (the startup
// system prompt in Resolve, and the interactive config) so the live /settings
// toggle strips/re-adds the exact same string.
func autoSwarmAddendum() string {
	addendum := i18n.P("swarm.addendum", AutoSwarmSystemAddendum)
	roster := personaRoster(dispatchablePersonas())
	if roster == "" {
		return addendum
	}
	return addendum + "\n\n" + roster
}

// ResolveDispatchPersona validates a model-supplied Persona NAME against the
// trusted library and returns its canonical name. It rejects path-like values
// (defence-in-depth alongside the swarm_spawn tool's own check) and unknown
// names, so a model can only dispatch a real, trusted Persona by name.
func ResolveDispatchPersona(name string) (string, error) {
	s := strings.TrimSpace(name)
	if s == "" {
		return "", nil
	}
	if strings.ContainsAny(s, `/\`) || strings.HasSuffix(s, ".md") {
		return "", fmt.Errorf("persona %q must be a built-in/installed name, not a path", s)
	}
	p, err := persona.LoadByName(s)
	if err != nil {
		return "", err
	}
	return p.Ref(), nil
}

// logPersonaRosterTripwire emits a one-line notice when the dispatchable roster
// exceeds the inject-everything tripwire — the signal to move to an on-demand
// Persona-search tool (phase 2b). Called once at startup.
func logPersonaRosterTripwire() {
	if n := len(dispatchablePersonas()); n > personaRosterTripwire {
		fmt.Fprintf(os.Stderr, "terva: %d dispatchable personas exceed the inject-roster tripwire (%d); consider an on-demand persona-search tool (see docs/proposals/persona-swarm-dispatch.md)\n", n, personaRosterTripwire)
	}
}
